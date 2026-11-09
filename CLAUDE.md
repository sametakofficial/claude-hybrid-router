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
cmd/claude-hybrid/       main.go run(args) dispatch · serve.go serve(ctx), rotating proxy.log
                         daemon.go start/stop/restart/status/panel/env · launch.go runs claude
                         service_linux.go / service_other.go systemd unit · proc_{unix,windows}.go
internal/config/         config.go schema · defaults.go · validate.go (ValidationError) · migrate.go
                         (CurrentVersion, migrations on yaml.Node) · edit.go · paths.go · store.go
                         (atomic 0600 save, Current() read-only, Watch, picks up hand edits)
internal/marker/         agent marker, v1 legacy marker, the one agent-name rule
internal/agents/         agents.go parse/render · dir.go list/get/save/rename/trash
internal/anthropic/      Messages wire helpers: Request, errors, SSE, UsageWriter, token estimate,
                         placeholder thinking signature
internal/router/         Decide(cfg, body) -> Decision (Via, Source, Subscription) · Apply (system_mode)
internal/provider/       Adapter/Reply/Request/UpstreamError · client.go (transport, retries)
                         registry.go · anthropic.go · openai_chat.go · models.go · test.go (Test button)
internal/translate/openaichat/  pure Anthropic <-> Chat Completions (request.go, response.go)
internal/proxy/          proxy.go ServeHTTP · passthrough.go (ReverseProxy to Anthropic) · routed.go
                         (adapter dispatch, error mapping)
internal/stats/          ring buffer of requests + per-target totals, Entry.LogLine
internal/usage/          usage.json, subscription limits, savings report (fed from stats entries
                         and Proxy.OnUpstreamHeaders)
internal/panel/          server.go (mux, guard) · static.go (web/, headers, CSP) · status, providers,
                         agents, rules, settings, rawconfig, secrets, usage, meta, presets
internal/panel/web/      the UI: index.html, css/, js/ (ES modules, no build step), go:embed
providers/opencode/      optional separate Anthropic-API server backed by `opencode run`
```

Dependencies point down: proxy -> router, provider -> anthropic, translate,
config -> marker. panel uses provider (not proxy). Only one external module
(yaml.v3); no frameworks.

## Rules that matter

- The subscription `Authorization` header must only ever reach Anthropic.
  Provider requests are built fresh by the adapter; client headers are never copied.
- Markers are only read from the `system` field, never from messages.
- Never report a provider 401/403 to Claude Code as 401/403 (it would ask the
  user to log in again). See `proxy.providerFailure`.
- Passthrough bodies stay byte-identical unless a marker was removed.
- SSE must be flushed on every write (`FlushInterval: -1`, `provider.CopyFlush`).
- Retries happen only before the first byte reaches Claude Code, never on timeouts.
- Thinking blocks made from OpenAI-style reasoning carry the placeholder
  signature `claude-hybrid:<field>`. They go back to the same provider as that
  field and are removed before anything is sent to Anthropic.
- Agent identity: the file name without `.md` is the id. It is the API id, the
  config key and the marker name; saving writes it into the frontmatter too.
- Config changes go through `Store.Update`, which validates before saving.
  Validation errors are `*config.ValidationError` (panel: 400).
- Secrets (keys, header values, panel token) are masked in every panel
  response; a masked or blank value sent back means "keep" (`panel/secrets.go`).
- Files are never deleted: agent removal/rename moves files to `~/.claude-hybrid/trash/`.
- Adding a provider type: an Adapter in internal/provider, an entry in
  `provider.Registry`, its name in `config.ProviderTypes`.

## Commands

```bash
go test -race ./...        # all tests are in-process (httptest), no network
go vet ./...
make build                 # ./claude-hybrid with version from git
make install               # ~/.local/bin + restart the systemd user service
CLAUDE_HYBRID_HOME=/tmp/x ./claude-hybrid serve   # isolated instance (change ports in its config;
                                                  # stop/restart then never touch the systemd unit)
```

Panel API: the routes are listed in `internal/panel/server.go`; every
`/api/*` call needs the header `X-CHR: 1`. `CLAUDE_HYBRID_WEB_DIR=internal/panel/web`
serves the UI from disk while editing it.
