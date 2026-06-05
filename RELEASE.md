# clawdex Release Guide

## Versioning

Semantic versioning. Source of truth: `internal/version/version.go`.

| Bump | When |
|------|------|
| Patch (`0.x.Y`) | Bug fixes, minor improvements |
| Minor (`0.X.0`) | New features, new channel support, breaking config changes |
| Major (`X.0.0`) | Reserved for stable API guarantees (post-1.0) |

Git tags and GitHub releases use the `v` prefix: `v0.2.1`.

## Release Steps

```bash
# 1. Prepare
git checkout main && git pull --ff-only origin main
git fetch --tags origin
gh auth status
VERSION=0.2.1
TAG=v${VERSION}

# 2. Bump version
# Edit internal/version/version.go → var Version = "0.2.1"

# 3. Test
go test ./...

# 4. Commit & push
git add internal/version/version.go
git commit -m "chore: bump version to ${VERSION}"
git push origin main

# 5. Build artifacts
rm -rf dist && mkdir -p dist
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  GOOS=${target%/*} GOARCH=${target#*/} \
    go build -ldflags "-X github.com/Rememorio/clawdex/internal/version.Version=${VERSION}" \
    -o dist/clawdex-${target%/*}-${target#*/} ./cmd/clawdex
done

# 6. Tag & release
git tag ${TAG} && git push origin ${TAG}
gh release create ${TAG} --title "${TAG}" --notes-file /tmp/notes.md --latest dist/*
```

Windows is unsupported (daemon uses Unix process attributes).

## Release Notes Format

Write structured, user-facing notes. Use `--notes-file` or `--notes` with a
heredoc; avoid bare auto-generated changelog links.

```markdown
## Bug Fixes
- **Short title** — one-sentence root cause and fix

## Improvements
- **Short title** — what changed for the user

## Features              (minor/major only)
### Feature Name
- bullet points
```

Style rules:

- `**Bold title**` + ` — ` em dash + explanation.
- Start with a verb: Fix, Add, Remove, Improve.
- English only; no emojis.
- Reference root cause for bug fixes.
- Group related changes into one bullet.

To update a past release:

```bash
gh release edit v0.1.0 --notes "$(cat notes.md)"
```
