#!/usr/bin/env bash
# claude-hybrid — launcher script
#
# 1. Starts opencode-bridge (if not already up) on port 4568.
# 2. Execs claude-hybrid-proxy, which starts the MITM proxy and then claude.
#
# Logs:
#   ~/.claude-hybrid/opencode-bridge.log   bridge stdout/stderr
#   ~/.claude-hybrid/proxy.log             proxy LOCAL_ROUTE/LOCAL_OK lines
#
# All args are forwarded to claude-hybrid-proxy (e.g. --proxy-only, --verbose,
# `-- --dangerously-skip-permissions`).

set -euo pipefail

BRIDGE_PORT="${OPENCODE_BRIDGE_PORT:-4568}"
BRIDGE_BIN="${HOME}/.local/bin/opencode-bridge"
PROXY_BIN="${HOME}/.local/bin/claude-hybrid-proxy"
LOG_DIR="${HOME}/.claude-hybrid"
BRIDGE_LOG="${LOG_DIR}/opencode-bridge.log"

mkdir -p "$LOG_DIR"

bridge_alive() {
  curl -s -f --max-time 1 "http://127.0.0.1:${BRIDGE_PORT}/health" >/dev/null 2>&1
}

if ! bridge_alive; then
  if [[ ! -x "$BRIDGE_BIN" ]]; then
    echo "claude-hybrid: $BRIDGE_BIN not found or not executable" >&2
    exit 1
  fi
  # nohup + setsid so the bridge survives the parent shell
  nohup setsid "$BRIDGE_BIN" --port "$BRIDGE_PORT" \
    >>"$BRIDGE_LOG" 2>&1 </dev/null &
  disown || true
  for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
    sleep 0.1
    if bridge_alive; then break; fi
  done
  if ! bridge_alive; then
    echo "claude-hybrid: opencode-bridge failed to become ready (see $BRIDGE_LOG)" >&2
    exit 1
  fi
  echo "opencode-bridge ready on http://127.0.0.1:${BRIDGE_PORT} (log: $BRIDGE_LOG)"
else
  echo "opencode-bridge already running on http://127.0.0.1:${BRIDGE_PORT}"
fi

if [[ ! -x "$PROXY_BIN" ]]; then
  echo "claude-hybrid: $PROXY_BIN not found or not executable" >&2
  exit 1
fi

exec "$PROXY_BIN" "$@"
