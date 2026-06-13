#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
echo "=== Go coverage (priority packages) ==="
go test -count=1 -coverprofile=/tmp/aether_cov.out -covermode=atomic \
  ./cmd/executor/... ./cmd/monitor/... ./cmd/signer/... \
  ./internal/config/... ./internal/db/... ./internal/events/... \
  ./internal/grpc/... ./internal/signer/... ./internal/risk/... \
  ./internal/strategy/... ./internal/metrics/... 2>&1 | tee /tmp/go_test.log
echo "--- Per-package ---"
go tool cover -func=/tmp/aether_cov.out | grep -E 'total:|cmd/|internal/' | grep -v '100.0%' || true
echo "--- Below 98% ---"
go tool cover -func=/tmp/aether_cov.out | awk '$3+0 < 98 && $3+0 > 0 {print}' | head -60
echo "--- Total ---"
go tool cover -func=/tmp/aether_cov.out | tail -1
