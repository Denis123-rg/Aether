package signer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
)

func TestSignService_Address(t *testing.T) {
	kl, addrHex := loadedTestKey(t)
	svc := &SignService{kl: kl}
	var reply AddressReply
	if err := svc.Address(&AddressArgs{}, &reply); err != nil {
		t.Fatalf("Address: %v", err)
	}
	if reply.Address != addrHex {
		t.Fatalf("address = %q, want %q", reply.Address, addrHex)
	}
}

func TestDestroy_NilReceiver(t *testing.T) {
	var kl *KeyLoader
	kl.Destroy() // must not panic
}

func TestDestroy_DoubleDestroy(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pw", 1000)
	if err != nil {
		t.Fatal(err)
	}
	kl, err := LoadKey(blob, "pw")
	if err != nil {
		t.Fatal(err)
	}
	kl.Destroy()
	kl.Destroy()
	if _, err := kl.SignDigest(make([]byte, 32)); err == nil {
		t.Fatal("sign after double destroy should fail")
	}
}

func TestLoadKey_InvalidScalarAfterDecrypt(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pw", 1000)
	if err != nil {
		t.Fatal(err)
	}
	// Flip bytes in ciphertext region so GCM still decrypts but scalar is invalid.
	for i := len(blob) - 20; i < len(blob); i++ {
		blob[i] ^= 0xff
	}
	if _, err := LoadKey(blob, "pw"); err == nil {
		t.Fatal("expected error for invalid scalar after decrypt")
	}
}

func TestServer_ServeAcceptsClient(t *testing.T) {
	kl, _ := loadedTestKey(t)
	sock := shortSocketPath(t)
	srv, err := NewServer(kl, sock)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()

	time.Sleep(50 * time.Millisecond)
	client := Dial(sock)
	if err := client.Ping(); err != nil {
		cancel()
		t.Fatalf("Ping: %v", err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not exit")
	}
	_ = srv.Close()
}

func TestNewServer_CreatesParentDirectory(t *testing.T) {
	kl, _ := loadedTestKey(t)
	dir := t.TempDir()
	sock := filepath.Join(dir, "nested", "dir", "s.sock")
	srv, err := NewServer(kl, sock)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()
	if _, err := os.Stat(filepath.Dir(sock)); err != nil {
		t.Fatalf("parent dir missing: %v", err)
	}
}

func TestClient_SignDigestDialFailure(t *testing.T) {
	c := Dial(shortSocketPath(t))
	if _, err := c.SignDigest(crypto.Keccak256([]byte("x"))); err == nil {
		t.Fatal("expected dial error")
	}
}

func TestServer_CloseRemovesSocketFile(t *testing.T) {
	kl, _ := loadedTestKey(t)
	sock := shortSocketPath(t)
	srv, err := NewServer(kl, sock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("socket should exist before close: %v", err)
	}
	_ = srv.Close()
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket file should be removed, stat err = %v", err)
	}
}
