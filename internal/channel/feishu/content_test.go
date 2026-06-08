package feishu

import (
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/stretchr/testify/assert"
)

func TestExtractMessageText_PostRichContent(t *testing.T) {
	content := `{"post":{"zh_cn":{"title":"Release","content":[[{"tag":"text","text":"hello","style":{"bold":true}},{"tag":"text","text":" "},{"tag":"a","text":"docs","href":"https://example.com"},{"tag":"text","text":" "},{"tag":"at","user_name":"Alice"},{"tag":"text","text":" "},{"tag":"img","image_key":"img_1"}],[{"tag":"code_block","language":"go","text":"fmt.Println(1)"}]]}}}`

	msg := &larkim.EventMessage{MessageType: strPtr("post"), Content: strPtr(content)}
	got := extractMessageText(msg)

	assert.Equal(t, "Release\n\n**hello** [docs](https://example.com) @Alice [image]\n```go\nfmt.Println(1)\n```", got)
	assert.Equal(t, []string{"img_1"}, extractImageResourceKeys("post", content))
}

func TestExtractMessageText_PostFallback(t *testing.T) {
	msg := &larkim.EventMessage{MessageType: strPtr("post"), Content: strPtr(`{"bad":true}`)}
	assert.Equal(t, fallbackPostText, extractMessageText(msg))
	assert.Empty(t, extractImageResourceKeys("post", `{"bad":true}`))
}

func TestExtractMessageText_MediaTypes(t *testing.T) {
	tests := []struct {
		name        string
		messageType string
		content     string
		want        string
	}{
		{name: "audio speech", messageType: "audio", content: `{"speech_to_text":"  hello from voice  "}`, want: "hello from voice"},
		{name: "file name", messageType: "file", content: `{"file_name":"report.pdf"}`, want: "[file: report.pdf]"},
		{name: "image", messageType: "image", content: `{"image_key":"img_direct"}`, want: "[image]"},
		{name: "share chat body", messageType: "share_chat", content: `{"body":"shared context"}`, want: "shared context"},
		{name: "share chat id", messageType: "share_chat", content: `{"share_chat_id":"oc_123"}`, want: "[Forwarded message: oc_123]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := &larkim.EventMessage{MessageType: strPtr(tt.messageType), Content: strPtr(tt.content)}
			assert.Equal(t, tt.want, extractMessageText(msg))
		})
	}
}

func TestExtractImageResourceKeys(t *testing.T) {
	post := `{"zh_cn":{"content":[[{"tag":"img","image_key":"img_1"},{"tag":"text","text":"hello"}],[{"tag":"img","image_key":"img_2"},{"tag":"img","image_key":"img_1"}]]}}`
	assert.Equal(t, []string{"img_1", "img_2"}, extractImageResourceKeys("post", post))
	assert.Equal(t, []string{"img_direct"}, extractImageResourceKeys("image", `{"image_key":"img_direct"}`))
	assert.Empty(t, extractImageResourceKeys("file", `{"file_key":"file_1"}`))
}
