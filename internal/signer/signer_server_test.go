package signer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// shortSocketPath returns a socket path well under the ~108-byte sun_path
// limit, regardless of how long the test's default TempDir is.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "aeth")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

func startTestServer(t *testing.T, kl *KeyLoader) (*Client, func()) {
	t.Helper()
	sock := shortSocketPath(t)
	srv, err := NewServer(kl, sock)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	// 0600 socket permission is part of the security contract — assert it.
	info, err := os.Stat(sock)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("socket perms = %o, want 600", perm)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = srv.Serve(ctx)
		close(done)
	}()
	return Dial(sock), func() {
		cancel()
		<-done
	}
}

func loadedTestKey(t *testing.T) (*KeyLoader, string) {
	t.Helper()
	raw, addrHex := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pw", 1000)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	kl, err := LoadKey(blob, "pw")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	t.Cleanup(kl.Destroy)
	return kl, addrHex
}

func TestServerAddressRoundTrip(t *testing.T) {
	kl, addrHex := loadedTestKey(t)
	client, stop := startTestServer(t, kl)
	defer stop()

	got, err := client.Address()
	if err != nil {
		t.Fatalf("client.Address: %v", err)
	}
	if got != addrHex {
		t.Fatalf("address over socket = %s, want %s", got, addrHex)
	}
}

func TestServerSignDigestRoundTrip(t *testing.T) {
	kl, addrHex := loadedTestKey(t)
	client, stop := startTestServer(t, kl)
	defer stop()

	digest := crypto.Keccak256([]byte("over-the-wire bundle digest"))
	sig, err := client.SignDigest(digest)
	if err != nil {
		t.Fatalf("client.SignDigest: %v", err)
	}
	pub, err := crypto.SigToPub(digest, sig)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if got := crypto.PubkeyToAddress(*pub).Hex(); got != addrHex {
		t.Fatalf("recovered %s != signer %s", got, addrHex)
	}
}

func TestServerRejectsBadDigestOverWire(t *testing.T) {
	kl, _ := loadedTestKey(t)
	client, stop := startTestServer(t, kl)
	defer stop()

	if _, err := client.SignDigest([]byte("nope")); err == nil {
		t.Fatal("expected server to reject a non-32-byte digest")
	}
}

func TestNewServerRejectsNonSocketPath(t *testing.T) {
	kl, _ := loadedTestKey(t)
	dir, err := os.MkdirTemp("", "aeth")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	regular := filepath.Join(dir, "not-a-socket")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := NewServer(kl, regular); err == nil {
		t.Fatal("expected NewServer to refuse clobbering a regular file")
	}
}
