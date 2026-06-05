package gateway

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatThinkingTags_NoTags(t *testing.T) {
	assert.Equal(t, "hello world", FormatThinkingTags("hello world"))
}

func TestFormatThinkingTags_Empty(t *testing.T) {
	assert.Equal(t, "", FormatThinkingTags(""))
}

func TestFormatThinkingTags_ThinkBlock(t *testing.T) {
	input := "before<think>some reasoning</think>after"
	expected := "before<blockquote expandable>some reasoning</blockquote>after"
	assert.Equal(t, expected, FormatThinkingTags(input))
}

func TestFormatThinkingTags_ThinkingBlock(t *testing.T) {
	input := "before<thinking>some reasoning</thinking>after"
	expected := "before<blockquote expandable>some reasoning</blockquote>after"
	assert.Equal(t, expected, FormatThinkingTags(input))
}

func TestFormatThinkingTags_ThoughtBlock(t *testing.T) {
	input := "before<thought>some reasoning</thought>after"
	expected := "before<blockquote expandable>some reasoning</blockquote>after"
	assert.Equal(t, expected, FormatThinkingTags(input))
}

func TestFormatThinkingTags_AntThinking(t *testing.T) {
	input := "before<antThinking>some reasoning</antThinking>after"
	expected := "before<blockquote expandable>some reasoning</blockquote>after"
	assert.Equal(t, expected, FormatThinkingTags(input))
}

func TestFormatThinkingTags_MultipleBlocks(t *testing.T) {
	input := "<think>first</think>middle<think>second</think>end"
	expected := "<blockquote expandable>first</blockquote>middle<blockquote expandable>second</blockquote>end"
	assert.Equal(t, expected, FormatThinkingTags(input))
}

func TestFormatThinkingTags_Unclosed(t *testing.T) {
	// Unclosed tag during streaming — show thinking content so far.
	input := "visible text<think>still thinking..."
	expected := "visible text<blockquote expandable>still thinking...</blockquote>"
	assert.Equal(t, expected, FormatThinkingTags(input))
}

func TestFormatThinkingTags_InsideFencedCode(t *testing.T) {
	input := "```\n<think>code example</think>\n```"
	assert.Equal(t, input, FormatThinkingTags(input))
}

func TestFormatThinkingTags_InsideInlineCode(t *testing.T) {
	input := "use `<think>` tags"
	assert.Equal(t, input, FormatThinkingTags(input))
}

func TestFormatThinkingTags_CaseInsensitive(t *testing.T) {
	input := "before<THINK>reasoning</THINK>after"
	expected := "before<blockquote expandable>reasoning</blockquote>after"
	assert.Equal(t, expected, FormatThinkingTags(input))
}

func TestFormatThinkingTags_WithAttributes(t *testing.T) {
	input := "before<thinking type=\"internal\">reasoning</thinking>after"
	expected := "before<blockquote expandable>reasoning</blockquote>after"
	assert.Equal(t, expected, FormatThinkingTags(input))
}

func TestFormatThinkingTags_Multiline(t *testing.T) {
	input := "before\n<think>\nline1\nline2\n</think>\nafter"
	expected := "before\n<blockquote expandable>line1\nline2</blockquote>\nafter"
	assert.Equal(t, expected, FormatThinkingTags(input))
}

func TestFormatThinkingTags_EmptyThinkBlock(t *testing.T) {
	input := "before<think></think>after"
	assert.Equal(t, "beforeafter", FormatThinkingTags(input))
}

func TestFormatThinkingTags_WhitespaceOnlyThinkBlock(t *testing.T) {
	input := "before<think>   \n  </think>after"
	assert.Equal(t, "beforeafter", FormatThinkingTags(input))
}
