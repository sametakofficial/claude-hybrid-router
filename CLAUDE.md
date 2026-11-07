# CLAUDE.md

Guidance for Claude Code when working in this repository.

## What this is

`claude-hybrid` is a local router for Claude Code. Claude Code runs with
`ANTHROPIC_BASE_URL=http://127.0.0.1:3457`. Normal requests pass through to
Anthropic untouched (subscription OAuth). Requests from routed subagents, or
whose model matches a rule, go to other providers (OpenAI-compatible with
translation, or Anthropic-compatible directly). A web panel on :3458 manages
providers, agents, rules and settings. v2 has no MITM and no root CA.

## Layout

```
cmd/claude-hybrid/       main.go: subcommands (serve/start/stop/status/panel/env/install-service) + claude launcher
                         proc_unix.go / proc_windows.go: detach + terminate
internal/config/         schema, defaults, validation, Store (atomic 0600 save, Current() is read-only)
internal/agents/         parse/render ~/.claude/agents/*.md, marker regex, trash instead of delete
internal/proxy/          route.go: decide() marker → agent route, legacy URL marker, model rules
                         proxy.go: passthrough (httputil.ReverseProxy), toOpenAI, toAnthropic, errors, retries
                         writer.go: statusWriter that reads token usage from SSE/JSON without changing bytes
                         test.go: Proxy.Test used by the panel's Test button
internal/translate/      request.go (Anthropic → Chat Completions), response.go (JSON + streaming back)
internal/stats/          ring buffer of requests + per-target totals
internal/panel/          JSON API + guard (Host check, X-CHR header, optional token), presets
internal/panel/web/      index.html: the whole UI, vanilla JS, embedded with go:embed
providers/opencode/      optional separate Anthropic-API server backed by `opencode run`
```

## Rules that matter

- The subscription `Authorization` header must only ever reach Anthropic.
  Provider requests are built fresh; client headers are never copied.
- Markers are only read from the `system` field, never from messages.
- Never report a provider 401/403 to Claude Code as 401/403 (it would ask the
  user to log in again). See `providerError`.
- Passthrough bodies stay byte-identical unless a marker was removed.
- SSE must be flushed on every write (`FlushInterval: -1`, `copyFlush`).
- Config changes go through `Store.Update`, which validates before saving.
- Files are never deleted: agent removal/rename moves files to `~/.claude-hybrid/trash/`.

## Commands

```bash
go test -race ./...        # all tests are in-process (httptest), no network
go vet ./...
make build                 # ./claude-hybrid with version from git
make install               # ~/.local/bin + restart the systemd user service
CLAUDE_HYBRID_HOME=/tmp/x ./claude-hybrid serve   # isolated instance (change ports in its config)
```

Panel API is documented by the handlers in `internal/panel/panel.go`; every
`/api/*` call needs the header `X-CHR: 1`.
