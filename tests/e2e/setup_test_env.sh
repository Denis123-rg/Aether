#!/usr/bin/env bash
# Build all binaries and test fixtures for the Aether E2E pipeline.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
BUILD_DIR="${BUILD_DIR:-$PROJECT_ROOT/build/e2e}"

STAGING_KEY="${STAGING_KEY:-0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcda11cb7257a0b8d2}"
SIGNER_PASS="${SIGNER_PASS:-e2e-test-pass}"

mkdir -p "$BUILD_DIR" "$BUILD_DIR/config" "$BUILD_DIR/logs"

echo "[setup] Building Go services..."
(cd "$PROJECT_ROOT" && go build -o "$BUILD_DIR/aether-executor" ./cmd/executor)
(cd "$PROJECT_ROOT" && go build -o "$BUILD_DIR/aether-telebot" ./cmd/telebot)
(cd "$PROJECT_ROOT" && go build -o "$BUILD_DIR/aether-signer" ./cmd/signer)

echo "[setup] Building Rust grpc-server..."
(cd "$PROJECT_ROOT" && cargo build --release -p aether-grpc-server)

echo "[setup] Preparing signer encrypted key..."
KEY_FILE="$BUILD_DIR/test_signer.key"
if [[ ! -f "$KEY_FILE" ]]; then
  printf '%s' "$SIGNER_PASS" | \
    AETHER_PRIVATE_KEY="$STAGING_KEY" \
    "$BUILD_DIR/aether-signer" encrypt -out "$KEY_FILE"
fi

SIGNER_SOCK="$BUILD_DIR/signer.sock"
cat > "$BUILD_DIR/config/signer.yaml" <<EOF
socket_path: "$SIGNER_SOCK"
key_file: "$KEY_FILE"
EOF

echo "[setup] Done. Artifacts in $BUILD_DIR"
