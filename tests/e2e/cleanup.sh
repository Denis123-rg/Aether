#!/usr/bin/env bash
# Stop all E2E services and remove temp sockets.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
BUILD_DIR="${BUILD_DIR:-$PROJECT_ROOT/build/e2e}"
PID_FILE="$BUILD_DIR/pids.txt"
ANVIL_PORT="${ANVIL_PORT:-8545}"
REDIS_PORT="${REDIS_PORT:-6379}"

if [[ -f "$PID_FILE" ]]; then
  while read -r pid _; do
    kill "$pid" 2>/dev/null || true
  done < "$PID_FILE" 2>/dev/null || true
  : > "$PID_FILE"
fi

pkill -f "anvil.*--port $ANVIL_PORT" 2>/dev/null || true
pkill -f "aether-grpc-server" 2>/dev/null || true
pkill -f "build/e2e/aether-executor" 2>/dev/null || true
pkill -f "build/e2e/aether-signer" 2>/dev/null || true
pkill -f "build/e2e/aether-telebot" 2>/dev/null || true
pkill -f "mock_builder.py" 2>/dev/null || true

redis-cli -p "$REDIS_PORT" shutdown 2>/dev/null || true
rm -f "$BUILD_DIR/signer.sock" "$BUILD_DIR/*.sock" 2>/dev/null || true

echo "[cleanup] All E2E processes stopped."
