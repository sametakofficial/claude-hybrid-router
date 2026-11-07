# Changelog

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
