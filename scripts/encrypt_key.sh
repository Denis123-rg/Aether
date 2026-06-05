#!/usr/bin/env bash
# Encrypt a raw secp256k1 private key into the AES-256-GCM blob that
# `aether-signer serve` reads at startup.
#
# This is a thin, format-safe wrapper around `go run ./cmd/signer encrypt` so
# the on-disk layout always matches the loader — never hand-roll openssl here,
# the header/KDF framing is owned by internal/signer.
#
# Usage:
#   AETHER_PRIVATE_KEY=0x<hex>  AETHER_SIGNER_PASSPHRASE=<pass> \
#     ./scripts/encrypt_key.sh /etc/aether/signer/encrypted_key.bin
#
# Or interactively (passphrase prompted, never echoed into argv/history):
#   ./scripts/encrypt_key.sh /etc/aether/signer/encrypted_key.bin
#     # then paste the 0x private key when prompted, then the passphrase
#
# The output file is created with O_EXCL — remove an existing key file first to
# rotate.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

OUT="${1:-}"
if [[ -z "$OUT" ]]; then
    echo "usage: $0 <output-key-file> [pbkdf2-iters]" >&2
    exit 1
fi
ITERS="${2:-}"

# Private key: from env, else prompt without echo.
if [[ -z "${AETHER_PRIVATE_KEY:-}" ]]; then
    read -r -s -p "Private key (0x-optional hex): " AETHER_PRIVATE_KEY
    echo
    export AETHER_PRIVATE_KEY
fi

# Passphrase: from env, else prompt twice without echo and confirm.
if [[ -z "${AETHER_SIGNER_PASSPHRASE:-}" ]]; then
    read -r -s -p "Encryption passphrase: " p1; echo
    read -r -s -p "Confirm passphrase: " p2; echo
    if [[ "$p1" != "$p2" ]]; then
        echo "passphrases do not match" >&2
        exit 1
    fi
    AETHER_SIGNER_PASSPHRASE="$p1"
    export AETHER_SIGNER_PASSPHRASE
    unset p1 p2
fi

ARGS=(run ./cmd/signer encrypt -out "$OUT")
if [[ -n "$ITERS" ]]; then
    ARGS+=(-iters "$ITERS")
fi

# The passphrase is consumed from the env var (then unset by the Go process);
# the private key likewise. Nothing sensitive is passed on argv.
go "${ARGS[@]}"

echo "encrypted key written to $OUT"
