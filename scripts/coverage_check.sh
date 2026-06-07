#!/usr/bin/env bash
set -euo pipefail
cd /home/denis/Aether
for pkg in internal/strategy internal/risk internal/config internal/events internal/grpc internal/db internal/signer; do
  echo "=== $pkg ==="
  go test -coverprofile=/tmp/p.out -covermode=atomic "./$pkg/..." 2>&1 | tail -1
  go tool cover -func=/tmp/p.out | awk '$3+0 < 95 {print}'
  go tool cover -func=/tmp/p.out | tail -1
  echo
done
