package gateway

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/Rememorio/clawdex/internal/channel"
	"github.com/Rememorio/clawdex/internal/codex"
)

// commandDef defines a bot slash command (name + help text only).
type commandDef struct {
	name string
	help string
}

// commandDefs is the ordered list of commands shown in /help.
var commandDefs = []commandDef{
	{"/help", "Show this help message"},
	{"/new", "Start a fresh conversation"},
	{"/sessions", "List recent codex sessions"},
	{"/resume <id>", "Switch to an existing session"},
	{"/cancel", "Cancel the running task"},
	{"/cron", "Manage scheduled jobs for this chat"},
	{"/status", "Show current chat context"},
}

// keyboardButton is the package-private keyboard button used in command responses.
type keyboardButton struct {
	text         string
	callbackData string
}

// commandResponse is the result of a slash command handler.
type commandResponse struct {
	text        string
	keyboard    [][]keyboardButton
	sessionCard *channel.SessionCard // rich session card (optional, WeCom-style)
}

// commandHandlers maps command names to their handler functions.
// All handlers receive the codex client, the original message, and any
// arguments that followed the command name (already trimmed).
var commandHandlers = map[string]func(c *codex.Client, msg channel.Message, args string) commandResponse{
	"/help":     cmdHelp,
	"/new":      cmdNew,
	"/sessions": cmdSessions,
	"/resume":   cmdResume,
	"/status":   cmdStatus,
}

// groupDisabledCommands are slash commands that stay disabled in group chats.
// Group sessions are isolated per sender, but exposing session management in
// busy groups still adds noise and confusion.
var groupDisabledCommands = map[string]bool{
	"/new":      true,
	"/sessions": true,
	"/resume":   true,
}

// handleCommand checks whether the message is a slash command. If so it
// executes the command and returns (response, true). Otherwise returns (zero,
// false).
func handleCommand(c *codex.Client, msg channel.Message) (commandResponse, bool) {
	cmd, args, ok := parseCommandText(msg.Text)
	if !ok {
		return commandResponse{}, false
	}
	if fn, ok := commandHandlers[cmd]; ok {
		if msg.ChatType == "group" && groupDisabledCommands[cmd] {
			return commandResponse{text: fmt.Sprintf("%s is not available in group chats.", cmd)}, true
		}
		return fn(c, msg, args), true
	}
	return commandResponse{}, false
}

// parseCommandText splits a slash command using Unicode whitespace.
func parseCommandText(text string) (string, string, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return "", "", false
	}
	cmd := stripFormatRunes(fields[0])
	if len(fields) == 1 {
		return cmd, "", true
	}
	return cmd, strings.Join(fields[1:], " "), true
}

// isCancel reports whether the message text is a /cancel command.
// This is checked separately from handleCommand because /cancel must be
// processed without acquiring the chat lock (to avoid deadlock with the
// running job that holds it).
func isCancel(text string) bool {
	cmd, _, ok := parseCommandText(text)
	return ok && (cmd == "/cancel" || cmd == "/stop")
}

// stripFormatRunes removes invisible Unicode formatting characters.
func stripFormatRunes(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, text)
}

// normalizeSessionID removes invisible characters often introduced by copy and
// paste in chat clients.
func normalizeSessionID(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	return strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return -1
		case unicode.Is(unicode.Cf, r):
			return -1
		default:
			return r
		}
	}, trimmed)
}

func cmdHelp(_ *codex.Client, msg channel.Message, _ string) commandResponse {
	isGroup := msg.ChatType == "group"
	var b strings.Builder
	b.WriteString("Available commands:\n")
	for _, def := range commandDefs {
		cmd, _, _ := strings.Cut(def.name, " ")
		if isGroup && groupDisabledCommands[cmd] {
			continue
		}
		fmt.Fprintf(&b, "  %s — %s\n", def.name, def.help)
	}
	appendCronHelpSection(&b)

	// Build keyboard, filtering out group-disabled commands.
	allButtons := []keyboardButton{
		{text: "/new", callbackData: "/new"},
		{text: "/sessions", callbackData: "/sessions"},
		{text: "/status", callbackData: "/status"},
	}
	var buttons []keyboardButton
	for _, btn := range allButtons {
		if isGroup && groupDisabledCommands[btn.callbackData] {
			continue
		}
		buttons = append(buttons, btn)
	}
	var keyboard [][]keyboardButton
	if len(buttons) > 0 {
		keyboard = [][]keyboardButton{buttons}
	}

	var card *channel.SessionCard
	if !isGroup {
		helpCard := channel.SessionCard{
			Title: "Help",
			Desc:  "Available commands, including /cron jobs",
			Body:  b.String(),
			Buttons: []channel.SessionCardButton{
				{Text: "/sessions", CallbackData: "/sessions:sessions"},
				{Text: "/status", CallbackData: "/sessions:status"},
			},
		}
		card = &helpCard
	}

	return commandResponse{
		text:        b.String(),
		keyboard:    keyboard,
		sessionCard: card,
	}
}

func cmdNew(c *codex.Client, msg channel.Message, _ string) commandResponse {
	c.ResetSession(sessionScopeID(msg))
	text := "Session cleared. Next message starts a fresh conversation."
	newCard := channel.SessionCard{
		Title: "New Session",
		Desc:  text,
		Body:  text,
		Buttons: []channel.SessionCardButton{
			{Text: "/sessions", CallbackData: "/sessions:sessions"},
			{Text: "/status", CallbackData: "/sessions:status"},
		},
	}
	return commandResponse{text: text, sessionCard: &newCard}
}

func cmdSessions(c *codex.Client, msg channel.Message, _ string) commandResponse {
	resp := sessionListResponse(
		c,
		sessionScopeID(msg),
		"No sessions found.",
	)
	// Attach a rich session card for channels that support it.
	resp.sessionCard = cmdSessionsCard(c, msg)
	return resp
}

// cmdSessionsCard builds a rich session card for channels that support it.
// Returns nil if no sessions exist.
func cmdSessionsCard(c *codex.Client, msg channel.Message) *channel.SessionCard {
	scopeID := sessionScopeID(msg)
	sessions := c.Store.List(scopeID, 10)
	if len(sessions) == 0 {
		return nil
	}

	currentThreadID := c.GetSessionID(scopeID)
	card := buildSessionsCard(currentThreadID, sessions)
	return &card
}

// buildSessionsCard constructs a channel.SessionCard from stored session data.
func buildSessionsCard(currentThreadID string, sessions []codex.StoredSession) channel.SessionCard {
	shortCur := currentThreadID
	if len(shortCur) > 8 {
		shortCur = shortCur[:8]
	}

	var descParts []string
	if shortCur != "" {
		descParts = append(descParts, fmt.Sprintf("Current: %s", shortCur))
	} else {
		descParts = append(descParts, "Current: none")
	}
	descParts = append(descParts, fmt.Sprintf("%d session(s)", len(sessions)))
	desc := strings.Join(descParts, " · ")

	// Build numbered body text.
	var body strings.Builder
	for i, s := range sessions {
		marker := "  "
		if s.ThreadID == currentThreadID {
			marker = "▸ "
		}
		label := cleanSessionTitle(s.Title)
		if label == "" {
			label = "(untitled)"
		}
		if r := []rune(label); len(r) > 36 {
			label = string(r[:36]) + "…"
		}
		shortTid := s.ThreadID
		if len(shortTid) > 8 {
			shortTid = shortTid[:8]
		}
		ago := relativeTime(s.UpdatedAt)
		fmt.Fprintf(&body, "%s%d. %s %s %s\n", marker, i+1, shortTid, label, ago)
	}
	body.WriteString("\nSelect from dropdown and tap Switch, or use /resume <id>.")

	// Build dropdown options.
	options := make([]channel.SessionCardOption, 0, len(sessions))
	for i, s := range sessions {
		label := cleanSessionTitle(s.Title)
		if label == "" {
			label = "(untitled)"
		}
		if r := []rune(label); len(r) > 22 {
			label = string(r[:22]) + "…"
		}
		shortTid := s.ThreadID
		if len(shortTid) > 8 {
			shortTid = shortTid[:8]
		}
		optLabel := fmt.Sprintf("%d. %s %s", i+1, shortTid, label)
		if s.ThreadID == currentThreadID {
			optLabel += " ✓"
		}
		options = append(options, channel.SessionCardOption{
			ID:    s.ThreadID,
			Label: optLabel,
		})
	}

	return channel.SessionCard{
		Title:     "🧵 Sessions",
		Desc:      desc,
		Body:      body.String(),
		Sessions:  options,
		CurrentID: currentThreadID,
		Buttons: []channel.SessionCardButton{
			{Text: "/resume", CallbackData: "/sessions:switch"},
			{Text: "/new", CallbackData: "/sessions:new"},
		},
	}
}

func cmdResume(c *codex.Client, msg channel.Message, args string) commandResponse {
	scopeID := sessionScopeID(msg)
	sessionID := normalizeSessionID(args)
	if sessionID == "" {
		resp := sessionListResponse(
			c,
			scopeID,
			"No sessions to resume.",
		)
		resp.sessionCard = cmdSessionsCard(c, msg)
		return resp
	}

	// Support both full UUIDs and short prefixes.

	// If it looks like a full UUID (>= 36 chars), use it directly.
	if len(sessionID) >= 36 {
		c.SetSession(scopeID, sessionID)
		return commandResponse{text: fmt.Sprintf("Switched to session %s. Next message will resume it.", sessionID)}
	}

	// Short prefix — search the store.
	matches := c.Store.FindByPrefix(scopeID, sessionID)
	if len(matches) == 0 {
		return commandResponse{text: fmt.Sprintf("No session found matching %q.", sessionID)}
	}
	if len(matches) > 1 {
		return commandResponse{text: fmt.Sprintf("Ambiguous prefix %q — matches multiple sessions.", sessionID)}
	}

	sessionID = matches[0].ThreadID
	c.SetSession(scopeID, sessionID)
	return commandResponse{text: fmt.Sprintf("Switched to session %s. Next message will resume it.", sessionID)}
}

// sessionListResponse builds a session list with resume buttons.
// currentChatID is used to highlight the active session for this chat.
// Keyboard data is always built; the gateway dispatches to KeyboardResponder
// if the responder supports it, otherwise falls back to plain Reply.
func sessionListResponse(c *codex.Client, currentChatID int64, emptyText string) commandResponse {
	sessions := c.Store.List(currentChatID, 10)
	if len(sessions) == 0 {
		return commandResponse{text: emptyText}
	}

	currentThreadID := c.GetSessionID(currentChatID)

	var b strings.Builder
	if currentThreadID != "" {
		shortCur := currentThreadID
		if len(shortCur) > 8 {
			shortCur = shortCur[:8]
		}
		b.WriteString(fmt.Sprintf("Current session: `%s`\n\n", shortCur))
	} else {
		b.WriteString("Current session: none\n\n")
	}
	b.WriteString("Recent sessions:\n")
	var kbRows [][]keyboardButton
	const maxKeyboardRows = 3
	for i, s := range sessions {
		label := cleanSessionTitle(s.Title)
		if label == "" {
			label = "(untitled)"
		}
		// Truncate label to 40 runes for display.
		if r := []rune(label); len(r) > 40 {
			label = string(r[:40]) + "…"
		}
		tid := s.ThreadID
		shortTid := tid
		if len(shortTid) > 8 {
			shortTid = shortTid[:8]
		}
		ago := relativeTime(s.UpdatedAt)
		marker := " "
		if s.ThreadID == currentThreadID {
			marker = "▸"
		}
		fmt.Fprintf(&b, " %s%d. `%s` %s %s\n", marker, i+1, shortTid, label, ago)

		// Build keyboard buttons for the most recent sessions only.
		if i < maxKeyboardRows {
			btnLabel := fmt.Sprintf("%s %s", shortTid, truncRunes(label, 20))
			kbRows = append(kbRows, []keyboardButton{{
				text:         btnLabel,
				callbackData: "/resume " + tid,
			}})
		}
	}
	b.WriteString("\nTap a button or use `/resume <id>` to switch.")
	return commandResponse{text: b.String(), keyboard: kbRows}
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// cleanSessionTitle strips the "[sender: xxx]\n" prefix that codexPrompt()
// prepends to every message. Without this, session titles in the list are
// dominated by the sender tag and the actual user question is pushed out
// of view.
func cleanSessionTitle(title string) string {
	if strings.HasPrefix(title, "[sender: ") {
		if idx := strings.Index(title, "]\n"); idx >= 0 {
			return strings.TrimSpace(title[idx+2:])
		}
		if idx := strings.Index(title, "]"); idx >= 0 {
			return strings.TrimSpace(title[idx+1:])
		}
	}
	// Also strip group chat prefix.
	if strings.HasPrefix(title, "[shared group chat message]\n") {
		lines := strings.SplitN(title, "\n", 2)
		if len(lines) > 1 {
			return strings.TrimSpace(lines[len(lines)-1])
		}
	}
	return title
}

func statusScope(msg channel.Message) string {
	if msg.ChatType == groupChatType {
		return "group chat (shared session)"
	}
	return "private chat"
}

func statusSoulState(c *codex.Client, channelName string) string {
	return c.SoulState(channelName)
}

func cmdStatus(c *codex.Client, msg channel.Message, _ string) commandResponse {
	var b strings.Builder
	b.WriteString("clawdex chat status\n")
	if msg.Channel != "" {
		fmt.Fprintf(&b, "  Channel:  %s\n", msg.Channel)
	}
	fmt.Fprintf(&b, "  Scope:    %s\n", statusScope(msg))
	if sid := c.GetSessionID(sessionScopeID(msg)); sid != "" {
		shortSid := sid
		if len(shortSid) > 8 {
			shortSid = shortSid[:8]
		}
		fmt.Fprintf(&b, "  Session:  `%s`\n", shortSid)
	} else {
		b.WriteString("  Session:  none\n")
	}
	fmt.Fprintf(&b, "  SOUL.md:  %s\n",
		statusSoulState(c, msg.Channel))

	text := b.String()
	statusCard := channel.SessionCard{
		Title: "Status",
		Desc:  "Current chat context",
		Body:  text,
		Buttons: []channel.SessionCardButton{
			{Text: "/sessions", CallbackData: "/sessions:sessions"},
			{Text: "/new", CallbackData: "/sessions:new"},
		},
	}
	return commandResponse{text: text, sessionCard: &statusCard}
}

// relativeTime returns a human-friendly relative timestamp like "2h ago".
func relativeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
