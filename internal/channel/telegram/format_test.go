package telegram

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatTelegramHTML_Empty(t *testing.T) {
	assert.Equal(t, "", FormatTelegramHTML(""))
}

func TestFormatTelegramHTML_PlainText(t *testing.T) {
	assert.Equal(t, "hello world", FormatTelegramHTML("hello world"))
}

func TestFormatTelegramHTML_Bold(t *testing.T) {
	assert.Equal(t, "this is <b>bold</b> text", FormatTelegramHTML("this is **bold** text"))
}

func TestFormatTelegramHTML_ItalicStar(t *testing.T) {
	assert.Equal(t, "this is <i>italic</i> text", FormatTelegramHTML("this is *italic* text"))
}

func TestFormatTelegramHTML_ItalicUnderscore(t *testing.T) {
	assert.Equal(t, "this is <i>italic</i> text", FormatTelegramHTML("this is _italic_ text"))
}

func TestFormatTelegramHTML_Strikethrough(t *testing.T) {
	assert.Equal(t, "this is <s>struck</s> text", FormatTelegramHTML("this is ~~struck~~ text"))
}

func TestFormatTelegramHTML_InlineCode(t *testing.T) {
	assert.Equal(t, "use <code>fmt.Println</code> here", FormatTelegramHTML("use `fmt.Println` here"))
}

func TestFormatTelegramHTML_FencedCodeBlock(t *testing.T) {
	input := "```go\nfmt.Println(\"hello\")\n```"
	expected := "<pre><code>fmt.Println(&quot;hello&quot;)</code></pre>"
	// Note: escapeHTML doesn't escape quotes, but Telegram doesn't require it.
	// Let me check: escapeHTML only escapes & < >
	expected = "<pre><code>fmt.Println(\"hello\")</code></pre>"
	assert.Equal(t, expected, FormatTelegramHTML(input))
}

func TestFormatTelegramHTML_CodeBlockPreservesContent(t *testing.T) {
	// Markdown inside code blocks should NOT be converted.
	input := "```\n**bold** and *italic*\n```"
	expected := "<pre><code>**bold** and *italic*</code></pre>"
	assert.Equal(t, expected, FormatTelegramHTML(input))
}

func TestFormatTelegramHTML_InlineCodePreservesContent(t *testing.T) {
	// HTML entities inside inline code should be escaped.
	assert.Equal(t, "run <code>&lt;script&gt;</code>", FormatTelegramHTML("run `<script>`"))
}

func TestFormatTelegramHTML_HTMLEscaping(t *testing.T) {
	assert.Equal(t, "a &amp; b &lt; c &gt; d", FormatTelegramHTML("a & b < c > d"))
}

func TestFormatTelegramHTML_Link(t *testing.T) {
	assert.Equal(t,
		`click <a href="https://example.com">here</a>`,
		FormatTelegramHTML(`click [here](https://example.com)`))
}

func TestFormatTelegramHTML_Blockquote(t *testing.T) {
	input := "> line one\n> line two"
	expected := "<blockquote>\nline one\nline two\n</blockquote>"
	assert.Equal(t, expected, FormatTelegramHTML(input))
}

func TestFormatTelegramHTML_BlockquoteSingle(t *testing.T) {
	input := "> quoted text"
	expected := "<blockquote>\nquoted text\n</blockquote>"
	assert.Equal(t, expected, FormatTelegramHTML(input))
}

func TestFormatTelegramHTML_Mixed(t *testing.T) {
	input := "**bold** and *italic* with `code`"
	expected := "<b>bold</b> and <i>italic</i> with <code>code</code>"
	assert.Equal(t, expected, FormatTelegramHTML(input))
}

func TestFormatTelegramHTML_BoldItalicNested(t *testing.T) {
	input := "***bold italic***"
	// **X** matches first, leaving *...*
	result := FormatTelegramHTML(input)
	assert.Contains(t, result, "<b>")
	assert.Contains(t, result, "<i>")
}

func TestEscapeHTML(t *testing.T) {
	assert.Equal(t, "&amp;&lt;&gt;", escapeHTML("&<>"))
	assert.Equal(t, "plain", escapeHTML("plain"))
}

func TestFormatTelegramHTML_ExpandableBlockquote(t *testing.T) {
	// Expandable blockquotes from FormatThinkingTags should be preserved,
	// with inner markdown converted to HTML.
	input := "<blockquote expandable>**bold** thinking</blockquote>\nAnswer here"
	expected := "<blockquote expandable><b>bold</b> thinking</blockquote>\nAnswer here"
	assert.Equal(t, expected, FormatTelegramHTML(input))
}

func TestFormatTelegramHTML_ExpandableBlockquoteWithCode(t *testing.T) {
	input := "<blockquote expandable>use `fmt.Println` here</blockquote>"
	expected := "<blockquote expandable>use <code>fmt.Println</code> here</blockquote>"
	assert.Equal(t, expected, FormatTelegramHTML(input))
}

func TestFormatTelegramHTML_ExpandableBlockquoteHTMLEscaping(t *testing.T) {
	// HTML entities inside expandable blockquotes should be escaped.
	input := "<blockquote expandable>a & b < c</blockquote>"
	expected := "<blockquote expandable>a &amp; b &lt; c</blockquote>"
	assert.Equal(t, expected, FormatTelegramHTML(input))
}
