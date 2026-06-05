package weixin

import (
	"strings"
)

// markdownFilter performs minimal cleanup on AI-generated Markdown before
// sending to WeChat. WeChat's AI bot scene natively renders most Markdown
// (headings, lists, code blocks, bold, blockquotes, tables), so we only
// strip constructs that have no rendering support or produce visual noise.
//
// Strips:
//   - Image syntax ![alt](url) — WeChat shows raw text, not an image
//   - HTML-style thinking tags <thinking>...</thinking>
//
// Passes through everything else (headings, bold, italic, code, tables, etc.)
func markdownFilter(text string) string {
	// Remove image syntax.
	text = removeImages(text)
	// Remove thinking tags (Codex sometimes emits these).
	text = removeThinkingTags(text)
	return text
}

// removeImages removes markdown image syntax ![alt](url) from text.
func removeImages(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if i < len(s)-1 && s[i] == '!' && s[i+1] == '[' {
			// Find closing ](url).
			j := strings.Index(s[i:], "](")
			if j > 0 {
				k := strings.Index(s[i+j:], ")")
				if k > 0 {
					// Skip the entire ![...](...) construct.
					i += j + k + 1
					continue
				}
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// removeThinkingTags strips <thinking>...</thinking> blocks from text.
func removeThinkingTags(s string) string {
	for {
		start := strings.Index(s, "<thinking>")
		if start == -1 {
			break
		}
		end := strings.Index(s[start:], "</thinking>")
		if end == -1 {
			// Unclosed tag — remove from <thinking> to end.
			s = s[:start]
			break
		}
		s = s[:start] + s[start+end+len("</thinking>"):]
	}
	return strings.TrimSpace(s)
}
