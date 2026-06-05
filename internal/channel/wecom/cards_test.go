package wecom

import (
	"testing"

	"github.com/Rememorio/clawdex/internal/channel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildSessionTemplateCard_Basic(t *testing.T) {
	card := channel.SessionCard{
		Title:     "🧵 Sessions",
		Desc:      "Current: a1b2c3d4 · 3 session(s)",
		Body:      "▸ 1. a1b2c3d4 Chat about Go\n  2. e5f6g7h8 Debug session\n  3. i9j0k1l2 Code review\n",
		CurrentID: "a1b2c3d4-full-uuid",
		Sessions: []channel.SessionCardOption{
			{ID: "a1b2c3d4-full-uuid", Label: "1. a1b2c3d4 Chat about Go ✓"},
			{ID: "e5f6g7h8-full-uuid", Label: "2. e5f6g7h8 Debug session"},
			{ID: "i9j0k1l2-full-uuid", Label: "3. i9j0k1l2 Code review"},
		},
		Buttons: []channel.SessionCardButton{
			{Text: "🔁 Go", CallbackData: "/sessions:switch"},
			{Text: "🆕 New", CallbackData: "/sessions:new"},
			{Text: "📍 Info", CallbackData: "/sessions:status"},
		},
	}

	tc := buildSessionTemplateCard(card, 42)

	assert.Equal(t, templateCardTypeButtonInteraction, tc.CardType)
	require.NotNil(t, tc.MainTitle)
	assert.Equal(t, "🧵 Sessions", tc.MainTitle.Title)
	assert.Contains(t, tc.MainTitle.Desc, "Current: a1b2c3d4")
	assert.Contains(t, tc.SubTitleText, "Chat about Go")

	// Dropdown selection.
	require.NotNil(t, tc.ButtonSelection)
	assert.Equal(t, cardSessionQuestionKey, tc.ButtonSelection.QuestionKey)
	assert.Equal(t, "a1b2c3d4-full-uuid", tc.ButtonSelection.SelectedID)
	require.Len(t, tc.ButtonSelection.OptionList, 3)
	assert.True(t, tc.ButtonSelection.OptionList[0].IsChecked)
	assert.False(t, tc.ButtonSelection.OptionList[1].IsChecked)

	// Buttons.
	require.Len(t, tc.ButtonList, 3)
	assert.Equal(t, "🔁 Go", tc.ButtonList[0].Text)
	assert.Equal(t, "/sessions:switch", tc.ButtonList[0].Key)

	// TaskID should be non-empty.
	assert.NotEmpty(t, tc.TaskID)
	assert.Contains(t, tc.TaskID, "sessions-42-")
}

func TestBuildSessionTemplateCard_EmptySessions(t *testing.T) {
	card := channel.SessionCard{
		Title:   "🧵 Sessions",
		Desc:    "Current: none · 0 session(s)",
		Body:    "No sessions found.",
		Buttons: []channel.SessionCardButton{},
	}

	tc := buildSessionTemplateCard(card, 1)

	assert.Equal(t, templateCardTypeButtonInteraction, tc.CardType)
	assert.Nil(t, tc.ButtonSelection, "no dropdown when no sessions")
	assert.Empty(t, tc.ButtonList)
}

func TestBuildSessionTemplateCard_SelectionLimit(t *testing.T) {
	// Create 15 sessions — should be capped at 10.
	var sessions []channel.SessionCardOption
	for i := 0; i < 15; i++ {
		sessions = append(sessions, channel.SessionCardOption{
			ID:    "session-" + string(rune('a'+i)),
			Label: "Session " + string(rune('A'+i)),
		})
	}
	card := channel.SessionCard{
		Title:     "🧵 Sessions",
		Sessions:  sessions,
		CurrentID: "session-a",
	}

	tc := buildSessionTemplateCard(card, 1)

	require.NotNil(t, tc.ButtonSelection)
	assert.Len(t, tc.ButtonSelection.OptionList, templateCardSelectionLimit)
}

func TestBuildSessionTemplateCard_ButtonLimit(t *testing.T) {
	// Create 8 buttons — should be capped at 6.
	var buttons []channel.SessionCardButton
	for i := 0; i < 8; i++ {
		buttons = append(buttons, channel.SessionCardButton{
			Text:         "Btn",
			CallbackData: "btn",
		})
	}
	card := channel.SessionCard{
		Title:   "🧵 Sessions",
		Buttons: buttons,
	}

	tc := buildSessionTemplateCard(card, 1)
	assert.Len(t, tc.ButtonList, templateCardButtonLimit)
}

func TestBuildSessionTemplateCard_NoCurrentSession(t *testing.T) {
	card := channel.SessionCard{
		Title:     "🧵 Sessions",
		CurrentID: "",
		Sessions: []channel.SessionCardOption{
			{ID: "abc123", Label: "Session A"},
			{ID: "def456", Label: "Session B"},
		},
	}

	tc := buildSessionTemplateCard(card, 1)

	require.NotNil(t, tc.ButtonSelection)
	// When CurrentID is empty, first option should be selected.
	assert.Equal(t, "abc123", tc.ButtonSelection.SelectedID)
	assert.False(t, tc.ButtonSelection.OptionList[0].IsChecked)
	assert.False(t, tc.ButtonSelection.OptionList[1].IsChecked)
}

func TestSelectedTemplateCardOption(t *testing.T) {
	event := &TemplateCardEvent{
		SelectedItems: templateCardSelectedItemWrapper{
			SelectedItem: []templateCardSelectedItem{
				{
					QuestionKey: cardSessionQuestionKey,
					OptionIDs:   templateCardOptionIDArray{OptionID: []string{"session-abc"}},
				},
			},
		},
	}

	assert.Equal(t, "session-abc", selectedTemplateCardOption(event, cardSessionQuestionKey))
	assert.Equal(t, "", selectedTemplateCardOption(event, "unknown_key"))
	assert.Equal(t, "", selectedTemplateCardOption(nil, cardSessionQuestionKey))
}

func TestSelectedTemplateCardOption_EmptyOptionIDs(t *testing.T) {
	event := &TemplateCardEvent{
		SelectedItems: templateCardSelectedItemWrapper{
			SelectedItem: []templateCardSelectedItem{
				{
					QuestionKey: cardSessionQuestionKey,
					OptionIDs:   templateCardOptionIDArray{OptionID: []string{"", "  "}},
				},
			},
		},
	}

	assert.Equal(t, "", selectedTemplateCardOption(event, cardSessionQuestionKey))
}

func TestResolveCardSessionSelection(t *testing.T) {
	event := &TemplateCardEvent{
		SelectedItems: templateCardSelectedItemWrapper{
			SelectedItem: []templateCardSelectedItem{
				{
					QuestionKey: cardSessionQuestionKey,
					OptionIDs:   templateCardOptionIDArray{OptionID: []string{"selected-id"}},
				},
			},
		},
	}

	assert.Equal(t, "selected-id", resolveCardSessionSelection(event))
}

func TestShortID(t *testing.T) {
	assert.Equal(t, "a1b2c3d4", shortID("a1b2c3d4-long-uuid-here"))
	assert.Equal(t, "short", shortID("short"))
	assert.Equal(t, "12345678", shortID("12345678"))
	assert.Equal(t, "", shortID(""))
}

func TestTruncCardRunes(t *testing.T) {
	assert.Equal(t, "hello", truncCardRunes("hello", 10))
	assert.Equal(t, "hell…", truncCardRunes("hello world", 4))
	assert.Equal(t, "你好世…", truncCardRunes("你好世界测试", 3))
}

func TestFormatCardSessionIndex(t *testing.T) {
	label := formatCardSessionIndex(1, "a1b2c3d4-full-uuid", "My Session", true)
	assert.Contains(t, label, "1.")
	assert.Contains(t, label, "a1b2c3d4")
	assert.Contains(t, label, "My Session")
	assert.Contains(t, label, "✓")

	label = formatCardSessionIndex(2, "e5f6g7h8-full-uuid", "Other", false)
	assert.Contains(t, label, "2.")
	assert.NotContains(t, label, "✓")

	// Untitled.
	label = formatCardSessionIndex(3, "x9y0z1a2-full-uuid", "", false)
	assert.Contains(t, label, "(untitled)")

	// Long title truncation.
	longTitle := "This is a very long session title that should be truncated"
	label = formatCardSessionIndex(1, "abc", longTitle, false)
	assert.Contains(t, label, "…")
}

func TestNewSessionCardTaskID(t *testing.T) {
	id1 := newSessionCardTaskID(42)
	id2 := newSessionCardTaskID(42)
	assert.NotEqual(t, id1, id2, "task IDs should be unique")
	assert.Contains(t, id1, "sessions-42-")
}

func TestTemplateCardConstants(t *testing.T) {
	assert.Equal(t, "text_notice", templateCardTypeTextNotice)
	assert.Equal(t, "button_interaction", templateCardTypeButtonInteraction)
	assert.Equal(t, 1, templateCardButtonStyleDefault)
	assert.Equal(t, 6, templateCardButtonLimit)
	assert.Equal(t, 10, templateCardSelectionLimit)
}
