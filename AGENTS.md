# Agent Development Guide

Rules and conventions for AI agents working on this codebase.

## Core Principles

1. **Design before code** — Understand the existing architecture before changing
   anything. Read the relevant packages, identify the patterns, and follow them.
   A new feature should look like it was written by the same author as the rest.

2. **No accidental complexity** — Do not add code "just in case." Every struct,
   function, and file must earn its place. If you cannot explain why something
   exists in one sentence, it probably shouldn't.

3. **Minimal surface area** — Prefer editing existing files over creating new
   ones. Prefer extending an existing interface over inventing a new abstraction.
   The best code is the code you didn't write.

## Architecture Awareness

Before implementing anything non-trivial:

- Read `internal/channel/channel.go` — the interface contract all drivers follow.
- Read one existing driver (telegram or wecom) to absorb the conventions.
- Read `internal/gateway/service.go` — the orchestration layer your driver plugs into.
- Read `internal/config/config.go` — how config flows from file to runtime.
- Read `internal/app/gateway.go` — how drivers are constructed and wired.

New channels must implement `channel.Driver` (at minimum) and optionally
`MediaResponder`, `StreamResponder`, `ThinkingIndicator`, etc. The gateway
handles all business logic — drivers only handle transport.

## Code Quality Standards

### Structure

- **One file per concern**: `types.go`, `api.go`, `crypto.go`, `driver.go`.
  Each source file has a matching `_test.go`.
- **Unexported by default**: Only export what other packages genuinely need.
- **Constructor pattern**: `New(cfg Config, ...) *Driver` with defaults applied
  inside.

### Style

- Follow existing naming: `camelCase` locals, `PascalCase` exports, short
  receiver names (`d` for Driver, `c` for Client, `r` for Responder).
- Structured logging: always include `"channel", d.name` in log fields.
- Errors: `fmt.Errorf("context: %w", err)` — wrap with context, never swallow.
- No panics in production paths. `panic` only in truly impossible branches
  (crypto/rand failure).

### Patterns to follow

- **Access control**: `checkAccess` → `accessAllowed | accessDenied | accessPairing`.
- **String IDs → int64**: Hash via `fnv.New64a()` for `channel.Message.ChatID`.
- **Graceful degradation**: If typing ticket is unavailable, typing becomes a
  no-op. If media upload fails, send an error notice but don't crash.
- **Per-user state in `sync.Map` or `map` + `sync.RWMutex`**: context tokens,
  typing tickets, allowlists.

### What NOT to do

- Do not add a feature just because you can. Ask: does this solve a real problem
  the user reported?
- Do not add abstractions for a single use case. Wait until the pattern repeats.
- Do not add config options without a clear default that works for 90% of users.
- Do not scatter business logic across driver and gateway. Drivers handle I/O;
  the gateway handles orchestration.
- Do not hardcode Chinese/English strings in the driver. User-facing messages
  that depend on locale belong in the SOUL prompt or gateway commands.

## Testing

### Requirements

- Every PR must pass `go test ./...` with zero regressions.
- New packages must have ≥85% statement coverage.
- Each source file (`foo.go`) has a corresponding `foo_test.go`.
- Tests must be fast (<30s total). Use `httptest.NewServer` for API mocking,
  not real network calls.

### Test patterns

- **Table-driven tests** for pure functions (crypto, parsing, helpers).
- **httptest mocks** for API client methods.
- **Context cancellation tests** for long-running loops (Start, polling).
- **Interface satisfaction** verified via compile-time assertions or mock handlers.

### Coverage gaps that are acceptable

- `crypto/rand` failure branches (cannot be triggered in tests).
- `os.MkdirTemp` / filesystem failure branches (system-level).
- Exact timeout races in streaming tests.

## Documentation

### When to update docs

- New channel → add `docs/<CHANNEL>.md` (English) and `docs/<CHANNEL>_CN.md`
  (Chinese).
- New config field → update the config example in README and channel doc.
- New CLI command → update `printUsage()` in `main.go` and README.
- Architecture change → update the tree in README.

### Doc style

- Match the tone of existing docs: concise, technical, no fluff.
- Use tables for config references and feature matrices.
- Include the actual `clawdex onboard` terminal output as examples.
- Troubleshooting section at the end of each channel doc.

## Release Process

See [RELEASE.md](RELEASE.md) for the full flow. Key points:

- Bump `internal/version/version.go` before tagging.
- All tests must pass before release.
- Release notes must be structured (see RELEASE.md format).
- Build all four platform artifacts.

## Commit Messages

Follow conventional commits:

```
feat(weixin): add typing indicator with 5s keepalive
fix(gateway): prevent duplicate reply when FinishStream succeeds
docs: add Weixin channel documentation
chore: bump version to 0.2.1
refactor(codex): extract JSONL parsing into helper
```

Scope is the package or area: `weixin`, `gateway`, `codex`, `config`, `onboard`.
