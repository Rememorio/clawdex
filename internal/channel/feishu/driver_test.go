package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/Rememorio/clawdex/internal/channel"
	"github.com/Rememorio/clawdex/internal/pairing"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeMessageAPI struct {
	replies       []replyCall
	sends         []sendCall
	reactions     []reactionCall
	deletions     []deleteReactionCall
	botOpenID     string
	botErr        error
	reactionErr   error
	deleteErr     error
	nextReactionN int
	resources     map[string][]byte
	resourceCalls []resourceCall
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

type reactionCall struct {
	messageID string
	emojiType string
}

type deleteReactionCall struct {
	messageID  string
	reactionID string
}

type resourceCall struct {
	messageID    string
	fileKey      string
	resourceType string
	destPath     string
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

func (f *fakeMessageAPI) CreateReaction(_ context.Context, messageID, emojiType string) (string, error) {
	f.reactions = append(f.reactions, reactionCall{messageID: messageID, emojiType: emojiType})
	if f.reactionErr != nil {
		return "", f.reactionErr
	}
	f.nextReactionN++
	return "reaction-" + strconv.Itoa(f.nextReactionN), nil
}

func (f *fakeMessageAPI) DeleteReaction(_ context.Context, messageID, reactionID string) error {
	f.deletions = append(f.deletions, deleteReactionCall{messageID: messageID, reactionID: reactionID})
	return f.deleteErr
}

func (f *fakeMessageAPI) DownloadResource(_ context.Context, messageID, fileKey, resourceType, destPath string) error {
	f.resourceCalls = append(f.resourceCalls, resourceCall{
		messageID:    messageID,
		fileKey:      fileKey,
		resourceType: resourceType,
		destPath:     destPath,
	})
	data := f.resources[fileKey]
	if data == nil {
		data = []byte("image:" + fileKey)
	}
	return os.WriteFile(destPath, data, 0o600)
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

func TestHandleMessageEvent_ImageDownload(t *testing.T) {
	api := &fakeMessageAPI{resources: map[string][]byte{"img_key": []byte("image-bytes")}}
	d := New(Config{Name: "fs", DMPolicy: "open"}, nil)
	d.api = api
	h := &captureHandler{}
	d.handler = h

	err := d.handleMessageEvent(context.Background(), newMediaEvent("p2p", "image", `{"image_key":"img_key"}`))
	require.NoError(t, err)

	require.Len(t, h.messages, 1)
	msg := h.messages[0]
	assert.Equal(t, "[image]", msg.Text)
	require.Len(t, msg.MediaPaths, 1)
	require.Equal(t, msg.MediaPaths, msg.CleanupPaths)
	assert.Contains(t, msg.MediaPaths[0], "clawdex-feishu-media-")

	data, err := os.ReadFile(msg.MediaPaths[0])
	require.NoError(t, err)
	assert.Equal(t, []byte("image-bytes"), data)
	require.Equal(t, []resourceCall{{
		messageID:    "om_msg",
		fileKey:      "img_key",
		resourceType: "image",
		destPath:     msg.MediaPaths[0],
	}}, api.resourceCalls)
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

func TestResponderSetReaction_ReplacesPreviousReaction(t *testing.T) {
	api := &fakeMessageAPI{}
	d := New(Config{Name: "fs"}, nil)
	d.api = api

	r := &responder{driver: d, messageID: "om_msg", chatID: "oc_chat"}
	err := r.SetReaction(context.Background(), 1, 10, "👀")
	require.NoError(t, err)
	err = r.SetReaction(context.Background(), 1, 10, "👍")
	require.NoError(t, err)
	err = r.SetReaction(context.Background(), 1, 10, "❌")
	require.NoError(t, err)

	require.Equal(t, []reactionCall{
		{messageID: "om_msg", emojiType: "Typing"},
		{messageID: "om_msg", emojiType: "THUMBSUP"},
		{messageID: "om_msg", emojiType: "ERROR"},
	}, api.reactions)
	require.Equal(t, []deleteReactionCall{
		{messageID: "om_msg", reactionID: "reaction-1"},
		{messageID: "om_msg", reactionID: "reaction-2"},
	}, api.deletions)
}

func TestResponderSetReaction_UnsupportedEmojiNoops(t *testing.T) {
	api := &fakeMessageAPI{}
	d := New(Config{Name: "fs"}, nil)
	d.api = api

	r := &responder{driver: d, messageID: "om_msg", chatID: "oc_chat"}
	err := r.SetReaction(context.Background(), 1, 10, "🫠")
	require.NoError(t, err)

	assert.Empty(t, api.reactions)
	assert.Empty(t, api.deletions)
}

func TestHandleMessageEvent_ResponderSupportsStatusReactor(t *testing.T) {
	api := &fakeMessageAPI{}
	d := New(Config{Name: "fs", DMPolicy: "open"}, nil)
	d.api = api
	h := &captureHandler{}
	d.handler = h

	err := d.handleMessageEvent(context.Background(), newTextEvent("p2p", "hello", nil))
	require.NoError(t, err)

	require.Len(t, h.responders, 1)
	_, ok := h.responders[0].(channel.StatusReactor)
	assert.True(t, ok)
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

func newMediaEvent(chatType, messageType, content string) *larkim.P2MessageReceiveV1 {
	return &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId: &larkim.UserId{OpenId: strPtr("ou_user")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr("om_msg"),
				ChatId:      strPtr("oc_chat"),
				ChatType:    strPtr(chatType),
				MessageType: strPtr(messageType),
				Content:     strPtr(content),
			},
		},
	}
}

func strPtr(s string) *string {
	return &s
}
