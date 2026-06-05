package termcolor

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStyleFormatsEnabledText(t *testing.T) {
	style := NewEnabled(true)

	assert.Equal(t, "\033[1mhello\033[0m", style.Bold("hello"))
	assert.Equal(t, "\033[2mhello\033[0m", style.Dim("hello"))
	assert.Equal(t, "\033[31mhello\033[0m", style.Red("hello"))
	assert.Equal(t, "\033[32mhello\033[0m", style.Green("hello"))
	assert.Equal(t, "\033[33mhello\033[0m", style.Yellow("hello"))
	assert.Equal(t, "\033[36mhello\033[0m", style.Cyan("hello"))
}

func TestStyleReturnsPlainTextWhenDisabled(t *testing.T) {
	style := NewEnabled(false)

	assert.Equal(t, "hello", style.Bold("hello"))
	assert.Equal(t, "hello", style.Dim("hello"))
	assert.Equal(t, "hello", style.Red("hello"))
	assert.Equal(t, "hello", style.Green("hello"))
	assert.Equal(t, "hello", style.Yellow("hello"))
	assert.Equal(t, "hello", style.Cyan("hello"))
}

func TestStyleDetectsNonTerminalWriter(t *testing.T) {
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	defer reader.Close()
	defer writer.Close()

	style := New(writer)
	assert.Equal(t, "hello", style.Green("hello"))
}

func TestStyleWithoutWriterDisablesColor(t *testing.T) {
	style := New(nil)
	assert.Equal(t, "hello", style.Cyan("hello"))
}
