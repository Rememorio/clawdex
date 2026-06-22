// Package version provides the clawdex version information.
package version

// Version is the clawdex version (semver, e.g., "0.1.0").
// Set via ldflags at build time:
//
//	go build -ldflags "-X github.com/Rememorio/clawdex/internal/version.Version=0.1.0"
var Version = "1.2.14"
