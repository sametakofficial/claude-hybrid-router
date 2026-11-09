# claude-hybrid

Keep using Claude Code with your Anthropic subscription, and send **chosen
subagents** (or chosen models) to **any other provider**: OpenAI,
OpenRouter, DeepSeek, Gemini, Groq, a local Ollama, your own gateway.
Everything is managed from a small web panel.

```
                       ┌──────────────── claude-hybrid ────────────────┐
Claude Code ──HTTP──▶  │ proxy :3457                                   │ ──▶ api.anthropic.com
(ANTHROPIC_BASE_URL)   │  • normal request ............ pass through   │     (your subscription)
                       │  • subagent with a route ..... translate ─────┼──▶ OpenAI-compatible API
                       │  • model matches a rule ...... forward ───────┼──▶ Anthropic-compatible API
                       │ web panel :3458  (providers, agents, rules)   │
                       └───────────────────────────────────────────────┘
```

* One static Go binary, one dependency (`gopkg.in/yaml.v3`).
* No MITM, no root certificate, no Docker sidecar. Claude Code talks to the
  proxy over plain HTTP on localhost via `ANTHROPIC_BASE_URL`.
* Built-in Anthropic ⇄ OpenAI Chat Completions translation, including tool
  calls, images, streaming and token usage.
* Your subscription token is only ever sent to Anthropic. Routed requests
  carry the provider's own key.

## Quick start

```bash
go build -o claude-hybrid ./cmd/claude-hybrid
cp claude-hybrid ~/.local/bin/          # anywhere on PATH

claude-hybrid panel                     # starts the router and opens http://127.0.0.1:3458
claude-hybrid                           # use this instead of `claude`
```

`claude-hybrid` starts the router in the background if it is not running,
then runs `claude` with every argument you give it (`claude-hybrid --resume`,
`claude-hybrid -p "hi"`, ...).

On Linux you can make the router a systemd user service so it is always up:

```bash
claude-hybrid install-service
```

To use the router from an existing shell or an IDE, point Claude Code at it:

```bash
eval "$(claude-hybrid env)"   # export ANTHROPIC_BASE_URL=http://127.0.0.1:3457
```

## Using the panel

1. **Providers → Add provider.** Pick a template (OpenAI, OpenRouter,
   DeepSeek, Gemini, Groq, Ollama, LM Studio, Z.ai, Kimi, any
   OpenAI-compatible or Anthropic-compatible URL), paste the key, press
   **Fetch from provider** to load the model list, then **Test**.
2. **Agents.** Lists the subagents in `~/.claude/agents`. Create or edit one:
   name, description, tools, color, system prompt. Under **Routing**, pick a
   provider and model. Leave it on *Anthropic (subscription)* to keep it on
   Claude.
3. **Rules** (optional). Route requests that are not from a routed agent by
   the model Claude Code asks for. Example: `claude-haiku-*` → a cheap model
   (Claude Code uses Haiku for small background jobs). `*` routes everything.
   First match wins; no match means Anthropic.
4. **Logs** shows every request: which agent or rule matched, where it
   went, status, latency, and tokens.

Per route you can also set a model, a `max_tokens` cap, temperature,
reasoning effort (sent as `reasoning_effort` to OpenAI-style APIs), and the
system prompt mode:

| mode      | effect |
|-----------|--------|
| `keep`    | send Claude Code's system prompt as is (default) |
| `append`  | add your text after it, e.g. extra rules for a weaker model |
| `replace` | send only your text |

Changes apply immediately, no restart needed (except listen addresses).

## How routing works

When you save an agent in the panel, claude-hybrid writes one line into the
agent file, right after the frontmatter:

```markdown
---
name: coder
description: Writes and edits code.
tools: Read, Edit, Bash
---
<!-- claude-hybrid:agent=coder -->

You are coder...
```

Claude Code sends the agent's prompt as the request's `system` field. The
proxy finds the marker there, removes it, and looks up the agent's route in
the config. The provider and model live in the config, not in the file, so
switching models is one click and the agent file never changes.

The agent's id is its file name (`coder.md` is `coder`): the panel, the
config key and the marker all use it, and saving writes the same name into
the frontmatter. A route in the config whose agent file is gone is shown in
the panel as missing, where you can recreate the file or remove the route.

Only the `system` field is scanned. A marker that appears in a chat message
or tool output (for example when Claude reads an agent file) never reroutes
the main conversation.

The v1 marker `<!-- @proxy-local-route:af83e9 url=http://... -->` still works:
the request is forwarded unchanged to that Anthropic-compatible URL.

## Provider types

| type | sent to | notes |
|------|---------|-------|
| `openai` | `{base_url}/chat/completions` | Translated. Server tools such as web search (and their results in the history) are dropped. Reasoning text (`reasoning_content` / `reasoning`, e.g. DeepSeek, Kimi, GLM, OpenRouter) is shown as thinking and sent back to the provider in the same field; Claude's own thinking blocks are not sent. Use `max_tokens_field: max_completion_tokens` for OpenAI's reasoning models. |
| `anthropic` | `{base_url}/v1/messages` | Passed through with the model, `max_tokens` and temperature overrides; a thinking budget is kept below a capped `max_tokens`. Key sent as both `x-api-key` and `Authorization: Bearer`. `anthropic-beta` headers are not forwarded, so beta-only request fields (`context_management`, 1h cache TTL, ...) are removed. |

Provider errors come back to Claude Code as Anthropic-style errors that name
the provider; a provider's "context length exceeded" is reported as "prompt is
too long", so Claude Code compacts the conversation. Connection failures and
502/503/504 are retried quickly before anything is sent to Claude Code;
timeouts are not retried. A provider's 401/403 is reported as 400 so Claude Code never
thinks your own Anthropic login expired.

## Config

Everything lives in `~/.claude-hybrid/config.yaml` (mode 0600, holds keys).
The panel edits it, and you can edit it by hand: the router reloads the file
when it changes. Keys can also come from environment variables with
`api_key_env`. See [`config.example.yaml`](config.example.yaml).

| path | what |
|------|------|
| `~/.claude-hybrid/config.yaml` | providers, agent routes, rules, settings |
| `~/.claude-hybrid/proxy.log` | one line per request (0600); rotated to `proxy.log.1` at 10 MB |
| `~/.claude-hybrid/trash/` | agent files removed or renamed in the panel |
| `~/.claude/agents/*.md` | the agent files themselves (Claude Code's own folder) |

Set `CLAUDE_HYBRID_HOME` to use another folder (an instance started that way
is never confused with the systemd service). An edit made by hand is picked
up within 2 seconds; an invalid edit is reported in the log and the running
config is kept (the panel then refuses to save over it). An empty file is an
error, not "no providers". Old config versions are migrated on load.

## Commands

```
claude-hybrid [claude args...]   start the router if needed, then run claude through it
claude-hybrid serve              run in the foreground (what the service runs; exits non-zero on failure)
claude-hybrid start|stop|restart|status   (stop/restart use systemctl when the service runs it)
claude-hybrid panel              open the web panel
claude-hybrid env                print the ANTHROPIC_BASE_URL export line
claude-hybrid install-service    systemd user service (Linux)
claude-hybrid version
```

## Security

* Both listeners bind to `127.0.0.1` by default.
* The proxy refuses requests that come from a browser (`Origin`,
  `Sec-Fetch-*`) or carry a non-local `Host`, so a web page cannot use your
  provider keys. v1 URL markers are honoured only for loopback URLs.
* The panel rejects requests whose `Host` is not localhost (DNS rebinding)
  and API calls without the `X-CHR` header (cross-site requests from other
  web pages). To open the panel on your LAN, set `server.panel_token` (at
  least 16 characters) and `server.panel_listen`; the token is then required
  for every API call.
* The panel sends `Content-Security-Policy` (scripts only from itself),
  `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`,
  `Referrer-Policy: no-referrer` and `Cache-Control: no-store`.
* API keys, custom header values and the panel token are never sent to the
  browser (at most the last 4 characters). Sending the mask or an empty
  value back keeps the stored value.
* `upstream`, `log_file` and `agents_dir` can only be changed in
  `config.yaml`, not from the panel. A stored key is only used for model
  discovery against the URL it was saved for. Provider requests never follow
  redirects.
* config.yaml and proxy.log are 0600.

## Upgrading from v1

v1 used a MITM proxy and installed its own root CA. v2 needs neither. If you
ran v1, remove its CA from the system trust store (Linux example):

```bash
sudo mv /etc/ca-certificates/trust-source/anchors/claude-hybrid-ca.crt ~/claude-hybrid-ca.crt.old
sudo trust extract-compat
```

and move `~/.claude-hybrid/certs/` away. Re-save your agents in the panel to
switch them to the new marker; old markers keep working meanwhile.

## Development

```bash
make test     # go test -race ./...
make build
CLAUDE_HYBRID_WEB_DIR=internal/panel/web ./claude-hybrid serve   # edit the UI without rebuilding
```

The UI is plain HTML, CSS and ES modules in `internal/panel/web/` (no build
step), embedded into the binary.

### Adding a provider type

* Write an `Adapter` in `internal/provider/` (`Messages`, `CountTokens`,
  `ListModels`; see `anthropic.go` and `openai_chat.go`). A pure wire-format
  translation goes into its own package under `internal/translate/`.
* Add it to `provider.Registry` with a name, label and help text (the panel's
  type picker reads them from `/api/meta`).
* Add the name to `config.ProviderTypes`; a test fails until both lists match.

`providers/opencode` is an optional separate provider that answers the
Anthropic API by running `opencode run`; add it as an `anthropic` provider
at `http://127.0.0.1:4568`.

## License

MIT. See `LICENSE`.
