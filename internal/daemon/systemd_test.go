package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rememorio/clawdex/internal/termcolor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── SystemdStatus tests ──

func TestSystemdStatus_NotInstalled(t *testing.T) {
	setupTestHome(t)
	oldTerm := term
	term = termcolor.NewEnabled(false)
	defer func() { term = oldTerm }()

	output := captureStdout(t, func() {
		SystemdStatus()
	})
	assert.Contains(t, output, "systemd user service is not installed")
	assert.Contains(t, output, "clawdex daemon install")
}

func TestSystemdStatus_UnitFileExists(t *testing.T) {
	home := setupTestHome(t)
	oldTerm := term
	term = termcolor.NewEnabled(false)
	defer func() { term = oldTerm }()

	// Create the unit file so SystemdStatus sees it as installed.
	unitPath := filepath.Join(home, ".config", "systemd", "user", serviceName+".service")
	require.NoError(t, os.MkdirAll(filepath.Dir(unitPath), 0o755))
	require.NoError(t, os.WriteFile(unitPath, []byte("[Unit]\nDescription=test\n"), 0o644))

	output := captureStdout(t, func() {
		SystemdStatus()
	})
	// Should print the unit file path regardless of systemctl availability.
	assert.Contains(t, output, unitPath)
	// Should print the journalctl hint.
	assert.Contains(t, output, "journalctl --user -u "+serviceName)
}

func TestSystemdStatus_HomeError(t *testing.T) {
	t.Setenv("HOME", "")
	oldTerm := term
	term = termcolor.NewEnabled(false)
	defer func() { term = oldTerm }()

	// Should not panic even when HOME is unset.
	output := captureStdout(t, func() {
		SystemdStatus()
	})
	// Either prints an error or "not installed" — just verify no panic.
	_ = output
}

func TestSystemd_UnitFilePath(t *testing.T) {
	home := setupTestHome(t)
	path, err := unitFilePath()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".config", "systemd", "user", "clawdex-gateway.service"), path)
}

func TestSystemd_GenerateUnit(t *testing.T) {
	content, err := generateUnit("/usr/local/bin/clawdex")
	require.NoError(t, err)

	assert.Contains(t, content, "ExecStart=/usr/local/bin/clawdex gateway run")
	assert.Contains(t, content, "Restart=on-failure")
	assert.Contains(t, content, "RestartSec=5")
	assert.Contains(t, content, "WorkingDirectory=%h/.clawdex")
	assert.Contains(t, content, "EnvironmentFile=-%h/.clawdex/env")
	assert.Contains(t, content, "WantedBy=default.target")
	assert.Contains(t, content, "After=network-online.target")

	// Verify it's a valid INI-ish structure with expected sections.
	assert.Contains(t, content, "[Unit]")
	assert.Contains(t, content, "[Service]")
	assert.Contains(t, content, "[Install]")
}

func TestSystemd_GenerateUnit_PathWithSpaces(t *testing.T) {
	content, err := generateUnit("/home/user/my programs/clawdex")
	require.NoError(t, err)
	assert.Contains(t, content, "ExecStart=/home/user/my programs/clawdex gateway run")
}

func TestSystemd_UnitFilePath_HomeError(t *testing.T) {
	t.Setenv("HOME", "")
	// On some systems unsetting HOME still works; we just verify no panic.
	_, _ = unitFilePath()
}

func TestSystemd_Uninstall_NoUnitFile(t *testing.T) {
	setupTestHome(t)

	// Uninstall when no unit file exists should not fail on Remove
	// (it will fail on systemctl which isn't available in test, but
	// we're testing the os.Remove tolerance for missing files).
	unitPath, err := unitFilePath()
	require.NoError(t, err)

	// Ensure the file doesn't exist.
	_, err = os.Stat(unitPath)
	assert.True(t, os.IsNotExist(err))

	// We can't fully test Uninstall without systemctl, but we can verify
	// that os.Remove of a non-existent file is tolerated.
	err = os.Remove(unitPath)
	assert.True(t, os.IsNotExist(err))
}

func TestSystemd_GenerateUnit_Content(t *testing.T) {
	content, err := generateUnit("/opt/clawdex/bin/clawdex")
	require.NoError(t, err)

	// Verify the description.
	assert.Contains(t, content, "Description=clawdex gateway")

	// Count sections — should have exactly 3.
	sections := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			sections++
		}
	}
	assert.Equal(t, 3, sections)
}

func TestSystemd_EnsureEnvTemplate_CreatesFile(t *testing.T) {
	home := setupTestHome(t)
	envPath := filepath.Join(home, ".clawdex", "env")

	// File should not exist initially.
	_, err := os.Stat(envPath)
	require.True(t, os.IsNotExist(err))

	// Call ensureEnvTemplate.
	err = ensureEnvTemplate()
	require.NoError(t, err)

	// File should now exist with expected content.
	data, err := os.ReadFile(envPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "TELEGRAM_BOT_TOKEN")
	assert.Contains(t, string(data), "WECOM_TOKEN")
	assert.Contains(t, string(data), "WECOM_BOTID")
	assert.Contains(t, string(data), "WECOM_SECRET")
}

func TestSystemd_EnsureEnvTemplate_DoesNotOverwrite(t *testing.T) {
	home := setupTestHome(t)
	envPath := filepath.Join(home, ".clawdex", "env")

	// Create an existing env file with custom content.
	existingContent := "MY_CUSTOM_VAR=value123\n"
	err := os.MkdirAll(filepath.Dir(envPath), 0o755)
	require.NoError(t, err)
	err = os.WriteFile(envPath, []byte(existingContent), 0o600)
	require.NoError(t, err)

	// Call ensureEnvTemplate.
	err = ensureEnvTemplate()
	require.NoError(t, err)

	// File should still contain the original content.
	data, err := os.ReadFile(envPath)
	require.NoError(t, err)
	assert.Equal(t, existingContent, string(data))
}

// ── parseEnvFileKeys tests ──

func TestParseEnvFileKeys(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "env")
	content := "FOO=bar\n#BAZ=commented\nQUX=123\n"
	require.NoError(t, os.WriteFile(envPath, []byte(content), 0o600))

	keys := parseEnvFileKeys(envPath)
	assert.True(t, keys["FOO"])
	assert.False(t, keys["BAZ"]) // commented-out lines are NOT considered defined
	assert.True(t, keys["QUX"])
	assert.False(t, keys["MISSING"])
}

func TestParseEnvFileKeys_Nonexistent(t *testing.T) {
	keys := parseEnvFileKeys("/nonexistent/path")
	assert.Empty(t, keys)
}

// ── syncEnvFile tests ──

func TestSyncEnvFile_AppendsMissingVars(t *testing.T) {
	home := setupTestHome(t)
	dataDir := filepath.Join(home, ".clawdex")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	// Write a config with two ${VAR} references.
	configPath := filepath.Join(dataDir, "clawdex.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"channels": {
			"tg": {"bot_token": "${TG_TOKEN_TEST}"},
			"wc": {"secret": "${WC_SECRET_TEST}"}
		}
	}`), 0o644))

	// Write an env file that already has WC_SECRET_TEST.
	envPath := filepath.Join(dataDir, "env")
	require.NoError(t, os.WriteFile(envPath, []byte("WC_SECRET_TEST=existing\n"), 0o600))

	// Set TG_TOKEN_TEST in current shell.
	t.Setenv("TG_TOKEN_TEST", "my-tg-token")

	err := syncEnvFile()
	require.NoError(t, err)

	// Env file should now have TG_TOKEN_TEST appended.
	data, err := os.ReadFile(envPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "TG_TOKEN_TEST=my-tg-token")
	// Original content preserved.
	assert.Contains(t, string(data), "WC_SECRET_TEST=existing")
	// WC_SECRET_TEST should NOT be duplicated.
	assert.Equal(t, 1, strings.Count(string(data), "WC_SECRET_TEST"))
}

func TestSyncEnvFile_AppendsPathWithoutConfigRefs(t *testing.T) {
	home := setupTestHome(t)
	dataDir := filepath.Join(home, ".clawdex")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	envPath := filepath.Join(dataDir, "env")
	require.NoError(t, os.WriteFile(envPath, []byte(""), 0o600))

	pathValue := "/tmp/codex-bin:/usr/bin"
	t.Setenv("PATH", pathValue)

	err := syncEnvFile()
	require.NoError(t, err)

	data, err := os.ReadFile(envPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "PATH="+pathValue)
}

func TestSyncEnvFile_NoConfigFile(t *testing.T) {
	setupTestHome(t)
	// No config file should still be tolerated.
	err := syncEnvFile()
	assert.NoError(t, err)
}

func TestSyncEnvFile_NoMissingVars(t *testing.T) {
	home := setupTestHome(t)
	dataDir := filepath.Join(home, ".clawdex")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	configPath := filepath.Join(dataDir, "clawdex.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"channels": {"tg": {"bot_token": "${ALREADY_THERE}"}}
	}`), 0o644))

	envPath := filepath.Join(dataDir, "env")
	pathValue := "/tmp/test-bin:/usr/bin"
	require.NoError(t, os.WriteFile(
		envPath,
		[]byte("ALREADY_THERE=value\nPATH="+pathValue+"\n"),
		0o600,
	))

	t.Setenv("ALREADY_THERE", "value")
	t.Setenv("PATH", pathValue)

	err := syncEnvFile()
	require.NoError(t, err)

	// File should not be modified (no "Auto-synced" comment).
	data, err := os.ReadFile(envPath)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "Auto-synced")
}

func TestSyncEnvFile_SyncsOverCommentedTemplateEntries(t *testing.T) {
	// Simulates fresh install: ensureEnvTemplate creates file with
	// commented entries, then syncEnvFile should still sync from shell.
	home := setupTestHome(t)
	dataDir := filepath.Join(home, ".clawdex")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	configPath := filepath.Join(dataDir, "clawdex.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"channels": {
			"tg": {"bot_token": "${MY_TG_TOKEN}"},
			"wc": {"secret": "${MY_WC_SECRET}"}
		}
	}`), 0o644))

	// Env file has only commented template entries (like ensureEnvTemplate creates).
	envPath := filepath.Join(dataDir, "env")
	require.NoError(t, os.WriteFile(envPath, []byte(
		"# Template\n#MY_TG_TOKEN=placeholder\n#MY_WC_SECRET=placeholder\n",
	), 0o600))

	// Set both vars in shell.
	t.Setenv("MY_TG_TOKEN", "real-tg-token")
	t.Setenv("MY_WC_SECRET", "real-wc-secret")

	err := syncEnvFile()
	require.NoError(t, err)

	data, err := os.ReadFile(envPath)
	require.NoError(t, err)
	content := string(data)
	// Both should be synced because commented entries don't count as defined.
	assert.Contains(t, content, "MY_TG_TOKEN=real-tg-token")
	assert.Contains(t, content, "MY_WC_SECRET=real-wc-secret")
}
