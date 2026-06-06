#!/usr/bin/env bash
# Run anvil-fork integration tests that require ETH_RPC_URL.
#
# Usage:
#   export ETH_RPC_URL=https://eth-mainnet.g.alchemy.com/v2/YOUR_KEY
#   ./scripts/run_fork_tests.sh
#
# Without ETH_RPC_URL the script exits 0 after printing a skip notice
# (CI uses this pattern so the default pipeline stays green).

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ -z "${ETH_RPC_URL:-}" ]]; then
  echo "ETH_RPC_URL unset — fork tests skipped (exit 0)"
  exit 0
fi

if ! command -v anvil >/dev/null 2>&1; then
  echo "anvil not found in PATH — install Foundry (https://book.getfoundry.sh)"
  exit 1
fi

echo "==> discovery validator fork tests"
cargo test -p aether-discovery --test validator_fork_test -- --nocapture

echo "==> simulator fee-on-transfer fork tests"
cargo test -p aether-simulator --test fee_on_transfer_fork_test -- --nocapture

echo "==> simulator mempool backrun fork tests"
cargo test -p aether-simulator --test mempool_backrun_fork_test -- --nocapture

echo "==> grpc-server service integration (in-crate)"
cargo test -p aether-grpc-server --lib stream_arbs -- --nocapture

echo "==> integration-tests anvil fork suite"
cargo test -p aether-integration-tests --test anvil_fork_test -- --nocapture

echo "==> previously ignored in-crate fork tests"
cargo test -p aether-discovery --lib -- --ignored --nocapture
cargo test -p aether-simulator --lib fee_on_transfer -- --ignored --nocapture
cargo test -p aether-simulator --lib mempool_backrun -- --ignored --nocapture

echo "All fork tests completed."
