package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Rememorio/clawdex/internal/channel"
	"github.com/Rememorio/clawdex/internal/pairing"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeMessageAPI struct {
	replies   []replyCall
	sends     []sendCall
	botOpenID string
	botErr    error
}

type replyCall struct {
	messageID     string
	text          string
	replyInThread bool
}

type sendCall struct {
	chatID string
	text   string
}

func (f *fakeMessageAPI) ReplyText(_ context.Context, messageID, text string, replyInThread bool) error {
	f.replies = append(f.replies, replyCall{
		messageID:     messageID,
		text:          text,
		replyInThread: replyInThread,
	})
	return nil
}

func (f *fakeMessageAPI) SendText(_ context.Context, chatID, text string) error {
	f.sends = append(f.sends, sendCall{chatID: chatID, text: text})
	return nil
}

func (f *fakeMessageAPI) BotOpenID(_ context.Context) (string, error) {
	if f.botErr != nil {
		return "", f.botErr
	}
	return f.botOpenID, nil
}

type captureHandler struct {
	messages   []channel.Message
	responders []channel.Responder
}

func (h *captureHandler) Handle(_ context.Context, msg channel.Message, responder channel.Responder) {
	h.messages = append(h.messages, msg)
	h.responders = append(h.responders, responder)
}

func TestHandleMessageEvent_DMOpen(t *testing.T) {
	api := &fakeMessageAPI{}
	d := New(Config{Name: "fs", DMPolicy: "open"}, nil)
	d.api = api
	h := &captureHandler{}
	d.handler = h

	err := d.handleMessageEvent(context.Background(), newTextEvent("p2p", "hello", nil))
	require.NoError(t, err)

	require.Len(t, h.messages, 1)
	msg := h.messages[0]
	assert.Equal(t, "fs", msg.Channel)
	assert.Equal(t, "hello", msg.Text)
	assert.Equal(t, "", msg.ChatType)
	assert.Equal(t, hashStringID("fs", "oc_chat"), msg.ChatID)
	assert.Equal(t, hashStringID("fs", "om_msg"), msg.MessageID)
	assert.Equal(t, hashStringID("", "ou_user"), msg.SenderID)
	assert.Equal(t, "ou_user", msg.SenderName)
	assert.Empty(t, api.replies)
}

func TestHandleMessageEvent_GroupRequiresMention(t *testing.T) {
	api := &fakeMessageAPI{botOpenID: "ou_bot"}
	d := New(Config{Name: "fs", GroupPolicy: "open"}, nil)
	d.api = api
	h := &captureHandler{}
	d.handler = h

	err := d.handleMessageEvent(context.Background(), newTextEvent("group", "hello", nil))
	require.NoError(t, err)
	assert.Empty(t, h.messages)

	mention := &larkim.MentionEvent{
		Key:           strPtr("@_user_1"),
		Id:            &larkim.UserId{OpenId: strPtr("ou_bot")},
		MentionedType: strPtr("app"),
	}
	err = d.handleMessageEvent(context.Background(), newTextEvent("group", "@_user_1 hello", []*larkim.MentionEvent{mention}))
	require.NoError(t, err)

	require.Len(t, h.messages, 1)
	assert.Equal(t, "group", h.messages[0].ChatType)
	assert.Equal(t, "hello", h.messages[0].Text)
}

func TestHandleMessageEvent_GroupMentionFallback(t *testing.T) {
	api := &fakeMessageAPI{botErr: errors.New("offline")}
	d := New(Config{Name: "fs", GroupPolicy: "open"}, nil)
	d.api = api
	h := &captureHandler{}
	d.handler = h

	mention := &larkim.MentionEvent{
		Key:           strPtr("@_app_1"),
		MentionedType: strPtr("app"),
	}
	err := d.handleMessageEvent(context.Background(), newTextEvent("group", "@_app_1 ping", []*larkim.MentionEvent{mention}))
	require.NoError(t, err)

	require.Len(t, h.messages, 1)
	assert.Equal(t, "ping", h.messages[0].Text)
}

func TestHandleMessageEvent_Pairing(t *testing.T) {
	api := &fakeMessageAPI{}
	d := New(Config{Name: "fs"}, pairing.NewStore(time.Minute))
	d.api = api
	h := &captureHandler{}
	d.handler = h

	err := d.handleMessageEvent(context.Background(), newTextEvent("p2p", "hello", nil))
	require.NoError(t, err)

	assert.Empty(t, h.messages)
	require.Len(t, api.replies, 1)
	assert.Equal(t, "om_msg", api.replies[0].messageID)
	assert.Contains(t, api.replies[0].text, "Your pairing code")
}

func TestResponderReplyChunks(t *testing.T) {
	api := &fakeMessageAPI{}
	d := New(Config{Name: "fs", TextChunkLimit: 3}, nil)
	d.api = api

	r := &responder{driver: d, messageID: "om_msg", chatID: "oc_chat", replyInThread: true}
	err := r.Reply(context.Background(), channel.Message{}, "abcdefg")
	require.NoError(t, err)

	require.Len(t, api.replies, 3)
	assert.Equal(t, []string{"abc", "def", "g"}, []string{
		api.replies[0].text,
		api.replies[1].text,
		api.replies[2].text,
	})
	for _, call := range api.replies {
		assert.Equal(t, "om_msg", call.messageID)
		assert.True(t, call.replyInThread)
	}
}

func TestExtractPostText(t *testing.T) {
	content := `{"zh_cn":{"title":"t","content":[[{"tag":"text","text":"hello"},{"tag":"a","text":"skip"}],[{"tag":"text","text":"world"}]]}}`
	msg := &larkim.EventMessage{MessageType: strPtr("post"), Content: strPtr(content)}
	got := extractMessageText(msg)
	assert.Equal(t, "hello\nworld", got)
}

func TestSplitTextEmpty(t *testing.T) {
	assert.Equal(t, []string{"(empty response)"}, splitText("   ", 10))
}

func newTextEvent(chatType, text string, mentions []*larkim.MentionEvent) *larkim.P2MessageReceiveV1 {
	data, _ := json.Marshal(textContent{Text: text})
	content := string(data)
	return &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId: &larkim.UserId{OpenId: strPtr("ou_user")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr("om_msg"),
				ChatId:      strPtr("oc_chat"),
				ChatType:    strPtr(chatType),
				MessageType: strPtr("text"),
				Content:     strPtr(content),
				Mentions:    mentions,
			},
		},
	}
}

func strPtr(s string) *string {
	return &s
}
