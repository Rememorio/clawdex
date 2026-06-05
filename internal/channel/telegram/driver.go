// Package telegram implements Telegram channel driver for the gateway.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
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
	defaultStartupProbeTimeout = 8 * time.Second
	defaultHTTPTimeout         = 30 * time.Second
	defaultTextChunkLimit      = 3500
	pollErrorRetryDelay        = 2 * time.Second
	pollHTTPTimeoutBuffer      = 5 * time.Second
	telegramCaptionLimit       = 1024
	maxDownloadBytes           = 20 * 1024 * 1024 // 20MB
)

// GroupRule defines per-group access settings for Telegram.
type GroupRule struct {
	Enabled        *bool   // nil = enabled; explicit false = disabled
	AllowFrom      []int64 // sender allowlist within this group; empty = all users
	RequireMention *bool   // nil = true for groups; explicit false = no mention required
}

// Config controls Telegram driver behavior.
type Config struct {
	Name                string
	BotToken            string
	PollTimeout         int
	StartupProbeTimeout time.Duration
	ChunkMode           string              // "length" (default) or "newline"
	TextChunkLimit      int                 // max runes per chunk (default 3500)
	DMPolicy            string              // "open", "pairing" (default), "allowlist"
	AllowFrom           []int64             // user IDs allowed to DM the bot
	GroupPolicy         string              // "allowlist" (default), "open", "disabled"
	GroupAllowFrom      []int64             // group-level chatID allowlist
	Groups              map[int64]GroupRule // chatID → rule; -1 = wildcard fallback
	RequireMention      *bool               // global default for requireMention in groups
}

// Driver is the Telegram channel implementation.
type Driver struct {
	api          *apiClient
	cfg          Config
	name         string
	botID        int64
	botUsername  string
	pairingStore *pairing.Store
	mu           sync.RWMutex // protects AllowFrom mutations
}

// New constructs a Telegram driver with the provided configuration.
func New(cfg Config, ps *pairing.Store) *Driver {
	probeTimeout := cfg.StartupProbeTimeout
	if probeTimeout <= 0 {
		probeTimeout = defaultStartupProbeTimeout
	}
	chunkMode := cfg.ChunkMode
	if chunkMode == "" {
		chunkMode = "length"
	}
	chunkLimit := cfg.TextChunkLimit
	if chunkLimit <= 0 {
		chunkLimit = defaultTextChunkLimit
	}
	dmPolicy := cfg.DMPolicy
	if dmPolicy == "" {
		dmPolicy = "pairing"
	}
	groupPolicy := cfg.GroupPolicy
	if groupPolicy == "" {
		groupPolicy = "allowlist"
	}
	name := cfg.Name
	if name == "" {
		name = "telegram"
	}

	d := &Driver{
		api: &apiClient{
			baseURL:            "https://api.telegram.org/bot" + cfg.BotToken,
			client:             &http.Client{},
			disableLinkPreview: false,
		},
		cfg: Config{
			Name:                cfg.Name,
			BotToken:            cfg.BotToken,
			PollTimeout:         cfg.PollTimeout,
			StartupProbeTimeout: probeTimeout,
			ChunkMode:           chunkMode,
			TextChunkLimit:      chunkLimit,
			DMPolicy:            dmPolicy,
			AllowFrom:           cfg.AllowFrom,
			GroupPolicy:         groupPolicy,
			GroupAllowFrom:      cfg.GroupAllowFrom,
			Groups:              cfg.Groups,
			RequireMention:      cfg.RequireMention,
		},
		name:         name,
		pairingStore: ps,
	}
	return d
}

// Name returns the driver id.
func (d *Driver) Name() string { return d.name }

// Start verifies token state, then enters the polling loop.
func (d *Driver) Start(ctx context.Context, handler channel.Handler) error {
	if err := d.startupProbe(ctx); err != nil {
		return err
	}
	return d.startPolling(ctx, handler)
}

// startPolling enters the long-polling loop.
func (d *Driver) startPolling(ctx context.Context, handler channel.Handler) error {
	var offset int64
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		updates, err := d.api.getUpdates(ctx, offset, d.cfg.PollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			logger.Error("telegram getUpdates failed", "error", err)
			time.Sleep(pollErrorRetryDelay)
			continue
		}

		for _, upd := range updates {
			offset = upd.UpdateID + 1
			d.processUpdate(ctx, upd, handler)
		}
	}
}

// processUpdate handles a single update from polling.
func (d *Driver) processUpdate(ctx context.Context, upd update, handler channel.Handler) {
	// Handle callback queries (inline keyboard button presses).
	if upd.CallbackQuery != nil {
		cq := upd.CallbackQuery
		if cq.Message == nil {
			return
		}
		chatID := cq.Message.Chat.ID
		chatType := cq.Message.Chat.Type
		var senderID int64
		if cq.From != nil {
			senderID = cq.From.ID
		}
		d.logInboundUpdate(
			"callback_query",
			upd.UpdateID,
			chatID,
			chatType,
			senderID,
			cq.Message.MessageID,
			cq.Message.MessageThreadID,
			cq.Data,
			0,
		)
		_ = d.api.answerCallbackQuery(ctx, cq.ID)
		if d.checkAccess(chatType, chatID, senderID) != accessAllowed {
			d.logSkippedUpdate(
				"access_denied",
				"callback_query",
				upd.UpdateID,
				chatID,
				chatType,
				senderID,
				cq.Message.MessageID,
				cq.Message.MessageThreadID,
				cq.Data,
				0,
			)
			return
		}
		handler.Handle(ctx, channel.Message{
			Channel:    d.Name(),
			ChatID:     chatID,
			MessageID:  cq.Message.MessageID,
			ThreadID:   cq.Message.MessageThreadID,
			SenderID:   senderID,
			SenderName: telegramSenderName(cq.From),
			ChatType:   telegramChatType(chatType),
			Text:       cq.Data,
		}, d)
		return
	}

	msg := pickMessage(upd)
	if msg == nil {
		return
	}

	kind := updateKind(upd)
	textPreview := strings.TrimSpace(firstNonEmpty(msg.Text, msg.Caption))
	fileIDs := resolveImageFileIDs(msg)
	var senderID int64
	if msg.From != nil {
		senderID = msg.From.ID
	}
	mediaCount := len(fileIDs)
	d.logInboundUpdate(
		kind,
		upd.UpdateID,
		msg.Chat.ID,
		msg.Chat.Type,
		senderID,
		msg.MessageID,
		msg.MessageThreadID,
		textPreview,
		mediaCount,
	)

	switch d.checkAccess(msg.Chat.Type, msg.Chat.ID, senderID) {
	case accessDenied:
		d.logSkippedUpdate(
			"access_denied",
			kind,
			upd.UpdateID,
			msg.Chat.ID,
			msg.Chat.Type,
			senderID,
			msg.MessageID,
			msg.MessageThreadID,
			textPreview,
			mediaCount,
		)
		return
	case accessPairing:
		d.logSkippedUpdate(
			"pairing_required",
			kind,
			upd.UpdateID,
			msg.Chat.ID,
			msg.Chat.Type,
			senderID,
			msg.MessageID,
			msg.MessageThreadID,
			textPreview,
			mediaCount,
		)
		d.handlePairing(ctx, msg, senderID)
		return
	case accessAllowed:
		// continue normal flow.
	}

	text, respond := d.shouldRespond(msg)
	if !respond {
		d.logSkippedUpdate(
			"mention_required",
			kind,
			upd.UpdateID,
			msg.Chat.ID,
			msg.Chat.Type,
			senderID,
			msg.MessageID,
			msg.MessageThreadID,
			textPreview,
			mediaCount,
		)
		return
	}

	var mediaPaths []string
	if mediaCount > 0 {
		paths, err := d.downloadMedia(ctx, fileIDs)
		if err != nil {
			logger.Error("telegram media download failed",
				"chat_id", msg.Chat.ID, "error", err)
		} else {
			mediaPaths = paths
		}
	}

	if text == "" && len(mediaPaths) == 0 {
		d.logSkippedUpdate(
			"empty_content",
			kind,
			upd.UpdateID,
			msg.Chat.ID,
			msg.Chat.Type,
			senderID,
			msg.MessageID,
			msg.MessageThreadID,
			textPreview,
			mediaCount,
		)
		return
	}
	if text == "" {
		text = mediaPlaceholder(msg)
	}

	handler.Handle(ctx, channel.Message{
		Channel:    d.Name(),
		ChatID:     msg.Chat.ID,
		MessageID:  msg.MessageID,
		ThreadID:   msg.MessageThreadID,
		SenderID:   senderID,
		SenderName: telegramSenderName(msg.From),
		ChatType:   telegramChatType(msg.Chat.Type),
		Text:       text,
		MediaPaths: mediaPaths,
	}, d)
}

func updateKind(upd update) string {
	switch {
	case upd.CallbackQuery != nil:
		return "callback_query"
	case upd.Message != nil:
		return "message"
	case upd.ChannelPost != nil:
		return "channel_post"
	default:
		return "update"
	}
}

func (d *Driver) logInboundUpdate(
	kind string,
	updateID int64,
	chatID int64,
	chatType string,
	senderID int64,
	messageID int64,
	threadID int64,
	text string,
	mediaCount int,
) {
	logger.Info("telegram recv",
		telegramLogFields(d.Name(), kind, updateID, chatID, chatType,
			senderID, messageID, threadID, text, mediaCount)...)
}

func (d *Driver) logSkippedUpdate(
	reason string,
	kind string,
	updateID int64,
	chatID int64,
	chatType string,
	senderID int64,
	messageID int64,
	threadID int64,
	text string,
	mediaCount int,
) {
	fields := []any{"reason", reason}
	fields = append(fields, telegramLogFields(d.Name(), kind, updateID,
		chatID, chatType, senderID, messageID, threadID, text,
		mediaCount)...)
	logger.Info("telegram skip", fields...)
}

func telegramLogFields(
	channelName string,
	kind string,
	updateID int64,
	chatID int64,
	chatType string,
	senderID int64,
	messageID int64,
	threadID int64,
	text string,
	mediaCount int,
) []any {
	fields := []any{
		"channel", channelName,
		"kind", kind,
		"update_id", updateID,
		"chat_id", chatID,
		"chat_type", firstNonEmpty(chatType, "private"),
		"sender_id", senderID,
		"message_id", messageID,
	}
	if threadID > 0 {
		fields = append(fields, "thread_id", threadID)
	}
	cleanText := strings.TrimSpace(text)
	if cleanText != "" {
		fields = append(fields, "text", cleanText)
	}
	if mediaCount > 0 {
		fields = append(fields, "media_count", mediaCount)
	}
	return fields
}

// Typing sends Telegram typing state to indicate processing.
func (d *Driver) Typing(ctx context.Context, msg channel.Message) error {
	return d.api.sendChatAction(ctx, msg.ChatID, "typing", msg.ThreadID)
}

// Reply sends the gateway output back to Telegram, splitting large payloads.
func (d *Driver) Reply(ctx context.Context, msg channel.Message, text string) error {
	for i, chunk := range d.splitText(text) {
		replyTo := int64(0)
		if i == 0 {
			replyTo = msg.MessageID
		}
		if err := d.api.sendMessage(ctx, msg.ChatID, FormatTelegramHTML(chunk), replyTo, msg.ThreadID); err != nil {
			return err
		}
	}
	return nil
}

// ReplyWithKeyboard sends a text reply with an inline keyboard.
func (d *Driver) ReplyWithKeyboard(ctx context.Context, msg channel.Message, text string, keyboard [][]channel.KeyboardButton) error {
	var rows [][]inlineKeyboardButton
	for _, row := range keyboard {
		var r []inlineKeyboardButton
		for _, b := range row {
			r = append(r, inlineKeyboardButton{Text: b.Text, CallbackData: b.CallbackData})
		}
		rows = append(rows, r)
	}
	markup, err := json.Marshal(inlineKeyboardMarkup{InlineKeyboard: rows})
	if err != nil {
		return err
	}
	_, err = d.api.sendMessageAndGetID(ctx, msg.ChatID, FormatTelegramHTML(text), msg.MessageID, markup, msg.ThreadID)
	return err
}

// SendMessage sends a new text message and returns the sent message ID.
func (d *Driver) SendMessage(ctx context.Context, msg channel.Message, text string) (int64, error) {
	return d.api.sendMessageAndGetID(ctx, msg.ChatID, FormatTelegramHTML(text), msg.MessageID, nil, msg.ThreadID)
}

// EditMessage edits an existing message's text.
func (d *Driver) EditMessage(ctx context.Context, chatID int64, messageID int64, text string) error {
	return d.api.editMessageText(ctx, chatID, messageID, FormatTelegramHTML(text), nil)
}

// ReplyWithMedia sends files as photo or document attachments.
func (d *Driver) ReplyWithMedia(ctx context.Context, msg channel.Message, caption string, filePaths []string) error {
	fmtCaption := FormatTelegramHTML(caption)
	for i, fp := range filePaths {
		c := ""
		if i == 0 {
			c = fmtCaption
			// Telegram caption limit is 1024 chars.
			if len([]rune(c)) > telegramCaptionLimit {
				c = string([]rune(c)[:telegramCaptionLimit])
			}
		}
		replyTo := int64(0)
		if i == 0 {
			replyTo = msg.MessageID
		}
		ext := strings.ToLower(filepath.Ext(fp))
		switch ext {
		case ".jpg", ".jpeg", ".png", ".gif", ".webp":
			if err := d.api.sendPhoto(ctx, msg.ChatID, fp, c, replyTo, msg.ThreadID); err != nil {
				return err
			}
		case ".ogg", ".oga":
			if err := d.api.sendVoice(ctx, msg.ChatID, fp, c, replyTo, msg.ThreadID); err != nil {
				return err
			}
		default:
			if err := d.api.sendDocument(ctx, msg.ChatID, fp, c, replyTo, msg.ThreadID); err != nil {
				return err
			}
		}
	}
	// If caption was truncated, send the overflow as a separate text message.
	if len([]rune(fmtCaption)) > telegramCaptionLimit {
		overflow := string([]rune(fmtCaption)[telegramCaptionLimit:])
		if err := d.api.sendMessage(ctx, msg.ChatID, overflow, 0, msg.ThreadID); err != nil {
			return err
		}
	}
	return nil
}

// SetReaction sets an emoji reaction on a message. Best-effort: errors are
// logged but not propagated.
func (d *Driver) SetReaction(ctx context.Context, chatID, messageID int64, emoji string) error {
	err := d.api.setMessageReaction(ctx, chatID, messageID, emoji)
	if err != nil {
		logger.Warn("telegram setReaction failed", "chat", chatID, "msg", messageID, "error", err)
	}
	return err
}

// SendDraft sends or updates a draft streaming message. On the first call
// (when the message doesn't exist yet), it sends a new message and returns its ID.
// On subsequent calls, it edits the existing message.
func (d *Driver) SendDraft(ctx context.Context, msg channel.Message, text string) (int64, error) {
	return d.api.sendMessageAndGetID(ctx, msg.ChatID, FormatTelegramHTML(text), msg.MessageID, nil, msg.ThreadID)
}

// FinalizeDraft performs the final edit on a draft streaming message.
func (d *Driver) FinalizeDraft(ctx context.Context, chatID int64, messageID int64, text string) error {
	return d.api.editMessageText(ctx, chatID, messageID, FormatTelegramHTML(text), nil)
}

// shouldRespond returns the message text and whether the bot should respond.
// For private chats, always respond. For groups, check if mention is required.
func (d *Driver) shouldRespond(msg *message) (string, bool) {
	text := strings.TrimSpace(firstNonEmpty(msg.Text, msg.Caption))
	chatType := msg.Chat.Type

	// Private chat: always respond.
	if chatType == "" || chatType == "private" {
		return text, true
	}

	// Group/supergroup: check if mention is required.
	if d.requireMentionForGroup(msg.Chat.ID) {
		// Check if the bot is mentioned.
		if !d.isBotMentioned(msg) {
			return "", false
		}
		// Strip the mention from the text.
		text = d.stripMention(text)
	}

	return text, true
}

// requireMentionForGroup checks if @mention is required for a specific group.
func (d *Driver) requireMentionForGroup(chatID int64) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// Check per-group config first.
	if entry, ok := d.cfg.Groups[chatID]; ok {
		if entry.RequireMention != nil {
			return *entry.RequireMention
		}
	}
	// Check wildcard fallback.
	if entry, ok := d.cfg.Groups[-1]; ok {
		if entry.RequireMention != nil {
			return *entry.RequireMention
		}
	}
	// Check global default.
	if d.cfg.RequireMention != nil {
		return *d.cfg.RequireMention
	}
	// Default: require mention in groups.
	return true
}

// isBotMentioned checks if the bot is mentioned in the message.
func (d *Driver) isBotMentioned(msg *message) bool {
	// Check entities for mention.
	for _, ent := range msg.Entities {
		if ent.Type == "mention" || ent.Type == "text_mention" {
			// Extract the mention text.
			if ent.Offset >= 0 && ent.Length > 0 && ent.Offset+ent.Length <= len(msg.Text) {
				mention := msg.Text[ent.Offset : ent.Offset+ent.Length]
				// Check if it matches the bot username.
				if strings.EqualFold(mention, "@"+d.botUsername) {
					return true
				}
			}
		}
		// text_mention directly references a user.
		if ent.Type == "text_mention" {
			return true
		}
	}

	// Fallback: check if text starts with @username.
	text := strings.TrimSpace(msg.Text)
	return strings.HasPrefix(strings.ToLower(text), "@"+strings.ToLower(d.botUsername))
}

// stripMention removes the bot mention from the text.
func (d *Driver) stripMention(text string) string {
	// Remove @username from the beginning.
	prefix := "@" + d.botUsername
	if strings.HasPrefix(strings.ToLower(text), strings.ToLower(prefix)) {
		text = strings.TrimSpace(text[len(prefix):])
	}
	return text
}

// telegramChatType maps Telegram chat types to the canonical "group" or "".
func telegramChatType(t string) string {
	switch t {
	case "group", "supergroup":
		return "group"
	default:
		return ""
	}
}

// accessResult indicates the outcome of an access check.
type accessResult int

const (
	accessAllowed accessResult = iota
	accessDenied
	accessPairing
)

// checkAccess determines whether a message from the given sender is allowed.
// For private chats, it checks DMPolicy. For groups, it checks GroupPolicy.
func (d *Driver) checkAccess(chatType string, chatID, senderID int64) accessResult {
	// Handle group/supergroup access.
	if chatType == "group" || chatType == "supergroup" {
		if !d.resolveGroupAccess(chatID, senderID) {
			return accessDenied
		}
		return accessAllowed
	}

	// Private chat: apply DM policy.
	d.mu.RLock()
	allowFrom := d.cfg.AllowFrom
	d.mu.RUnlock()

	switch d.cfg.DMPolicy {
	case "open":
		return accessAllowed
	case "allowlist":
		if containsInt64(allowFrom, senderID) {
			return accessAllowed
		}
		return accessDenied
	default: // "pairing"
		if containsInt64(allowFrom, senderID) {
			return accessAllowed
		}
		return accessPairing
	}
}

// resolveGroupAccess checks if a group message should be processed.
func (d *Driver) resolveGroupAccess(chatID, senderID int64) bool {
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
			if !containsInt64(groupAllowFrom, chatID) && !containsInt64(groupAllowFrom, -1) {
				return false
			}
		}

		// Layer 2: per-group config from groups map.
		entry, ok := groups[chatID]
		if !ok {
			entry, ok = groups[-1] // wildcard fallback (-1 represents "*")
		}
		if !ok {
			// No groups entry but passed group-level allowlist → allowed.
			return len(groupAllowFrom) > 0
		}
		if entry.Enabled != nil && !*entry.Enabled {
			return false
		}
		return len(entry.AllowFrom) <= 0 || containsInt64(entry.AllowFrom, senderID)
	}
}

// handlePairing sends or re-sends a pairing code to an unknown user.
func (d *Driver) handlePairing(ctx context.Context, msg *message, senderID int64) {
	if d.pairingStore == nil {
		return
	}
	username := ""
	if msg.From != nil {
		username = msg.From.Username
	}

	if code, pending := d.pairingStore.HasPending(senderID, d.Name()); pending {
		text := fmt.Sprintf("Your pairing code is still pending: <b>%s</b>\n\nAsk the admin to run: <code>clawdex pairing approve %s</code>", code, code)
		_ = d.api.sendMessage(ctx, msg.Chat.ID, text, msg.MessageID, msg.MessageThreadID)
		return
	}

	code := d.pairingStore.Create(senderID, "", username, d.Name())
	text := fmt.Sprintf("Your pairing code: <b>%s</b>\n\nAsk the admin to run: <code>clawdex pairing approve %s</code>", code, code)
	_ = d.api.sendMessage(ctx, msg.Chat.ID, text, msg.MessageID, msg.MessageThreadID)
}

func telegramSenderName(u *user) string {
	if u == nil {
		return ""
	}
	if u.Username != "" {
		return u.Username
	}
	fullName := strings.TrimSpace(strings.TrimSpace(u.FirstName + " " + u.LastName))
	if fullName != "" {
		return fullName
	}
	return ""
}

// AddAllowedUser appends a user ID to the runtime AllowFrom list.
func (d *Driver) AddAllowedUser(userID int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !containsInt64(d.cfg.AllowFrom, userID) {
		d.cfg.AllowFrom = append(d.cfg.AllowFrom, userID)
	}
}

// SendNotification sends a message to a specific chat ID (used for approval notifications).
func (d *Driver) SendNotification(ctx context.Context, chatID int64, text string) error {
	return d.api.sendMessage(ctx, chatID, text, 0, 0)
}

func containsInt64(ids []int64, id int64) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

func (d *Driver) startupProbe(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, d.cfg.StartupProbeTimeout)
	defer cancel()

	bot, err := d.api.getMe(ctx)
	if err != nil {
		return fmt.Errorf("telegram startup probe failed: %w", err)
	}
	d.botID = bot.ID
	d.botUsername = bot.Username
	if bot.Username != "" {
		logger.Info("telegram bot verified", "username", bot.Username)
	} else {
		logger.Info("telegram bot verified", "id", bot.ID)
	}

	// Clean up any stale webhook.
	if err := d.api.deleteWebhook(ctx); err != nil {
		logger.Warn("telegram webhook cleanup failed", "error", err)
	}

	// Register bot command menu (best-effort).
	commands := []botCommand{
		{Command: "help", Description: "Show available commands"},
		{Command: "new", Description: "Start a fresh conversation"},
		{Command: "sessions", Description: "List recent sessions"},
		{Command: "resume", Description: "Switch to an existing session"},
		{Command: "status", Description: "Show current config and session"},
	}
	if err := d.api.setMyCommands(ctx, commands); err != nil {
		logger.Warn("telegram setMyCommands failed", "error", err)
	}

	return nil
}

type botCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

type updateResponse struct {
	OK          bool     `json:"ok"`
	Result      []update `json:"result"`
	Description string   `json:"description,omitempty"`
}

type update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *message       `json:"message,omitempty"`
	ChannelPost   *message       `json:"channel_post,omitempty"`
	CallbackQuery *callbackQuery `json:"callback_query,omitempty"`
}

type message struct {
	MessageID       int64       `json:"message_id"`
	MessageThreadID int64       `json:"message_thread_id,omitempty"`
	Text            string      `json:"text,omitempty"`
	Caption         string      `json:"caption,omitempty"`
	Chat            chat        `json:"chat"`
	From            *user       `json:"from,omitempty"`
	Entities        []entity    `json:"entities,omitempty"`
	ReplyToMessage  *message    `json:"reply_to_message,omitempty"`
	Photo           []photoSize `json:"photo,omitempty"`
	Document        *document   `json:"document,omitempty"`
	Video           *video      `json:"video,omitempty"`
	VideoNote       *videoNote  `json:"video_note,omitempty"`
	Audio           *audio      `json:"audio,omitempty"`
	Voice           *voice      `json:"voice,omitempty"`
	Sticker         *sticker    `json:"sticker,omitempty"`
}

type chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type,omitempty"`
}

type user struct {
	ID        int64  `json:"id"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
}

type entity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}

type callbackQuery struct {
	ID      string   `json:"id"`
	From    *user    `json:"from,omitempty"`
	Message *message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

type inlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
}

type inlineKeyboardMarkup struct {
	InlineKeyboard [][]inlineKeyboardButton `json:"inline_keyboard"`
}

type sendMessageResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description,omitempty"`
	Result      struct {
		MessageID int64 `json:"message_id"`
	} `json:"result"`
}

type photoSize struct {
	FileID string `json:"file_id"`
}

type document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name,omitempty"`
}

type video struct {
	FileID string `json:"file_id"`
}

type videoNote struct {
	FileID string `json:"file_id"`
}

type audio struct {
	FileID string `json:"file_id"`
}

type voice struct {
	FileID string `json:"file_id"`
}

type sticker struct {
	FileID     string `json:"file_id"`
	IsAnimated bool   `json:"is_animated"`
	IsVideo    bool   `json:"is_video"`
}

type meResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description,omitempty"`
	Result      struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	} `json:"result"`
}

type apiClient struct {
	baseURL            string
	client             *http.Client
	disableLinkPreview bool
}

func (a *apiClient) getMe(ctx context.Context) (*struct {
	ID       int64
	Username string
}, error) {
	respBody, err := a.get(ctx, "/getMe", nil)
	if err != nil {
		return nil, err
	}
	var payload meResponse
	if err := json.Unmarshal(respBody, &payload); err != nil {
		return nil, err
	}
	if !payload.OK {
		return nil, errors.New(firstNonEmpty(payload.Description, "telegram getMe returned ok=false"))
	}
	return &struct {
		ID       int64
		Username string
	}{ID: payload.Result.ID, Username: payload.Result.Username}, nil
}

func (a *apiClient) deleteWebhook(ctx context.Context) error {
	q := url.Values{}
	q.Set("drop_pending_updates", "false")
	_, err := a.get(ctx, "/deleteWebhook", q)
	return err
}

func (a *apiClient) getUpdates(
	ctx context.Context,
	offset int64,
	timeout int,
) ([]update, error) {
	q := url.Values{}
	q.Set("offset", strconv.FormatInt(offset, 10))
	q.Set("timeout", strconv.Itoa(timeout))
	q.Set("allowed_updates", `["message","channel_post","callback_query"]`)

	body, err := a.getWithTimeout(
		ctx,
		"/getUpdates",
		q,
		longPollRequestTimeout(timeout),
	)
	if err != nil {
		return nil, err
	}

	var payload updateResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if !payload.OK {
		return nil, errors.New(firstNonEmpty(payload.Description,
			"telegram getUpdates returned ok=false"))
	}
	return payload.Result, nil
}

func noopCancel() {}

func withRequestTimeout(
	ctx context.Context,
	timeout time.Duration,
) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return ctx, noopCancel
	}
	return context.WithTimeout(ctx, timeout)
}

func longPollRequestTimeout(timeout int) time.Duration {
	if timeout <= 0 {
		return defaultHTTPTimeout
	}
	return time.Duration(timeout)*time.Second + pollHTTPTimeoutBuffer
}

func (a *apiClient) get(
	ctx context.Context,
	path string,
	query url.Values,
) ([]byte, error) {
	return a.getWithTimeout(ctx, path, query, defaultHTTPTimeout)
}

func (a *apiClient) getWithTimeout(
	ctx context.Context,
	path string,
	query url.Values,
	timeout time.Duration,
) ([]byte, error) {
	ctx, cancel := withRequestTimeout(ctx, timeout)
	defer cancel()

	fullURL := a.baseURL + path
	if len(query) > 0 {
		fullURL += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram %s status=%d body=%s", path,
			resp.StatusCode, string(body))
	}
	return body, nil
}

func (a *apiClient) sendChatAction(ctx context.Context, chatID int64, action string, threadID int64) error {
	form := url.Values{}
	form.Set("chat_id", strconv.FormatInt(chatID, 10))
	form.Set("action", action)
	if threadID > 0 {
		form.Set("message_thread_id", strconv.FormatInt(threadID, 10))
	}

	return a.postForm(ctx, "/sendChatAction", form)
}

func (a *apiClient) sendMessage(ctx context.Context, chatID int64, text string, replyTo int64, threadID int64) error {
	_, err := a.sendMessageAndGetID(ctx, chatID, text, replyTo, nil, threadID)
	return err
}

func (a *apiClient) sendMessageAndGetID(ctx context.Context, chatID int64, text string, replyTo int64, replyMarkup []byte, threadID int64) (int64, error) {
	form := url.Values{}
	form.Set("chat_id", strconv.FormatInt(chatID, 10))
	form.Set("text", text)
	form.Set("parse_mode", "HTML")
	if replyTo > 0 {
		form.Set("reply_to_message_id", strconv.FormatInt(replyTo, 10))
	}
	if replyMarkup != nil {
		form.Set("reply_markup", string(replyMarkup))
	}
	if threadID > 0 {
		form.Set("message_thread_id", strconv.FormatInt(threadID, 10))
	}
	if a.disableLinkPreview {
		form.Set("link_preview_options", `{"is_disabled":true}`)
	}
	body, err := a.postFormGetBody(ctx, "/sendMessage", form)
	if err != nil {
		return 0, err
	}
	var resp sendMessageResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, err
	}
	if !resp.OK {
		return 0, errors.New(firstNonEmpty(resp.Description, "telegram sendMessage returned ok=false"))
	}
	return resp.Result.MessageID, nil
}

func (a *apiClient) editMessageText(ctx context.Context, chatID int64, messageID int64, text string, replyMarkup []byte) error {
	form := url.Values{}
	form.Set("chat_id", strconv.FormatInt(chatID, 10))
	form.Set("message_id", strconv.FormatInt(messageID, 10))
	form.Set("text", text)
	form.Set("parse_mode", "HTML")
	if replyMarkup != nil {
		form.Set("reply_markup", string(replyMarkup))
	}
	if a.disableLinkPreview {
		form.Set("link_preview_options", `{"is_disabled":true}`)
	}
	return a.postForm(ctx, "/editMessageText", form)
}

func (a *apiClient) answerCallbackQuery(ctx context.Context, callbackQueryID string) error {
	form := url.Values{}
	form.Set("callback_query_id", callbackQueryID)
	return a.postForm(ctx, "/answerCallbackQuery", form)
}

func (a *apiClient) setMessageReaction(ctx context.Context, chatID, messageID int64, emoji string) error {
	reaction := fmt.Sprintf(`[{"type":"emoji","emoji":"%s"}]`, emoji)
	form := url.Values{}
	form.Set("chat_id", strconv.FormatInt(chatID, 10))
	form.Set("message_id", strconv.FormatInt(messageID, 10))
	form.Set("reaction", reaction)
	return a.postForm(ctx, "/setMessageReaction", form)
}

func (a *apiClient) setMyCommands(ctx context.Context, commands []botCommand) error {
	body := struct {
		Commands []botCommand `json:"commands"`
	}{Commands: commands}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/setMyCommands", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram /setMyCommands status=%d body=%s", resp.StatusCode, string(respBody))
	}
	return nil
}

func (a *apiClient) postFormGetBody(ctx context.Context, path string, form url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram %s status=%d body=%s", path, resp.StatusCode, string(body))
	}
	return body, nil
}

func (a *apiClient) postForm(ctx context.Context, path string, form url.Values) error {
	_, err := a.postFormGetBody(ctx, path, form)
	return err
}

func (a *apiClient) postMultipart(ctx context.Context, apiPath string, chatID int64, filePath, fieldName, caption string, replyTo int64, threadID int64) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	_ = w.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	if caption != "" {
		_ = w.WriteField("caption", caption)
		_ = w.WriteField("parse_mode", "HTML")
	}
	if replyTo > 0 {
		_ = w.WriteField("reply_to_message_id", strconv.FormatInt(replyTo, 10))
	}
	if threadID > 0 {
		_ = w.WriteField("message_thread_id", strconv.FormatInt(threadID, 10))
	}

	filename := filepath.Base(filePath)
	part, err := w.CreateFormFile(fieldName, filename)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, f); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+apiPath, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram %s status=%d body=%s", apiPath, resp.StatusCode, string(body))
	}
	return nil
}

func (a *apiClient) sendPhoto(ctx context.Context, chatID int64, filePath, caption string, replyTo int64, threadID int64) error {
	return a.postMultipart(ctx, "/sendPhoto", chatID, filePath, "photo", caption, replyTo, threadID)
}

func (a *apiClient) sendDocument(ctx context.Context, chatID int64, filePath, caption string, replyTo int64, threadID int64) error {
	return a.postMultipart(ctx, "/sendDocument", chatID, filePath, "document", caption, replyTo, threadID)
}

func (a *apiClient) sendVoice(ctx context.Context, chatID int64, filePath, caption string, replyTo int64, threadID int64) error {
	return a.postMultipart(ctx, "/sendVoice", chatID, filePath, "voice", caption, replyTo, threadID)
}

type getFileResponse struct {
	OK     bool `json:"ok"`
	Result struct {
		FilePath string `json:"file_path"`
	} `json:"result"`
	Description string `json:"description,omitempty"`
}

// getFile calls Telegram getFile API and returns the file_path for downloading.
func (a *apiClient) getFile(ctx context.Context, fileID string) (string, error) {
	q := url.Values{}
	q.Set("file_id", fileID)
	body, err := a.get(ctx, "/getFile", q)
	if err != nil {
		return "", err
	}
	var payload getFileResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	if !payload.OK {
		return "", errors.New(firstNonEmpty(payload.Description, "telegram getFile returned ok=false"))
	}
	return payload.Result.FilePath, nil
}

// downloadFile downloads a file from the Telegram file API to the local
// filesystem and returns the local path.
func (a *apiClient) downloadFile(
	ctx context.Context,
	filePath, destDir string,
) (string, error) {
	ctx, cancel := withRequestTimeout(ctx, defaultHTTPTimeout)
	defer cancel()

	fileURL := a.fileBaseURL() + "/" + filePath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("telegram file download status=%d", resp.StatusCode)
	}

	// Determine a reasonable filename from the filePath.
	name := filepath.Base(filePath)
	if name == "" || name == "." || name == "/" {
		name = "media"
	}
	localPath := filepath.Join(destDir, name)

	f, err := os.Create(localPath)
	if err != nil {
		return "", err
	}
	// Limit to 20 MB (Telegram Bot API limit).
	if _, err := io.Copy(f, io.LimitReader(resp.Body, maxDownloadBytes)); err != nil {
		f.Close()
		os.Remove(localPath)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(localPath)
		return "", err
	}
	return localPath, nil
}

// fileBaseURL returns the Telegram file API base URL derived from the bot API URL.
// e.g. "https://api.telegram.org/bot<token>" → "https://api.telegram.org/file/bot<token>"
func (a *apiClient) fileBaseURL() string {
	return strings.Replace(a.baseURL, "/bot", "/file/bot", 1)
}

// resolveImageFileIDs extracts file IDs for image-like media that codex can
// consume via --image. Non-image media (audio, video, documents) are skipped
// because codex has no way to process them; only a text placeholder is sent.
func resolveImageFileIDs(msg *message) []string {
	var ids []string
	if msg.Sticker != nil && !msg.Sticker.IsAnimated && !msg.Sticker.IsVideo && msg.Sticker.FileID != "" {
		ids = append(ids, msg.Sticker.FileID)
	}
	if len(msg.Photo) > 0 {
		ids = append(ids, msg.Photo[len(msg.Photo)-1].FileID)
	}
	return ids
}

// downloadMedia downloads files for the given file IDs via the Telegram API.
// Creates a temp directory to store files. Returns local file paths.
func (d *Driver) downloadMedia(ctx context.Context, fileIDs []string) ([]string, error) {
	tmpDir, err := os.MkdirTemp("", "clawdex-tg-media-")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}

	var paths []string
	for _, fid := range fileIDs {
		filePath, err := d.api.getFile(ctx, fid)
		if err != nil {
			logger.Error("telegram getFile failed", "file_id", fid, "error", err)
			continue
		}
		if filePath == "" {
			continue
		}
		localPath, err := d.api.downloadFile(ctx, filePath, tmpDir)
		if err != nil {
			logger.Error("telegram download failed", "path", filePath, "error", err)
			continue
		}
		paths = append(paths, localPath)
	}
	if len(paths) == 0 {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("all media downloads failed")
	}
	return paths, nil
}

// mediaPlaceholder returns a text placeholder when a message has media but no text.
func mediaPlaceholder(msg *message) string {
	if len(msg.Photo) > 0 {
		return "[image]"
	}
	if msg.Video != nil || msg.VideoNote != nil {
		return "[video]"
	}
	if msg.Audio != nil || msg.Voice != nil {
		return "[audio]"
	}
	if msg.Document != nil {
		name := msg.Document.FileName
		if name != "" {
			return "[document: " + name + "]"
		}
		return "[document]"
	}
	if msg.Sticker != nil {
		return "[sticker]"
	}
	return "[media]"
}

func pickMessage(upd update) *message {
	if upd.Message != nil {
		return upd.Message
	}
	if upd.ChannelPost != nil {
		return upd.ChannelPost
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// splitText delegates to the configured chunking strategy.
func (d *Driver) splitText(text string) []string {
	if d.cfg.ChunkMode == "newline" {
		return splitByNewline(text, d.cfg.TextChunkLimit)
	}
	return splitByRuneLimit(text, d.cfg.TextChunkLimit)
}

// splitByNewline splits text at paragraph boundaries ("\n\n"), accumulating
// paragraphs into chunks up to the rune limit. Single paragraphs exceeding the
// limit are further split on "\n", then " ", then hard rune split.
func splitByNewline(text string, limit int) []string {
	clean := strings.TrimSpace(text)
	if clean == "" {
		return []string{"(empty response)"}
	}
	if len([]rune(clean)) <= limit {
		return []string{clean}
	}

	paragraphs := strings.Split(clean, "\n\n")
	var chunks []string
	var current strings.Builder

	flush := func() {
		if current.Len() > 0 {
			chunks = append(chunks, strings.TrimSpace(current.String()))
			current.Reset()
		}
	}

	for _, para := range paragraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		if len([]rune(para)) > limit {
			// Paragraph too large — split further.
			flush()
			chunks = append(chunks, splitLargeParagraph(para, limit)...)
			continue
		}
		// Would adding this paragraph exceed the limit?
		sep := ""
		if current.Len() > 0 {
			sep = "\n\n"
		}
		if len([]rune(current.String()))+len([]rune(sep))+len([]rune(para)) > limit {
			flush()
		}
		if current.Len() > 0 {
			current.WriteString("\n\n")
		}
		current.WriteString(para)
	}
	flush()

	if len(chunks) == 0 {
		return []string{"(empty response)"}
	}
	return chunks
}

// splitLargeParagraph breaks a single paragraph that exceeds the limit by
// splitting on newlines, then spaces, then hard rune boundaries.
func splitLargeParagraph(text string, limit int) []string {
	lines := strings.Split(text, "\n")
	var chunks []string
	var current strings.Builder

	for _, line := range lines {
		if len([]rune(line)) > limit {
			// Line too long — split on spaces.
			if current.Len() > 0 {
				chunks = append(chunks, strings.TrimSpace(current.String()))
				current.Reset()
			}
			chunks = append(chunks, splitByWords(line, limit)...)
			continue
		}
		sep := ""
		if current.Len() > 0 {
			sep = "\n"
		}
		if len([]rune(current.String()))+len([]rune(sep))+len([]rune(line)) > limit {
			chunks = append(chunks, strings.TrimSpace(current.String()))
			current.Reset()
		}
		if current.Len() > 0 {
			current.WriteString("\n")
		}
		current.WriteString(line)
	}
	if current.Len() > 0 {
		chunks = append(chunks, strings.TrimSpace(current.String()))
	}
	return chunks
}

// splitByWords splits a line on space boundaries. Words exceeding the limit are
// split with hard rune boundaries.
func splitByWords(text string, limit int) []string {
	words := strings.Fields(text)
	var chunks []string
	var current strings.Builder

	for _, word := range words {
		if len([]rune(word)) > limit {
			if current.Len() > 0 {
				chunks = append(chunks, current.String())
				current.Reset()
			}
			chunks = append(chunks, splitByRuneLimit(word, limit)...)
			continue
		}
		sep := ""
		if current.Len() > 0 {
			sep = " "
		}
		if len([]rune(current.String()))+len([]rune(sep))+len([]rune(word)) > limit {
			chunks = append(chunks, current.String())
			current.Reset()
		}
		if current.Len() > 0 {
			current.WriteString(" ")
		}
		current.WriteString(word)
	}
	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}
	return chunks
}

func splitByRuneLimit(text string, limit int) []string {
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
	parts := make([]string, 0, (len(runes)/limit)+1)
	for start := 0; start < len(runes); start += limit {
		end := min(start+limit, len(runes))
		parts = append(parts, string(runes[start:end]))
	}
	return parts
}
