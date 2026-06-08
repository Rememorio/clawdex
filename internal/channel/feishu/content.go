package feishu

import (
	"encoding/json"
	"fmt"
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const fallbackPostText = "[Rich text message]"

type postParseResult struct {
	text      string
	imageKeys []string
}

func extractMessageText(msg *larkim.EventMessage) string {
	if msg == nil {
		return ""
	}

	msgType := stringValue(msg.MessageType)
	content := stringValue(msg.Content)
	if content == "" {
		return ""
	}

	switch msgType {
	case "text":
		var c textContent
		if err := json.Unmarshal([]byte(content), &c); err != nil {
			return strings.TrimSpace(content)
		}
		return strings.TrimSpace(c.Text)
	case "post":
		return parseFeishuPost(content).text
	case "audio":
		m := parseContentObject(content)
		if speech := trimAnyString(m["speech_to_text"]); speech != "" {
			return speech
		}
		return mediaPlaceholderText(msgType, m)
	case "image", "file", "media", "sticker", "video":
		return mediaPlaceholderText(msgType, parseContentObject(content))
	case "share_chat":
		return shareChatText(content)
	case "share_user":
		return "[share_user]"
	case "merge_forward":
		return "[Merged and Forwarded Message]"
	default:
		return strings.TrimSpace(content)
	}
}

func extractImageResourceKeys(messageType, content string) []string {
	switch messageType {
	case "post":
		return parseFeishuPost(content).imageKeys
	case "image", "media":
		m := parseContentObject(content)
		key := trimAnyString(m["image_key"])
		if key == "" {
			return nil
		}
		return []string{key}
	default:
		return nil
	}
}

func parseContentObject(content string) map[string]any {
	var raw map[string]any
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return nil
	}
	return raw
}

func mediaPlaceholderText(messageType string, content map[string]any) string {
	name := trimAnyString(content["file_name"])
	if name == "" {
		name = trimAnyString(content["name"])
	}
	if name != "" {
		return fmt.Sprintf("[%s: %s]", messageType, name)
	}
	return "[" + messageType + "]"
}

func shareChatText(content string) string {
	m := parseContentObject(content)
	for _, key := range []string{"body", "summary", "share_chat_id"} {
		if value := trimAnyString(m[key]); value != "" {
			if key == "share_chat_id" {
				return "[Forwarded message: " + value + "]"
			}
			return value
		}
	}
	return "[Forwarded message]"
}

func parseFeishuPost(content string) postParseResult {
	var raw any
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return postParseResult{text: fallbackPostText}
	}

	payload, ok := resolvePostPayload(raw)
	if !ok {
		return postParseResult{text: fallbackPostText}
	}

	title := trimAnyString(payload["title"])
	rawContent, _ := payload["content"].([]any)
	imageSeen := make(map[string]bool)
	var imageKeys []string
	var paragraphs []string

	for _, rawParagraph := range rawContent {
		paragraph, ok := rawParagraph.([]any)
		if !ok {
			continue
		}

		var sb strings.Builder
		for _, element := range paragraph {
			sb.WriteString(renderPostElement(element, imageSeen, &imageKeys))
		}
		if text := strings.TrimSpace(sb.String()); text != "" {
			paragraphs = append(paragraphs, text)
		}
	}

	body := strings.Join(paragraphs, "\n")
	text := strings.TrimSpace(strings.Join(nonEmptyStrings(title, body), "\n\n"))
	if text == "" {
		text = fallbackPostText
	}
	return postParseResult{text: text, imageKeys: imageKeys}
}

func resolvePostPayload(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	if isPostPayload(m) {
		return m, true
	}
	if post, ok := m["post"]; ok {
		if payload, ok := resolvePostPayload(post); ok {
			return payload, true
		}
	}
	for _, child := range m {
		if payload, ok := resolvePostPayload(child); ok {
			return payload, true
		}
	}
	return nil, false
}

func isPostPayload(m map[string]any) bool {
	_, ok := m["content"].([]any)
	return ok
}

func renderPostElement(v any, imageSeen map[string]bool, imageKeys *[]string) string {
	m, ok := v.(map[string]any)
	if !ok {
		return anyString(v)
	}

	switch strings.ToLower(trimAnyString(m["tag"])) {
	case "text":
		return renderPostText(m)
	case "a":
		return renderPostLink(m)
	case "at":
		return renderPostMention(m)
	case "img":
		addPostImageKey(trimAnyString(m["image_key"]), imageSeen, imageKeys)
		return "[image]"
	case "media":
		if name := trimAnyString(m["file_name"]); name != "" {
			return "[media: " + name + "]"
		}
		return "[media]"
	case "emotion":
		if emoji := trimAnyString(m["emoji"]); emoji != "" {
			return emoji
		}
		if text := anyString(m["text"]); text != "" {
			return text
		}
		return trimAnyString(m["emoji_type"])
	case "md", "lark_md":
		if text := anyString(m["text"]); text != "" {
			return text
		}
		return anyString(m["content"])
	case "br":
		return "\n"
	case "hr":
		return "\n---\n"
	case "code":
		return wrapInlineCode(anyString(m["text"]))
	case "code_block", "pre":
		return renderPostCodeBlock(m)
	default:
		return anyString(m["text"])
	}
}

func renderPostText(m map[string]any) string {
	text := anyString(m["text"])
	if text == "" {
		return ""
	}
	style, _ := m["style"].(map[string]any)
	if postStyleEnabled(style, "code") {
		return wrapInlineCode(text)
	}
	if postStyleEnabled(style, "bold") {
		text = "**" + text + "**"
	}
	if postStyleEnabled(style, "italic") {
		text = "*" + text + "*"
	}
	if postStyleEnabled(style, "underline") {
		text = "<u>" + text + "</u>"
	}
	if postStyleEnabled(style, "strikethrough") ||
		postStyleEnabled(style, "line_through") ||
		postStyleEnabled(style, "lineThrough") {
		text = "~~" + text + "~~"
	}
	return text
}

func renderPostLink(m map[string]any) string {
	href := trimAnyString(m["href"])
	text := anyString(m["text"])
	if text == "" {
		text = href
	}
	if text == "" {
		return ""
	}
	if href == "" || href == text {
		return text
	}
	return "[" + text + "](" + href + ")"
}

func renderPostMention(m map[string]any) string {
	for _, key := range []string{"user_name", "name", "open_id", "user_id"} {
		if value := trimAnyString(m[key]); value != "" {
			return "@" + value
		}
	}
	return ""
}

func renderPostCodeBlock(m map[string]any) string {
	code := anyString(m["text"])
	if code == "" {
		code = anyString(m["content"])
	}
	if code == "" {
		return ""
	}
	lang := sanitizePostLanguage(trimAnyString(m["language"]))
	if lang == "" {
		lang = sanitizePostLanguage(trimAnyString(m["lang"]))
	}
	if !strings.HasSuffix(code, "\n") {
		code += "\n"
	}
	return "```" + lang + "\n" + code + "```"
}

func addPostImageKey(key string, seen map[string]bool, keys *[]string) {
	if key == "" || seen[key] {
		return
	}
	seen[key] = true
	*keys = append(*keys, key)
}

func postStyleEnabled(style map[string]any, key string) bool {
	if style == nil {
		return false
	}
	switch v := style[key].(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true") || strings.TrimSpace(v) == "1"
	default:
		return false
	}
}

func wrapInlineCode(text string) string {
	if text == "" {
		return ""
	}
	maxRun := 0
	current := 0
	for _, r := range text {
		if r == '`' {
			current++
			if current > maxRun {
				maxRun = current
			}
			continue
		}
		current = 0
	}
	fence := strings.Repeat("`", maxRun+1)
	if strings.HasPrefix(text, "`") || strings.HasSuffix(text, "`") {
		text = " " + text + " "
	}
	return fence + text + fence
}

func sanitizePostLanguage(lang string) string {
	var b strings.Builder
	for _, r := range lang {
		if r >= 'a' && r <= 'z' ||
			r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9' ||
			r == '_' || r == '+' || r == '#' || r == '.' || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func trimAnyString(v any) string {
	return strings.TrimSpace(anyString(v))
}

func anyString(v any) string {
	s, _ := v.(string)
	return s
}

func nonEmptyStrings(values ...string) []string {
	var out []string
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}
