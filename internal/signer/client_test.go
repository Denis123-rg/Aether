package signer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// Well-known Anvil/Hardhat account #0 — test only.
const testPrivateKeyHex = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"

func startClientTestSigner(t *testing.T) (sock string, stop func()) {
	t.Helper()
	raw, err := ParseHexKey(testPrivateKeyHex)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	blob, err := Encrypt(raw, "pw", 1000)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	kl, err := LoadKey(blob, "pw")
	if err != nil {
		t.Fatalf("load key: %v", err)
	}
	t.Cleanup(kl.Destroy)

	dir, err := os.MkdirTemp("", "aeth-signer-client")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock = filepath.Join(dir, "s.sock")

	srv, err := NewServer(kl, sock)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = srv.Serve(ctx)
		close(done)
	}()
	return sock, func() {
		cancel()
		<-done
	}
}

func TestClientPing(t *testing.T) {
	sock, stop := startClientTestSigner(t)
	defer stop()
	c := Dial(sock)
	if err := c.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestClientSignFlashbotsPayload(t *testing.T) {
	sock, stop := startClientTestSigner(t)
	defer stop()
	c := Dial(sock)

	payload := []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_sendBundle"}`)
	got, err := c.SignFlashbotsPayload(payload)
	if err != nil {
		t.Fatalf("SignFlashbotsPayload: %v", err)
	}
	if got == "" {
		t.Fatal("empty flashbots signature")
	}

	// Independent local reference for the same key.
	key, _ := crypto.HexToECDSA(testPrivateKeyHex)
	localAddr := crypto.PubkeyToAddress(key.PublicKey)
	if got[:len(localAddr.Hex())] != localAddr.Hex() {
		t.Fatalf("signature prefix %q, want address %s", got, localAddr.Hex())
	}
}
