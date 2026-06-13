#!/usr/bin/env bash
# Issue #1 verification: no stale cmd/risk or cmd/pooldiscovery references.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

echo "==> grep cmd/risk in docs/build configs"
if grep -r "cmd/risk" "$ROOT" \
  --include='*.md' --include='*.toml' --include='*.yaml' --include='*.sh' \
  --exclude-dir=lib --exclude='CHANGELOG.md' --exclude='AETHER_*_REPORT.md' \
  --exclude='FINAL_COVERAGE_REPORT.md' --exclude='COVERAGE_FINAL_REPORT.md' \
  --exclude='test_issue1_references.sh' --exclude='test_production_hardening.sh' 2>/dev/null \
  | grep -v 'no standalone' | grep -v 'no `cmd/risk' | grep -v 'removed'; then
  echo "FAIL: cmd/risk references remain" >&2
  exit 1
fi
echo "OK"

echo "==> grep cmd/pooldiscovery"
if grep -ri "cmd/pooldiscovery\|pooldiscovery" "$ROOT" \
  --include='*.md' --include='*.toml' --include='*.yaml' --include='*.sh' \
  --exclude-dir=lib --exclude='AETHER_*_REPORT.md' --exclude='CHANGELOG.md' \
  --exclude='test_issue1_references.sh' --exclude='test_production_hardening.sh' 2>/dev/null \
  | grep -v 'no separate Go pool-discovery' | grep -v 'Rust-only'; then
  echo "FAIL: pooldiscovery references remain" >&2
  exit 1
fi
echo "OK"

echo "==> cmd/risk directory absent"
test ! -d "$ROOT/cmd/risk"
echo "OK"

echo "==> executor uses internal/risk"
grep -q 'internal/risk' "$ROOT/cmd/executor/run.go"
echo "OK"

echo "==> no risk.service systemd unit"
if ls "$ROOT/deploy/systemd/"*risk* 2>/dev/null; then
  echo "FAIL: risk systemd unit found" >&2
  exit 1
fi
echo "OK"

echo "==> remote-deploy does not copy risk/pooldiscovery binaries"
if grep -E 'cmd/risk|pooldiscovery' "$ROOT/deploy/remote-deploy.sh" 2>/dev/null; then
  echo "FAIL" >&2
  exit 1
fi
echo "OK"

echo "Issue #1 checks passed."
