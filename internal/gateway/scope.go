package gateway

import (
	"hash/fnv"
	"strconv"
	"strings"

	"github.com/Rememorio/clawdex/internal/channel"
)

const (
	groupChatType      = "group"
	scopeSeparator     = "|"
	scopeFallbackID    = int64(1)
	unknownSpeakerName = "unknown-user"
	mainChatLabel      = "main-chat"
)

// sessionScopeID returns the Codex session scope for an inbound message.
// Chats keep a shared session, while topic threads get their own scope so
// unrelated discussions in the same group do not bleed together.
func sessionScopeID(msg channel.Message) int64 {
	if msg.ThreadID == 0 {
		return msg.ChatID
	}

	var b strings.Builder
	b.Grow(len(msg.Channel) + 48)
	b.WriteString(msg.Channel)
	b.WriteString(scopeSeparator)
	b.WriteString(strconv.FormatInt(msg.ChatID, 10))
	b.WriteString(scopeSeparator)
	b.WriteString(strconv.FormatInt(msg.ThreadID, 10))

	h := fnv.New64a()
	_, _ = h.Write([]byte(b.String()))
	scopeID := int64(h.Sum64())
	if scopeID == 0 {
		return scopeFallbackID
	}
	return scopeID
}

// codexPrompt returns the text forwarded to Codex for the inbound message.
// Group chats use a shared session, so each prompt carries speaker metadata
// and attribution rules to reduce cross-speaker confusion.
// DM chats include the sender name so the AI knows who it is talking to.
func codexPrompt(msg channel.Message) string {
	if msg.ChatType != groupChatType {
		if name := strings.TrimSpace(msg.SenderName); name != "" {
			return "[sender: " + name + "]\n" + msg.Text
		}
		return msg.Text
	}
	return formatGroupPrompt(msg)
}

func formatGroupPrompt(msg channel.Message) string {
	speaker := groupSpeakerName(msg)
	thread := mainChatLabel
	if msg.ThreadID != 0 {
		thread = strconv.FormatInt(msg.ThreadID, 10)
	}

	var b strings.Builder
	b.Grow(len(msg.Text) + len(msg.Channel) + len(speaker) + 256)
	b.WriteString("[shared group chat message]\n")
	b.WriteString("This session is shared by everyone in the group.\n")
	b.WriteString("Treat the speaker below as the author of this message only.\n")
	b.WriteString("When asked who said something, rely only on messages\n")
	b.WriteString("already present in this session and never attribute another\n")
	b.WriteString("speaker's words to the current speaker.\n")
	b.WriteString("Channel: ")
	b.WriteString(msg.Channel)
	b.WriteString("\n")
	b.WriteString("Thread: ")
	b.WriteString(thread)
	b.WriteString("\n")
	b.WriteString("Speaker: ")
	b.WriteString(speaker)
	if msg.SenderID != 0 {
		b.WriteString("\nSpeakerRef: u")
		b.WriteString(strconv.FormatInt(msg.SenderID, 10))
	}
	b.WriteString("\nMessage:\n")
	b.WriteString(msg.Text)
	return b.String()
}

func groupSpeakerName(msg channel.Message) string {
	name := strings.Join(strings.Fields(msg.SenderName), " ")
	if name != "" {
		return name
	}
	if msg.SenderID != 0 {
		return "user-" + strconv.FormatInt(msg.SenderID, 10)
	}
	return unknownSpeakerName
}
