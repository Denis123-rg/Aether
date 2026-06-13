#!/usr/bin/env bash
# Issue #5: pools.toml / discovery.toml Rust ownership documentation checks.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

echo "==> architecture.md documents Rust ownership"
grep -q "only by the Rust engine" "$ROOT/docs/architecture.md"
grep -q "ReloadConfig" "$ROOT/docs/architecture.md"
echo "OK"

echo "==> README.md documents Rust ownership"
grep -q "only by the Rust engine" "$ROOT/README.md"
echo "OK"

echo "==> Go code does not load pools.toml directly"
if grep -r 'pools\.toml\|discovery\.toml' "$ROOT/cmd" "$ROOT/internal" --include='*.go' 2>/dev/null; then
  echo "FAIL: Go imports pool config files" >&2
  exit 1
fi
echo "OK"

echo "Issue #5 checks passed."
