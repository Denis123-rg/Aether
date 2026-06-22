#!/usr/bin/env bash
# Minimal Anvil watchdog: keeps the local fork close to mainnet head and
# re-injects the AetherExecutor bytecode at the deterministic address so the
# revm sim can call into it instead of an empty account.
#
# Each tick:
#   1. Fetch current mainnet head from Alchemy.
#   2. anvil_reset the local fork to that block.
#   3. anvil_setCode to re-inject the executor bytecode (reset wipes it).
#
# Defaults: 30s interval, reads executor address from build/live/executor_addr.txt
# and bytecode from contracts/out/AetherExecutor.sol/AetherExecutor.json.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

ANVIL_RPC="${ANVIL_RPC:-http://127.0.0.1:8545}"
UPSTREAM_RPC="${UPSTREAM_RPC:?UPSTREAM_RPC is required — set to an Ethereum mainnet RPC endpoint}"
INTERVAL="${INTERVAL:-90}"
# Only reset when upstream is at least this many blocks ahead of Anvil.
# Each reset wipes Anvil's storage cache and forces the engine to refetch
# every queried slot via the Alchemy proxy, so frequent resets hammer the
# rate limit. Skipping resets when the lag is already small lets Anvil's
# cache amortise across multiple sims.
RESET_LAG_THRESHOLD="${RESET_LAG_THRESHOLD:-6}"
EXEC_ADDR="$(cat build/live/executor_addr.txt 2>/dev/null || echo 0xaF901aCfaC3b07cab22E6DE5101768B21d46CAcD)"
ABI_JSON="contracts/out/AetherExecutor.sol/AetherExecutor.json"
LOG="build/live/watchdog.audit.log"

if [ ! -f "$ABI_JSON" ]; then
  echo "ERROR: $ABI_JSON missing — build contracts first" >&2
  exit 2
fi

EXEC_CODE="$(jq -r '.deployedBytecode.object' "$ABI_JSON")"
if [ -z "$EXEC_CODE" ] || [ "$EXEC_CODE" = "null" ]; then
  echo "ERROR: could not extract deployedBytecode from $ABI_JSON" >&2
  exit 2
fi

mkdir -p build/live
echo "$(date -u +%FT%TZ) [START] interval=${INTERVAL}s addr=${EXEC_ADDR} code_bytes=${#EXEC_CODE}" >> "$LOG"

while true; do
  TS="$(date -u +%FT%TZ)"

  UP_HEX=$(curl -sfm 10 -H 'Content-Type: application/json' \
    -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
    "$UPSTREAM_RPC" 2>/dev/null | jq -r '.result' 2>/dev/null || true)
  if [ -z "$UP_HEX" ] || [ "$UP_HEX" = "null" ]; then
    echo "$TS [ERR] upstream eth_blockNumber failed" >> "$LOG"
    sleep "$INTERVAL"
    continue
  fi
  UP_DEC=$((UP_HEX))

  # Read Anvil's current block; skip reset if the lag is small.
  ANVIL_HEX=$(curl -sfm 5 -H 'Content-Type: application/json' \
    -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
    "$ANVIL_RPC" 2>/dev/null | jq -r '.result' 2>/dev/null || true)
  if [ -n "$ANVIL_HEX" ] && [ "$ANVIL_HEX" != "null" ]; then
    ANVIL_DEC=$((ANVIL_HEX))
    LAG=$((UP_DEC - ANVIL_DEC))
    if [ "$LAG" -lt "$RESET_LAG_THRESHOLD" ]; then
      echo "$TS [SKIP] lag=$LAG < threshold=$RESET_LAG_THRESHOLD (anvil=$ANVIL_DEC up=$UP_DEC)" >> "$LOG"
      sleep "$INTERVAL"
      continue
    fi
  fi

  # anvil_reset to upstream head — wipes all local state including injected code.
  RST=$(curl -sfm 30 -H 'Content-Type: application/json' \
    -d "{\"jsonrpc\":\"2.0\",\"method\":\"anvil_reset\",\"params\":[{\"forking\":{\"jsonRpcUrl\":\"$UPSTREAM_RPC\",\"blockNumber\":$UP_DEC}}],\"id\":1}" \
    "$ANVIL_RPC" 2>/dev/null || true)
  # anvil_reset returns `"result": null` on success; treat missing `.error`
  # as the success signal rather than checking for a non-null result.
  if ! echo "$RST" | jq -e '.error == null' >/dev/null 2>&1; then
    echo "$TS [ERR] anvil_reset failed: $RST" >> "$LOG"
    sleep "$INTERVAL"
    continue
  fi

  # Re-inject executor bytecode.
  INJ=$(curl -sfm 10 -H 'Content-Type: application/json' \
    -d "{\"jsonrpc\":\"2.0\",\"method\":\"anvil_setCode\",\"params\":[\"$EXEC_ADDR\",\"$EXEC_CODE\"],\"id\":1}" \
    "$ANVIL_RPC" 2>/dev/null || true)
  if ! echo "$INJ" | jq -e '.result // (.error == null)' >/dev/null 2>&1; then
    echo "$TS [WARN] anvil_setCode response: $INJ" >> "$LOG"
  fi

  echo "$TS [OK] reset_to=$UP_DEC exec_code=${#EXEC_CODE}B" >> "$LOG"
  sleep "$INTERVAL"
done
