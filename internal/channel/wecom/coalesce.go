package wecom

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Rememorio/clawdex/internal/logger"
)

const defaultCoalesceWindow = time.Second

// pendingMsg accumulates content from one or more WS messages from the same
// sender within a short window before dispatching.
type pendingMsg struct {
	ctx          context.Context
	hashedChat   int64
	hashedSender int64
	senderName   string
	chatType     string
	reqID        string   // most recent reqID (used for reply)
	texts        []string // accumulated text fragments
	mediaURLs    []string
	aesKeys      []string
	timer        *time.Timer
}

// mergedText returns accumulated texts joined by newline.
func (pm *pendingMsg) mergedText() string {
	return strings.Join(pm.texts, "\n")
}

// chatCoalescer groups WS messages from the same sender within a short window
// so that text + file messages arriving separately get merged into one dispatch.
type chatCoalescer struct {
	mu       sync.Mutex
	window   time.Duration
	pending  map[string]*pendingMsg
	dispatch func(context.Context, *pendingMsg)
}

// newChatCoalescer creates a coalescer that waits for window before dispatching.
func newChatCoalescer(window time.Duration, dispatchFn func(context.Context, *pendingMsg)) *chatCoalescer {
	return &chatCoalescer{
		window:   window,
		pending:  make(map[string]*pendingMsg),
		dispatch: dispatchFn,
	}
}

// add enqueues a message fragment for coalescing. If this is the first fragment
// for the sender in the chat, a timer is started. If a pending entry already
// exists, the new content is merged and the timer is reset.
func (c *chatCoalescer) add(
	ctx context.Context,
	hashedChat, hashedSender int64,
	senderName, chatType, reqID, text string,
	mediaURLs, aesKeys []string,
) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := coalesceKey(hashedChat, hashedSender)
	pm, ok := c.pending[key]
	if !ok {
		pm = &pendingMsg{
			ctx:          ctx,
			hashedChat:   hashedChat,
			hashedSender: hashedSender,
			senderName:   senderName,
			chatType:     chatType,
			reqID:        reqID,
		}
		if text != "" {
			pm.texts = append(pm.texts, text)
		}
		pm.mediaURLs = append(pm.mediaURLs, mediaURLs...)
		pm.aesKeys = append(pm.aesKeys, aesKeys...)

		pm.timer = time.AfterFunc(c.window, func() {
			c.fire(key)
		})
		c.pending[key] = pm

		logger.Debug("wecom coalescer: new pending",
			"chat", hashedChat,
			"sender", hashedSender,
			"text", text,
			"urls", len(mediaURLs),
		)
		return
	}

	if text != "" {
		pm.texts = append(pm.texts, text)
	}
	if senderName != "" {
		pm.senderName = senderName
	}
	pm.mediaURLs = append(pm.mediaURLs, mediaURLs...)
	pm.aesKeys = append(pm.aesKeys, aesKeys...)
	pm.reqID = reqID
	pm.timer.Reset(c.window)

	logger.Debug("wecom coalescer: merged",
		"chat", hashedChat,
		"sender", hashedSender,
		"texts", len(pm.texts),
		"urls", len(pm.mediaURLs),
	)
}

// fire is called when the timer expires. It removes the pending entry and dispatches.
func (c *chatCoalescer) fire(key string) {
	c.mu.Lock()
	pm, ok := c.pending[key]
	if ok {
		delete(c.pending, key)
	}
	c.mu.Unlock()

	if !ok || pm == nil {
		return
	}

	logger.Debug("wecom coalescer: dispatching",
		"chat", pm.hashedChat,
		"sender", pm.hashedSender,
		"texts", len(pm.texts),
		"urls", len(pm.mediaURLs),
	)
	c.dispatch(pm.ctx, pm)
}

func coalesceKey(hashedChat, hashedSender int64) string {
	return strconv.FormatInt(hashedChat, 10) + ":" +
		strconv.FormatInt(hashedSender, 10)
}
