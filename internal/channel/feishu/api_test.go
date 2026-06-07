package feishu

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarshalTextContent(t *testing.T) {
	got, err := marshalTextContent(`hello "feishu"`)
	require.NoError(t, err)

	var parsed textContent
	require.NoError(t, json.Unmarshal([]byte(got), &parsed))
	assert.Equal(t, `hello "feishu"`, parsed.Text)
}
