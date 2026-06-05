package weixin

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMarkdownFilterImages(t *testing.T) {
	assert.Equal(t, "看这个：", markdownFilter("看这个：![图片](http://example.com/a.jpg)"))
	assert.Equal(t, "前后", markdownFilter("前![x](url)后"))
	assert.Equal(t, "no image here", markdownFilter("no image here"))
}

func TestMarkdownFilterThinkingTags(t *testing.T) {
	assert.Equal(t, "hello world", markdownFilter("hello <thinking>internal thought</thinking>world"))
	assert.Equal(t, "before", markdownFilter("before<thinking>unclosed"))
	assert.Equal(t, "result", markdownFilter("<thinking>thought1</thinking>result"))
}

func TestMarkdownFilterPassthrough(t *testing.T) {
	// Markdown that WeChat renders natively — should NOT be stripped.
	tests := []struct {
		name  string
		input string
	}{
		{"bold", "**加粗文字**"},
		{"italic", "*斜体文字*"},
		{"heading", "## 二级标题"},
		{"code fence", "```go\nfmt.Println(\"hi\")\n```"},
		{"inline code", "使用 `go build` 编译"},
		{"blockquote", "> 这是引用"},
		{"unordered list", "- 项目一\n- 项目二"},
		{"ordered list", "1. 第一步\n2. 第二步"},
		{"table", "| 列1 | 列2 |\n|---|---|\n| a | b |"},
		{"link", "[点击这里](https://example.com)"},
		{"strikethrough", "~~删除线~~"},
		{"h5 heading", "##### 五级标题"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.input, markdownFilter(tc.input))
		})
	}
}

func TestMarkdownFilterCombined(t *testing.T) {
	input := "# 标题\n\n看图：![pic](http://x.com/a.png)\n\n**重点**内容"
	expected := "# 标题\n\n看图：\n\n**重点**内容"
	assert.Equal(t, expected, markdownFilter(input))
}

func TestMarkdownFilterPlainChinese(t *testing.T) {
	text := "这是一段普通的中文文本，没有任何格式。"
	assert.Equal(t, text, markdownFilter(text))
}

func TestRemoveThinkingTagsMultiple(t *testing.T) {
	input := "a<thinking>x</thinking>b<thinking>y</thinking>c"
	assert.Equal(t, "abc", removeThinkingTags(input))
}
