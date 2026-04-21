# Security Model

claude-hybrid-router is a MITM (Man-in-the-Middle) TLS proxy by design. This document explains why that is intentional, what is in scope, and how to report vulnerabilities.

## Threat Model

This proxy is a **local development tool**. It intercepts HTTPS traffic between Claude Code (running on the same machine) and Anthropic's API to enable routing requests to alternative model providers.

### Trust Boundaries

1. **Localhost only.** The proxy binds to `127.0.0.1` on a random port. It never listens on external interfaces. Remote machines cannot reach it.

2. **User-installed CA certificate.** The MITM CA is generated locally in `~/.claude-hybrid/certs/` on first run. The user must explicitly install it into their trust store (`claude-hybrid trust install`). No CA material is shared, uploaded, or reused across machines.

3. **Single-user scope.** The proxy runs under the invoking user's account and inherits that user's permissions. It does not escalate privileges.

### What is intercepted

The proxy performs MITM TLS decryption **only** for HTTPS CONNECT tunnels that are **not** in the bypass list. In practice this means traffic to Anthropic's API (`api.anthropic.com`) and any configured local provider endpoints.

The proxy inspects the `system` field of Anthropic Messages API requests for a routing marker. It never inspects or modifies the `messages` array for routing purposes.

### What is bypassed (no MITM)

The following hosts are tunneled directly without TLS interception:

- **Python packaging:** pypi.org, files.pythonhosted.org, *.pythonhosted.org
- **Node.js packaging:** registry.npmjs.org, *.npmjs.org
- **Google OAuth:** accounts.google.com, oauth2.googleapis.com
- **GitHub:** github.com, api.github.com
- **Ruby packaging:** rubygems.org
- **Rust packaging:** crates.io, static.crates.io
- **Go modules:** proxy.golang.org, sum.golang.org

Traffic to these hosts passes through as a plain TCP tunnel -- the proxy never sees the plaintext.

### API key handling

- API keys from provider error responses are redacted before writing to the log file.
- Bearer tokens and key-prefixed strings (`sk-*`, `key-*`) are scrubbed from all log output.
- The proxy does not store, cache, or transmit API keys beyond forwarding them in the original request headers.

### Log files

Logs are written to `~/.claude-hybrid/proxy.log` with daily rotation. By default, logging is sparse (route decisions and errors only). `--verbose` mode includes more detail but still redacts credentials.

## Reporting Vulnerabilities

If you discover a security issue, please email **jacklondon0303@gmail.com** with:

- A description of the vulnerability
- Steps to reproduce
- Impact assessment

Please allow 7 days for an initial response. Do not open a public GitHub issue for security vulnerabilities.

## Out of Scope

The following are **not** considered vulnerabilities:

- The proxy decrypts TLS traffic (this is the intended design)
- A user with local access can read the CA private key (it is stored with user-only permissions)
- Log files contain request metadata (they are stored in the user's home directory)
