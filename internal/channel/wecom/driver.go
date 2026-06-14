package wecom

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"hash/fnv"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Rememorio/clawdex/internal/channel"
	"github.com/Rememorio/clawdex/internal/logger"
	"github.com/Rememorio/clawdex/internal/pairing"
)

const (
	defaultTextChunkLimit = 4096
	incomingChanBuffer    = 64
	httpBodyLimit         = 1 << 20 // 1MB
	webhookExpiry         = 2 * time.Hour
	responseURLExpiry     = 1 * time.Hour
	httpClientTimeout     = 30 * time.Second
	uploadHTTPTimeout     = 60 * time.Second
	maxMediaBytes         = 20 * 1024 * 1024 // 20MB
	maxImageBytes         = 2 * 1024 * 1024  // 2MB
)

// GroupRule defines per-group access settings.
type GroupRule struct {
	Enabled   *bool    // nil = enabled; explicit false = disabled
	AllowFrom []string // sender allowlist within this group; empty = all users
}

// Config controls WeCom driver behavior.
type Config struct {
	Name           string
	Token          string
	EncodingAESKey string
	WebhookPath    string               // required for webhook mode
	TextChunkLimit int                  // max UTF-8 bytes per chunk (default 4096)
	DMPolicy       string               // "pairing" (default), "open", "allowlist"
	AllowFrom      []string             // UserID strings
	GroupPolicy    string               // "allowlist" (default), "open", "disabled"
	GroupAllowFrom []string             // group-level chatID allowlist
	Groups         map[string]GroupRule // chatID → rule; "*" = wildcard fallback

	// WebSocket mode fields.
	ConnectionMode    string        // "webhook" (default) or "websocket"
	BotID             string        // required for websocket
	Secret            string        // required for websocket
	WSURL             string        // optional, default wss://openws.work.weixin.qq.com
	HeartbeatInterval time.Duration // optional, default 30s

}

// incomingJob carries data from the HTTP handler to the Start() loop.
type incomingJob struct {
	msg        channel.Message
	webhookURL string
	chatID     string // original string chatID
	senderID   string
}

// webhookEntry caches a per-chat webhook URL with expiry.
type webhookEntry struct {
	url       string
	expiresAt time.Time
}

// Driver is the WeCom channel implementation.
type Driver struct {
	cfg          Config
	name         string
	incoming     chan incomingJob
	webhookCache sync.Map // hashedChatID (int64) → *webhookEntry
	chatIDMap    sync.Map // hashedChatID (int64) → string (original chatID)
	httpHandler  http.HandlerFunc
	pairingStore *pairing.Store
	mu           sync.RWMutex // protects AllowFrom mutations and wsSession

	// WebSocket mode fields.
	wsSession      *wsSession               // current WebSocket session (nil in webhook mode)
	callbackReqIDs sync.Map                 // hashedChatID (int64) → string (most recent callback req_id)
	streamIDs      sync.Map                 // hashedChatID (int64) → string (current stream id)
	handler        channel.Handler          // stored for WebSocket dispatch
	coalescer      *chatCoalescer           // per-chat message coalescer (WS mode only)
	cardHandler    channel.CardEventHandler // handles card button clicks synchronously
}

// New constructs a WeCom driver with the provided configuration.
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

	name := cfg.Name
	if name == "" {
		name = "wecom"
	}

	d := &Driver{
		cfg:          cfg,
		name:         name,
		incoming:     make(chan incomingJob, incomingChanBuffer),
		pairingStore: ps,
	}
	d.httpHandler = d.handleHTTP
	return d
}

// Name returns the driver id.
func (d *Driver) Name() string { return d.name }

// Handler returns the HTTP handler for WeCom webhook route registration.
func (d *Driver) Handler() http.HandlerFunc {
	return d.httpHandler
}

// Start drains the incoming channel and dispatches to the gateway handler.
// In WebSocket mode, it connects to WeCom via WebSocket instead.
func (d *Driver) Start(ctx context.Context, handler channel.Handler) error {
	d.handler = handler

	if d.cfg.isWebSocket() {
		d.coalescer = newChatCoalescer(defaultCoalesceWindow, d.dispatchCoalesced)
		logger.Info("wecom driver started in websocket mode", "channel", d.name, "bot", d.cfg.BotID)
		return d.runWebSocket(ctx)
	}

	logger.Info("wecom driver started on webhook path", "channel", d.name, "path", d.cfg.WebhookPath)
	// Wrap self as webhookResponder so the gateway won't attempt streaming
	// (the full Driver satisfies StreamResponder, which is only valid in WS mode).
	responder := &webhookResponder{d: d}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case job, ok := <-d.incoming:
			if !ok {
				return nil
			}
			handler.Handle(ctx, job.msg, responder)
		}
	}
}

// Typing is a no-op for WeCom (no typing indicator API).
func (d *Driver) Typing(_ context.Context, _ channel.Message) error {
	return nil
}

// SuppressTextWithMedia keeps WeCom media replies to one visible item.
func (d *Driver) SuppressTextWithMedia() bool {
	return true
}

// Reply sends the gateway output back via WeCom webhook or WebSocket.
func (d *Driver) Reply(ctx context.Context, msg channel.Message, text string) error {
	if d.cfg.isWebSocket() {
		return d.replyViaWebSocket(ctx, msg, text)
	}

	webhookURL, chatID := d.lookupWebhook(msg.ChatID)
	if webhookURL == "" {
		return fmt.Errorf("no cached webhook url for chat %d", msg.ChatID)
	}

	chunks := splitByByteLimit(text, d.cfg.TextChunkLimit)
	logger.Debug("wecom reply", "channel", d.name, "chat", chatID, "chunks", len(chunks), "len", len(text))
	for _, chunk := range chunks {
		payload := markdownPayload{
			MsgType:  "markdown",
			Markdown: markdownContent{Content: chunk},
			ChatID:   chatID,
		}
		if err := d.postJSON(ctx, webhookURL, payload); err != nil {
			return fmt.Errorf("wecom reply: %w", err)
		}
	}
	return nil
}

// replyViaWebSocket sends a markdown reply through the active WebSocket session.
// If the chat is in card event mode (responding to a card button click), it wraps
// the text in a button_interaction card and sends via aibot_respond_update_msg.
func (d *Driver) replyViaWebSocket(ctx context.Context, msg channel.Message, text string) error {
	d.mu.RLock()
	session := d.wsSession
	d.mu.RUnlock()

	if session == nil {
		return fmt.Errorf("wecom websocket: no active session")
	}

	reqIDVal, ok := d.callbackReqIDs.Load(msg.ChatID)
	if !ok {
		return fmt.Errorf("wecom websocket: no callback req_id for chat %d", msg.ChatID)
	}
	reqID := reqIDVal.(string)

	chunks := splitByByteLimit(text, d.cfg.TextChunkLimit)
	for _, chunk := range chunks {
		if _, err := session.request(ctx, wsOutboundFrame{
			Command: wsCommandRespond,
			Headers: wsFrameHeaders{ReqID: reqID},
			Body: wsReplyBody{
				MsgType:  "markdown",
				Markdown: &wsMarkdown{Content: chunk},
			},
		}); err != nil {
			return fmt.Errorf("wecom websocket reply: %w", err)
		}
	}
	return nil
}

// ReplyWithKeyboard sends a template card with smart-reply jump buttons,
// followed by the full text as a markdown message via WebSocket. The card is
// sent first so that WeCom renders the markdown (which arrives second) below
// the buttons, preserving visual order.
// In webhook mode it falls back to a plain text reply.
func (d *Driver) ReplyWithKeyboard(ctx context.Context, msg channel.Message, text string, keyboard [][]channel.KeyboardButton) error {
	if !d.cfg.isWebSocket() {
		return d.Reply(ctx, msg, text)
	}

	d.mu.RLock()
	session := d.wsSession
	d.mu.RUnlock()

	if session == nil {
		return fmt.Errorf("wecom websocket: no active session")
	}

	reqIDVal, ok := d.callbackReqIDs.Load(msg.ChatID)
	if !ok {
		return fmt.Errorf("wecom websocket: no callback req_id for chat %d", msg.ChatID)
	}
	reqID := reqIDVal.(string)

	// Build jump_list from keyboard buttons (type 3 = smart reply).
	// WeCom limits jump_list to 3 items max.
	const maxJumpItems = 3
	var jumpList []templateCardJumpAction
	for _, row := range keyboard {
		for _, btn := range row {
			if len(jumpList) >= maxJumpItems {
				break
			}
			jumpList = append(jumpList, templateCardJumpAction{
				Type:     3,
				Title:    btn.Text,
				Question: btn.CallbackData,
			})
		}
		if len(jumpList) >= maxJumpItems {
			break
		}
	}

	// Step 1: send the template card with jump buttons.
	card := &templateCard{
		CardType: templateCardTypeTextNotice,
		MainTitle: &templateCardMainTitle{
			Title: "Quick actions",
		},
		JumpList: jumpList,
		CardAction: &templateCardCardAction{
			Type: 1,
			URL:  "https://work.weixin.qq.com/",
		},
	}

	if err := session.send(ctx, wsOutboundFrame{
		Command: wsCommandRespond,
		Headers: wsFrameHeaders{ReqID: reqID},
		Body: wsReplyBody{
			MsgType:      "template_card",
			TemplateCard: card,
		},
	}); err != nil {
		return fmt.Errorf("wecom keyboard card: %w", err)
	}

	// Step 2: send the full text as markdown (appears below the card).
	return d.replyViaWebSocket(ctx, msg, text)
}

// ReplyWithSessionCard sends a button_interaction template card with a
// dropdown session selector and action buttons. Falls back to plain text
// in webhook mode.
// When responding to a card button click (cardEventMode), it uses
// aibot_respond_update_msg to update the existing card in-place.
func (d *Driver) ReplyWithSessionCard(ctx context.Context, msg channel.Message, card channel.SessionCard) error {
	if !d.cfg.isWebSocket() {
		// Webhook mode: fall back to plain text with the card body.
		return d.Reply(ctx, msg, card.Title+"\n"+card.Desc+"\n\n"+card.Body)
	}

	d.mu.RLock()
	session := d.wsSession
	d.mu.RUnlock()

	if session == nil {
		return fmt.Errorf("wecom websocket: no active session")
	}

	reqIDVal, ok := d.callbackReqIDs.Load(msg.ChatID)
	if !ok {
		return fmt.Errorf("wecom websocket: no callback req_id for chat %d", msg.ChatID)
	}
	reqID := reqIDVal.(string)

	tc := buildSessionTemplateCard(card, msg.ChatID)

	return session.send(ctx, wsOutboundFrame{
		Command: wsCommandRespond,
		Headers: wsFrameHeaders{ReqID: reqID},
		Body: wsReplyBody{
			MsgType:      "template_card",
			TemplateCard: tc,
		},
	})
}

// ReplyWithMedia sends file attachments via WeCom webhook or WebSocket.
// Images (jpg/png) are sent inline as base64; other files are uploaded first.
func (d *Driver) ReplyWithMedia(ctx context.Context, msg channel.Message, caption string, filePaths []string) error {
	if d.cfg.isWebSocket() {
		return d.replyWithWebSocketMedia(ctx, msg, caption, filePaths)
	}

	webhookURL, chatID := d.lookupWebhook(msg.ChatID)
	if webhookURL == "" {
		return fmt.Errorf("no cached webhook url for chat %d", msg.ChatID)
	}

	// Send caption as markdown if non-empty.
	if caption != "" {
		chunks := splitByByteLimit(caption, d.cfg.TextChunkLimit)
		for _, chunk := range chunks {
			payload := markdownPayload{
				MsgType:  "markdown",
				Markdown: markdownContent{Content: chunk},
				ChatID:   chatID,
			}
			if err := d.postJSON(ctx, webhookURL, payload); err != nil {
				logger.Warn("wecom media caption failed", "channel", d.name, "error", err)
			}
		}
	}

	for _, fp := range filePaths {
		ext := strings.ToLower(filepath.Ext(fp))
		switch ext {
		case ".jpg", ".jpeg", ".png":
			if err := d.sendImage(ctx, webhookURL, chatID, fp); err != nil {
				logger.Warn("wecom send image failed", "channel", d.name, "file", fp, "error", err)
			}
		default:
			if err := d.sendFile(ctx, webhookURL, chatID, fp); err != nil {
				logger.Warn("wecom send file failed", "channel", d.name, "file", fp, "error", err)
			}
		}
	}
	return nil
}

// AddAllowedUser appends a user ID to the runtime AllowFrom list.
func (d *Driver) AddAllowedUser(userID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !containsString(d.cfg.AllowFrom, userID) {
		d.cfg.AllowFrom = append(d.cfg.AllowFrom, userID)
	}
}

// SetCardEventHandler registers the handler for card button click events.
// The gateway calls this during setup so the driver can handle card events
// synchronously without going through the async job queue.
func (d *Driver) SetCardEventHandler(h channel.CardEventHandler) {
	d.cardHandler = h
}

// webhookResponder wraps a Driver to expose only the Responder interface,
// hiding StreamResponder so the gateway uses blocking (non-streaming) replies.
type webhookResponder struct {
	d *Driver
}

func (w *webhookResponder) Reply(ctx context.Context, msg channel.Message, text string) error {
	return w.d.Reply(ctx, msg, text)
}

func (w *webhookResponder) Typing(ctx context.Context, msg channel.Message) error {
	return w.d.Typing(ctx, msg)
}

func (w *webhookResponder) SuppressTextWithMedia() bool {
	return true
}

func (w *webhookResponder) ReplyWithMedia(ctx context.Context, msg channel.Message, caption string, filePaths []string) error {
	return w.d.ReplyWithMedia(ctx, msg, caption, filePaths)
}

// ── HTTP handling ──

func (d *Driver) handleHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		d.handleVerify(w, r)
	case http.MethodPost:
		d.handleMessage(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleVerify handles WeCom URL verification (GET).
func (d *Driver) handleVerify(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	msgSig := q.Get("msg_signature")
	timestamp := q.Get("timestamp")
	nonce := q.Get("nonce")
	echostr := q.Get("echostr")

	if msgSig == "" || timestamp == "" || nonce == "" || echostr == "" {
		http.Error(w, "missing parameters", http.StatusBadRequest)
		return
	}

	if !verifySignature(d.cfg.Token, timestamp, nonce, echostr, msgSig) {
		http.Error(w, "signature verification failed", http.StatusForbidden)
		return
	}

	plaintext, err := decrypt(d.cfg.EncodingAESKey, echostr)
	if err != nil {
		logger.Warn("wecom verify decrypt failed", "channel", d.name, "error", err)
		http.Error(w, "decryption failed", http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(plaintext))
}

// handleMessage handles inbound WeCom messages (POST).
// Supports both notification bot (XML envelope + XML body) and
// AI bot (JSON envelope + JSON body) formats.
func (d *Driver) handleMessage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	msgSig := q.Get("msg_signature")
	timestamp := q.Get("timestamp")
	nonce := q.Get("nonce")

	if msgSig == "" || timestamp == "" || nonce == "" {
		http.Error(w, "missing parameters", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, httpBodyLimit))
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}

	// Extract the Encrypt field from the outer envelope.
	// AI bot sends JSON {"encrypt":"..."}, notification bot sends XML <xml><Encrypt>...</Encrypt></xml>.
	encryptedField, envelopeFormat := parseEncryptedEnvelope(body)
	if encryptedField == "" {
		logger.Warn("wecom cannot parse envelope", "channel", d.name, "tried", envelopeFormat)
		http.Error(w, "invalid envelope", http.StatusBadRequest)
		return
	}

	if !verifySignature(d.cfg.Token, timestamp, nonce, encryptedField, msgSig) {
		http.Error(w, "signature verification failed", http.StatusForbidden)
		return
	}

	content, err := decrypt(d.cfg.EncodingAESKey, encryptedField)
	if err != nil {
		logger.Error("wecom message decrypt failed", "channel", d.name, "error", err)
		http.Error(w, "decryption failed", http.StatusForbidden)
		return
	}

	// Respond 200 immediately (async processing).
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("success"))

	// Dispatch based on envelope format:
	// AI bot decrypts to JSON, notification bot decrypts to XML.
	if envelopeFormat == "json" {
		go d.dispatchJSONMessage(content)
	} else {
		go d.dispatchXMLMessage(content)
	}
}

// parseEncryptedEnvelope extracts the Encrypt field from the POST body.
// Returns (encryptedField, format). format is "json" or "xml".
func parseEncryptedEnvelope(body []byte) (string, string) {
	// Try JSON first (AI bot): {"encrypt":"..."}
	var jsonEnv jsonEncryptEnvelope
	if err := json.Unmarshal(body, &jsonEnv); err == nil && jsonEnv.Encrypt != "" {
		return jsonEnv.Encrypt, "json"
	}

	// Fall back to XML (notification bot): <xml><Encrypt>...</Encrypt></xml>
	var xmlEnv xmlEnvelope
	if err := xml.Unmarshal(body, &xmlEnv); err == nil && xmlEnv.Encrypt != "" {
		return xmlEnv.Encrypt, "xml"
	}

	return "", "unknown"
}

// dispatchXMLMessage handles a decrypted XML message (notification bot).
func (d *Driver) dispatchXMLMessage(content string) {
	var msg xmlMessage
	if err := xml.Unmarshal([]byte(content), &msg); err != nil {
		logger.Error("wecom xml message parse failed", "channel", d.name, "error", err)
		return
	}

	logger.Debug("wecom recv [xml]", "channel", d.name, "type", msg.MsgType, "from", msg.From.UserID, "chat", msg.ChatID)

	if msg.MsgType == "event" || msg.WebhookURL == "" {
		return
	}

	text, imageURLs := d.extractContent(&msg)
	if text == "" && len(imageURLs) == 0 {
		logger.Debug("wecom skip empty content", "channel", d.name, "type", msg.MsgType)
		return
	}
	logger.Debug("wecom dispatch [xml]", "channel", d.name, "type", msg.MsgType, "text", text, "images", len(imageURLs))

	hashedChat := hashChatID(d.name, msg.ChatID)
	hashedSender := hashUserID(msg.From.UserID)

	d.webhookCache.Store(hashedChat, &webhookEntry{
		url:       msg.WebhookURL,
		expiresAt: time.Now().Add(webhookExpiry),
	})
	d.chatIDMap.Store(hashedChat, msg.ChatID)

	if msg.ChatType == "group" {
		if !d.resolveGroupAccess(msg.ChatID, msg.From.UserID) {
			return
		}
		text = stripAtMention(text)
	} else {
		switch d.checkAccess(msg.From.UserID) {
		case accessDenied:
			return
		case accessPairing:
			d.handlePairing(msg, hashedSender)
			return
		case accessAllowed:
			// continue
		}
	}

	var mediaPaths []string
	if len(imageURLs) > 0 {
		mediaPaths = d.downloadImages(context.Background(), imageURLs, nil)
	}

	// Append non-image file paths to the text so Codex can access them via tools.
	var cleanupPaths []string
	text, mediaPaths, cleanupPaths = annotateNonImagePaths(text, mediaPaths)
	senderName := wecomSenderName(msg.From.Name, msg.From.Alias, msg.From.UserID)

	d.incoming <- incomingJob{
		msg: channel.Message{
			Channel:      d.Name(),
			ChatID:       hashedChat,
			SenderID:     hashedSender,
			SenderName:   senderName,
			ChatType:     msg.ChatType,
			Target:       msg.ChatID,
			Text:         text,
			MediaPaths:   mediaPaths,
			CleanupPaths: cleanupPaths,
		},
		webhookURL: msg.WebhookURL,
		chatID:     msg.ChatID,
		senderID:   msg.From.UserID,
	}
}

// dispatchJSONMessage handles a decrypted JSON message (AI bot webhook mode).
func (d *Driver) dispatchJSONMessage(content string) {
	var msg wsMessage
	if err := json.Unmarshal([]byte(content), &msg); err != nil {
		logger.Error("wecom json message parse failed", "channel", d.name, "error", err)
		return
	}

	logger.Debug("wecom recv [json]", "channel", d.name, "type", msg.MsgType, "from", msg.From.UserID, "chat", msg.ChatID, "response_url", msg.ResponseURL)

	if msg.MsgType == "event" {
		logger.Debug("wecom skip event", "channel", d.name, "event", msg.Event.EventType)
		return
	}

	text, imageURLs, aesKeys := d.extractWSContent(&msg)
	if text == "" && len(imageURLs) == 0 {
		logger.Debug("wecom skip empty content", "channel", d.name, "type", msg.MsgType)
		return
	}

	hashedChat := hashChatID(d.name, msg.ChatID)
	hashedSender := hashUserID(msg.From.UserID)

	// Cache response_url (AI bot's one-shot reply URL) as webhook URL.
	if msg.ResponseURL != "" {
		d.webhookCache.Store(hashedChat, &webhookEntry{
			url:       msg.ResponseURL,
			expiresAt: time.Now().Add(responseURLExpiry),
		})
	}
	d.chatIDMap.Store(hashedChat, msg.ChatID)

	if msg.ChatType == "group" {
		if !d.resolveGroupAccess(msg.ChatID, msg.From.UserID) {
			return
		}
		text = stripAtMention(text)
	} else {
		switch d.checkAccess(msg.From.UserID) {
		case accessDenied:
			return
		case accessPairing:
			d.handleJSONPairing(msg, hashedSender)
			return
		case accessAllowed:
			// continue
		}
	}

	var mediaPaths []string
	if len(imageURLs) > 0 {
		mediaPaths = d.downloadImages(context.Background(), imageURLs, aesKeys)
	}

	// Append non-image file paths to the text so Codex can access them via tools.
	var cleanupPaths []string
	text, mediaPaths, cleanupPaths = annotateNonImagePaths(text, mediaPaths)
	senderName := wecomSenderName(msg.From.Name, msg.From.Alias, msg.From.UserID)

	d.incoming <- incomingJob{
		msg: channel.Message{
			Channel:      d.Name(),
			ChatID:       hashedChat,
			SenderID:     hashedSender,
			SenderName:   senderName,
			ChatType:     msg.ChatType,
			Target:       msg.ChatID,
			Text:         text,
			MediaPaths:   mediaPaths,
			CleanupPaths: cleanupPaths,
		},
		webhookURL: msg.ResponseURL,
		chatID:     msg.ChatID,
		senderID:   msg.From.UserID,
	}
}

// handleJSONPairing handles pairing for AI bot webhook mode (JSON messages).
func (d *Driver) handleJSONPairing(msg wsMessage, hashedSender int64) {
	if d.pairingStore == nil || msg.ResponseURL == "" {
		return
	}

	username := msg.From.Name
	if username == "" {
		username = msg.From.Alias
	}

	var text string
	if code, pending := d.pairingStore.HasPending(hashedSender, d.Name()); pending {
		text = fmt.Sprintf("Your pairing code is still pending: **%s**\n\nAsk the admin to run: `clawdex pairing approve %s`", code, code)
	} else {
		code := d.pairingStore.Create(hashedSender, msg.From.UserID, username, d.Name())
		text = fmt.Sprintf("Your pairing code: **%s**\n\nAsk the admin to run: `clawdex pairing approve %s`", code, code)
	}

	payload := markdownPayload{
		MsgType:  "markdown",
		Markdown: markdownContent{Content: text},
		ChatID:   msg.ChatID,
	}
	_ = d.postJSON(context.Background(), msg.ResponseURL, payload)
}

func wecomSenderName(name, alias, userID string) string {
	name = strings.TrimSpace(name)
	alias = strings.TrimSpace(alias)
	userID = strings.TrimSpace(userID)

	// When both alias and name are available, combine them like "alias(name)".
	if alias != "" && name != "" && alias != name {
		return alias + "(" + name + ")"
	}
	if name != "" {
		return name
	}
	if alias != "" {
		return alias
	}
	return userID
}

// extractContent returns the text content and image URLs from a WeCom message.
func (d *Driver) extractContent(msg *xmlMessage) (text string, imageURLs []string) {
	switch msg.MsgType {
	case "text":
		return strings.TrimSpace(msg.Content), nil
	case "image":
		if msg.PicURL != "" {
			return "[image]", []string{msg.PicURL}
		}
		return "[image]", nil
	case "mixed":
		return d.extractMixed(msg)
	case "voice":
		return "[voice]", nil
	case "file":
		if msg.FileName != "" {
			return "[file: " + msg.FileName + "]", nil
		}
		return "[file]", nil
	case "link":
		return d.extractLink(msg), nil
	case "location":
		return "[location]", nil
	default:
		return "", nil
	}
}

// extractMixed extracts text and image URLs from a mixed (图文混排) message.
// WeCom mixed messages use <MixedMessage><MsgItem> structure, where each item
// has its own MsgType (text or image).
func (d *Driver) extractMixed(msg *xmlMessage) (string, []string) {
	var texts []string
	var urls []string
	for _, item := range msg.MixedItems {
		switch item.MsgType {
		case "text":
			if t := strings.TrimSpace(item.Content); t != "" {
				texts = append(texts, t)
			}
		case "image":
			if item.PicURL != "" {
				urls = append(urls, item.PicURL)
			}
		}
	}
	// Also try Articles for news-style mixed messages.
	for _, art := range msg.Articles {
		if art.Title != "" {
			texts = append(texts, art.Title)
		}
		if art.Desc != "" {
			texts = append(texts, art.Desc)
		}
		if art.URL != "" {
			texts = append(texts, art.URL)
		}
		if art.PicURL != "" {
			urls = append(urls, art.PicURL)
		}
	}
	text := strings.Join(texts, "\n")
	if text == "" && len(urls) > 0 {
		text = "[image]"
	}
	return text, urls
}

// extractLink extracts content from a link message.
func (d *Driver) extractLink(msg *xmlMessage) string {
	var parts []string
	if msg.Title != "" {
		parts = append(parts, msg.Title)
	}
	if msg.Desc != "" {
		parts = append(parts, msg.Desc)
	}
	if msg.LinkURL != "" {
		parts = append(parts, msg.LinkURL)
	}
	if len(parts) == 0 {
		return "[link]"
	}
	return strings.Join(parts, "\n")
}

// downloadImages downloads image URLs to a temp directory and returns local paths.
func (d *Driver) downloadImages(ctx context.Context, urls []string, aesKeys []string) []string {
	if len(urls) == 0 {
		return nil
	}

	tmpDir, err := os.MkdirTemp("", "clawdex-wecom-media-")
	if err != nil {
		logger.Error("wecom create temp dir failed", "channel", d.name, "error", err)
		return nil
	}

	client := &http.Client{Timeout: httpClientTimeout}
	var paths []string

	for i, u := range urls {
		var perImageKey string
		if i < len(aesKeys) {
			perImageKey = aesKeys[i]
		}
		localPath, err := downloadOneMedia(ctx, client, u, tmpDir, i, perImageKey, d.cfg.EncodingAESKey, d.name)
		if err != nil {
			logger.Error("wecom media download failed", "channel", d.name, "url", u, "error", err)
			continue
		}
		logger.Debug("wecom media download ok", "channel", d.name, "path", localPath)
		paths = append(paths, localPath)
	}

	if len(paths) == 0 {
		os.RemoveAll(tmpDir)
		return nil
	}
	return paths
}

// downloadOneMedia fetches a single media URL and optionally decrypts it.
// WeCom AI Bot (WS mode) returns AES-256-CBC encrypted data instead of raw images.
// Flow: download → detect format → if not image, try decrypt → detect again.
// Non-image files are still saved to disk with an inferred extension.
func downloadOneMedia(ctx context.Context, client *http.Client, mediaURL, destDir string, index int, aesKey, encodingAESKey, logTag string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	logger.Debug("wecom media download", "channel", logTag, "index", index, "content_type", ct, "url", mediaURL)

	// Read entire body into memory (limit 20 MB).
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxMediaBytes))
	if err != nil {
		return "", err
	}

	// If already a valid image, write directly.
	if ext, ok := detectImageFormat(data); ok {
		return writeMediaFile(destDir, index, ext, data)
	}

	// Not a valid image — try AES decryption.
	decrypted, ext, err := tryDecryptImage(data, aesKey, encodingAESKey)
	if err == nil {
		logger.Debug("wecom media decrypted as image", "channel", logTag, "index", index, "format", ext, "before", len(data), "after", len(decrypted))
		return writeMediaFile(destDir, index, ext, decrypted)
	}

	// Decryption didn't yield an image either. Try decrypting and save as raw file.
	rawData := data
	keys := []string{aesKey, encodingAESKey}
	for _, key := range keys {
		if key == "" {
			continue
		}
		if dec, decErr := decryptFileData(key, data); decErr == nil {
			rawData = dec
			logger.Debug("wecom media decrypted (non-image)", "channel", logTag, "index", index, "before", len(data), "after", len(rawData))
			break
		}
	}

	// Save as non-image file with inferred extension.
	ext = inferFileExtension(rawData, ct)
	logger.Debug("wecom media save non-image", "channel", logTag, "index", index, "ext", ext, "size", len(rawData))
	return writeMediaFile(destDir, index, ext, rawData)
}

// tryDecryptImage attempts to decrypt data using per-image aesKey first, then channel encodingAESKey.
func tryDecryptImage(data []byte, aesKey, encodingAESKey string) ([]byte, string, error) {
	keys := []string{aesKey, encodingAESKey}
	for _, key := range keys {
		if key == "" {
			continue
		}
		decrypted, err := decryptFileData(key, data)
		if err != nil {
			continue
		}
		if ext, ok := detectImageFormat(decrypted); ok {
			return decrypted, ext, nil
		}
	}
	return nil, "", fmt.Errorf("no valid image after decryption (tried %d keys)", len(keys))
}

// writeMediaFile writes media data to a file in destDir and returns the path.
func writeMediaFile(destDir string, index int, ext string, data []byte) (string, error) {
	localPath := filepath.Join(destDir, fmt.Sprintf("media_%d%s", index, ext))
	if err := os.WriteFile(localPath, data, 0o644); err != nil {
		return "", err
	}
	return localPath, nil
}

// detectImageFormat checks magic bytes and returns the file extension and true if valid.
func detectImageFormat(data []byte) (ext string, ok bool) {
	if len(data) < 4 {
		return "", false
	}
	switch {
	case data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return ".jpg", true
	case data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47:
		return ".png", true
	case data[0] == 0x47 && data[1] == 0x49 && data[2] == 0x46:
		return ".gif", true
	case len(data) >= 12 &&
		data[0] == 0x52 && data[1] == 0x49 && data[2] == 0x46 && data[3] == 0x46 &&
		data[8] == 0x57 && data[9] == 0x45 && data[10] == 0x42 && data[11] == 0x50:
		return ".webp", true
	}
	return "", false
}

// inferFileExtension guesses a file extension from magic bytes or Content-Type.
// Returns a dot-prefixed extension (e.g. ".pdf") or ".bin" as fallback.
func inferFileExtension(data []byte, contentType string) string {
	// Check magic bytes first.
	if len(data) >= 4 {
		switch {
		case data[0] == 0x25 && data[1] == 0x50 && data[2] == 0x44 && data[3] == 0x46: // %PDF
			return ".pdf"
		case data[0] == 0x50 && data[1] == 0x4B && data[2] == 0x03 && data[3] == 0x04: // PK (zip/docx/xlsx/pptx)
			return ".zip"
		case data[0] == 0xD0 && data[1] == 0xCF && data[2] == 0x11 && data[3] == 0xE0: // OLE2 (doc/xls/ppt)
			return ".doc"
		}
	}

	// Fall back to Content-Type.
	ct := strings.ToLower(contentType)
	if idx := strings.IndexByte(ct, ';'); idx >= 0 {
		ct = strings.TrimSpace(ct[:idx])
	}
	switch ct {
	case "application/pdf":
		return ".pdf"
	case "application/msword":
		return ".doc"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return ".docx"
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return ".xlsx"
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		return ".pptx"
	case "text/plain":
		return ".txt"
	case "text/csv":
		return ".csv"
	case "application/json":
		return ".json"
	case "application/zip":
		return ".zip"
	}

	return ".bin"
}

// isImageExtension returns true if the extension belongs to an image format.
func isImageExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
		return true
	}
	return false
}

type accessResult int

const (
	accessAllowed accessResult = iota
	accessDenied
	accessPairing
)

func (d *Driver) checkAccess(userID string) accessResult {
	d.mu.RLock()
	allowFrom := d.cfg.AllowFrom
	policy := d.cfg.DMPolicy
	d.mu.RUnlock()

	switch policy {
	case "open":
		return accessAllowed
	case "allowlist":
		if containsString(allowFrom, userID) {
			return accessAllowed
		}
		return accessDenied
	default: // "pairing"
		if containsString(allowFrom, userID) {
			return accessAllowed
		}
		return accessPairing
	}
}

func (d *Driver) resolveGroupAccess(chatID, senderID string) bool {
	d.mu.RLock()
	policy := d.cfg.GroupPolicy
	groupAllowFrom := d.cfg.GroupAllowFrom
	groups := d.cfg.Groups
	d.mu.RUnlock()

	switch policy {
	case "disabled":
		return false
	case "open":
		return true
	default: // "allowlist"
		// Layer 1: group-level allowlist (if configured).
		if len(groupAllowFrom) > 0 {
			if !containsString(groupAllowFrom, chatID) && !containsString(groupAllowFrom, "*") {
				return false
			}
		}

		// Layer 2: per-group config from groups map.
		entry, ok := groups[chatID]
		if !ok {
			entry, ok = groups["*"]
		}
		if !ok {
			// No groups entry but passed group-level allowlist → allowed.
			return len(groupAllowFrom) > 0
		}
		if entry.Enabled != nil && !*entry.Enabled {
			return false
		}
		return len(entry.AllowFrom) <= 0 || containsString(entry.AllowFrom, senderID)
	}
}

// stripAtMention removes a leading "@name " prefix from text.
// WeCom prepends "@BotName " when the bot is @mentioned in a group.
func stripAtMention(text string) string {
	if !strings.HasPrefix(text, "@") {
		return text
	}
	if idx := strings.IndexByte(text, ' '); idx > 0 {
		return strings.TrimSpace(text[idx+1:])
	}
	return text
}

func (d *Driver) handlePairing(msg xmlMessage, hashedSender int64) {
	if d.pairingStore == nil {
		return
	}

	webhookURL := msg.WebhookURL
	chatID := msg.ChatID
	username := msg.From.Name
	if username == "" {
		username = msg.From.Alias
	}

	if code, pending := d.pairingStore.HasPending(hashedSender, d.Name()); pending {
		text := fmt.Sprintf("Your pairing code is still pending: **%s**\n\nAsk the admin to run: `clawdex pairing approve %s`", code, code)
		payload := markdownPayload{
			MsgType:  "markdown",
			Markdown: markdownContent{Content: text},
			ChatID:   chatID,
		}
		_ = d.postJSON(context.Background(), webhookURL, payload)
		return
	}

	code := d.pairingStore.Create(hashedSender, msg.From.UserID, username, d.Name())
	text := fmt.Sprintf("Your pairing code: **%s**\n\nAsk the admin to run: `clawdex pairing approve %s`", code, code)
	payload := markdownPayload{
		MsgType:  "markdown",
		Markdown: markdownContent{Content: text},
		ChatID:   chatID,
	}
	_ = d.postJSON(context.Background(), webhookURL, payload)
}

// ── Outbound helpers ──

func (d *Driver) lookupWebhook(hashedChatID int64) (string, string) {
	val, ok := d.webhookCache.Load(hashedChatID)
	if !ok {
		return "", ""
	}
	entry := val.(*webhookEntry)
	if time.Now().After(entry.expiresAt) {
		d.webhookCache.Delete(hashedChatID)
		return "", ""
	}

	var chatID string
	if v, ok := d.chatIDMap.Load(hashedChatID); ok {
		chatID = v.(string)
	}
	return entry.url, chatID
}

// SendText sends a proactive WeCom message. WebSocket mode uses
// aibot_send_msg; webhook mode falls back to the cached response route.
func (d *Driver) SendText(ctx context.Context, target channel.DeliveryTarget, text string) error {
	if d.cfg.isWebSocket() {
		return d.sendTextViaWebSocket(ctx, target, text)
	}

	chatID := target.ChatID
	if chatID == 0 && target.Target != "" {
		chatID = hashChatID(d.name, target.Target)
	}
	if chatID == 0 {
		return fmt.Errorf("wecom proactive send: missing chat id")
	}
	msg := channel.Message{
		Channel:  d.name,
		ChatID:   chatID,
		ChatType: target.ChatType,
		Target:   target.Target,
	}
	return d.Reply(ctx, msg, text)
}

func (d *Driver) sendTextViaWebSocket(
	ctx context.Context,
	target channel.DeliveryTarget,
	text string,
) error {
	d.mu.RLock()
	session := d.wsSession
	d.mu.RUnlock()
	if session == nil {
		return fmt.Errorf("wecom websocket proactive send: no active session")
	}

	chatID := d.proactiveChatID(target)
	if chatID == "" {
		return fmt.Errorf("wecom websocket proactive send: missing target chat id")
	}

	chunks := splitByByteLimit(text, d.cfg.TextChunkLimit)
	for _, chunk := range chunks {
		if _, err := session.request(ctx, wsOutboundFrame{
			Command: wsCommandSend,
			Headers: wsFrameHeaders{
				ReqID: nextWSReqID("send"),
			},
			Body: wsSendBody{
				ChatID:  chatID,
				MsgType: "markdown",
				Markdown: &wsMarkdown{
					Content: chunk,
				},
			},
		}); err != nil {
			return fmt.Errorf("wecom websocket proactive send: %w", err)
		}
	}
	return nil
}

func (d *Driver) proactiveChatID(target channel.DeliveryTarget) string {
	if chatID := normalizeProactiveTarget(target.Target); chatID != "" {
		return chatID
	}
	if target.ChatID == 0 {
		return ""
	}
	if v, ok := d.chatIDMap.Load(target.ChatID); ok {
		if chatID, ok := v.(string); ok {
			return normalizeProactiveTarget(chatID)
		}
	}
	return ""
}

func normalizeProactiveTarget(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	base, _, _ := strings.Cut(value, "?")
	switch {
	case strings.HasPrefix(base, "group:"):
		return strings.TrimSpace(strings.TrimPrefix(base, "group:"))
	case strings.HasPrefix(base, "single:"):
		return strings.TrimSpace(strings.TrimPrefix(base, "single:"))
	default:
		return base
	}
}

func (d *Driver) postJSON(ctx context.Context, webhookURL string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: httpClientTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("wecom api status=%d body=%s", resp.StatusCode, string(respBody))
	}

	var apiResp apiResponse
	if err := json.Unmarshal(respBody, &apiResp); err == nil && apiResp.ErrCode != 0 {
		return fmt.Errorf("wecom api error: %d %s", apiResp.ErrCode, apiResp.ErrMsg)
	}
	return nil
}

// sendImage reads a local image file, encodes it as base64, and sends it.
func (d *Driver) sendImage(ctx context.Context, webhookURL, chatID, filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read image %s: %w", filePath, err)
	}

	// WeCom image limit: 2MB.
	if len(data) > maxImageBytes {
		// Fall back to file upload.
		return d.sendFile(ctx, webhookURL, chatID, filePath)
	}

	hash := md5.Sum(data)
	payload := imagePayload{
		MsgType: "image",
		Image: imageContent{
			Base64: base64.StdEncoding.EncodeToString(data),
			MD5:    fmt.Sprintf("%x", hash),
		},
		ChatID: chatID,
	}
	return d.postJSON(ctx, webhookURL, payload)
}

// sendFile uploads a file via upload_media, then sends the media_id.
func (d *Driver) sendFile(ctx context.Context, webhookURL, chatID, filePath string) error {
	key, err := extractAPIKey(webhookURL)
	if err != nil {
		return err
	}

	mediaID, err := d.uploadMedia(ctx, key, filePath)
	if err != nil {
		return fmt.Errorf("upload media: %w", err)
	}

	payload := filePayload{
		MsgType: "file",
		File:    fileContent{MediaID: mediaID},
		ChatID:  chatID,
	}
	return d.postJSON(ctx, webhookURL, payload)
}

// uploadMedia uploads a file to WeCom and returns the media_id.
func (d *Driver) uploadMedia(ctx context.Context, key, filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	part, err := w.CreateFormFile("media", filepath.Base(filePath))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, f); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}

	uploadURL := fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/webhook/upload_media?key=%s&type=file", key)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	client := &http.Client{Timeout: uploadHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("upload_media status=%d body=%s", resp.StatusCode, string(respBody))
	}

	var result mediaUploadResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parse upload response: %w", err)
	}
	if result.ErrCode != 0 {
		return "", fmt.Errorf("upload error: %d %s", result.ErrCode, result.ErrMsg)
	}
	if result.MediaID == "" {
		return "", fmt.Errorf("upload succeeded but no media_id returned")
	}
	return result.MediaID, nil
}

// ── Utilities ──

// annotateNonImagePaths separates image and non-image media paths.
// Non-image file paths are appended to the text (so Codex can access them via tools),
// and only image paths are returned in imagePaths. allPaths contains every original
// path for temp directory cleanup.
func annotateNonImagePaths(text string, mediaPaths []string) (newText string, imagePaths []string, allPaths []string) {
	if len(mediaPaths) == 0 {
		return text, nil, nil
	}

	var images []string
	var filePaths []string
	for _, p := range mediaPaths {
		ext := strings.ToLower(filepath.Ext(p))
		if isImageExtension(ext) {
			images = append(images, p)
		} else {
			filePaths = append(filePaths, p)
		}
	}

	if len(filePaths) > 0 {
		var sb strings.Builder
		sb.WriteString(text)
		sb.WriteString("\n\nThe following file(s) have been downloaded to local disk. Use the Read tool to view them:")
		for _, fp := range filePaths {
			sb.WriteString("\n- ")
			sb.WriteString(fp)
		}
		text = sb.String()
	}

	return text, images, mediaPaths
}

// hashChatID converts a WeCom string ChatID to an int64 via FNV-64a.
// The driverName is mixed in so that different bot instances produce
// distinct hashed IDs for the same underlying WeCom group.
func hashChatID(driverName, chatID string) int64 {
	h := fnv.New64a()
	h.Write([]byte(driverName))
	h.Write([]byte{0}) // separator
	h.Write([]byte(chatID))
	return int64(h.Sum64())
}

// hashUserID converts a WeCom string UserID to an int64 via FNV-64a.
func hashUserID(userID string) int64 {
	h := fnv.New64a()
	h.Write([]byte(userID))
	return int64(h.Sum64())
}

// splitByByteLimit splits text into chunks that each fit within maxBytes of UTF-8.
func splitByByteLimit(text string, maxBytes int) []string {
	clean := strings.TrimSpace(text)
	if clean == "" {
		return []string{"(empty response)"}
	}
	if len(clean) <= maxBytes {
		return []string{clean}
	}

	var chunks []string
	remaining := clean

	for len(remaining) > 0 {
		if len(remaining) <= maxBytes {
			chunks = append(chunks, remaining)
			break
		}

		// Find the largest prefix that fits within maxBytes.
		// Start at maxBytes (which might be mid-rune) and walk back.
		end := maxBytes
		if end > len(remaining) {
			end = len(remaining)
		}
		// Ensure we don't cut in the middle of a UTF-8 sequence.
		for end > 0 && !isUTF8Start(remaining[end]) {
			end--
		}

		candidate := remaining[:end]

		// Try to split at a natural boundary.
		splitIdx := -1
		half := len(candidate) / 2
		if idx := strings.LastIndex(candidate, "\n\n"); idx > half {
			splitIdx = idx
		} else if idx := strings.LastIndex(candidate, "\n"); idx > half {
			splitIdx = idx
		} else if idx := strings.LastIndex(candidate, " "); idx > half {
			splitIdx = idx
		}

		if splitIdx > 0 {
			chunks = append(chunks, strings.TrimSpace(remaining[:splitIdx]))
			remaining = strings.TrimSpace(remaining[splitIdx:])
		} else {
			chunks = append(chunks, strings.TrimSpace(candidate))
			remaining = strings.TrimSpace(remaining[end:])
		}
	}

	if len(chunks) == 0 {
		return []string{"(empty response)"}
	}
	return chunks
}

// isUTF8Start returns true if b is the start of a UTF-8 character (not a continuation byte).
func isUTF8Start(b byte) bool {
	return b&0xC0 != 0x80
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
