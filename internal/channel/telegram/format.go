package telegram

import (
	"regexp"
	"strings"
)

// FormatTelegramHTML converts Markdown-formatted text to Telegram-compatible HTML.
// Supported conversions: fenced code blocks, inline code, bold, italic, strikethrough,
// blockquotes, links, and expandable blockquotes (pre-inserted by FormatThinkingTags).
// HTML entities (& < >) are escaped in plain text regions.
func FormatTelegramHTML(md string) string {
	if md == "" {
		return ""
	}

	// Phase 1: Extract expandable blockquotes into placeholders.
	// These are inserted by FormatThinkingTags and contain raw markdown that
	// needs conversion. We extract them first, convert the inner content
	// recursively, then re-insert as pre-formed HTML.
	var expandBlocks []string
	md = expandableBlockquoteRe.ReplaceAllStringFunc(md, func(match string) string {
		sub := expandableBlockquoteRe.FindStringSubmatch(match)
		inner := ""
		if len(sub) > 1 {
			inner = sub[1]
		}
		formatted := FormatTelegramHTML(inner)
		idx := len(expandBlocks)
		expandBlocks = append(expandBlocks, "<blockquote expandable>"+formatted+"</blockquote>")
		return expandPlaceholder(idx)
	})

	// Phase 2: Extract fenced code blocks into placeholders.
	var codeBlocks []string
	md = fencedCodeRe.ReplaceAllStringFunc(md, func(match string) string {
		sub := fencedCodeRe.FindStringSubmatch(match)
		content := ""
		if len(sub) > 1 {
			content = sub[1]
		}
		// Trim one leading newline and one trailing newline if present.
		content = strings.TrimPrefix(content, "\n")
		content = strings.TrimSuffix(content, "\n")
		idx := len(codeBlocks)
		codeBlocks = append(codeBlocks, "<pre><code>"+escapeHTML(content)+"</code></pre>")
		return codePlaceholder(idx)
	})

	// Phase 2: Extract inline code spans into placeholders.
	var inlineCode []string
	md = inlineCodeRe.ReplaceAllStringFunc(md, func(match string) string {
		sub := inlineCodeRe.FindStringSubmatch(match)
		content := ""
		if len(sub) > 1 {
			content = sub[1]
		}
		idx := len(inlineCode)
		inlineCode = append(inlineCode, "<code>"+escapeHTML(content)+"</code>")
		return inlinePlaceholder(idx)
	})

	// Phase 4: Escape HTML in remaining text.
	md = escapeHTML(md)

	// Phase 4: Convert markdown syntax to HTML tags.
	// Links: [text](url)
	md = linkRe.ReplaceAllString(md, `<a href="$2">$1</a>`)

	// Bold: **text**
	md = boldRe.ReplaceAllString(md, `<b>$1</b>`)

	// Italic: *text* or _text_ (but not inside words for underscore)
	md = italicStarRe.ReplaceAllString(md, `<i>$1</i>`)
	md = italicUnderRe.ReplaceAllString(md, `<i>$1</i>`)

	// Strikethrough: ~~text~~
	md = strikeRe.ReplaceAllString(md, `<s>$1</s>`)

	// Blockquotes: lines starting with >
	md = convertBlockquotes(md)

	// Phase 5: Re-insert all placeholder types.
	for i, block := range codeBlocks {
		md = strings.Replace(md, codePlaceholder(i), block, 1)
	}
	for i, code := range inlineCode {
		md = strings.Replace(md, inlinePlaceholder(i), code, 1)
	}
	for i, block := range expandBlocks {
		md = strings.Replace(md, expandPlaceholder(i), block, 1)
	}

	return md
}

// escapeHTML escapes &, <, and > for Telegram HTML.
func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

var (
	fencedCodeRe           = regexp.MustCompile("(?s)```[a-zA-Z0-9]*\n?(.*?)```")
	inlineCodeRe           = regexp.MustCompile("`([^`\n]+)`")
	expandableBlockquoteRe = regexp.MustCompile(`(?s)<blockquote expandable>(.*?)</blockquote>`)
	linkRe                 = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	boldRe                 = regexp.MustCompile(`\*\*(.+?)\*\*`)
	italicStarRe           = regexp.MustCompile(`\*(.+?)\*`)
	italicUnderRe          = regexp.MustCompile(`_(.+?)_`)
	strikeRe               = regexp.MustCompile(`~~(.+?)~~`)
	blockquoteLineRe       = regexp.MustCompile(`(?m)^&gt;\s?(.*)$`)
)

func codePlaceholder(i int) string {
	return "\x00CODEBLOCK" + strings.Repeat("\x01", i) + "\x00"
}

func inlinePlaceholder(i int) string {
	return "\x00INLINECODE" + strings.Repeat("\x01", i) + "\x00"
}

func expandPlaceholder(i int) string {
	return "\x00EXPANDBLOCK" + strings.Repeat("\x01", i) + "\x00"
}

// convertBlockquotes merges consecutive > lines into <blockquote> blocks.
func convertBlockquotes(text string) string {
	lines := strings.Split(text, "\n")
	var result []string
	inBlock := false

	for _, line := range lines {
		if m := blockquoteLineRe.FindStringSubmatch(line); m != nil {
			if !inBlock {
				result = append(result, "<blockquote>")
				inBlock = true
			}
			result = append(result, m[1])
		} else {
			if inBlock {
				result = append(result, "</blockquote>")
				inBlock = false
			}
			result = append(result, line)
		}
	}
	if inBlock {
		result = append(result, "</blockquote>")
	}
	return strings.Join(result, "\n")
}
