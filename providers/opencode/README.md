# opencode provider (bash bridge)

An Anthropic-API-compatible server that does NOT itself run a model. Instead
it pretends to be an assistant that just happens to need the `Bash` tool —
returning a `tool_use(Bash)` response whose command is

```
OPENCODE_NO_DEBUG=1 opencode run --format json [--session ses_xxx] [--agent NAME] '<prompt>'
```

Claude Code executes the command. The command's stdout (newline-delimited JSON
events from `opencode run --format json`) comes back as a `tool_result` on
turn 2. This provider parses that stream, extracts the session ID and the
assistant's text, caches the session ID per agent, and replies with a normal
`text` content block.

Net effect: Claude Code delegates a task to an opencode session, without the
provider having to stream tokens itself. The opencode wrapper handles all
execution, tool calls, thinking, and timeouts.

## Install

```bash
# Prerequisite: opencode CLI on PATH
opencode --version

# Build the provider binary from the repo root
go build -o opencode-bridge ./providers/opencode
```

`claude-hybrid` does not build or start the bridge; run it yourself (or as
your own service) and add it in the panel as an **Anthropic-compatible**
provider with base URL `http://127.0.0.1:4568` (the "opencode-bridge (local)"
template). Then route a subagent to it.

## Run

```bash
./opencode-bridge --port 4568
```

Flags:

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `4568` | TCP port to listen on (HTTP, no TLS). |
| `--cache` | `$CLAUDE_HYBRID_HOME/opencode-sessions.json` (default `~/.claude-hybrid/`) | Disk-persisted agent → session ID map (0600). |

Verify:

```bash
curl -s http://127.0.0.1:4568/health
# {"status":"ok"}
```

## Agent-name detection

The provider scans the incoming request body for a marker:

```
@opencode-agent:NAME
```

anywhere inside `system` or any `messages[].content` text block. Characters
allowed in NAME: `[A-Za-z0-9_-]`. If no marker is found, falls back to
`general`.

To pick the opencode agent for a Claude Code subagent, put this line in the
subagent's prompt (in the panel, or in its `.md` file below the routing
marker):

```markdown
<!-- @opencode-agent:simplifier -->
```

claude-hybrid removes only its own routing marker; this one rides along in
the system prompt and is read here. With the route's system prompt mode
`replace`, Claude Code's system prompt (and so this line) is not sent:
put the line into the replacement text instead, or use `keep` / `append`.

## Session cache

Cache file (default `~/.claude-hybrid/opencode-sessions.json`):

```json
{
  "simplifier": { "id": "ses_2537b8...", "last_at": "2026-04-20T23:11:06+03:00" }
}
```

Rules:

* Keyed by the detected agent name.
* TTL = 2 hours, refreshed on every use (GET-as-touch).
* Atomic writes (tempfile + rename), file mode 0600.
* The session id is shell-quoted in the generated command.
* Stale entries are dropped on load, so a long-quiet agent doesn't resurface.

Wipe the file to make every agent start fresh.

## Two-turn protocol at a glance

```
Turn 1 (Claude → provider)
  request:   {"messages": [{"role":"user","content":"refactor this"}]}
  response:  {"content":[{"type":"tool_use","name":"Bash",
              "input":{"command":"OPENCODE_NO_DEBUG=1 opencode run --format json --agent simplifier 'refactor this'"}}],
              "stop_reason":"tool_use"}

Claude Code runs the bash command, captures stdout.

Turn 2 (Claude → provider, with tool_result)
  request:   {"messages": [..., {"role":"user","content":[
               {"type":"tool_result","content":"{...json events...}"}
             ]}]}
  response:  {"content":[{"type":"text","text":"Here is the simpler version..."}],
              "stop_reason":"end_turn"}
```

SSE streaming is supported (`Accept: text/event-stream`) for both turns.

## Tests

```bash
go test ./providers/opencode/ -v
```

The tests cover: agent detection (string/block system, user messages,
fallback), prompt extraction (backwards walk, system-reminder stripping),
tool_result extraction (string/block array), opencode command building
(session injection, quote escaping), output parsing (session+text, raw
passthrough, non-JSON skip), and session cache (set/get, TTL expiry, TTL
refresh, disk persistence, stale-drop on load).

## Why bash and not HTTP?

An earlier iteration talked to `opencode serve` over HTTP. It worked but
reimplemented session lifecycle, agent dispatch, and streaming — all of
which `opencode run` already does correctly as a CLI. The bash approach
leans on Claude Code's own `Bash` tool to do the execution, so the provider
is just a thin two-turn protocol translator. Simpler to debug (you can
literally run the printed command yourself), easier to reason about
failures (timeouts, idle watchdogs, prompt caching are the wrapper's job).
