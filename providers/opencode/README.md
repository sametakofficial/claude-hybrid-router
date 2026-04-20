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

Or let the top-level `claude-hybrid` launcher build and start it for you.

## Run

```bash
./opencode-bridge --port 4568
```

Flags:

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `4568` | TCP port to listen on (HTTP, no TLS). |
| `--cache` | `~/.claude-hybrid/opencode-sessions.json` | Disk-persisted agent → session ID map. |

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

To route a specific Claude Code subagent to a specific opencode agent, add a
second marker alongside the routing one in the subagent's `.md` file:

```markdown
<!-- @proxy-local-route:af83e9 url=http://127.0.0.1:4568 -->
<!-- @opencode-agent:simplifier -->
```

The core proxy strips only the first marker (`@proxy-local-route`). The
second one rides along in the body untouched and is read here.

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
* Atomic writes (tempfile + rename) — safe across concurrent invocations.
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

19 unit tests cover: agent detection (string/block system, user messages,
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
