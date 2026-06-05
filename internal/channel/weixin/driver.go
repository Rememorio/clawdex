package weixin

import (
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Rememorio/clawdex/internal/channel"
	"github.com/Rememorio/clawdex/internal/logger"
	"github.com/Rememorio/clawdex/internal/pairing"
)

const (
	defaultBaseURL         = "https://oai.ilink.bot"
	defaultTextChunkLimit  = 4000
	defaultLongPollTimeout = int64(35000) // ms
	maxConsecutiveFailures = 3
	backoffDelay           = 30 * time.Second
	retryDelay             = 2 * time.Second
	sessionExpiredErrCode  = -14
)

// Config controls Weixin driver behavior.
type Config struct {
	Name           string
	BaseURL        string // API base URL (default: https://oai.ilink.bot)
	Token          string // iLink bot token
	TextChunkLimit int    // max runes per chunk (default 4000)
	DMPolicy       string // "open", "pairing" (default), "allowlist"
	AllowFrom      []string
}

// Driver is the Weixin channel implementation.
type Driver struct {
	cfg          Config
	name         string
	api          *apiClient
	pairingStore *pairing.Store

	mu            sync.RWMutex
	allowFrom     map[string]bool
	contextTokens map[string]string // userID → latest contextToken
	typingTickets map[string]string // userID → cached typing_ticket
}

// New constructs a Weixin driver with the provided configuration.
func New(cfg Config, ps *pairing.Store) *Driver {
	if cfg.TextChunkLimit <= 0 {
		cfg.TextChunkLimit = defaultTextChunkLimit
	}
	if cfg.DMPolicy == "" {
		cfg.DMPolicy = "pairing"
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	name := cfg.Name
	if name == "" {
		name = "weixin"
	}

	allowFrom := make(map[string]bool, len(cfg.AllowFrom))
	for _, id := range cfg.AllowFrom {
		allowFrom[id] = true
	}

	return &Driver{
		cfg:           cfg,
		name:          name,
		api:           newAPIClient(cfg.BaseURL, cfg.Token),
		pairingStore:  ps,
		allowFrom:     allowFrom,
		contextTokens: make(map[string]string),
		typingTickets: make(map[string]string),
	}
}

// Name returns the driver name for logging and routing.
func (d *Driver) Name() string { return d.name }

// Start begins the long-poll loop and dispatches inbound messages to the handler.
// It blocks until the context is cancelled.
func (d *Driver) Start(ctx context.Context, handler channel.Handler) error {
	// Notify the server that we're starting.
	if err := d.api.notifyStart(ctx); err != nil {
		logger.Warn("weixin notifyStart failed (continuing)", "channel", d.name, "error", err)
	}

	logger.Info("weixin driver started", "channel", d.name, "base_url", d.cfg.BaseURL)

	// Ensure we notify stop on exit.
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := d.api.notifyStop(stopCtx); err != nil {
			logger.Warn("weixin notifyStop failed", "channel", d.name, "error", err)
		}
	}()

	var getUpdatesBuf string
	var consecutiveFailures int
	pollTimeout := defaultLongPollTimeout

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		resp, err := d.api.getUpdates(ctx, getUpdatesBuf, pollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			consecutiveFailures++
			logger.Warn("weixin getUpdates failed",
				"channel", d.name,
				"error", err,
				"consecutive", consecutiveFailures,
			)
			if consecutiveFailures >= maxConsecutiveFailures {
				logger.Error("weixin too many consecutive failures, backing off",
					"channel", d.name, "delay", backoffDelay)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(backoffDelay):
				}
				consecutiveFailures = 0
			} else {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(retryDelay):
				}
			}
			continue
		}
		consecutiveFailures = 0

		// Check for session expired.
		if resp.ErrCode == sessionExpiredErrCode {
			logger.Error("weixin session expired, driver stopping", "channel", d.name)
			return fmt.Errorf("weixin session expired (errcode=%d)", resp.ErrCode)
		}

		// Update poll state.
		if resp.GetUpdatesBuf != "" {
			getUpdatesBuf = resp.GetUpdatesBuf
		}
		if resp.LongPollingTimeoutMs > 0 {
			pollTimeout = resp.LongPollingTimeoutMs
		}

		// Process messages.
		for i := range resp.Msgs {
			d.processInbound(ctx, &resp.Msgs[i], handler)
		}
	}
}

// AddAllowedUser adds a user to the allowlist (called after pairing approval).
func (d *Driver) AddAllowedUser(userID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.allowFrom[userID] = true
}

// processInbound handles a single inbound message.
func (d *Driver) processInbound(ctx context.Context, msg *weixinMessage, handler channel.Handler) {
	// Skip bot messages (echoes).
	if msg.MessageType == messageTypeBot {
		return
	}
	// Skip messages without user content.
	if msg.FromUserID == "" {
		return
	}

	// Cache the context token for this user.
	if msg.ContextToken != "" {
		d.mu.Lock()
		d.contextTokens[msg.FromUserID] = msg.ContextToken
		d.mu.Unlock()
	}

	// Extract text and media from item_list.
	text, mediaPaths, cleanupPaths := d.extractContent(ctx, msg)
	if text == "" && len(mediaPaths) == 0 {
		return
	}

	// Access control.
	switch d.checkAccess(msg.FromUserID) {
	case accessDenied:
		logger.Info("weixin access denied", "channel", d.name, "user", msg.FromUserID)
		return
	case accessPairing:
		d.handlePairing(ctx, msg)
		return
	}

	chatID := hashStringID(msg.FromUserID)
	senderID := chatID
	channelMsg := channel.Message{
		Channel:      d.name,
		ChatID:       chatID,
		MessageID:    msg.MessageID,
		SenderID:     senderID,
		SenderName:   msg.FromUserID,
		Text:         text,
		MediaPaths:   mediaPaths,
		CleanupPaths: cleanupPaths,
	}

	responder := &weixinResponder{
		driver: d,
		userID: msg.FromUserID,
	}

	handler.Handle(ctx, channelMsg, responder)
}

// extractContent reads text and downloads media from the message items.
func (d *Driver) extractContent(ctx context.Context, msg *weixinMessage) (string, []string, []string) {
	var texts []string
	var mediaPaths []string
	var cleanupPaths []string

	for _, item := range msg.ItemList {
		switch item.Type {
		case itemTypeText:
			if item.TextItem != nil && item.TextItem.Text != "" {
				texts = append(texts, item.TextItem.Text)
			}
		case itemTypeImage:
			if item.ImageItem != nil {
				path, err := d.downloadImage(ctx, item.ImageItem, msg.FromUserID)
				if err != nil {
					logger.Warn("weixin image download failed", "channel", d.name, "error", err)
					continue
				}
				mediaPaths = append(mediaPaths, path)
				cleanupPaths = append(cleanupPaths, path)
			}
		case itemTypeVoice:
			if item.VoiceItem != nil && item.VoiceItem.Text != "" {
				// Use voice transcription as text.
				texts = append(texts, item.VoiceItem.Text)
			}
		case itemTypeFile:
			// Log but skip file attachments for now.
			if item.FileItem != nil {
				logger.Debug("weixin file received (skipped)", "channel", d.name, "name", item.FileItem.FileName)
			}
		case itemTypeVideo:
			// Log but skip video for now.
			logger.Debug("weixin video received (skipped)", "channel", d.name)
		}
	}

	return strings.Join(texts, "\n"), mediaPaths, cleanupPaths
}

// downloadImage downloads an image from CDN and saves it locally.
func (d *Driver) downloadImage(ctx context.Context, img *imageItem, _ string) (string, error) {
	if img.Media == nil && img.URL == "" {
		return "", fmt.Errorf("no image source")
	}

	// Determine AES key (prefer hex aeskey field over media.aes_key).
	aesKeyHex := img.AESKey

	tmpDir, err := os.MkdirTemp("", "clawdex-wx-media-")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}

	filename := "image_" + strconv.FormatInt(time.Now().UnixMilli(), 10) + ".jpg"
	return d.api.downloadMedia(ctx, img.Media, aesKeyHex, tmpDir, filename)
}

// getTypingTicket returns the cached typing ticket for a user, fetching on first use.
func (d *Driver) getTypingTicket(ctx context.Context, userID string) string {
	d.mu.RLock()
	ticket := d.typingTickets[userID]
	d.mu.RUnlock()
	if ticket != "" {
		return ticket
	}

	// Fetch from server.
	contextToken := d.getContextToken(userID)
	resp, err := d.api.getConfig(ctx, userID, contextToken)
	if err != nil {
		logger.Warn("weixin getConfig failed", "channel", d.name, "user", userID, "error", err)
		return ""
	}
	if resp.TypingTicket == "" {
		return ""
	}

	d.mu.Lock()
	d.typingTickets[userID] = resp.TypingTicket
	d.mu.Unlock()
	return resp.TypingTicket
}

// getContextToken returns the cached context token for a user.
func (d *Driver) getContextToken(userID string) string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.contextTokens[userID]
}

// ── Access control ──

type accessResult int

const (
	accessAllowed accessResult = iota
	accessDenied
	accessPairing
)

func (d *Driver) checkAccess(userID string) accessResult {
	d.mu.RLock()
	defer d.mu.RUnlock()

	switch d.cfg.DMPolicy {
	case "open":
		return accessAllowed
	case "allowlist":
		if d.allowFrom[userID] {
			return accessAllowed
		}
		return accessDenied
	default: // "pairing"
		if d.allowFrom[userID] {
			return accessAllowed
		}
		return accessPairing
	}
}

// handlePairing sends a pairing code to an unauthenticated user.
func (d *Driver) handlePairing(ctx context.Context, msg *weixinMessage) {
	senderID := hashStringID(msg.FromUserID)
	code := d.pairingStore.Create(senderID, msg.FromUserID, msg.FromUserID, d.name)

	text := fmt.Sprintf("🔐 Pairing required.\n\nYour code: %s\n\nAsk the admin to approve:\n  clawdex pairing approve %s", code, code)
	d.sendText(ctx, msg.FromUserID, text)
}

// sendText is a convenience method to send a text message to a user.
func (d *Driver) sendText(ctx context.Context, toUserID, text string) {
	contextToken := d.getContextToken(toUserID)
	outMsg := &weixinMessage{
		ToUserID:     toUserID,
		ClientID:     generateClientID(),
		MessageType:  messageTypeBot,
		MessageState: messageStateFinish,
		ItemList: []messageItem{
			{
				Type:     itemTypeText,
				TextItem: &textItem{Text: text},
			},
		},
		ContextToken: contextToken,
	}
	if err := d.api.sendMessage(ctx, outMsg); err != nil {
		logger.Error("weixin send failed", "channel", d.name, "to", toUserID, "error", err)
	}
}

// ── Responder implementation ──

// weixinResponder implements channel.Responder and channel.MediaResponder.
// It maintains a typing keepalive goroutine that sends typing indicators
// every 5 seconds until a reply is delivered.
type weixinResponder struct {
	driver       *Driver
	userID       string
	typingCancel context.CancelFunc // cancels the typing keepalive goroutine
}

const typingKeepaliveInterval = 5 * time.Second

func (r *weixinResponder) Typing(ctx context.Context, _ channel.Message) error {
	ticket := r.driver.getTypingTicket(ctx, r.userID)
	if ticket == "" {
		return nil
	}

	// Send initial typing indicator.
	_ = r.driver.api.sendTyping(ctx, r.userID, ticket)

	// Start keepalive goroutine — resends typing every 5s.
	typingCtx, cancel := context.WithCancel(ctx)
	r.typingCancel = cancel

	go func() {
		ticker := time.NewTicker(typingKeepaliveInterval)
		defer ticker.Stop()
		for {
			select {
			case <-typingCtx.Done():
				return
			case <-ticker.C:
				_ = r.driver.api.sendTyping(typingCtx, r.userID, ticket)
			}
		}
	}()

	return nil
}

// stopTyping cancels the keepalive and sends a cancel-typing signal.
func (r *weixinResponder) stopTyping(ctx context.Context) {
	if r.typingCancel != nil {
		r.typingCancel()
		r.typingCancel = nil
	}
	// Send cancel typing (best-effort, use cached ticket).
	r.driver.mu.RLock()
	ticket := r.driver.typingTickets[r.userID]
	r.driver.mu.RUnlock()
	if ticket != "" {
		_ = r.driver.api.sendTypingCancel(ctx, r.userID, ticket)
	}
}

func (r *weixinResponder) Reply(ctx context.Context, _ channel.Message, text string) error {
	r.stopTyping(ctx)

	if text == "" {
		return nil
	}

	// Apply markdown filter for WeChat readability.
	text = markdownFilter(text)

	// Split long messages into chunks.
	chunks := splitTextChunks(text, r.driver.cfg.TextChunkLimit)
	for _, chunk := range chunks {
		r.driver.sendText(ctx, r.userID, chunk)
	}
	return nil
}

func (r *weixinResponder) ReplyWithMedia(ctx context.Context, _ channel.Message, caption string, filePaths []string) error {
	r.stopTyping(ctx)
	// Send caption text first if present.
	if caption != "" {
		r.driver.sendText(ctx, r.userID, markdownFilter(caption))
	}

	for _, fp := range filePaths {
		mediaType := detectMediaType(fp)
		uploadParam, aesKeyHex, err := r.driver.api.uploadMedia(ctx, fp, r.userID, mediaType)
		if err != nil {
			logger.Warn("weixin media upload failed", "channel", r.driver.name, "file", fp, "error", err)
			r.driver.sendText(ctx, r.userID, "⚠️ 媒体文件上传失败，请稍后重试。")
			continue
		}

		// Build the appropriate media item.
		var item messageItem
		switch mediaType {
		case uploadMediaTypeImage:
			item = messageItem{
				Type: itemTypeImage,
				ImageItem: &imageItem{
					Media:  &cdnMedia{EncryptQueryParam: uploadParam},
					AESKey: aesKeyHex,
				},
			}
		default:
			item = messageItem{
				Type: itemTypeFile,
				FileItem: &fileItem{
					Media:    &cdnMedia{EncryptQueryParam: uploadParam},
					FileName: filepath.Base(fp),
				},
			}
		}

		contextToken := r.driver.getContextToken(r.userID)
		outMsg := &weixinMessage{
			ToUserID:     r.userID,
			ClientID:     generateClientID(),
			MessageType:  messageTypeBot,
			MessageState: messageStateFinish,
			ItemList:     []messageItem{item},
			ContextToken: contextToken,
		}
		if err := r.driver.api.sendMessage(ctx, outMsg); err != nil {
			logger.Warn("weixin media send failed", "channel", r.driver.name, "file", fp, "error", err)
		}
	}
	return nil
}

// ── Helpers ──

// hashStringID hashes a string user ID to int64 via FNV-64a (same as WeCom driver).
func hashStringID(s string) int64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return int64(h.Sum64())
}

// generateClientID creates a unique message client ID.
func generateClientID() string {
	return fmt.Sprintf("clawdex_%d", time.Now().UnixNano())
}

// splitTextChunks splits text into chunks at rune boundaries.
func splitTextChunks(text string, limit int) []string {
	runes := []rune(text)
	if len(runes) <= limit {
		return []string{text}
	}
	var chunks []string
	for i := 0; i < len(runes); i += limit {
		end := min(i+limit, len(runes))
		chunks = append(chunks, string(runes[i:end]))
	}
	return chunks
}

// detectMediaType infers the CDN media type from file extension.
func detectMediaType(filePath string) int {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp":
		return uploadMediaTypeImage
	case ".mp4", ".avi", ".mov", ".mkv":
		return uploadMediaTypeVideo
	case ".amr", ".mp3", ".wav", ".ogg", ".silk":
		return uploadMediaTypeVoice
	default:
		return uploadMediaTypeFile
	}
}
