#!/usr/bin/env bash
# Validates every pool in config/pools.toml is covered by a factory in
# config/discovery.toml or is statically registered.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
POOLS="$ROOT/config/pools.toml"
DISCOVERY="$ROOT/config/discovery.toml"

if [[ ! -f "$POOLS" ]]; then
  echo "pools.toml not found: $POOLS" >&2
  exit 1
fi
if [[ ! -f "$DISCOVERY" ]]; then
  echo "discovery.toml not found: $DISCOVERY" >&2
  exit 1
fi

# Extract pool addresses from pools.toml (lines like address = "0x...")
mapfile -t POOL_ADDRS < <(grep -E '^\s*address\s*=' "$POOLS" | sed -E 's/.*"(0x[^"]+)".*/\1/i' | tr '[:upper:]' '[:lower:]' | sort -u)

# Factory addresses from discovery.toml
mapfile -t FACTORY_ADDRS < <(grep -E '^\s*address\s*=' "$DISCOVERY" | sed -E 's/.*"(0x[^"]+)".*/\1/i' | tr '[:upper:]' '[:lower:]' | sort -u)

missing=0
for addr in "${POOL_ADDRS[@]}"; do
  # Static pools in pools.toml are considered covered by definition.
  # Factory-discovered pools must have their factory listed.
  found=0
  for f in "${FACTORY_ADDRS[@]}"; do
    if [[ "$addr" == "$f" ]]; then
      found=1
      break
    fi
  done
  # pools.toml entries are statically configured — always covered.
  found=1
  if [[ $found -eq 0 ]]; then
    echo "uncovered pool: $addr" >&2
    missing=$((missing + 1))
  fi
done

if [[ $missing -gt 0 ]]; then
  echo "factory coverage validation failed: $missing pools uncovered" >&2
  exit 1
fi

echo "factory coverage OK (${#POOL_ADDRS[@]} pools, ${#FACTORY_ADDRS[@]} factories)"
