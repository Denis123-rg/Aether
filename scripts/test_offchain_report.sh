#!/usr/bin/env bash
# Emit before/after coverage summary for off-chain packages.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
COVERAGE_DIR="${PROJECT_ROOT}/coverage"
GO_COVER="${COVERAGE_DIR}/go.out"

mkdir -p "${COVERAGE_DIR}"

echo "# Aether Off-Chain Test Report"
echo "Generated: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo ""

echo "## Go coverage (off-chain packages)"
cd "${PROJECT_ROOT}"
go test ./internal/... ./cmd/executor/... ./cmd/telebot/... ./tests/... \
  -count=1 -timeout 300s -coverprofile="${GO_COVER}" 2>&1 | grep -E 'coverage:|^ok|^FAIL' || true
echo ""
go tool cover -func="${GO_COVER}" 2>/dev/null | tail -1 || echo "total: (run go test first)"
echo ""

echo "## Rust workspace tests"
cargo test --workspace -- --test-threads=4 2>&1 | tail -8
echo ""

echo "## Integration scenario count"
go test ./tests/integration/... -list '.*' 2>/dev/null | grep -c '^Test' || echo "0"
echo ""

echo "## Fuzz targets"
ls -1 "${PROJECT_ROOT}/fuzz/fuzz_targets/" 2>/dev/null || echo "(none)"
echo ""

echo "## E2E pipeline"
echo "Run: ETH_RPC_URL=... make test-offchain-e2e"
