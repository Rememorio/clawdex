package wecom

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/Rememorio/clawdex/internal/channel"
	"github.com/Rememorio/clawdex/internal/logger"
)

// Compile-time check that Driver implements StreamResponder,
// StreamFinisher, and ThinkingIndicator in WebSocket mode.
var _ channel.StreamResponder = (*Driver)(nil)
var _ channel.StreamFinisher = (*Driver)(nil)
var _ channel.ThinkingIndicator = (*Driver)(nil)
var _ channel.KeyboardResponder = (*Driver)(nil)
var _ channel.SessionCardResponder = (*Driver)(nil)

var streamIDCounter atomic.Uint64

// thinkingMessage is the WeCom thinking placeholder tag.
// The client renders a "thinking" animation when it receives this.
const thinkingMessage = "<think></think>"

func nextStreamID() string {
	id := streamIDCounter.Add(1)
	return fmt.Sprintf("clawdex-stream-%d", id)
}

// SendThinking sends a thinking placeholder via the WeCom stream protocol.
// The client displays a "thinking" animation until the next stream frame
// arrives with real content or the stream is finished.
func (d *Driver) SendThinking(ctx context.Context, msg channel.Message) error {
	if !d.cfg.isWebSocket() {
		return nil
	}

	d.mu.RLock()
	session := d.wsSession
	d.mu.RUnlock()
	if session == nil {
		return fmt.Errorf("wecom thinking: no active websocket session")
	}

	reqIDVal, ok := d.callbackReqIDs.Load(msg.ChatID)
	if !ok {
		return fmt.Errorf(
			"wecom thinking: no callback req_id for chat %d",
			msg.ChatID,
		)
	}
	reqID := reqIDVal.(string)

	streamID := nextStreamID()

	// Store the stream ID early so SendMessage can reuse it.
	d.streamIDs.Store(msg.ChatID, streamID)

	if err := session.send(ctx, wsOutboundFrame{
		Command: wsCommandRespond,
		Headers: wsFrameHeaders{ReqID: reqID},
		Body: wsReplyBody{
			MsgType: "stream",
			Stream: &wsStreamPayload{
				ID:      streamID,
				Finish:  false,
				Content: thinkingMessage,
			},
		},
	}); err != nil {
		// Clean up the stream ID on failure.
		d.streamIDs.Delete(msg.ChatID)
		return fmt.Errorf("wecom thinking send: %w", err)
	}

	logger.Debug("wecom thinking sent",
		"channel", d.name,
		"id", streamID,
		"chat", msg.ChatID,
	)
	return nil
}

// SendMessage sends the first streaming frame via WebSocket.
// If SendThinking was called previously, it reuses the existing stream ID
// so the thinking placeholder is seamlessly replaced with real content.
// Returns a synthetic message ID (stream counter) for EditMessage tracking.
func (d *Driver) SendMessage(ctx context.Context, msg channel.Message, text string) (int64, error) {
	if !d.cfg.isWebSocket() {
		return 0, fmt.Errorf(
			"wecom streaming: only supported in websocket mode",
		)
	}

	d.mu.RLock()
	session := d.wsSession
	d.mu.RUnlock()
	if session == nil {
		return 0, fmt.Errorf(
			"wecom streaming: no active websocket session",
		)
	}

	reqIDVal, ok := d.callbackReqIDs.Load(msg.ChatID)
	if !ok {
		return 0, fmt.Errorf(
			"wecom streaming: no callback req_id for chat %d",
			msg.ChatID,
		)
	}
	reqID := reqIDVal.(string)

	// Reuse existing stream ID from SendThinking, or create a new one.
	var streamID string
	if val, exists := d.streamIDs.Load(msg.ChatID); exists {
		streamID = val.(string)
	} else {
		streamID = nextStreamID()
		d.streamIDs.Store(msg.ChatID, streamID)
	}

	if err := session.send(ctx, wsOutboundFrame{
		Command: wsCommandRespond,
		Headers: wsFrameHeaders{ReqID: reqID},
		Body: wsReplyBody{
			MsgType: "stream",
			Stream: &wsStreamPayload{
				ID:      streamID,
				Finish:  false,
				Content: text,
			},
		},
	}); err != nil {
		return 0, fmt.Errorf("wecom streaming send: %w", err)
	}

	logger.Debug("wecom stream started",
		"channel", d.name,
		"id", streamID,
		"chat", msg.ChatID,
	)

	// Return stream counter as synthetic message ID.
	return int64(streamIDCounter.Load()), nil
}

// EditMessage sends a subsequent streaming frame (snapshot, not finish).
// The gateway's runEditStreaming calls this with accumulated text snapshots.
func (d *Driver) EditMessage(ctx context.Context, chatID int64, _ int64, text string) error {
	if !d.cfg.isWebSocket() {
		return fmt.Errorf("wecom streaming: only supported in websocket mode")
	}

	d.mu.RLock()
	session := d.wsSession
	d.mu.RUnlock()
	if session == nil {
		return fmt.Errorf("wecom streaming: no active websocket session")
	}

	reqIDVal, ok := d.callbackReqIDs.Load(chatID)
	if !ok {
		return fmt.Errorf("wecom streaming: no callback req_id for chat %d", chatID)
	}
	reqID := reqIDVal.(string)

	streamIDVal, ok := d.streamIDs.Load(chatID)
	if !ok {
		return fmt.Errorf("wecom streaming: no stream id for chat %d", chatID)
	}
	streamID := streamIDVal.(string)

	return session.send(ctx, wsOutboundFrame{
		Command: wsCommandRespond,
		Headers: wsFrameHeaders{ReqID: reqID},
		Body: wsReplyBody{
			MsgType: "stream",
			Stream: &wsStreamPayload{
				ID:      streamID,
				Finish:  false,
				Content: text,
			},
		},
	})
}

// FinishStream sends the final stream frame with finish=true and the complete
// response text. WeCom smart bot requires the finish frame to carry the final
// content so the client can dismiss the "searching" indicator properly.
func (d *Driver) FinishStream(ctx context.Context, chatID int64, finalText string) error {
	if !d.cfg.isWebSocket() {
		return nil
	}

	d.mu.RLock()
	session := d.wsSession
	d.mu.RUnlock()
	if session == nil {
		return fmt.Errorf("wecom streaming: no active websocket session")
	}

	reqIDVal, ok := d.callbackReqIDs.Load(chatID)
	if !ok {
		return fmt.Errorf("wecom streaming: no callback req_id for chat %d", chatID)
	}
	reqID := reqIDVal.(string)

	streamIDVal, ok := d.streamIDs.Load(chatID)
	if !ok {
		return fmt.Errorf("wecom streaming: no stream id for chat %d", chatID)
	}
	streamID := streamIDVal.(string)

	// Clean up the stream ID.
	d.streamIDs.Delete(chatID)

	logger.Debug("wecom stream finish", "channel", d.name, "id", streamID, "chat", chatID)

	return session.send(ctx, wsOutboundFrame{
		Command: wsCommandRespond,
		Headers: wsFrameHeaders{ReqID: reqID},
		Body: wsReplyBody{
			MsgType: "stream",
			Stream: &wsStreamPayload{
				ID:      streamID,
				Finish:  true,
				Content: finalText,
			},
		},
	})
}
