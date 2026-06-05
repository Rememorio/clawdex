package secret

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolve_Empty(t *testing.T) {
	val, err := Resolve("")
	require.NoError(t, err)
	assert.Equal(t, "", val)
}

func TestResolve_PlainString(t *testing.T) {
	val, err := Resolve("123456:ABC-DEF")
	require.NoError(t, err)
	assert.Equal(t, "123456:ABC-DEF", val)
}

func TestResolve_PlainStringTrimmed(t *testing.T) {
	val, err := Resolve("  hello  ")
	require.NoError(t, err)
	assert.Equal(t, "hello", val)
}

func TestResolve_EnvRef(t *testing.T) {
	t.Setenv("TEST_SECRET_VAR", "my-secret-value")
	val, err := Resolve("${TEST_SECRET_VAR}")
	require.NoError(t, err)
	assert.Equal(t, "my-secret-value", val)
}

func TestResolve_EnvRefMissing(t *testing.T) {
	t.Setenv("TEST_SECRET_MISSING", "")
	_, err := Resolve("${TEST_SECRET_MISSING}")
	assert.ErrorContains(t, err, "TEST_SECRET_MISSING")
	assert.ErrorContains(t, err, "not set or empty")
}

func TestResolve_EnvRefUnset(t *testing.T) {
	// Ensure the var does not exist
	os.Unsetenv("TEST_SECRET_UNSET_XYZ")
	_, err := Resolve("${TEST_SECRET_UNSET_XYZ}")
	assert.ErrorContains(t, err, "TEST_SECRET_UNSET_XYZ")
}

func TestResolve_FileRef(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "token.txt")
	require.NoError(t, os.WriteFile(p, []byte("  file-secret-value  \n"), 0o644))

	val, err := Resolve("file://" + p)
	require.NoError(t, err)
	assert.Equal(t, "file-secret-value", val)
}

func TestResolve_FileRefEmpty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.txt")
	require.NoError(t, os.WriteFile(p, []byte("   \n"), 0o644))

	_, err := Resolve("file://" + p)
	assert.ErrorContains(t, err, "empty")
}

func TestResolve_FileRefNotFound(t *testing.T) {
	_, err := Resolve("file:///nonexistent/path/to/secret")
	assert.Error(t, err)
	assert.ErrorContains(t, err, "read secret file")
}

func TestResolve_FileRefEmptyPath(t *testing.T) {
	_, err := Resolve("file://")
	assert.ErrorContains(t, err, "path is empty")
}

func TestResolve_NotAnEnvRef(t *testing.T) {
	// These should be treated as plain strings, not env refs.
	cases := []string{
		"${}", "${}", "${123}", "$NOBRACES", "prefix${VAR}", "${VAR}suffix",
	}
	for _, c := range cases {
		val, err := Resolve(c)
		require.NoError(t, err, "input: %q", c)
		assert.Equal(t, c, val, "input: %q", c)
	}
}

func TestIsRef(t *testing.T) {
	assert.True(t, IsRef("${FOO}"))
	assert.True(t, IsRef("file:///etc/secret"))
	assert.False(t, IsRef("plain-string"))
	assert.False(t, IsRef(""))
}

func TestDescribe_Empty(t *testing.T) {
	assert.Equal(t, "(not set)", Describe(""))
}

func TestDescribe_EnvRef(t *testing.T) {
	assert.Equal(t, "env: MY_VAR", Describe("${MY_VAR}"))
}

func TestDescribe_FileRef(t *testing.T) {
	assert.Equal(t, "file: /run/secrets/token", Describe("file:///run/secrets/token"))
}

func TestDescribe_PlainShort(t *testing.T) {
	assert.Equal(t, "****", Describe("short"))
}

func TestDescribe_PlainLong(t *testing.T) {
	desc := Describe("123456:ABC-DEF-GHIJ")
	assert.Equal(t, "123456...GHIJ", desc)
}

// ── FindEnvRefs tests ──

func TestFindEnvRefs(t *testing.T) {
	data := []byte(`{
		"bot_token": "${TELEGRAM_BOT_TOKEN}",
		"secret": "${WECOM_SECRET}",
		"plain": "no-ref-here",
		"dup": "${TELEGRAM_BOT_TOKEN}"
	}`)
	refs := FindEnvRefs(data)
	assert.Equal(t, []string{"TELEGRAM_BOT_TOKEN", "WECOM_SECRET"}, refs)
}

func TestFindEnvRefs_Empty(t *testing.T) {
	refs := FindEnvRefs([]byte(`{"plain": "value"}`))
	assert.Empty(t, refs)
}

func TestFindEnvRefs_NoData(t *testing.T) {
	refs := FindEnvRefs(nil)
	assert.Empty(t, refs)
}
