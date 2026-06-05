package wecom

import (
	"fmt"
	"strconv"
	"time"

	"github.com/Rememorio/clawdex/internal/channel"
)

const (
	// Dropdown question key for session selection.
	cardSessionQuestionKey = "session_select"
)

// newSessionCardTaskID generates a unique task ID for the card.
func newSessionCardTaskID(chatID int64) string {
	return fmt.Sprintf("sessions-%d-%d", chatID, time.Now().UnixNano())
}

// buildSessionTemplateCard converts a channel.SessionCard into a WeCom
// button_interaction template card with a dropdown selector and action buttons.
func buildSessionTemplateCard(card channel.SessionCard, chatID int64) *templateCard {
	// Build dropdown options (max 10).
	var selection *templateCardSelection
	if len(card.Sessions) > 0 {
		limit := min(len(card.Sessions), templateCardSelectionLimit)
		options := make([]templateCardOption, 0, limit)
		for i := 0; i < limit; i++ {
			s := card.Sessions[i]
			options = append(options, templateCardOption{
				ID:        s.ID,
				Text:      s.Label,
				IsChecked: s.ID == card.CurrentID,
			})
		}
		selectedID := card.CurrentID
		// If current is not in the options, default to first.
		if selectedID == "" && len(options) > 0 {
			selectedID = options[0].ID
		}
		selection = &templateCardSelection{
			QuestionKey: cardSessionQuestionKey,
			Title:       "Recent sessions",
			SelectedID:  selectedID,
			OptionList:  options,
		}
	}

	// Build buttons (max 6).
	var buttons []templateCardButton
	for i, btn := range card.Buttons {
		if i >= templateCardButtonLimit {
			break
		}
		buttons = append(buttons, templateCardButton{
			Text:  btn.Text,
			Style: templateCardButtonStyleDefault,
			Key:   btn.CallbackData,
		})
	}

	return &templateCard{
		CardType: templateCardTypeButtonInteraction,
		MainTitle: &templateCardMainTitle{
			Title: card.Title,
			Desc:  card.Desc,
		},
		SubTitleText:    card.Body,
		ButtonSelection: selection,
		ButtonList:      buttons,
		TaskID:          newSessionCardTaskID(chatID),
	}
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// resolveCardSessionSelection extracts the selected session ID from a card event.
func resolveCardSessionSelection(event *TemplateCardEvent) string {
	return selectedTemplateCardOption(event, cardSessionQuestionKey)
}

// truncCardRunes truncates a string to n runes, appending "…" if truncated.
func truncCardRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// formatCardSessionIndex generates a human-friendly session index label.
func formatCardSessionIndex(index int, threadID string, title string, isCurrent bool) string {
	short := shortID(threadID)
	label := title
	if label == "" {
		label = "(untitled)"
	}
	label = truncCardRunes(label, 20)
	text := strconv.Itoa(index) + ". " + short + " " + label
	if isCurrent {
		text += " ✓"
	}
	return text
}
