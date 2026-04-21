# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Local Claude Code routing proxy. It preserves Anthropic subscription traffic while inspecting Claude requests for a routing marker and forwarding marked requests to alternate base URLs. In practice Claude Code can reach the proxy through CONNECT+MITM and through direct API requests routed to the local listener.

**Routing marker format:** `@proxy-local-route:af83e9 url=<endpoint>`

The proxy checks both the top-level `system` field and Claude Code's `<system-reminder>` blocks that land in `messages[].content[].text`.

## Repository Structure

```
├── go.mod
├── cmd/
│   ├── claude-hybrid/
│   │   ├── main.go              # Launcher: cert gen, config load, start proxy, exec claude
│   │   ├── bundle.go            # Combined CA bundle creation (system roots + MITM CA)
│   │   ├── claude_unix.go       # Unix: resolveClaudeBinary() returns "claude"
│   │   ├── claude_windows.go    # Windows PATH resolution (Desktop app shadowing fix)
│   │   ├── env.go               # Child process environment variable management
│   │   ├── log_unix.go          # Unix log file truncation with flock
│   │   ├── log_windows.go       # Windows log file truncation (exclusive open)
│   │   ├── selfcheck.go         # Startup self-check: CA cert generation + validation
│   │   ├── trust.go             # `claude-hybrid trust` subcommand (install/status)
│   │   ├── trust_darwin.go      # macOS Keychain CA trust integration
│   │   ├── trust_linux.go       # Linux CA trust store integration
│   │   ├── trust_unix.go        # Shared Unix trust helpers
│   │   ├── trust_windows.go     # Windows certificate store trust integration
│   │   └── versioning.go        # Build version embedding and display
│   └── integration-test/
│       └── main.go              # Integration test runner against real providers
├── internal/
│   ├── config/
│   │   ├── config.go            # Env-overridable constants (timeouts, limits)
│   │   └── routes.go            # YAML config parsing, route/provider resolution
│   ├── mitm/
│   │   ├── mitm.go              # CA generation, per-domain cert gen, LRU cache
│   │   └── mitm_test.go         # MITM cert generation tests
│   ├── proxy/
│   │   ├── proxy.go             # CONNECT handler, MITM TLS, tunnel loop, upstream/local forwarding
│   │   ├── route.go             # Route marker detection + stub response generation
│   │   ├── errors.go            # Anthropic-format error response builders
│   │   ├── proxy_test.go        # Core proxy integration tests
│   │   ├── route_test.go        # Route marker detection tests
│   │   ├── egress_test.go       # Egress forwarding tests
│   │   └── testhelpers_test.go  # Shared test fixtures
│   └── testutil/
│       ├── certs.go             # Test cert generation helpers
│       ├── echo.go              # Mock HTTPS echo server
│       └── musistudio.go        # Mock Musistudio/claude-code-router server
└── providers/
    └── opencode/
        ├── main.go              # opencode-bridge: Anthropic-API server delegating to opencode CLI
        ├── cache.go             # Per-agent session ID cache
        └── bridge_test.go       # Bridge unit tests
```

## Commands

```bash
# Run all tests
go test ./... -v

# Build the binary
go build -o claude-hybrid ./cmd/claude-hybrid

# Run it (starts proxy + claude)
./claude-hybrid

# Pass claude flags after --
./claude-hybrid -- --dangerously-skip-permissions

# Verbose logging
./claude-hybrid --verbose

# Proxy-only mode (for testing without claude)
./claude-hybrid --proxy-only

# Integration test against real provider (requires proxy running)
go run ./cmd/integration-test -proxy HOST:PORT [-stream]
```

## Architecture

```
Claude Code  ──►  Proxy (localhost:random)
                     ├─ CONNECT path: terminate TLS with generated MITM cert
                     ├─ Direct HTTP path: accept API requests routed to local listener
                     ├─ Read JSON body and detect `@proxy-local-route`
                     ├─ If marker found + url=BASE_URL:
                     │   ├─ Strip marker from request body
                     │   ├─ Optionally rewrite system prompt / inject reminder
                     │   └─ Forward to BASE_URL and relay response
                     └─ If no marker:
                         └─ Forward upstream to Anthropic unchanged
```

## Key Files

| File | Purpose |
|------|---------|
| `cmd/claude-hybrid/main.go` | Launcher: CA cert gen (with lock file for multi-instance safety), config load, proxy start, graceful shutdown, exec claude with env vars |
| `cmd/claude-hybrid/bundle.go` | Combined CA bundle creation: merges system roots + MITM CA into a single PEM file |
| `cmd/claude-hybrid/claude_unix.go` | Unix: `resolveClaudeBinary()` returns `"claude"` |
| `cmd/claude-hybrid/claude_windows.go` | Windows: PATH resolution to avoid Desktop app shadowing the CLI binary |
| `cmd/claude-hybrid/env.go` | Builds child-process environment, stripping stale vars before injecting proxy settings |
| `cmd/claude-hybrid/selfcheck.go` | Startup self-check: CA cert generation, validation, and lock-file safety |
| `cmd/claude-hybrid/trust.go` | `claude-hybrid trust install\|status` subcommand dispatcher |
| `cmd/claude-hybrid/versioning.go` | Build-time version embedding (`-ldflags`) and `--version` output |
| `internal/proxy/proxy.go` | Core proxy: CONNECT handler, MITM TLS, keep-alive tunnel loop, upstream forwarding, local model forwarding |
| `internal/proxy/route.go` | Route marker detection in `system` and `messages[].content[].text` + Anthropic stub response (JSON and SSE) |
| `internal/proxy/errors.go` | Anthropic-format error response builders for local provider failures |
| `internal/config/config.go` | Constants: timeouts, body size limits, concurrency cap |
| `internal/config/routes.go` | YAML config parsing (`~/.claude-hybrid/config.yaml`), route and provider resolution |
| `internal/mitm/mitm.go` | Dynamic per-domain cert generation + LRU tls.Certificate cache |
| `providers/opencode/main.go` | opencode-bridge: Anthropic-API-compatible server that delegates to opencode CLI via Bash tool_use |
| `providers/opencode/cache.go` | Per-agent opencode session ID cache with TTL |

## Testing

Tests run in-process — no external services needed. Certificates are generated programmatically in memory. The proxy, echo server, and mock OpenAI server start in goroutines. Tests exercise: clean request forwarding, GET requests, keep-alive (multiple requests per tunnel), local route detection (non-streaming and streaming), marker stripping from `system` and message reminder blocks, auth header sanitization in logs, request/response translation, streaming translation, tool use round-trips, error handling, and local provider error propagation (truncated responses, garbled SSE streams).

## Development Notes

- Go 1.24+ required
- One external dependency: `gopkg.in/yaml.v3` (for config parsing)
- MITM certs generated in memory via `tls.X509KeyPair`
- CA certs stored in `~/.claude-hybrid/certs/` (auto-generated on first run, lock file prevents races)
- Provider config at `~/.claude-hybrid/config.yaml` (optional)
- Logs written to `~/.claude-hybrid/proxy.log` (daily rotation with flock, session ID prefix `[s<pid>]`)
- `--verbose` enables detailed logging (including dropped SSE chunks); default is sparse (LOCAL_ROUTE + LOCAL_OK + LOCAL_ERR)
- Error log prefixes: `[LOCAL_ERR:CONNECTION]`, `[LOCAL_ERR:TIMEOUT]`, `[LOCAL_ERR:HTTP_N]`, `[LOCAL_ERR:TRANSLATE]`, `[LOCAL_ERR:PARSE]`
- API keys in provider error responses are redacted before logging
- Multiple instances safe: each gets its own proxy port, shares CA cert (read-only) and log file (append)
- Graceful shutdown: 5s timeout for in-flight requests when Claude exits
- Single static binary — no runtime dependencies
