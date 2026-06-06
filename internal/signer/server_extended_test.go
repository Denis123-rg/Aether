package signer

import (
	"context"
	"testing"
	"time"
)

func TestServer_AddrReturnsSocketPath(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pw", 1000)
	if err != nil {
		t.Fatal(err)
	}
	kl, err := LoadKey(blob, "pw")
	if err != nil {
		t.Fatal(err)
	}
	defer kl.Destroy()

	sock := shortSocketPath(t)
	srv, err := NewServer(kl, sock)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	if got := srv.Addr(); got != sock {
		t.Fatalf("Addr() = %q, want %q", got, sock)
	}
}

func TestServer_ServeStopsOnContextCancel(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pw", 1000)
	if err != nil {
		t.Fatal(err)
	}
	kl, err := LoadKey(blob, "pw")
	if err != nil {
		t.Fatal(err)
	}
	defer kl.Destroy()

	sock := shortSocketPath(t)
	srv, err := NewServer(kl, sock)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not exit after context cancel")
	}
	_ = srv.Close()
}

func TestLoadKeyFile_MissingFile(t *testing.T) {
	_, err := LoadKeyFile(shortSocketPath(t)+".enc", "pw")
	if err == nil {
		t.Fatal("expected error for missing key file")
	}
}
