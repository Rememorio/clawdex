package feishu

import (
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/Rememorio/clawdex/internal/channel"
	"github.com/Rememorio/clawdex/internal/logger"
	"github.com/Rememorio/clawdex/internal/pairing"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

const (
	defaultTextChunkLimit = 4000
	defaultMaxImageCount  = 8
)

var spaceRe = regexp.MustCompile(`\s+`)

var feishuStatusEmojiTypes = map[string]string{
	"👀":  "Typing",
	"⏳":  "Typing",
	"👍":  "THUMBSUP",
	"✅":  "THUMBSUP",
	"❌":  "ERROR",
	"❤️": "HEART",
	"❤":  "HEART",
	"😀":  "SMILE",
	"🙂":  "SMILE",
	"😊":  "SMILE",
}

// GroupRule defines per-group access settings.
type GroupRule struct {
	Enabled        *bool
	AllowFrom      []string
	RequireMention *bool
}

// Config controls Feishu channel behavior.
type Config struct {
	Name           string
	AppID          string
	AppSecret      string
	BaseURL        string
	TextChunkLimit int
	DMPolicy       string
	AllowFrom      []string
	GroupPolicy    string
	GroupAllowFrom []string
	Groups         map[string]GroupRule
	RequireMention *bool
}

// Driver is the Feishu channel implementation.
type Driver struct {
	cfg          Config
	name         string
	api          messageAPI
	handler      channel.Handler
	pairingStore *pairing.Store
	mu           sync.RWMutex
}

// New constructs a Feishu driver.
func New(cfg Config, ps *pairing.Store) *Driver {
	if cfg.TextChunkLimit <= 0 {
		cfg.TextChunkLimit = defaultTextChunkLimit
	}
	if cfg.DMPolicy == "" {
		cfg.DMPolicy = "pairing"
	}
	if cfg.GroupPolicy == "" {
		cfg.GroupPolicy = "allowlist"
	}
	if cfg.RequireMention == nil {
		requireMention := true
		cfg.RequireMention = &requireMention
	}

	name := cfg.Name
	if name == "" {
		name = "feishu"
	}

	return &Driver{
		cfg:          cfg,
		name:         name,
		api:          newMessageAPI(cfg.AppID, cfg.AppSecret, cfg.BaseURL, name),
		pairingStore: ps,
	}
}

// Name returns the driver id.
func (d *Driver) Name() string { return d.name }

// Start opens the Feishu long connection and dispatches message events.
func (d *Driver) Start(ctx context.Context, handler channel.Handler) error {
	d.handler = handler

	eventHandler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(d.handleMessageEvent)
	eventHandler.InitConfig(
		larkevent.WithLogger(sdkLogger{channel: d.name}),
		larkevent.WithLogLevel(larkcore.LogLevelWarn),
		larkevent.WithSkipSignVerify(true),
	)

	opts := []larkws.ClientOption{
		larkws.WithEventHandler(eventHandler),
		larkws.WithLogLevel(larkcore.LogLevelWarn),
		larkws.WithLogger(sdkLogger{channel: d.name}),
		larkws.WithOnReady(func() {
			logger.Info("feishu long connection ready", "channel", d.name)
		}),
		larkws.WithOnError(func(err error) {
			logger.Warn("feishu long connection error", "channel", d.name, "error", err)
		}),
		larkws.WithOnReconnecting(func() {
			logger.Warn("feishu long connection reconnecting", "channel", d.name)
		}),
		larkws.WithOnReconnected(func() {
			logger.Info("feishu long connection reconnected", "channel", d.name)
		}),
		larkws.WithOnDisconnected(func() {
			logger.Warn("feishu long connection disconnected", "channel", d.name)
		}),
	}
	if strings.TrimSpace(d.cfg.BaseURL) != "" {
		opts = append(opts, larkws.WithDomain(d.cfg.BaseURL))
	}
	wsClient := larkws.NewClient(d.cfg.AppID, d.cfg.AppSecret, opts...)

	errCh := make(chan error, 1)
	go func() {
		errCh <- wsClient.Start(ctx)
	}()

	logger.Info("feishu driver started", "channel", d.name)

	select {
	case <-ctx.Done():
		wsClient.Close()
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

// Typing is a no-op; Feishu does not expose a simple typing API for bots.
func (d *Driver) Typing(_ context.Context, _ channel.Message) error {
	return nil
}

// AddAllowedUser appends a user open_id to the runtime AllowFrom list.
func (d *Driver) AddAllowedUser(openID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !containsString(d.cfg.AllowFrom, openID) {
		d.cfg.AllowFrom = append(d.cfg.AllowFrom, openID)
	}
}

// Reply is only available on the per-message responder created from an inbound
// Feishu event, because outbound routing requires the original message_id/chat_id.
func (d *Driver) Reply(ctx context.Context, msg channel.Message, text string) error {
	return fmt.Errorf("feishu reply requires per-message responder")
}

func (d *Driver) reply(ctx context.Context, messageID, chatID string, replyInThread bool, text string) error {
	chunks := splitText(text, d.cfg.TextChunkLimit)
	for i, chunk := range chunks {
		if messageID != "" {
			if err := d.api.ReplyText(ctx, messageID, chunk, replyInThread); err != nil {
				return fmt.Errorf("feishu reply chunk %d/%d: %w", i+1, len(chunks), err)
			}
			continue
		}
		if chatID == "" {
			return fmt.Errorf("feishu reply: missing message_id and chat_id")
		}
		if err := d.api.SendText(ctx, chatID, chunk); err != nil {
			return fmt.Errorf("feishu send chunk %d/%d: %w", i+1, len(chunks), err)
		}
	}
	return nil
}

type responder struct {
	driver        *Driver
	messageID     string
	chatID        string
	replyInThread bool
	mu            sync.Mutex
	reactionID    string
}

func (r *responder) Typing(ctx context.Context, msg channel.Message) error {
	return r.driver.Typing(ctx, msg)
}

func (r *responder) Reply(ctx context.Context, msg channel.Message, text string) error {
	return r.driver.reply(ctx, r.messageID, r.chatID, r.replyInThread, text)
}

func (r *responder) SetReaction(ctx context.Context, chatID, messageID int64, emoji string) error {
	emojiType := feishuReactionType(emoji)
	if emojiType == "" {
		logger.Debug("feishu skip unsupported status reaction",
			"channel", r.driver.name,
			"chat", chatID,
			"msg", messageID,
			"emoji", emoji,
		)
		return nil
	}

	r.mu.Lock()
	oldReactionID := r.reactionID
	r.reactionID = ""
	r.mu.Unlock()

	if oldReactionID != "" {
		if err := r.driver.api.DeleteReaction(ctx, r.messageID, oldReactionID); err != nil {
			logger.Warn("feishu delete status reaction failed",
				"channel", r.driver.name,
				"chat", chatID,
				"msg", messageID,
				"reaction_id", oldReactionID,
				"error", err,
			)
			return err
		}
	}

	reactionID, err := r.driver.api.CreateReaction(ctx, r.messageID, emojiType)
	if err != nil {
		logger.Warn("feishu set status reaction failed",
			"channel", r.driver.name,
			"chat", chatID,
			"msg", messageID,
			"emoji_type", emojiType,
			"error", err,
		)
		return err
	}

	r.mu.Lock()
	r.reactionID = reactionID
	r.mu.Unlock()
	return nil
}

func feishuReactionType(emoji string) string {
	emoji = strings.TrimSpace(emoji)
	if emoji == "" {
		return ""
	}
	if t, ok := feishuStatusEmojiTypes[emoji]; ok {
		return t
	}
	if isFeishuEmojiType(emoji) {
		return emoji
	}
	return ""
}

func isFeishuEmojiType(value string) bool {
	if value == "Typing" {
		return true
	}
	for _, r := range value {
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '_' {
			continue
		}
		return false
	}
	return value != ""
}

func (d *Driver) handleMessageEvent(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	if d.handler == nil || event == nil || event.Event == nil || event.Event.Message == nil {
		return nil
	}

	raw := event.Event.Message
	sender := event.Event.Sender
	senderOpenID := senderOpenID(sender)
	if senderOpenID == "" {
		logger.Debug("feishu skip message with empty sender", "channel", d.name)
		return nil
	}

	chatID := stringValue(raw.ChatId)
	messageID := stringValue(raw.MessageId)
	if chatID == "" || messageID == "" {
		logger.Debug("feishu skip message with empty chat/message id", "channel", d.name)
		return nil
	}

	text := extractMessageText(raw)
	if text == "" {
		return nil
	}

	isGroup := isGroupChat(stringValue(raw.ChatType))
	mentionedBot := d.mentionedBot(ctx, raw.Mentions)
	if isGroup {
		if !d.resolveGroupAccess(chatID, senderOpenID, mentionedBot) {
			logger.Debug("feishu group rejected", "channel", d.name, "chat", chatID, "sender", senderOpenID, "mentioned_bot", mentionedBot)
			return nil
		}
		text = stripMentionKeys(text, raw.Mentions)
	} else {
		switch d.checkAccess(senderOpenID) {
		case accessDenied:
			logger.Info("feishu access denied", "channel", d.name, "sender", senderOpenID)
			return nil
		case accessPairing:
			d.handlePairing(ctx, senderOpenID, messageID, chatID)
			return nil
		case accessAllowed:
			// continue
		}
	}

	mediaPaths := d.downloadImageResources(ctx, messageID, stringValue(raw.MessageType), stringValue(raw.Content))

	chatHash := hashStringID(d.name, chatID)
	msgHash := hashStringID(d.name, messageID)
	senderHash := hashStringID("", senderOpenID)
	threadID := int64(0)
	if tid := stringValue(raw.ThreadId); tid != "" {
		threadID = hashStringID(d.name, tid)
	}

	chatType := ""
	if isGroup {
		chatType = "group"
	}

	d.handler.Handle(ctx, channel.Message{
		Channel:      d.name,
		ChatID:       chatHash,
		MessageID:    msgHash,
		ThreadID:     threadID,
		SenderID:     senderHash,
		SenderName:   senderOpenID,
		ChatType:     chatType,
		Text:         text,
		MediaPaths:   mediaPaths,
		CleanupPaths: mediaPaths,
	}, &responder{
		driver:        d,
		messageID:     messageID,
		chatID:        chatID,
		replyInThread: threadID != 0,
	})
	return nil
}

func (d *Driver) downloadImageResources(ctx context.Context, messageID, messageType, content string) []string {
	keys := extractImageResourceKeys(messageType, content)
	if len(keys) == 0 {
		return nil
	}
	if len(keys) > defaultMaxImageCount {
		keys = keys[:defaultMaxImageCount]
	}

	tmpDir, err := os.MkdirTemp("", "clawdex-feishu-media-")
	if err != nil {
		logger.Warn("feishu create media temp dir failed", "channel", d.name, "error", err)
		return nil
	}

	var paths []string
	for i, key := range keys {
		dest := filepath.Join(tmpDir, fmt.Sprintf("image_%d.jpg", i))
		if err := d.api.DownloadResource(ctx, messageID, key, "image", dest); err != nil {
			logger.Warn("feishu image download failed",
				"channel", d.name,
				"msg", messageID,
				"image_key", key,
				"error", err,
			)
			continue
		}
		paths = append(paths, dest)
	}
	if len(paths) == 0 {
		os.RemoveAll(tmpDir)
		return nil
	}
	return paths
}

type accessResult int

const (
	accessAllowed accessResult = iota
	accessDenied
	accessPairing
)

func (d *Driver) checkAccess(openID string) accessResult {
	d.mu.RLock()
	allowFrom := d.cfg.AllowFrom
	policy := d.cfg.DMPolicy
	d.mu.RUnlock()

	switch policy {
	case "open":
		return accessAllowed
	case "allowlist":
		if containsString(allowFrom, openID) {
			return accessAllowed
		}
		return accessDenied
	default:
		if containsString(allowFrom, openID) {
			return accessAllowed
		}
		return accessPairing
	}
}

func (d *Driver) resolveGroupAccess(chatID, senderOpenID string, mentionedBot bool) bool {
	d.mu.RLock()
	policy := d.cfg.GroupPolicy
	groupAllowFrom := d.cfg.GroupAllowFrom
	groups := d.cfg.Groups
	requireMention := d.cfg.RequireMention != nil && *d.cfg.RequireMention
	if entry, ok := groups[chatID]; ok && entry.RequireMention != nil {
		requireMention = *entry.RequireMention
	} else if entry, ok := groups["*"]; ok && entry.RequireMention != nil {
		requireMention = *entry.RequireMention
	}
	d.mu.RUnlock()

	if requireMention && !mentionedBot {
		return false
	}

	switch policy {
	case "disabled":
		return false
	case "open":
		return true
	default:
		if len(groupAllowFrom) > 0 {
			if !containsString(groupAllowFrom, chatID) && !containsString(groupAllowFrom, "*") {
				return false
			}
		}

		entry, ok := groups[chatID]
		if !ok {
			entry, ok = groups["*"]
		}
		if !ok {
			return len(groupAllowFrom) > 0
		}
		if entry.Enabled != nil && !*entry.Enabled {
			return false
		}
		return len(entry.AllowFrom) == 0 || containsString(entry.AllowFrom, senderOpenID)
	}
}

func (d *Driver) handlePairing(ctx context.Context, openID, messageID, chatID string) {
	if d.pairingStore == nil {
		return
	}

	senderHash := hashStringID("", openID)
	var text string
	if code, pending := d.pairingStore.HasPending(senderHash, d.name); pending {
		text = fmt.Sprintf("Your pairing code is still pending: **%s**\n\nAsk the admin to run: `clawdex pairing approve %s`", code, code)
	} else {
		code := d.pairingStore.Create(senderHash, openID, openID, d.name)
		text = fmt.Sprintf("Your pairing code: **%s**\n\nAsk the admin to run: `clawdex pairing approve %s`", code, code)
	}
	if err := d.reply(ctx, messageID, chatID, false, text); err != nil {
		logger.Warn("feishu pairing reply failed", "channel", d.name, "sender", openID, "error", err)
	}
}

func (d *Driver) mentionedBot(ctx context.Context, mentions []*larkim.MentionEvent) bool {
	if len(mentions) == 0 {
		return false
	}

	botOpenID, err := d.api.BotOpenID(ctx)
	if err != nil {
		logger.Debug("feishu bot identity unavailable", "channel", d.name, "error", err)
	}
	for _, mention := range mentions {
		if mention == nil {
			continue
		}
		if botOpenID != "" && mention.Id != nil && stringValue(mention.Id.OpenId) == botOpenID {
			return true
		}
		t := strings.ToLower(stringValue(mention.MentionedType))
		if botOpenID == "" && (t == "app" || t == "bot") {
			return true
		}
	}
	return false
}

func stripMentionKeys(text string, mentions []*larkim.MentionEvent) string {
	for _, mention := range mentions {
		if mention == nil || mention.Key == nil || *mention.Key == "" {
			continue
		}
		text = strings.ReplaceAll(text, *mention.Key, "")
	}
	text = spaceRe.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}

func senderOpenID(sender *larkim.EventSender) string {
	if sender == nil || sender.SenderId == nil {
		return ""
	}
	if v := stringValue(sender.SenderId.OpenId); v != "" {
		return v
	}
	if v := stringValue(sender.SenderId.UserId); v != "" {
		return v
	}
	return stringValue(sender.SenderId.UnionId)
}

func isGroupChat(chatType string) bool {
	return chatType == "group" || chatType == "topic_group"
}

func hashStringID(namespace, id string) int64 {
	h := fnv.New64a()
	if namespace != "" {
		_, _ = h.Write([]byte(namespace))
		_, _ = h.Write([]byte{0})
	}
	_, _ = h.Write([]byte(id))
	v := int64(h.Sum64())
	if v == 0 {
		return 1
	}
	return v
}

func splitText(text string, limit int) []string {
	clean := strings.TrimSpace(text)
	if clean == "" {
		return []string{"(empty response)"}
	}
	if limit <= 0 {
		limit = defaultTextChunkLimit
	}
	runes := []rune(clean)
	if len(runes) <= limit {
		return []string{clean}
	}
	chunks := make([]string, 0, len(runes)/limit+1)
	for start := 0; start < len(runes); start += limit {
		end := start + limit
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[start:end]))
	}
	return chunks
}

func stringValue(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

func containsString(list []string, item string) bool {
	for _, v := range list {
		if v == item {
			return true
		}
	}
	return false
}
