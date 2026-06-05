package qqbot

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Rememorio/clawdex/internal/logger"
	"github.com/gorilla/websocket"
)

const (
	defaultReconnectDelay = 3 * time.Second
	maxReconnectDelay     = 60 * time.Second
	wsWriteTimeout        = 10 * time.Second
)

// wsConn wraps a WebSocket connection with thread-safe writes.
type wsConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (w *wsConn) writeJSON(v any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	return w.conn.WriteJSON(v)
}

// wsState holds the WebSocket session state for resume.
type wsState struct {
	mu        sync.Mutex
	sessionID string
	lastSeq   int
}

func (s *wsState) update(seq int) {
	s.mu.Lock()
	s.lastSeq = seq
	s.mu.Unlock()
}

func (s *wsState) setSession(id string) {
	s.mu.Lock()
	s.sessionID = id
	s.mu.Unlock()
}

func (s *wsState) get() (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID, s.lastSeq
}

func (s *wsState) reset() {
	s.mu.Lock()
	s.sessionID = ""
	s.lastSeq = 0
	s.mu.Unlock()
}

// connectAndRun establishes the WebSocket connection and processes events.
// It reconnects automatically on disconnection until ctx is cancelled.
func (d *Driver) connectAndRun(ctx context.Context) error {
	state := &wsState{}
	delay := defaultReconnectDelay

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := d.runSession(ctx, state)
		if ctx.Err() != nil {
			return ctx.Err()
		}

		logger.Warn("qqbot websocket disconnected, reconnecting",
			"name", d.name,
			"error", err,
			"delay", delay,
		)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}

		// Exponential backoff.
		delay = delay * 2
		if delay > maxReconnectDelay {
			delay = maxReconnectDelay
		}
	}
}

// runSession connects a single WebSocket session.
func (d *Driver) runSession(ctx context.Context, state *wsState) error {
	gwURL, err := d.api.getGatewayURL()
	if err != nil {
		return fmt.Errorf("get gateway url: %w", err)
	}

	logger.Info("qqbot connecting", "name", d.name, "url", gwURL)

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, gwURL, nil)
	if err != nil {
		return fmt.Errorf("websocket dial: %w", err)
	}
	defer conn.Close()

	ws := &wsConn{conn: conn}
	logger.Info("qqbot websocket connected", "name", d.name)

	// Read first message — should be Hello (op:10).
	var hello wsPayload
	if err := conn.ReadJSON(&hello); err != nil {
		return fmt.Errorf("read hello: %w", err)
	}
	if hello.Op != opHello {
		return fmt.Errorf("expected op:10 hello, got op:%d", hello.Op)
	}

	var helloD helloData
	if err := json.Unmarshal(hello.D, &helloD); err != nil {
		return fmt.Errorf("parse hello data: %w", err)
	}

	// Identify or Resume.
	token, err := d.api.getAccessToken()
	if err != nil {
		return fmt.Errorf("get token for identify: %w", err)
	}

	sessionID, lastSeq := state.get()
	if sessionID != "" {
		// Attempt resume.
		err = ws.writeJSON(wsPayload{
			Op: opResume,
			D: mustMarshal(resumePayload{
				Token:     "QQBot " + token,
				SessionID: sessionID,
				Seq:       lastSeq,
			}),
		})
	} else {
		// Fresh identify.
		err = ws.writeJSON(wsPayload{
			Op: opIdentify,
			D: mustMarshal(identifyPayload{
				Token:   "QQBot " + token,
				Intents: defaultIntents,
				Shard:   []int{0, 1},
			}),
		})
	}
	if err != nil {
		return fmt.Errorf("send identify/resume: %w", err)
	}

	// Start heartbeat.
	heartbeatCtx, heartbeatCancel := context.WithCancel(ctx)
	defer heartbeatCancel()
	go d.heartbeatLoop(heartbeatCtx, ws, helloD.HeartbeatInterval, state)

	// Event loop.
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		var payload wsPayload
		if err := conn.ReadJSON(&payload); err != nil {
			return fmt.Errorf("read frame: %w", err)
		}

		// Update sequence number.
		if payload.S != nil {
			state.update(*payload.S)
		}

		switch payload.Op {
		case opDispatch:
			d.handleDispatch(ctx, payload, state)
			// Reset reconnect delay on successful dispatch.

		case opHeartbeatAck:
			// OK, heartbeat acknowledged.

		case opReconnect:
			logger.Info("qqbot received reconnect request", "name", d.name)
			return fmt.Errorf("server requested reconnect")

		case opInvalidSession:
			logger.Warn("qqbot invalid session, will re-identify", "name", d.name)
			state.reset()
			return fmt.Errorf("invalid session")
		}
	}
}

// heartbeatLoop sends periodic heartbeats.
func (d *Driver) heartbeatLoop(ctx context.Context, ws *wsConn, intervalMS int, state *wsState) {
	interval := time.Duration(intervalMS) * time.Millisecond
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, seq := state.get()
			var seqPtr *int
			if seq > 0 {
				seqPtr = &seq
			}
			if err := ws.writeJSON(wsPayload{Op: opHeartbeat, S: seqPtr}); err != nil {
				logger.Warn("qqbot heartbeat failed", "name", d.name, "error", err)
				return
			}
		}
	}
}

// handleDispatch processes a dispatch event (op:0).
func (d *Driver) handleDispatch(ctx context.Context, payload wsPayload, state *wsState) {
	switch payload.T {
	case "READY":
		var ready readyData
		if err := json.Unmarshal(payload.D, &ready); err != nil {
			logger.Warn("qqbot parse READY failed", "name", d.name, "error", err)
			return
		}
		state.setSession(ready.SessionID)
		logger.Info("qqbot ready",
			"name", d.name,
			"session", ready.SessionID,
			"user", ready.User.Username,
		)

	case "RESUMED":
		logger.Info("qqbot session resumed", "name", d.name)

	case "C2C_MESSAGE_CREATE":
		d.handleC2CMessage(ctx, payload.D)

	case "GROUP_AT_MESSAGE_CREATE":
		d.handleGroupMessage(ctx, payload.D)

	default:
		logger.Debug("qqbot unhandled event", "name", d.name, "type", payload.T)
	}
}

func mustMarshal(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}
