package wecom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Rememorio/clawdex/internal/channel"
	"github.com/Rememorio/clawdex/internal/logger"
	"github.com/gorilla/websocket"
)

const (
	wsCommandSubscribe         = "aibot_subscribe"
	wsCommandPing              = "ping"
	wsCommandRespond           = "aibot_respond_msg"
	wsCommandRespondUpdate     = "aibot_respond_update_msg"
	wsCommandRespondWelcome    = "aibot_respond_welcome_msg"
	wsCommandUploadMediaInit   = "aibot_upload_media_init"
	wsCommandUploadMediaChunk  = "aibot_upload_media_chunk"
	wsCommandUploadMediaFinish = "aibot_upload_media_finish"
	wsCommandMsgCallback       = "aibot_msg_callback"
	wsCommandEventCallback     = "aibot_event_callback"

	defaultWSURL             = "wss://openws.work.weixin.qq.com"
	defaultHeartbeatInterval = 30 * time.Second
	defaultReconnectDelay    = 3 * time.Second
	defaultWSWriteTimeout    = 10 * time.Second
	defaultWSRequestTimeout  = 10 * time.Second

	wsReqIDPrefix = "clawdex"
)

var wsReqIDCounter atomic.Uint64

func nextWSReqID(kind string) string {
	id := wsReqIDCounter.Add(1)
	return fmt.Sprintf("%s-%s-%d", wsReqIDPrefix, kind, id)
}

// wsSession wraps a single WebSocket connection with thread-safe writes.
type wsSession struct {
	conn *websocket.Conn
	mu   sync.Mutex

	ackMu      sync.Mutex
	ackWaiters map[string]chan wsInboundFrame
}

func newWSSession(conn *websocket.Conn) *wsSession {
	return &wsSession{
		conn:       conn,
		ackWaiters: make(map[string]chan wsInboundFrame),
	}
}

// send marshals and writes a frame to the WebSocket connection.
func (s *wsSession) send(ctx context.Context, frame wsOutboundFrame) error {
	if s == nil || s.conn == nil {
		return errors.New("wecom websocket: nil session")
	}
	data, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("wecom websocket: marshal frame: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.conn.SetWriteDeadline(time.Now().Add(defaultWSWriteTimeout)); err != nil {
		return fmt.Errorf("wecom websocket: set write deadline: %w", err)
	}
	if err := s.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return fmt.Errorf("wecom websocket: write frame: %w", err)
	}
	return nil
}

func (s *wsSession) request(ctx context.Context, frame wsOutboundFrame) (wsInboundFrame, error) {
	reqID := strings.TrimSpace(frame.Headers.ReqID)
	if reqID == "" {
		return wsInboundFrame{}, errors.New("wecom websocket: missing req_id")
	}

	ackCh := make(chan wsInboundFrame, 1)
	s.registerAck(reqID, ackCh)
	defer s.unregisterAck(reqID, ackCh)

	if err := s.send(ctx, frame); err != nil {
		return wsInboundFrame{}, err
	}

	timer := time.NewTimer(defaultWSRequestTimeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return wsInboundFrame{}, ctx.Err()
	case <-timer.C:
		return wsInboundFrame{}, errors.New("wecom websocket: wait ack timeout")
	case ack := <-ackCh:
		if ack.ErrCode != 0 {
			return wsInboundFrame{}, fmt.Errorf(
				"wecom websocket ack: %d %s",
				ack.ErrCode,
				strings.TrimSpace(ack.ErrMsg),
			)
		}
		return ack, nil
	}
}

func (s *wsSession) registerAck(reqID string, ackCh chan wsInboundFrame) {
	s.ackMu.Lock()
	defer s.ackMu.Unlock()
	s.ackWaiters[reqID] = ackCh
}

func (s *wsSession) unregisterAck(reqID string, ackCh chan wsInboundFrame) {
	s.ackMu.Lock()
	defer s.ackMu.Unlock()
	if current, ok := s.ackWaiters[reqID]; ok && current == ackCh {
		delete(s.ackWaiters, reqID)
	}
}

func (s *wsSession) deliverAck(frame wsInboundFrame) bool {
	reqID := strings.TrimSpace(frame.Headers.ReqID)
	if reqID == "" {
		return false
	}

	s.ackMu.Lock()
	ackCh, ok := s.ackWaiters[reqID]
	s.ackMu.Unlock()
	if !ok {
		return false
	}

	select {
	case ackCh <- frame:
	default:
	}
	return true
}

// runWebSocket connects to WeCom via WebSocket with auto-reconnect.
func (d *Driver) runWebSocket(ctx context.Context) error {
	reconnectDelay := d.cfg.reconnectDelay()

	for {
		err := d.runWebSocketSession(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			return nil
		}
		logger.Error("wecom websocket session failed", "channel", d.name, "error", err)

		timer := time.NewTimer(reconnectDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// runWebSocketSession runs a single WebSocket session: dial, subscribe, heartbeat, read.
func (d *Driver) runWebSocketSession(ctx context.Context) error {
	wsURL := d.cfg.wsURL()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("wecom websocket: dial: %w", err)
	}
	defer conn.Close()

	session := newWSSession(conn)

	// Subscribe.
	if err := session.send(ctx, wsOutboundFrame{
		Command: wsCommandSubscribe,
		Headers: wsFrameHeaders{ReqID: nextWSReqID("subscribe")},
		Body: wsSubscribeBody{
			BotID:  d.cfg.BotID,
			Secret: d.cfg.Secret,
		},
	}); err != nil {
		return err
	}
	logger.Info("wecom websocket subscribed", "channel", d.name, "bot", d.cfg.BotID)

	// Store session for reply.
	d.mu.Lock()
	d.wsSession = session
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.wsSession = nil
		d.mu.Unlock()
	}()

	// Read loop in goroutine.
	errCh := make(chan error, 1)
	go func() {
		errCh <- d.readWSFrames(ctx, session)
	}()

	// Heartbeat loop.
	interval := d.cfg.heartbeatInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errCh:
			return err
		case <-ticker.C:
			if err := session.send(ctx, wsOutboundFrame{
				Command: wsCommandPing,
				Headers: wsFrameHeaders{ReqID: nextWSReqID("ping")},
			}); err != nil {
				return err
			}
		}
	}
}

// readWSFrames reads frames from the WebSocket and dispatches them.
func (d *Driver) readWSFrames(ctx context.Context, session *wsSession) error {
	for {
		_, data, err := session.conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("wecom websocket: read frame: %w", err)
		}
		if err := d.handleWSFrame(ctx, session, data); err != nil {
			logger.Warn("wecom websocket handle frame failed", "channel", d.name, "error", err)
		}
	}
}

// handleWSFrame processes a single inbound WebSocket frame.
func (d *Driver) handleWSFrame(ctx context.Context, session *wsSession, data []byte) error {
	var frame wsInboundFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		return fmt.Errorf("wecom websocket: unmarshal frame: %w", err)
	}

	if frame.ErrCode != 0 {
		logger.Warn("wecom websocket error",
			"channel", d.name,
			"cmd", frame.Command,
			"req_id", frame.Headers.ReqID,
			"errcode", frame.ErrCode,
			"errmsg", frame.ErrMsg)
	}
	if session.deliverAck(frame) {
		return nil
	}

	switch frame.Command {
	case wsCommandSubscribe:
		if frame.ErrCode == 0 {
			logger.Debug("wecom websocket subscribe confirmed", "channel", d.name)
		}
		return nil
	case wsCommandMsgCallback, wsCommandEventCallback:
		var msg wsMessage
		if err := json.Unmarshal(frame.Body, &msg); err != nil {
			return fmt.Errorf("wecom websocket: unmarshal callback: %w", err)
		}
		if frame.Command == wsCommandEventCallback && msg.MsgType == "" {
			msg.MsgType = "event"
		}

		reqID := frame.Headers.ReqID
		go d.dispatchWSMessage(ctx, session, msg, reqID)
		return nil
	default:
		return nil
	}
}

// dispatchWSMessage converts a wsMessage to channel.Message and dispatches it.
func (d *Driver) dispatchWSMessage(ctx context.Context, session *wsSession, msg wsMessage, reqID string) {
	// Handle template card button click events by converting them to
	// synthetic text commands so the gateway can process them as regular
	// slash commands (e.g. "/sessions:switch" → "/status").
	if msg.MsgType == "event" {
		if msg.Event.EventType == "template_card_event" &&
			msg.Event.TemplateCardEvent != nil {
			d.dispatchTemplateCardEvent(ctx, session, msg, reqID)
			return
		}
		if msg.Event.EventType == "enter_chat" {
			d.dispatchEnterChatEvent(ctx, session, msg, reqID)
			return
		}
		logger.Debug("wecom websocket skip event", "channel", d.name, "event", msg.Event.EventType)
		return
	}

	logger.Debug("wecom recv [ws]",
		"channel", d.name,
		"type", msg.MsgType,
		"from", msg.From.UserID,
		"chat", msg.ChatID,
		"req_id", reqID)

	// Extract text and image URLs.
	text, imageURLs, aesKeys := d.extractWSContent(&msg)
	if text == "" && len(imageURLs) == 0 {
		logger.Debug("wecom skip [ws] empty content", "channel", d.name, "type", msg.MsgType)
		return
	}
	logger.Debug("wecom dispatch [ws]", "channel", d.name, "type", msg.MsgType, "text", text, "images", len(imageURLs))

	hashedChat := hashChatID(d.name, msg.ChatID)
	hashedSender := hashUserID(msg.From.UserID)

	// Store the chatID mapping early (needed even before coalescing).
	d.chatIDMap.Store(hashedChat, msg.ChatID)

	// Access control.
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
			d.handleWSPairing(ctx, session, msg, reqID, hashedSender)
			return
		case accessAllowed:
			// continue
		}
	}

	// Coalesce messages for the same sender within a short window,
	// so that text + file arriving separately still merge into one dispatch.
	senderName := wecomSenderName(msg.From.Name, msg.From.Alias, msg.From.UserID)
	if d.coalescer != nil {
		d.coalescer.add(
			ctx,
			hashedChat,
			hashedSender,
			senderName,
			msg.ChatType,
			reqID,
			text,
			imageURLs,
			aesKeys,
		)
		return
	}

	// Fallback (no coalescer): dispatch immediately.
	d.dispatchCoalesced(ctx, &pendingMsg{
		ctx:          ctx,
		hashedChat:   hashedChat,
		hashedSender: hashedSender,
		senderName:   senderName,
		chatType:     msg.ChatType,
		reqID:        reqID,
		texts:        []string{text},
		mediaURLs:    imageURLs,
		aesKeys:      aesKeys,
	})
}

// dispatchCoalesced handles a coalesced pendingMsg: downloads media, builds a
// channel.Message, and dispatches to the handler.
func (d *Driver) dispatchCoalesced(ctx context.Context, pm *pendingMsg) {
	text := pm.mergedText()

	// Store callback reqID for replies (use the latest one).
	d.callbackReqIDs.Store(pm.hashedChat, pm.reqID)

	// Download media if any.
	var mediaPaths []string
	if len(pm.mediaURLs) > 0 {
		mediaPaths = d.downloadImages(context.Background(), pm.mediaURLs, pm.aesKeys)
	}

	// Append non-image file paths to the text so Codex can access them via tools.
	var cleanupPaths []string
	text, mediaPaths, cleanupPaths = annotateNonImagePaths(text, mediaPaths)

	chMsg := d.buildChannelMessage(
		pm.hashedChat,
		pm.hashedSender,
		pm.senderName,
		pm.chatType,
		text,
		mediaPaths,
	)
	chMsg.CleanupPaths = cleanupPaths

	// In WebSocket mode, dispatch directly to handler (Start loop is busy with WS).
	if d.handler != nil {
		d.handler.Handle(ctx, chMsg, d)
		return
	}

	// Fallback: push to incoming channel.
	d.incoming <- incomingJob{
		msg: chMsg,
	}
}

// extractWSContent extracts text, image URLs, and per-image AES keys from a WebSocket JSON message.
// If the message includes a quoted message (quote field), quoted content is appended.
func (d *Driver) extractWSContent(msg *wsMessage) (text string, imageURLs []string, aesKeys []string) {
	switch msg.MsgType {
	case "text":
		text = strings.TrimSpace(msg.Text.Content)
	case "image":
		text = "[image]"
		if msg.Image.URL != "" {
			imageURLs = append(imageURLs, msg.Image.URL)
			aesKeys = append(aesKeys, msg.Image.AESKey)
		}
	case "voice":
		if msg.Voice.Content != "" {
			text = strings.TrimSpace(msg.Voice.Content)
		} else {
			text = "[voice]"
		}
	case "file":
		text = "[file]"
		if msg.File.URL != "" {
			imageURLs = append(imageURLs, msg.File.URL)
			aesKeys = append(aesKeys, msg.File.AESKey)
		}
	case "video":
		text = "[video]"
		if msg.Video.URL != "" {
			imageURLs = append(imageURLs, msg.Video.URL)
			aesKeys = append(aesKeys, msg.Video.AESKey)
		}
	case "mixed":
		text, imageURLs, aesKeys = d.extractWSMixed(msg)
	default:
		return "", nil, nil
	}

	// Append quoted message content (AI Bot: user quotes a previous message).
	if msg.Quote != nil {
		qt, qu, qk := d.extractWSQuote(msg.Quote)
		if qt != "" {
			if text != "" {
				text = text + "\n[quoted]\n" + qt
			} else {
				text = "[quoted]\n" + qt
			}
		}
		imageURLs = append(imageURLs, qu...)
		aesKeys = append(aesKeys, qk...)
	}

	return text, imageURLs, aesKeys
}

// extractWSMixed extracts text, image URLs, and per-image AES keys from a mixed WebSocket message.
func (d *Driver) extractWSMixed(msg *wsMessage) (string, []string, []string) {
	var texts []string
	var urls []string
	var keys []string
	for _, item := range msg.Mixed.MsgItem {
		switch item.MsgType {
		case "text":
			if t := strings.TrimSpace(item.Text.Content); t != "" {
				texts = append(texts, t)
			}
		case "image":
			if item.Image.URL != "" {
				urls = append(urls, item.Image.URL)
				keys = append(keys, item.Image.AESKey)
			}
		case "file":
			if item.File.URL != "" {
				urls = append(urls, item.File.URL)
				keys = append(keys, item.File.AESKey)
			}
		}
	}
	text := strings.Join(texts, "\n")
	if text == "" && len(urls) > 0 {
		text = "[image]"
	}
	return text, urls, keys
}

// extractWSQuote extracts text, image URLs, and AES keys from a quoted message.
func (d *Driver) extractWSQuote(q *wsQuote) (text string, urls []string, keys []string) {
	switch q.MsgType {
	case "text":
		return strings.TrimSpace(q.Text.Content), nil, nil
	case "image":
		if q.Image.URL != "" {
			return "[image]", []string{q.Image.URL}, []string{q.Image.AESKey}
		}
		return "[image]", nil, nil
	case "voice":
		if q.Voice.Content != "" {
			return strings.TrimSpace(q.Voice.Content), nil, nil
		}
		return "", nil, nil
	case "file":
		if q.File.URL != "" {
			return "[file]", []string{q.File.URL}, []string{q.File.AESKey}
		}
		return "[file]", nil, nil
	case "video":
		if q.Video.URL != "" {
			return "[video]", []string{q.Video.URL}, []string{q.Video.AESKey}
		}
		return "[video]", nil, nil
	case "mixed":
		// Reuse mixed extraction logic via a temporary wsMessage.
		tmp := &wsMessage{MsgType: "mixed", Mixed: q.Mixed}
		return d.extractWSMixed(tmp)
	default:
		return "", nil, nil
	}
}

// dispatchTemplateCardEvent handles a button_interaction card callback.
// It processes the event **synchronously** via the CardEventHandler and
// responds with aibot_respond_update_msg to update the card in-place.
// This bypasses the async job queue so the response arrives within WeCom's
// timeout window.
func (d *Driver) dispatchTemplateCardEvent(ctx context.Context, _ *wsSession, msg wsMessage, reqID string) {
	event := msg.Event.TemplateCardEvent
	eventKey := strings.TrimSpace(event.EventKey)

	logger.Debug("wecom template card event",
		"channel", d.name,
		"event_key", eventKey,
		"task_id", event.TaskID,
		"req_id", reqID,
		"from", msg.From.UserID,
		"chat", msg.ChatID)

	hashedChat := hashChatID(d.name, msg.ChatID)
	hashedSender := hashUserID(msg.From.UserID)
	d.chatIDMap.Store(hashedChat, msg.ChatID)
	d.callbackReqIDs.Store(hashedChat, reqID)

	// Use the CardEventHandler to process the event synchronously.
	if d.cardHandler == nil {
		logger.Debug("wecom no card event handler", "channel", d.name)
		return
	}

	selectedID := selectedTemplateCardOption(event, cardSessionQuestionKey)
	senderName := wecomSenderName(msg.From.Name, msg.From.Alias, msg.From.UserID)
	chMsg := d.buildChannelMessage(
		hashedChat,
		hashedSender,
		senderName,
		msg.ChatType,
		"", // text not needed — the handler uses eventKey
		nil,
	)

	card := d.cardHandler.HandleCardEvent(ctx, chMsg, eventKey, selectedID)
	if card == nil {
		logger.Debug("wecom card event handler returned nil", "channel", d.name, "event_key", eventKey)
		return
	}

	// Build the WeCom template card and send update in-place.
	d.mu.RLock()
	session := d.wsSession
	d.mu.RUnlock()
	if session == nil {
		return
	}

	tc := buildSessionTemplateCard(*card, hashedChat)

	// Reuse the original card's task_id so WeCom can match the update
	// to the existing card. Without this, WeCom returns errcode=-1
	// because it cannot find the card to update.
	if origTaskID := strings.TrimSpace(event.TaskID); origTaskID != "" {
		tc.TaskID = origTaskID
	}

	if err := session.send(ctx, wsOutboundFrame{
		Command: wsCommandRespondUpdate,
		Headers: wsFrameHeaders{ReqID: reqID},
		Body: wsTemplateCardUpdateBody{
			ResponseType: "update_template_card",
			TemplateCard: tc,
		},
	}); err != nil {
		logger.Warn("wecom card event update failed", "channel", d.name, "error", err)
	}
}

// dispatchEnterChatEvent handles the enter_chat event that fires each time a
// user opens or switches into a 1:1 conversation with the bot. It sends a
// welcome card via aibot_respond_welcome_msg, subject to cooldown dedup.
// Group chats are skipped to avoid spamming other members.
func (d *Driver) dispatchEnterChatEvent(ctx context.Context, _ *wsSession, msg wsMessage, reqID string) {
	logger.Debug("wecom enter_chat event",
		"channel", d.name,
		"req_id", reqID,
		"from", msg.From.UserID,
		"chat_type", msg.ChatType,
		"chat", msg.ChatID)

	if msg.ChatType == "group" {
		logger.Debug("wecom skip welcome: group chat", "channel", d.name)
		return
	}
	if d.cardHandler == nil {
		logger.Debug("wecom skip welcome: no card handler", "channel", d.name)
		return
	}

	hashedChat := hashChatID(d.name, msg.ChatID)
	hashedSender := hashUserID(msg.From.UserID)
	d.chatIDMap.Store(hashedChat, msg.ChatID)
	d.callbackReqIDs.Store(hashedChat, reqID)

	senderName := wecomSenderName(msg.From.Name, msg.From.Alias, msg.From.UserID)
	chMsg := d.buildChannelMessage(
		hashedChat,
		hashedSender,
		senderName,
		msg.ChatType,
		"",
		nil,
	)

	card := d.cardHandler.BuildWelcomeCard(ctx, chMsg)
	if card == nil {
		logger.Debug("wecom welcome card builder returned nil", "channel", d.name)
		return
	}

	d.mu.RLock()
	session := d.wsSession
	d.mu.RUnlock()
	if session == nil {
		return
	}

	tc := buildSessionTemplateCard(*card, hashedChat)
	if err := session.send(ctx, wsOutboundFrame{
		Command: wsCommandRespondWelcome,
		Headers: wsFrameHeaders{ReqID: reqID},
		Body: wsReplyBody{
			MsgType:      "template_card",
			TemplateCard: tc,
		},
	}); err != nil {
		logger.Warn("wecom welcome send failed", "channel", d.name, "error", err)
	}
}

// handleWSPairing handles pairing flow via WebSocket reply.
func (d *Driver) handleWSPairing(ctx context.Context, session *wsSession, msg wsMessage, reqID string, hashedSender int64) {
	if d.pairingStore == nil {
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

	_ = session.send(ctx, wsOutboundFrame{
		Command: wsCommandRespond,
		Headers: wsFrameHeaders{ReqID: reqID},
		Body: wsReplyBody{
			MsgType:  "markdown",
			Markdown: &wsMarkdown{Content: text},
		},
	})
}

// buildChannelMessage constructs a channel.Message from common fields.
func (d *Driver) buildChannelMessage(
	hashedChat, hashedSender int64,
	senderName, chatType, text string,
	mediaPaths []string,
) channel.Message {
	var target string
	if v, ok := d.chatIDMap.Load(hashedChat); ok {
		if chatID, ok := v.(string); ok {
			target = chatID
		}
	}
	return channel.Message{
		Channel:    d.Name(),
		ChatID:     hashedChat,
		SenderID:   hashedSender,
		SenderName: senderName,
		ChatType:   chatType,
		Target:     target,
		Text:       text,
		MediaPaths: mediaPaths,
	}
}

// Config helper methods.

func (c *Config) wsURL() string {
	if c.WSURL != "" {
		return c.WSURL
	}
	return defaultWSURL
}

func (c *Config) heartbeatInterval() time.Duration {
	if c.HeartbeatInterval > 0 {
		return c.HeartbeatInterval
	}
	return defaultHeartbeatInterval
}

func (c *Config) reconnectDelay() time.Duration {
	return defaultReconnectDelay
}

func (c *Config) isWebSocket() bool {
	return strings.EqualFold(c.ConnectionMode, "websocket")
}
