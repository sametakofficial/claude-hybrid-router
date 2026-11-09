# Changelog

## 2.3.0

Internal restructure (see CLAUDE.md for the new layout) plus fixes from the
audit. config.yaml stays compatible.

Routing and translation
- Provider adapters (`internal/provider`) behind one interface; the proxy
  applies retries, error mapping and flushing once for all of them.
- Retries: no more retrying timeouts (a hung provider used to block a turn
  for ~15 min and get the same request 3 times); only refused/reset
  connections and 502/503/504, quickly.
- OpenAI-compatible: reasoning (`reasoning_content` / `reasoning`) streams as
  thinking and is sent back to the provider (DeepSeek tool loops); a stream
  that is cut off is an error instead of a complete answer; SSE is detected
  from the body; `{"error":"..."}`, pretty-printed errors and tool arguments
  sent as objects are understood; tool ids are unique; server tool calls in
  the history no longer cause a 400; message_start reports an input
  estimate; a reply streamed without being asked is collected.
- Anthropic-compatible: thinking budget kept below a capped max_tokens;
  beta-only fields are removed (the beta header is not forwarded).
- Context-overflow errors read "prompt is too long" so Claude Code compacts.
- Empty system text blocks (marker-only system, replace with an empty
  prompt) are no longer sent.
- count_tokens counts images as ~1.6k tokens instead of as base64 text, and
  asks Anthropic-compatible providers first.
- A passthrough stream cut mid-way is still logged.

Panel
- The UI is split into files (`web/index.html`, `css/`, `js/`); served with
  CSP, nosniff, no-store and frame/referrer headers.
- New: `PUT /api/agents/{name}/route` (route only), `PUT
  /api/providers/{id}/enabled`, `provider_types` in `/api/meta`, `missing`
  and `name_mismatch` on agents, `clear_api_key` on provider save.
  Removed: `POST /api/providers/{id}/models`.
- Agents are identified by file name: renaming renames the file (old file to
  the trash), works for case-only renames, refuses to overwrite another
  agent, and never leaves a half-done rename.
- Custom header values are masked like keys; masks show at most 4
  characters; a masked key is never saved as a key.
- Validation errors are 400, I/O errors 500. Panel token >= 16 characters.

Config and ops
- Hand edits are no longer lost when the panel saves right after them; an
  invalid hand edit is never overwritten. Empty config files are refused.
  Version migration hook.
- `restart` under systemd restarts the unit; an instance with
  `CLAUDE_HYBRID_HOME` never touches the unit; `status`/`env` do not create
  config.yaml; `serve` exits non-zero on failure; proxy.log is 0600 and
  rotates while running; the launcher forwards SIGTERM/SIGHUP to claude;
  Windows `stop` reports success; unit hardening (UMask, NoNewPrivileges).
- opencode bridge: session id shell-quoted, cache 0600 under
  `CLAUDE_HYBRID_HOME`.
- CI: actions pinned by commit, govulncheck.

## 2.2.0

- Dashboard: subscription limit (from Anthropic's rate-limit headers) with
  "pts saved" per window, plan picker, offload share, 30-day chart, top
  routed agents. Savings use a measured rate (limit points per USD seen
  here); until the limit has risen 3 points the dashboard says calibrating.
- usage.json keeps 40 days of hourly totals.
- Security fixes from the audit (see commit c77bcde).

## 2.1.0

- Panel: everything is picked from lists. Provider models load automatically
  from /models (searchable checklist), one "Runs on" picker for agents and
  rules, agent templates, tool checkboxes, color swatches, inline route
  switch on agent cards, rule pattern presets.
- API: /api/meta, /api/discover, /api/providers/{id}/refresh, PUT ?create=1.
- Anthropic 429s are logged with their rate-limit headers.

## 2.0.0

Rewrite.

- Web panel (http://127.0.0.1:3458) to manage providers, subagents (name,
  description, tools, model, color, system prompt) and their routes, model
  rules, settings, raw config, and a live request log with token counts.
- Built-in Anthropic ⇄ OpenAI Chat Completions translation (tools, images,
  streaming, usage). The musistudio Docker sidecar is no longer needed.
- Anthropic-compatible providers are forwarded directly with model,
  max_tokens and temperature overrides.
- Per-route system prompt mode (keep / append / replace) and reasoning effort.
- Routing by agent name (`<!-- claude-hybrid:agent=NAME -->`); the target
  lives in the config. The v1 URL marker still works.
- Model rules (glob on the requested model) for non-agent traffic.
- The router runs as one background service (`claude-hybrid serve`,
  `install-service` for systemd); `claude-hybrid` launches claude through it.
- Removed: MITM proxy, root CA generation and trust-store install, CA
  bundles, proxy env vars.
- Subscription token is never forwarded to providers; provider 401/403 are
  reported as 400 so Claude Code does not ask you to log in again.
