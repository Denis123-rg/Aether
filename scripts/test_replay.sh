#!/usr/bin/env bash
# Historical mainnet replay tests (1k–50k block ranges).
# Requires ETH_RPC_URL (archive node recommended).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

if [[ -z "${ETH_RPC_URL:-}" ]]; then
  echo "SKIP: ETH_RPC_URL unset — replay tests require mainnet RPC"
  exit 0
fi

export PATH="${HOME}/.cargo/bin:${HOME}/.foundry/bin:${PATH}"

echo "=== Aether Replay Tests ==="

for blocks in 1000 5000 10000 50000; do
  echo "[replay] scanning ${blocks} blocks..."
  REPLAY_BLOCK_COUNT="${blocks}" \
    cargo test -p aether-integration-tests --test replay_block_ranges_test \
    -- --nocapture --test-threads=1 2>&1 | tail -5
done

echo "=== Replay tests complete ==="
