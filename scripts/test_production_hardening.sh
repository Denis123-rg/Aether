#!/usr/bin/env bash
# Production hardening verification tests (issues #5, #6, #10).
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

echo "==> Issue #5: cmd/risk removed"
test ! -d "$ROOT/cmd/risk"
echo "OK: cmd/risk absent"

echo "==> Issue #5: executor still references internal/risk"
grep -q 'internal/risk' "$ROOT/cmd/executor/run.go"
echo "OK: internal/risk wired in executor"

echo "==> Issue #6: no pooldiscovery in markdown"
if grep -ri pooldiscovery "$ROOT" --include='*.md' --exclude-dir=lib 2>/dev/null | grep -v 'AETHER_.*_REPORT'; then
  echo "FAIL: pooldiscovery references remain" >&2
  exit 1
fi
echo "OK: no pooldiscovery in docs"

echo "==> Issue #10: factory coverage script"
bash "$ROOT/scripts/validate_factory_coverage.sh"
echo "OK: factory coverage"

echo "All hardening doc/build checks passed."
