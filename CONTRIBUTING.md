# Contributing

## Requirements

- Go 1.24 or later
- No external runtime dependencies (single static binary)

## Build

```bash
# Main proxy binary
go build -o claude-hybrid ./cmd/claude-hybrid

# opencode bridge binary
go build -o opencode-bridge ./providers/opencode
```

## Test

```bash
# Run all tests (no external services needed)
go test ./... -v

# Vet for common mistakes
go vet ./...
```

## Cross-compile check

Before submitting a PR, verify the build succeeds on all target platforms:

```bash
GOOS=linux   GOARCH=amd64 go build ./cmd/claude-hybrid
GOOS=darwin  GOARCH=amd64 go build ./cmd/claude-hybrid
GOOS=windows GOARCH=amd64 go build ./cmd/claude-hybrid
```

## Pull Request Guidelines

1. **Tests must pass** on Linux, macOS, and Windows (CI runs all three).
2. **Cross-compilation must be clean** -- no platform-specific code outside `_unix.go` / `_windows.go` / `_darwin.go` / `_linux.go` files with appropriate build tags.
3. **`go vet ./...` must pass** with no warnings.
4. Keep commits focused. One logical change per commit.
5. Update `CLAUDE.md` if you add, remove, or rename files in `cmd/`, `internal/`, or `providers/`.

## Project Structure

See `CLAUDE.md` for a detailed breakdown of the repository structure and architecture.
