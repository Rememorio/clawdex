// Package channel defines the provider-agnostic gateway channel contracts.
package channel

import "context"

// Message is the canonical inbound message delivered by a channel driver.
type Message struct {
	Channel      string
	ChatID       int64
	MessageID    int64
	ThreadID     int64  // forum/topic thread ID (0 = no thread)
	SenderID     int64  // user ID of the message sender (0 = unknown)
	SenderName   string // display name or username of the message sender
	ChatType     string // "group" or "" (DM/default)
	Target       string // channel-native delivery target (chat id, openid, user id)
	Text         string
	MediaPaths   []string // local image paths passed to codex via --image
	CleanupPaths []string // all downloaded media paths (for temp dir cleanup)
}

// DeliveryTarget identifies a channel-native destination for proactive sends.
type DeliveryTarget struct {
	Channel    string `json:"channel"`
	ChatID     int64  `json:"chat_id,omitempty"`
	ThreadID   int64  `json:"thread_id,omitempty"`
	ChatType   string `json:"chat_type,omitempty"`
	Target     string `json:"target,omitempty"`
	SenderID   int64  `json:"sender_id,omitempty"`
	SenderName string `json:"sender_name,omitempty"`
}

// Responder sends channel-native feedback and reply messages.
type Responder interface {
	Typing(ctx context.Context, msg Message) error
	Reply(ctx context.Context, msg Message, text string) error
}

// ProactiveSender sends messages without an active inbound responder.
type ProactiveSender interface {
	Name() string
	SendText(ctx context.Context, target DeliveryTarget, text string) error
}

// Handler processes inbound messages from channel drivers.
type Handler interface {
	Handle(ctx context.Context, msg Message, responder Responder)
}

// KeyboardButton represents an inline keyboard button.
type KeyboardButton struct {
	Text         string
	CallbackData string
}

// KeyboardResponder can reply with an inline keyboard attached.
type KeyboardResponder interface {
	Responder
	ReplyWithKeyboard(ctx context.Context, msg Message, text string, keyboard [][]KeyboardButton) error
}

// SessionCard is a channel-agnostic session card that rich responders can render.
type SessionCard struct {
	Title     string              // card title (e.g. "🧵 Sessions")
	Desc      string              // summary line below the title
	Body      string              // multi-line body text
	Sessions  []SessionCardOption // dropdown options
	CurrentID string              // currently active session thread ID
	Buttons   []SessionCardButton // action buttons
}

// SessionCardOption is one entry in the session dropdown.
type SessionCardOption struct {
	ID    string // thread ID
	Label string // display label
}

// SessionCardButton is an action button on the card.
type SessionCardButton struct {
	Text         string
	CallbackData string
}

// SessionCardResponder can render a rich interactive session card.
// Channels that support template cards (e.g. WeCom button_interaction)
// implement this for a richer session management UX.
type SessionCardResponder interface {
	Responder
	ReplyWithSessionCard(ctx context.Context, msg Message, card SessionCard) error
}

// CardEventHandler processes interactive card button clicks synchronously.
// The driver calls HandleCardEvent when a user taps a card button.
// It returns a SessionCard to render as the card update, or nil to do nothing.
// BuildWelcomeCard is called when a user enters the chat for the first time
// (or after a cooldown) — return nil to skip sending any welcome.
type CardEventHandler interface {
	HandleCardEvent(ctx context.Context, msg Message, eventKey string, selectedID string) *SessionCard
	BuildWelcomeCard(ctx context.Context, msg Message) *SessionCard
}

// StreamResponder can send and edit messages for streaming output.
type StreamResponder interface {
	Responder
	SendMessage(ctx context.Context, msg Message, text string) (sentMessageID int64, err error)
	EditMessage(ctx context.Context, chatID int64, messageID int64, text string) error
}

// StreamFinisher can signal the end of a streaming session.
// Drivers that need an explicit "finish" signal (e.g. WeCom WebSocket stream)
// should implement this interface. The gateway calls FinishStream after the
// final EditMessage. The finalText parameter contains the complete response so
// that drivers requiring the final content in the finish frame can include it.
type StreamFinisher interface {
	FinishStream(ctx context.Context, chatID int64, finalText string) error
}

// ThinkingIndicator can send a "thinking" placeholder before the first
// streaming chunk arrives. Drivers like WeCom WebSocket use this to show
// a "thinking" animation in the client while waiting for AI output.
type ThinkingIndicator interface {
	// SendThinking sends a thinking placeholder for the given message.
	// It returns a stream-scoped ID that subsequent stream frames will
	// reuse, and a cleanup function to close the thinking stream if no
	// real content is produced.
	SendThinking(ctx context.Context, msg Message) error
}

// MediaResponder can reply with file attachments.
type MediaResponder interface {
	Responder
	ReplyWithMedia(ctx context.Context, msg Message, caption string, filePaths []string) error
}

// MediaTextSuppressor can suppress text when media is present.
type MediaTextSuppressor interface {
	SuppressTextWithMedia() bool
}

// StatusReactor can set emoji reactions on messages.
type StatusReactor interface {
	SetReaction(ctx context.Context, chatID, messageID int64, emoji string) error
}

// DraftResponder can use lightweight draft-style streaming with a shorter
// throttle interval than standard edit-based streaming. SendDraft creates or
// updates a streaming message. MaterializeDraft performs the final edit.
type DraftResponder interface {
	StreamResponder
	SendDraft(ctx context.Context, msg Message, text string) (int64, error)
	FinalizeDraft(ctx context.Context, chatID int64, messageID int64, text string) error
}

// Driver is the runtime implementation for one channel provider.
type Driver interface {
	Name() string
	Start(ctx context.Context, handler Handler) error
}
