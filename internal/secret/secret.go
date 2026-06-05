// Package secret resolves secret references stored in configuration files.
//
// A secret value in config can be one of three forms:
//
//   - Plain string: "sk-abc123" — used as-is.
//   - Env reference: "${TELEGRAM_BOT_TOKEN}" — resolved from environment variable.
//   - File reference: "file:///run/secrets/token" — resolved by reading the file.
//
// This mirrors the SecretRef pattern from OpenClaw, simplified for clawdex.
package secret

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// envRefRE matches a full "${VAR_NAME}" reference.
var envRefRE = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)

// envRefScanRE finds "${VAR_NAME}" references inside larger text blobs.
var envRefScanRE = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

const fileRefPrefix = "file://"

// Resolve takes a raw config value and returns the actual secret string.
// An empty input returns empty output with no error.
func Resolve(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}

	// Env reference: ${VAR_NAME}
	if m := envRefRE.FindStringSubmatch(raw); m != nil {
		envVar := m[1]
		val := strings.TrimSpace(os.Getenv(envVar))
		if val == "" {
			return "", fmt.Errorf("environment variable %s is not set or empty", envVar)
		}
		return val, nil
	}

	// File reference: file:///path/to/secret
	if path, ok := strings.CutPrefix(raw, fileRefPrefix); ok {
		if path == "" {
			return "", fmt.Errorf("file reference path is empty")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read secret file %s: %w", path, err)
		}
		val := strings.TrimSpace(string(data))
		if val == "" {
			return "", fmt.Errorf("secret file %s is empty", path)
		}
		return val, nil
	}

	// Plain string: use as-is
	return raw, nil
}

// IsRef returns true if the value is a reference (env or file) rather than
// a plain secret.
func IsRef(raw string) bool {
	raw = strings.TrimSpace(raw)
	return envRefRE.MatchString(raw) || strings.HasPrefix(raw, fileRefPrefix)
}

// Describe returns a human-readable description of the secret value for
// display purposes (without revealing the actual secret).
func Describe(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "(not set)"
	}
	if m := envRefRE.FindStringSubmatch(raw); m != nil {
		return fmt.Sprintf("env: %s", m[1])
	}
	if path, ok := strings.CutPrefix(raw, fileRefPrefix); ok {
		return fmt.Sprintf("file: %s", path)
	}
	return maskSecret(raw)
}

// maskSecret shows the first 6 and last 4 characters of a token.
func maskSecret(s string) string {
	if len(s) <= 10 {
		return "****"
	}
	return s[:6] + "..." + s[len(s)-4:]
}

// FindEnvRefs scans raw text (e.g. a JSON config file) and returns all
// unique environment variable names referenced via ${VAR} patterns.
func FindEnvRefs(data []byte) []string {
	matches := envRefScanRE.FindAllSubmatch(data, -1)
	seen := make(map[string]bool)
	var names []string
	for _, m := range matches {
		name := string(m[1])
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}
