package termcolor

import "os"

const (
	ansiPrefix = "\033["
	ansiReset  = "\033[0m"
	noColorEnv = "NO_COLOR"
	codeBold   = "1"
	codeDim    = "2"
	codeRed    = "31"
	codeGreen  = "32"
	codeYellow = "33"
	codeCyan   = "36"
)

// Style formats terminal text with ANSI colors when supported.
type Style struct {
	out     *os.File
	enabled *bool
}

// New creates a style that auto-detects color support for the writer.
func New(out *os.File) Style {
	return Style{out: out}
}

// NewEnabled creates a style with a fixed enabled state.
func NewEnabled(enabled bool) Style {
	return Style{enabled: &enabled}
}

// Bold returns bold text when color output is enabled.
func (s Style) Bold(text string) string {
	return s.wrap(codeBold, text)
}

// Dim returns dim text when color output is enabled.
func (s Style) Dim(text string) string {
	return s.wrap(codeDim, text)
}

// Red returns red text when color output is enabled.
func (s Style) Red(text string) string {
	return s.wrap(codeRed, text)
}

// Green returns green text when color output is enabled.
func (s Style) Green(text string) string {
	return s.wrap(codeGreen, text)
}

// Yellow returns yellow text when color output is enabled.
func (s Style) Yellow(text string) string {
	return s.wrap(codeYellow, text)
}

// Cyan returns cyan text when color output is enabled.
func (s Style) Cyan(text string) string {
	return s.wrap(codeCyan, text)
}

func (s Style) wrap(code, text string) string {
	if !s.isEnabled() {
		return text
	}
	return ansiPrefix + code + "m" + text + ansiReset
}

func (s Style) isEnabled() bool {
	if s.enabled != nil {
		return *s.enabled
	}
	return supportsColor(s.out)
}

func supportsColor(out *os.File) bool {
	if out == nil {
		return false
	}

	_, noColor := os.LookupEnv(noColorEnv)
	if noColor {
		return false
	}

	fi, err := out.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
