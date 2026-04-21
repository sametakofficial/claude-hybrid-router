# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- MITM CONNECT proxy with per-domain certificate generation and LRU cache
- Routing marker detection in system field (`<!-- @proxy-local-route:af83e9 url=BASE_URL -->`)
- Base URL forwarding to local/alternative model providers
- MITM bypass list for package managers, OAuth, and GitHub
- CA trust management (`claude-hybrid trust install|status`) for Linux, macOS, and Windows
- Combined CA bundle creation (system roots + MITM CA)
- Windows PATH resolution to avoid Desktop app shadowing the CLI binary
- Structured Anthropic-format error responses for local provider failures
- API key redaction in all log output
- Multi-instance safety with lock files and shared CA certs
- Graceful shutdown with 5-second timeout for in-flight requests
- Daily log rotation with flock-based locking
- opencode-bridge provider: Anthropic-API server delegating to opencode CLI
- Per-agent session ID cache for opencode-bridge
- Cross-platform builds (Linux, macOS, Windows) via goreleaser
- GitHub Actions CI (test matrix) and CD (tag-triggered releases)
