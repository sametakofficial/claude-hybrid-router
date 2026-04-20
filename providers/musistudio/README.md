# musistudio provider (claude-code-router)

An Anthropic-API-compatible server that forwards requests to OpenAI / OpenRouter
/ any OpenAI-compatible backend. This lets you route specific Claude Code
subagents to GPT-5, Sonnet-via-OR, local Llama, etc. — anything that speaks
the OpenAI Chat Completions API.

This folder does NOT ship Go code. musistudio is an external npm package
(`@musistudio/claude-code-router`). This README only documents how to install
it and wire it up as a claudebridge provider.

## Install

```bash
npm install -g @musistudio/claude-code-router
```

Verify:

```bash
ccr --help
```

## Configure

Create `~/.claude-code-router/config.json` (or let `ccr` generate a default):

```json
{
  "PORT": 3456,
  "Providers": [
    {
      "name": "openrouter",
      "api_base_url": "https://openrouter.ai/api/v1/chat/completions",
      "api_key": "sk-or-...",
      "models": ["openai/gpt-5.4", "anthropic/claude-sonnet-4"]
    }
  ],
  "Router": {
    "default": "openrouter,openai/gpt-5.4",
    "background": "openrouter,openai/gpt-5.4-mini",
    "think": "openrouter,anthropic/claude-sonnet-4",
    "longContext": "openrouter,openai/gpt-5.4"
  }
}
```

Full config reference: <https://github.com/musistudio/claude-code-router>

## Run

```bash
ccr start
```

Defaults to `http://127.0.0.1:3456`. Verify:

```bash
curl -s http://127.0.0.1:3456/ | jq .
# {"message":"LLMs API","version":"1.0.51"}
```

## Route a claudebridge subagent here

In the subagent's `.md` file (e.g. `.claude/agents/architecture-researcher.md`):

```markdown
<!-- @proxy-local-route:af83e9 url=http://127.0.0.1:3456 -->
```

That's it. claudebridge's proxy detects the marker, strips it, and forwards
the unmodified body to `ccr` on port 3456. `ccr` decides which underlying
model answers based on its own `Router` config.

## Why a separate provider?

musistudio already does full Anthropic↔OpenAI translation, transform
pipelines for provider quirks (OpenRouter, Groq, DeepSeek…), reasoning
extraction, tool-call repair, etc. Reimplementing all of that would
duplicate a mature project.

So in the claudebridge model:

* **proxy (core)** — intercepts Claude Code's HTTPS traffic, finds the
  routing marker, forwards to the URL as-is.
* **musistudio provider (this folder)** — handles "I want Claude's request
  answered by a different model" via its own translation + transform layer.

The proxy never knows which model actually runs. It only knows the URL.
