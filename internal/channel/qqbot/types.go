// Package qqbot implements the QQ Bot channel driver for the gateway.
package qqbot

import "encoding/json"

// WebSocket opcodes defined by the QQ Bot Gateway.
const (
	opDispatch       = 0  // Server → Client: event dispatch
	opHeartbeat      = 1  // Client → Server: heartbeat
	opIdentify       = 2  // Client → Server: identify on first connect
	opResume         = 6  // Client → Server: resume interrupted session
	opReconnect      = 7  // Server → Client: please reconnect
	opInvalidSession = 9  // Server → Client: session is invalid
	opHello          = 10 // Server → Client: hello after connect
	opHeartbeatAck   = 11 // Server → Client: heartbeat acknowledged
)

// Intent bits for the Identify payload.
const (
	intentGuilds             = 1 << 0
	intentGuildMembers       = 1 << 1
	intentGuildMessages      = 1 << 9
	intentGuildMessageReact  = 1 << 10
	intentDirectMessage      = 1 << 12
	intentInteraction        = 1 << 26
	intentMessageAudit       = 1 << 27
	intentGroupAndC2CMessage = 1 << 25
)

// defaultIntents is the intent set used by this driver.
// It subscribes to group+C2C messages and interactions.
var defaultIntents = intentGroupAndC2CMessage | intentDirectMessage | intentInteraction

// wsPayload is the top-level WebSocket frame.
type wsPayload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d,omitempty"`
	S  *int            `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
}

// helloData is the payload of op:10 (Hello).
type helloData struct {
	HeartbeatInterval int `json:"heartbeat_interval"` // milliseconds
}

// readyData is the payload of the READY dispatch event.
type readyData struct {
	Version   int    `json:"version"`
	SessionID string `json:"session_id"`
	User      struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Bot      bool   `json:"bot"`
	} `json:"user"`
	Shard []int `json:"shard"`
}

// resumedData is the payload of the RESUMED dispatch event (empty).
type resumedData struct{}

// identifyPayload is sent as op:2 on fresh connections.
type identifyPayload struct {
	Token   string `json:"token"`
	Intents int    `json:"intents"`
	Shard   []int  `json:"shard"`
}

// resumePayload is sent as op:6 to resume an interrupted session.
type resumePayload struct {
	Token     string `json:"token"`
	SessionID string `json:"session_id"`
	Seq       int    `json:"seq"`
}

// ── Event types ──

// c2cMessageEvent represents a C2C_MESSAGE_CREATE dispatch.
type c2cMessageEvent struct {
	ID          string       `json:"id"`
	Author      eventAuthor  `json:"author"`
	Content     string       `json:"content"`
	Timestamp   string       `json:"timestamp"`
	Attachments []attachment `json:"attachments,omitempty"`
}

// groupMessageEvent represents a GROUP_AT_MESSAGE_CREATE dispatch.
type groupMessageEvent struct {
	ID          string       `json:"id"`
	Author      eventAuthor  `json:"author"`
	Content     string       `json:"content"`
	Timestamp   string       `json:"timestamp"`
	GroupOpenID string       `json:"group_openid"`
	Attachments []attachment `json:"attachments,omitempty"`
}

// eventAuthor identifies the sender.
type eventAuthor struct {
	ID           string `json:"id"`
	UserOpenID   string `json:"user_openid"`
	MemberOpenID string `json:"member_openid"`
	UnionOpenID  string `json:"union_openid,omitempty"`
}

// attachment represents a message attachment (image, file, audio).
type attachment struct {
	ContentType string `json:"content_type"`
	URL         string `json:"url"`
	Filename    string `json:"filename,omitempty"`
}

// ── API types ──

// tokenResponse is the response from getAppAccessToken.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   string `json:"expires_in"` // seconds as string
}

// gatewayResponse is the response from GET /gateway.
type gatewayResponse struct {
	URL string `json:"url"`
}

// messageBody is the request body for sending messages.
type messageBody struct {
	Content          string  `json:"content,omitempty"`
	MsgType          int     `json:"msg_type"`
	MsgID            string  `json:"msg_id,omitempty"`
	MsgSeq           int     `json:"msg_seq,omitempty"`
	MessageReference *msgRef `json:"message_reference,omitempty"`
}

// msgRef references a previous message for quoting.
type msgRef struct {
	MessageID string `json:"message_id"`
}

// messageResponse is the response after sending a message.
type messageResponse struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
}

// mediaMessageBody is the request body for sending rich media messages.
type mediaMessageBody struct {
	MsgType int       `json:"msg_type"`
	MsgID   string    `json:"msg_id,omitempty"`
	MsgSeq  int       `json:"msg_seq,omitempty"`
	Media   mediaInfo `json:"media"`
}

// mediaInfo carries the file_info returned by the media upload API.
type mediaInfo struct {
	FileInfo string `json:"file_info"`
}

// mediaUploadResponse is the response from the media upload endpoint.
type mediaUploadResponse struct {
	FileInfo string `json:"file_info"`
	FileUUID string `json:"file_uuid"`
	TTL      int    `json:"ttl"`
}
