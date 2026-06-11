package cron

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewID(t *testing.T) {
	first := newID()
	second := newID()

	assert.True(t, strings.HasPrefix(first, "cron_"))
	assert.Len(t, first, len("cron_")+16)
	assert.NotEqual(t, first, second)
}
