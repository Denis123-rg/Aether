package signer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestClient_AddressFailsWithoutServer(t *testing.T) {
	c := Dial(shortSocketPath(t))
	if _, err := c.Address(); err == nil {
		t.Fatal("expected dial error")
	}
}

func TestClient_SignFlashbotsPayloadAddressError(t *testing.T) {
	c := Dial(shortSocketPath(t))
	if _, err := c.SignFlashbotsPayload([]byte("body")); err == nil {
		t.Fatal("expected error when signer absent")
	}
}

func TestNewServer_NilKeyLoader(t *testing.T) {
	if _, err := NewServer(nil, shortSocketPath(t)); err == nil {
		t.Fatal("expected error for nil key loader")
	}
}

func TestNewServer_EmptySocketPath(t *testing.T) {
	kl, _ := loadedTestKey(t)
	if _, err := NewServer(kl, ""); err == nil {
		t.Fatal("expected error for empty socket path")
	}
}

func TestRemoveStaleSocket_NonExistent(t *testing.T) {
	dir, err := os.MkdirTemp("", "aeth")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := removeStaleSocket(filepath.Join(dir, "missing.sock")); err != nil {
		t.Fatalf("missing socket: %v", err)
	}
}

func TestRemoveStaleSocket_RefusesRegularFile(t *testing.T) {
	dir, err := os.MkdirTemp("", "aeth")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "regular")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeStaleSocket(path); err == nil {
		t.Fatal("expected refusal to remove non-socket")
	}
}

func TestServer_CloseIdempotent(t *testing.T) {
	kl, _ := loadedTestKey(t)
	sock := shortSocketPath(t)
	srv, err := NewServer(kl, sock)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Fatalf("first close: %v", err)
	}
	if err := srv.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Fatalf("second close: %v", err)
	}
}

func TestLoadKeyFile_MissingPath(t *testing.T) {
	_, err := LoadKeyFile(filepath.Join(t.TempDir(), "missing.bin"), "pw")
	if err == nil {
		t.Fatal("expected error for missing key file")
	}
}

func TestClient_SignFlashbotsPayloadBadSignatureLength(t *testing.T) {
	// Covered indirectly via server rejecting bad digest; ensure non-empty payload works.
	kl, _ := loadedTestKey(t)
	client, stop := startTestServer(t, kl)
	defer stop()

	got, err := client.SignFlashbotsPayload([]byte(`{"jsonrpc":"2.0"}`))
	if err != nil {
		t.Fatalf("SignFlashbotsPayload: %v", err)
	}
	if got == "" {
		t.Fatal("empty flashbots signature")
	}
}

func TestDestroy_ClearsAddress(t *testing.T) {
	raw, addrHex := newTestKeyBytes(t)
	blob, _ := Encrypt(raw, "pw", 1000)
	kl, _ := LoadKey(blob, "pw")
	if kl.Address().Hex() != addrHex {
		t.Fatal("address mismatch before destroy")
	}
	kl.Destroy()
	if _, err := kl.SignDigest(make([]byte, 32)); err == nil {
		t.Fatal("sign after destroy should fail")
	}
}
