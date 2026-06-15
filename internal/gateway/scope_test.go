package gateway

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Rememorio/clawdex/internal/channel"
)

func TestSessionScopeID_PrivateChatUsesChatID(t *testing.T) {
	msg := channel.Message{
		Channel:  "telegram",
		ChatID:   42,
		SenderID: 7,
		ChatType: "",
	}

	assert.Equal(t, int64(42), sessionScopeID(msg))
}

func TestSessionScopeID_GroupUsesSharedChatSession(t *testing.T) {
	base := channel.Message{
		Channel:  "telegram",
		ChatID:   -100123,
		ChatType: groupChatType,
	}
	first := base
	first.SenderID = 7
	second := base
	second.SenderID = 8

	assert.Equal(t, sessionScopeID(first), sessionScopeID(second))
	assert.Equal(t, int64(-100123), sessionScopeID(first))
}

func TestSessionScopeID_GroupDiffersAcrossThreads(t *testing.T) {
	base := channel.Message{
		Channel:  "telegram",
		ChatID:   -100123,
		SenderID: 7,
		ChatType: groupChatType,
	}
	first := base
	first.ThreadID = 11
	second := base
	second.ThreadID = 12

	assert.NotEqual(t, sessionScopeID(first), sessionScopeID(second))
}

func TestCodexPrompt_PrivateChatPlainTextWhenNoSender(t *testing.T) {
	msg := channel.Message{ChatID: 42, Text: "hello"}

	prompt := codexPrompt(msg)
	assert.Contains(t, prompt, "[Current turn time: ")
	assert.Contains(t, prompt, "[Cron tool: ")
	assert.Contains(t, prompt, "Never answer scheduled-job state from chat history or memory")
	assert.True(t, strings.HasSuffix(prompt, "\nhello"))
}

func TestCodexPrompt_PrivateChatIncludesSenderName(t *testing.T) {
	msg := channel.Message{ChatID: 42, SenderName: "张三", Text: "hello"}

	prompt := codexPrompt(msg)
	assert.Contains(t, prompt, "[Current turn time: ")
	assert.Contains(t, prompt, "[sender: 张三]\nhello")
}

func TestCodexPrompt_GroupIncludesSpeakerMetadata(t *testing.T) {
	msg := channel.Message{
		Channel:    "telegram",
		ChatID:     -100123,
		ThreadID:   9,
		SenderID:   7,
		SenderName: "yuxanghuang",
		ChatType:   groupChatType,
		Text:       "我说了什么？",
	}

	prompt := codexPrompt(msg)
	assert.Contains(t, prompt, "[shared group chat message]")
	assert.Contains(t, prompt, "Speaker: yuxanghuang")
	assert.Contains(t, prompt, "SpeakerRef: u7")
	assert.Contains(t, prompt, "Message:\n我说了什么？")
	assert.Contains(t, prompt, "never attribute another")
}

func TestGroupSpeakerNameFallsBackToSenderID(t *testing.T) {
	msg := channel.Message{SenderID: 7}

	assert.Equal(t, "user-7", groupSpeakerName(msg))
}
