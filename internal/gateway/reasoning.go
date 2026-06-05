package gateway

import (
	"regexp"
	"strings"
)

// thinkingTagRe matches opening and closing thinking/thought tags.
var thinkingTagRe = regexp.MustCompile(`(?i)<\s*/?\s*(?:think(?:ing)?|thought|antthinking)\b[^<>]*>`)

// FormatThinkingTags replaces thinking/reasoning tags with Telegram expandable
// blockquotes. The thinking content is wrapped in <blockquote expandable>
// so it appears collapsed by default and can be expanded by the user.
// Tags inside fenced code blocks or inline code spans are preserved as-is.
func FormatThinkingTags(text string) string {
	if text == "" {
		return ""
	}
	// Quick check: if no tag pattern exists, return as-is.
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "<think") &&
		!strings.Contains(lower, "<thought") &&
		!strings.Contains(lower, "<antthinking") {
		return text
	}

	// Find code regions to protect.
	codeRegions := findCodeRegions(text)

	var result strings.Builder
	inThinking := false
	var thinkBuf strings.Builder
	lastEnd := 0

	matches := thinkingTagRe.FindAllStringIndex(text, -1)
	for _, m := range matches {
		start, end := m[0], m[1]

		// Skip tags inside code regions.
		if isInCodeRegion(start, codeRegions) {
			continue
		}

		tag := strings.ToLower(text[start:end])
		isClosing := strings.Contains(tag, "/")

		if !inThinking && !isClosing {
			// Opening tag: flush text before it, start capturing thinking content.
			result.WriteString(text[lastEnd:start])
			inThinking = true
			thinkBuf.Reset()
			lastEnd = end
		} else if inThinking && isClosing {
			// Closing tag: wrap captured thinking content.
			thinkBuf.WriteString(text[lastEnd:start])
			content := strings.TrimSpace(thinkBuf.String())
			if content != "" {
				result.WriteString("<blockquote expandable>")
				result.WriteString(content)
				result.WriteString("</blockquote>")
			}
			inThinking = false
			lastEnd = end
		} else if inThinking {
			// Nested opening inside thinking: treat as plain text.
			thinkBuf.WriteString(text[lastEnd:end])
			lastEnd = end
		}
	}

	if inThinking {
		// Unclosed thinking tag (model still reasoning during streaming):
		// show what we have so far as an expanding blockquote.
		thinkBuf.WriteString(text[lastEnd:])
		content := strings.TrimSpace(thinkBuf.String())
		if content != "" {
			result.WriteString("<blockquote expandable>")
			result.WriteString(content)
			result.WriteString("</blockquote>")
		}
	} else {
		// Flush remaining text.
		result.WriteString(text[lastEnd:])
	}

	return strings.TrimSpace(result.String())
}

// stripThinkingTags removes thinking/reasoning tag blocks entirely, leaving
// only the non-thinking text. Tags inside fenced code blocks or inline code
// spans are preserved as-is.
func stripThinkingTags(text string) string {
	if text == "" {
		return ""
	}
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "<think") &&
		!strings.Contains(lower, "<thought") &&
		!strings.Contains(lower, "<antthinking") {
		return text
	}

	codeRegions := findCodeRegions(text)

	var result strings.Builder
	inThinking := false
	lastEnd := 0

	matches := thinkingTagRe.FindAllStringIndex(text, -1)
	for _, m := range matches {
		start, end := m[0], m[1]

		if isInCodeRegion(start, codeRegions) {
			continue
		}

		tag := strings.ToLower(text[start:end])
		isClosing := strings.Contains(tag, "/")

		if !inThinking && !isClosing {
			result.WriteString(text[lastEnd:start])
			inThinking = true
			lastEnd = end
		} else if inThinking && isClosing {
			// Drop the thinking content entirely.
			inThinking = false
			lastEnd = end
		}
	}

	if !inThinking {
		result.WriteString(text[lastEnd:])
	}
	// If still in an unclosed thinking block (streaming), drop the tail.

	return strings.TrimSpace(result.String())
}

// codeRegion represents a region of text that is inside a code block or span.
type codeRegion struct {
	start, end int
}

// findCodeRegions returns byte-offset ranges for fenced code blocks and inline code spans.
func findCodeRegions(text string) []codeRegion {
	var regions []codeRegion

	// Fenced code blocks: ```...```
	fenced := regexp.MustCompile("(?s)```.*?```")
	for _, m := range fenced.FindAllStringIndex(text, -1) {
		regions = append(regions, codeRegion{m[0], m[1]})
	}

	// Inline code: `...` (not inside fenced blocks)
	inline := regexp.MustCompile("`[^`\n]+`")
	for _, m := range inline.FindAllStringIndex(text, -1) {
		if !isInCodeRegion(m[0], regions) {
			regions = append(regions, codeRegion{m[0], m[1]})
		}
	}

	return regions
}

// isInCodeRegion checks if a byte offset falls inside any code region.
func isInCodeRegion(offset int, regions []codeRegion) bool {
	for _, r := range regions {
		if offset >= r.start && offset < r.end {
			return true
		}
	}
	return false
}
