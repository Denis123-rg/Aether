#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

echo "==> Go tests"
go test ./... -count=1 -coverprofile=coverage.out -covermode=atomic 2>&1 | tee go_test_out.txt
echo "--- Go coverage (bottom) ---"
go tool cover -func=coverage.out | tail -20 | tee go_cov_summary.txt

echo "==> Rust tests"
cargo test --workspace 2>&1 | tee cargo_test_out.txt

echo "==> internal/db coverage"
go tool cover -func=coverage.out | grep 'internal/db' | tee db_cov.txt

echo "Done."
