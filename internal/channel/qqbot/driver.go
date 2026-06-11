package qqbot

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Rememorio/clawdex/internal/channel"
	"github.com/Rememorio/clawdex/internal/logger"
	"github.com/Rememorio/clawdex/internal/pairing"
)

const (
	defaultTextChunkLimit = 5000

	// passiveReplyLimit is the QQ Bot platform limit for passive replies
	// per inbound message per hour. After this many replies with msg_id set,
	// subsequent sends must omit msg_id (proactive mode).
	passiveReplyLimit = 4
)

// mentionTagRe matches QQ @mention tags like <@!xxx> or <@xxx>.
var mentionTagRe = regexp.MustCompile(`<@!?\w+>\s*`)

// Config controls QQ Bot driver behavior.
type Config struct {
	Name           string
	AppID          string
	ClientSecret   string
	DMPolicy       string   // "open" (default), "pairing", "allowlist"
	AllowFrom      []string // user openid allowlist (DM policy)
	GroupPolicy    string   // "allowlist" (default), "open", "disabled"
	GroupAllowFrom []string // group openid allowlist
	TextChunkLimit int      // max chars per message (default 5000)
}

// Driver is the QQ Bot channel implementation.
type Driver struct {
	cfg          Config
	name         string
	api          *apiClient
	handler      channel.Handler
	pairingStore *pairing.Store
	mu           sync.RWMutex // protects allowFrom mutations

	// msgSeq tracks per-message sequence numbers (msgID → counter).
	msgSeqMu sync.Mutex
	msgSeqs  map[string]*atomic.Int64
}

// New constructs a QQ Bot driver.
func New(cfg Config, ps *pairing.Store) *Driver {
	if cfg.DMPolicy == "" {
		cfg.DMPolicy = "open"
	}
	if cfg.GroupPolicy == "" {
		cfg.GroupPolicy = "allowlist"
	}
	if cfg.TextChunkLimit <= 0 {
		cfg.TextChunkLimit = defaultTextChunkLimit
	}
	name := cfg.Name
	if name == "" {
		name = "qqbot"
	}
	return &Driver{
		cfg:          cfg,
		name:         name,
		api:          newAPIClient(cfg.AppID, cfg.ClientSecret),
		pairingStore: ps,
		msgSeqs:      make(map[string]*atomic.Int64),
	}
}

// Name returns the driver identifier.
func (d *Driver) Name() string { return d.name }

// Start connects to the QQ Bot gateway and processes events.
func (d *Driver) Start(ctx context.Context, handler channel.Handler) error {
	d.handler = handler
	logger.Info("qqbot driver starting", "name", d.name, "app_id", d.cfg.AppID)
	return d.connectAndRun(ctx)
}

// AddAllowedUser appends a user openid to the DM allowlist (for pairing).
func (d *Driver) AddAllowedUser(openID string) {
	d.mu.Lock()
	d.cfg.AllowFrom = append(d.cfg.AllowFrom, openID)
	d.mu.Unlock()
}

// ── Responder implementation ──

// responder implements channel.Responder and channel.MediaResponder
// for a specific message context.
type responder struct {
	driver       *Driver
	isGroup      bool
	peerID       string // user openid or group openid
	triggerMsgID string // the inbound message ID (for passive reply)
}

func (r *responder) Typing(ctx context.Context, msg channel.Message) error {
	if r.isGroup {
		return nil // typing not supported in groups
	}
	return r.driver.api.sendC2CInputNotify(r.peerID, r.triggerMsgID)
}

func (r *responder) Reply(ctx context.Context, msg channel.Message, text string) error {
	chunks := chunkText(text, r.driver.cfg.TextChunkLimit)
	for i, chunk := range chunks {
		seq := r.driver.nextMsgSeq(r.triggerMsgID)
		// After passiveReplyLimit replies, switch to proactive mode (no msg_id).
		msgID := r.triggerMsgID
		if seq > passiveReplyLimit {
			msgID = ""
		}
		var err error
		if r.isGroup {
			err = r.driver.api.sendGroupMessage(r.peerID, chunk, msgID, seq)
		} else {
			err = r.driver.api.sendC2CMessage(r.peerID, chunk, msgID, seq)
		}
		if err != nil {
			return fmt.Errorf("qqbot: send chunk %d/%d: %w", i+1, len(chunks), err)
		}
	}
	return nil
}

func (r *responder) ReplyWithMedia(ctx context.Context, msg channel.Message, caption string, filePaths []string) error {
	// Send caption text if present.
	if caption != "" {
		if err := r.Reply(ctx, msg, caption); err != nil {
			return err
		}
	}
	// Send each media file.
	for _, fp := range filePaths {
		seq := r.driver.nextMsgSeq(r.triggerMsgID)
		msgID := r.triggerMsgID
		if seq > passiveReplyLimit {
			msgID = ""
		}
		var err error
		if r.isGroup {
			err = r.driver.api.sendGroupMedia(r.peerID, fp, msgID, seq)
		} else {
			err = r.driver.api.sendC2CMedia(r.peerID, fp, msgID, seq)
		}
		if err != nil {
			logger.Warn("qqbot media send failed", "name", r.driver.name, "file", fp, "error", err)
		}
	}
	return nil
}

// ── Message handling ──

// handleC2CMessage processes an inbound DM.
func (d *Driver) handleC2CMessage(ctx context.Context, raw json.RawMessage) {
	var ev c2cMessageEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		logger.Warn("qqbot parse C2C message failed", "name", d.name, "error", err)
		return
	}

	senderID := ev.Author.UserOpenID
	if senderID == "" {
		senderID = ev.Author.ID
	}

	// DM policy gating.
	if !d.allowDM(senderID) {
		logger.Debug("qqbot DM rejected by policy", "name", d.name, "sender", senderID)
		return
	}

	// Download attachments.
	var mediaPaths, cleanupPaths []string
	if len(ev.Attachments) > 0 {
		tmpDir, err := os.MkdirTemp("", "clawdex-qqbot-media-")
		if err == nil {
			for _, att := range ev.Attachments {
				url := att.URL
				if !strings.HasPrefix(url, "http") {
					url = "https://" + url
				}
				path, err := d.api.downloadAttachment(url, tmpDir)
				if err != nil {
					logger.Warn("qqbot download attachment failed", "name", d.name, "url", att.URL, "error", err)
					continue
				}
				mediaPaths = append(mediaPaths, path)
				cleanupPaths = append(cleanupPaths, path)
			}
		}
	}

	chatID := hashOpenID(senderID)
	msg := channel.Message{
		Channel:      d.name,
		ChatID:       chatID,
		MessageID:    hashOpenID(ev.ID),
		SenderID:     chatID,
		SenderName:   senderID,
		Target:       senderID,
		Text:         strings.TrimSpace(ev.Content),
		MediaPaths:   mediaPaths,
		CleanupPaths: cleanupPaths,
	}

	resp := &responder{
		driver:       d,
		isGroup:      false,
		peerID:       senderID,
		triggerMsgID: ev.ID,
	}

	d.handler.Handle(ctx, msg, resp)
}

// handleGroupMessage processes an inbound group @mention message.
func (d *Driver) handleGroupMessage(ctx context.Context, raw json.RawMessage) {
	var ev groupMessageEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		logger.Warn("qqbot parse group message failed", "name", d.name, "error", err)
		return
	}

	// Group policy gating.
	if !d.allowGroup(ev.GroupOpenID) {
		logger.Debug("qqbot group rejected by policy", "name", d.name, "group", ev.GroupOpenID)
		return
	}

	senderID := ev.Author.MemberOpenID
	if senderID == "" {
		senderID = ev.Author.UserOpenID
	}
	if senderID == "" {
		senderID = ev.Author.ID
	}

	// Download attachments.
	var mediaPaths, cleanupPaths []string
	if len(ev.Attachments) > 0 {
		tmpDir, err := os.MkdirTemp("", "clawdex-qqbot-media-")
		if err == nil {
			for _, att := range ev.Attachments {
				url := att.URL
				if !strings.HasPrefix(url, "http") {
					url = "https://" + url
				}
				path, err := d.api.downloadAttachment(url, tmpDir)
				if err != nil {
					logger.Warn("qqbot download attachment failed", "name", d.name, "url", att.URL, "error", err)
					continue
				}
				mediaPaths = append(mediaPaths, path)
				cleanupPaths = append(cleanupPaths, path)
			}
		}
	}

	chatID := hashOpenID(ev.GroupOpenID)
	msg := channel.Message{
		Channel:      d.name,
		ChatID:       chatID,
		MessageID:    hashOpenID(ev.ID),
		SenderID:     hashOpenID(senderID),
		SenderName:   senderID,
		ChatType:     "group",
		Target:       ev.GroupOpenID,
		Text:         stripMentions(ev.Content),
		MediaPaths:   mediaPaths,
		CleanupPaths: cleanupPaths,
	}

	resp := &responder{
		driver:       d,
		isGroup:      true,
		peerID:       ev.GroupOpenID,
		triggerMsgID: ev.ID,
	}

	d.handler.Handle(ctx, msg, resp)
}

// ── Policy checks ──

func (d *Driver) allowDM(senderOpenID string) bool {
	switch d.cfg.DMPolicy {
	case "open":
		return true
	case "pairing":
		d.mu.RLock()
		allowed := contains(d.cfg.AllowFrom, senderOpenID)
		d.mu.RUnlock()
		if allowed {
			return true
		}
		if d.pairingStore != nil {
			d.pairingStore.Create(hashOpenID(senderOpenID), senderOpenID, senderOpenID, d.name)
		}
		return false
	case "allowlist":
		d.mu.RLock()
		defer d.mu.RUnlock()
		return contains(d.cfg.AllowFrom, senderOpenID)
	default:
		return true
	}
}

func (d *Driver) allowGroup(groupOpenID string) bool {
	switch d.cfg.GroupPolicy {
	case "open":
		return true
	case "disabled":
		return false
	case "allowlist":
		return contains(d.cfg.GroupAllowFrom, groupOpenID)
	default:
		return contains(d.cfg.GroupAllowFrom, groupOpenID)
	}
}

// SendText sends a proactive text message to a QQ C2C or group target.
func (d *Driver) SendText(ctx context.Context, target channel.DeliveryTarget, text string) error {
	_ = ctx
	peerID := strings.TrimSpace(target.Target)
	if peerID == "" {
		return fmt.Errorf("qqbot proactive send: missing target")
	}
	chunks := chunkText(text, d.cfg.TextChunkLimit)
	seqKey := "cron:" + peerID
	for i, chunk := range chunks {
		seq := d.nextMsgSeq(seqKey)
		var err error
		if target.ChatType == "group" {
			err = d.api.sendGroupMessage(peerID, chunk, "", seq)
		} else {
			err = d.api.sendC2CMessage(peerID, chunk, "", seq)
		}
		if err != nil {
			return fmt.Errorf("qqbot proactive send chunk %d/%d: %w", i+1, len(chunks), err)
		}
	}
	return nil
}

// ── Helpers ──

// nextMsgSeq returns the next sequence number for a given trigger message.
func (d *Driver) nextMsgSeq(msgID string) int {
	d.msgSeqMu.Lock()
	counter, ok := d.msgSeqs[msgID]
	if !ok {
		counter = &atomic.Int64{}
		d.msgSeqs[msgID] = counter
	}
	d.msgSeqMu.Unlock()
	return int(counter.Add(1))
}

// hashOpenID converts a string openid to a stable int64 for use as ChatID.
func hashOpenID(openID string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(openID))
	v := int64(h.Sum64())
	if v == 0 {
		return 1
	}
	return v
}

// chunkText splits text into chunks of at most limit runes.
func chunkText(text string, limit int) []string {
	runes := []rune(text)
	if len(runes) <= limit {
		return []string{text}
	}
	var chunks []string
	for start := 0; start < len(runes); start += limit {
		end := start + limit
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[start:end]))
	}
	return chunks
}

func contains(list []string, item string) bool {
	for _, v := range list {
		if v == item {
			return true
		}
	}
	return false
}

// stripMentions removes QQ @mention tags (<@!xxx> or <@xxx>) from message
// content and trims surrounding whitespace.
func stripMentions(content string) string {
	cleaned := mentionTagRe.ReplaceAllString(content, "")
	return strings.TrimSpace(cleaned)
}
