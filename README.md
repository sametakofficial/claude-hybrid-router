# claudebridge

A small, honest piece of plumbing for Claude Code.

You pay Anthropic for a subscription. You'd like to keep using it — but
there are a few places where another model is a better tool for the job
(faster, cheaper, locally hosted, or just fun to try). claudebridge lets
you route **specific** Claude Code subagents to **specific** other models
without breaking the rest of your session.

No model lives inside claudebridge itself. The core is a transparent
HTTPS proxy that reads exactly one thing from the request body — a short
routing marker — and forwards the whole request, untouched, to the URL
the marker specifies. Everything else (translation, tool support, token
streaming, session management) is the job of the **provider** sitting
at that URL.

```
┌────────────┐  CONNECT /MITM TLS   ┌──────────────────────────┐
│ Claude     │─────────────────────▶│ claudebridge proxy       │
│ Code       │                      │  • peek at /v1/messages  │
│ (your sub) │◀─────────────────────│  • find routing marker?  │
└────────────┘                      │     ├─ no  → Anthropic   │
                                    │     └─ yes → provider    │
                                    └──────────────────────────┘
                                              │
                           ┌──────────────────┼──────────────────┐
                           ▼                                     ▼
                ┌──────────────────┐              ┌───────────────────────┐
                │ providers/       │              │ providers/            │
                │   opencode/      │              │   musistudio/         │
                │ (bash bridge to  │              │ (Anthropic↔OpenAI     │
                │  opencode run)   │              │  translator, ext.)    │
                └──────────────────┘              └───────────────────────┘
                           │                                     │
                           ▼                                     ▼
                      opencode CLI                    OpenRouter / local Llama /
                   (executes tools,                      DeepSeek / anything
                    thinks, replies)                    OpenAI-compatible
```

## The routing marker

Drop this one line into the `system` prompt of a Claude Code subagent
(or into its `.md` file, since Claude Code packages agent markdown into
the request body):

```
<!-- @proxy-local-route:af83e9 url=http://127.0.0.1:PORT -->
```

The proxy sees the marker, records the URL, removes just that marker
from the body, and forwards everything else — including any
provider-specific hints like `<!-- @opencode-agent:simplifier -->` — to
the given URL. Any agent without a marker keeps going to Anthropic,
using your subscription as usual.

Markers work in the top-level `system` field and in user messages
(where Claude Code actually packages CLAUDE.md content, wrapped in
`<system-reminder>` blocks).

## Project layout

```
claudebridge/
├── cmd/
│   └── claude-hybrid/        main binary: launcher + MITM proxy + cert mgmt
├── internal/
│   ├── config/               YAML config, RouteResolver, env knobs
│   ├── mitm/                 per-domain cert cache + in-memory CA
│   ├── proxy/                CONNECT handler, body inspection, forward
│   └── testutil/             in-process echo + mock servers
└── providers/                drop-in URL endpoints the proxy can forward to
    ├── opencode/             ships here: Anthropic API ↔ `opencode run` bash bridge
    └── musistudio/           documentation-only: install + wire `ccr` as a provider
```

`cmd/claude-hybrid/` is the only binary you're required to run. Each
`providers/*` folder is self-contained: either Go source you build
yourself (like `opencode/`), or a README that tells you how to install
and configure a third-party server (like `musistudio/`).

## Adding a new provider

A provider is **anything** that:

1. Listens on a local TCP port with HTTP.
2. Answers `POST /v1/messages` with an Anthropic Messages API response
   (blocking or SSE-streaming).

That's the whole contract. No framework, no plugin loader, no registration
step. Mechanisms:

* **Build-it-yourself**: add `providers/<name>/` with a Go `main` package
  that implements `/v1/messages` however you like. Look at
  `providers/opencode/` for a minimal example (session cache, two-turn
  tool-use protocol, agent-name detection).
* **Wrap an existing tool**: add `providers/<name>/README.md` that just
  tells users how to install and run the external binary (see
  `providers/musistudio/`).

Point Claude Code subagents at it by writing the marker into the
subagent's `.md` file:

```markdown
<!-- @proxy-local-route:af83e9 url=http://127.0.0.1:PROVIDER_PORT -->
```

## How a request actually flows

1. Claude Code calls `POST https://api.anthropic.com/v1/messages`. OS
   routes it through `HTTPS_PROXY=127.0.0.1:<claudebridge-port>`.
2. claudebridge answers CONNECT, hands back a MITM cert, speaks TLS.
3. claudebridge reads the plaintext HTTP/1.1 request, peeks at the body,
   regex-scans for `@proxy-local-route:af83e9 url=...`.
4. **Marker found**: strip the marker, forward the body verbatim to the
   URL with `Content-Type: application/json`. Relay the response bytes
   (including SSE) back to Claude Code. Log `LOCAL_ROUTE` and `LOCAL_OK`.
5. **No marker**: forward to `api.anthropic.com` over HTTP/2, relay the
   response. Claude Code's subscription auth header is already on the
   request, so Anthropic treats it like a normal call.

What claudebridge explicitly **does not** do:

* Translate between Anthropic and OpenAI schemas.
* Inject/modify tools or system prompts.
* Re-wrap SSE events, re-chunk responses, or cap token limits.
* Know what model is on the other end.

Those are provider concerns.

## Install & run

### Requirements

- Go 1.24+ (build only — the output is a static binary with zero runtime deps)
- `claude` CLI installed and on PATH ([install guide](https://docs.anthropic.com/en/docs/claude-code))

### Build

```bash
git clone https://github.com/peter-wagstaff/claude-hybrid-router.git
cd claude-hybrid-router
go build -o claude-hybrid   ./cmd/claude-hybrid
go build -o opencode-bridge ./providers/opencode   # optional provider
```

On Windows, the output is `claude-hybrid.exe` automatically.

### First run (one-time trust setup)

The proxy generates a local CA certificate on first launch. You must
install it into your OS trust store **once** so TLS validation works:

<details>
<summary><b>Linux</b></summary>

```bash
./claude-hybrid trust install   # uses sudo + update-ca-certificates or trust anchor
./claude-hybrid trust status    # verify: should print "System trust: true"
```

Supports Debian/Ubuntu, Fedora/RHEL, Arch, Alpine.
</details>

<details>
<summary><b>macOS</b></summary>

```bash
./claude-hybrid trust install   # uses sudo + security add-trusted-cert → System Keychain
./claude-hybrid trust status    # verify
```

Requires admin password once. Works on both Intel and Apple Silicon.
</details>

<details>
<summary><b>Windows</b></summary>

```powershell
.\claude-hybrid.exe trust install   # uses certutil → current user ROOT store (no admin needed)
.\claude-hybrid.exe trust status    # verify
```

No UAC elevation required — installs to the per-user certificate store.
</details>

### Run

```bash
claude-hybrid                       # launches `claude` under the proxy
claude-hybrid --proxy-only          # proxy only, no claude launch (for testing)
claude-hybrid --verbose             # log every request routing decision
claude-hybrid --require-trust       # abort if CA is not in system trust store
claude-hybrid -- --model opus       # pass flags to claude after --
```

The launcher also accepts a shell-script wrapper that manages the
opencode provider lifecycle. A ready-made one is in
[`scripts/claude-hybrid-launcher.sh`](scripts/claude-hybrid-launcher.sh)
— install it as `~/.local/bin/claude-hybrid` if you want `claude-hybrid`
to auto-start the bridge in the background on first use.

### Config file (optional)

`~/.claude-hybrid/config.yaml`:

```yaml
egress:
  timeout: 120                                   # seconds
  system_prompt_file: ~/.claude-hybrid/system-prompt.txt   # optional override
  reminder_file: ~/.claude-hybrid/reminder.txt             # optional footer
```

Both files are optional. Without them the proxy is pure pass-through.

## Logs

| File | What's in it |
|------|--------------|
| `~/.claude-hybrid/proxy.log` | `LOCAL_ROUTE`, `LOCAL_OK`, `LOCAL_ERR:*`, `[LOCAL_RETRY:n/m]`. Session-id prefixed so concurrent instances stay distinguishable. Daily rotation via flock. |
| `~/.claude-hybrid/opencode-bridge.log` | stdout/stderr of the opencode provider, if the launcher script started it. |

Tail both live:

```bash
tail -f ~/.claude-hybrid/proxy.log ~/.claude-hybrid/opencode-bridge.log
```

## Tests

```bash
go test ./...
```

In-process: no external services, no network. The proxy, echo server,
and provider fixtures all run in goroutines. Certificates are generated
in memory. Covers: CONNECT handling, MITM TLS, marker detection in
system + messages, marker stripping, auth-header sanitization, retry
backoff, SSE relay, provider two-turn tool-use protocol, session cache
TTL + disk persistence, agent-name detection, prompt extraction.

## Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| `UNABLE_TO_VERIFY_LEAF_SIGNATURE` | CA not in system trust store | Run `claude-hybrid trust install` |
| `SELF_SIGNED_CERT_IN_CHAIN` | Stale CA from older version | Delete `~/.claude-hybrid/certs/` and restart (auto-regenerates) |
| Proxy starts but Claude ignores it | `HTTPS_PROXY` not set | Let `claude-hybrid` launch Claude (it sets env vars automatically) |
| OAuth / GitHub login fails | OAuth host not bypassed | Should be fixed in latest; check `claude-hybrid --verbose` logs |
| Windows: `claude` opens Desktop GUI | PATH shadowing | Ensure npm-installed `claude` CLI is earlier in PATH than the Desktop app |
| `trust install` fails on macOS | No admin password | Run with `sudo` or ask IT to add the cert |
| Local provider unreachable | Provider not running | Start musistudio (`docker compose -f deploy/docker-compose.yaml up -d`) or opencode-bridge |

### Checking trust status

```bash
claude-hybrid trust status
# CA path: /home/you/.claude-hybrid/certs/ca.crt
# System trust: true
```

If `System trust: false`, run `claude-hybrid trust install` again.

### Environment variables set by the launcher

The launcher sets these on the Claude child process automatically:

| Variable | Value | Purpose |
|----------|-------|---------|
| `HTTPS_PROXY` / `HTTP_PROXY` | `http://127.0.0.1:<port>` | Route traffic through proxy |
| `NODE_EXTRA_CA_CERTS` | `~/.claude-hybrid/certs/ca.crt` | Node.js CA trust (legacy) |
| `NODE_USE_SYSTEM_CA` | `1` | Node.js reads OS trust store |
| `CLAUDE_CODE_CERT_STORE` | `system,bundled` | Claude Code reads OS trust store first |
| `SSL_CERT_FILE` | `~/.claude-hybrid/certs/bundle.crt` | Python/curl/git CA bundle |

## License

MIT. See `LICENSE`.

## Acknowledgements

* **musistudio/claude-code-router** — does the heavy translation work
  when you want a non-Anthropic model on the other end. Vendored as a
  provider via its own README; this project would be much larger
  without it.
* **opencode** — the CLI that makes the bash-bridge provider trivial.
  The whole two-turn protocol only exists because `opencode run` is
  already doing the hard part.
