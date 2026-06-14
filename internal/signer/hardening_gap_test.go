package signer

import (
	"testing"
)

func TestDialAuto_PooledWhenEnabled(t *testing.T) {
	t.Setenv("SIGNER_USE_CONNECTION_POOL", "true")
	c := DialAuto("/tmp/not-used.sock")
	if _, ok := c.(*PooledSignerClient); !ok {
		t.Fatalf("got %T", c)
	}
}

func TestDialAuto_LegacyByDefault(t *testing.T) {
	t.Setenv("SIGNER_USE_CONNECTION_POOL", "false")
	c := DialAuto("/tmp/not-used.sock")
	if _, ok := c.(*Client); !ok {
		t.Fatalf("got %T", c)
	}
}

func TestPooledSignerClient_CallRetryPath(t *testing.T) {
	kl, _ := loadedTestKey(t)
	client, stop := startPooledTestClient(t, kl)
	defer stop()

	if _, err := client.SignDigest(make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	if client.conn != nil {
		_ = client.conn.Close()
	}
	client.resetLocked()
	client.mu.Unlock()
	if _, err := client.SignFlashbotsPayload([]byte("payload")); err != nil {
		t.Fatalf("retry path: %v", err)
	}
}

func TestPooledSignerClient_PingAndAddress(t *testing.T) {
	kl, addrHex := loadedTestKey(t)
	client, stop := startPooledTestClient(t, kl)
	defer stop()
	if err := client.Ping(); err != nil {
		t.Fatal(err)
	}
	got, err := client.Address()
	if err != nil || got != addrHex {
		t.Fatalf("address = %q err=%v", got, err)
	}
}

func TestNewServer_CreatesSocketDir(t *testing.T) {
	kl, _ := loadedTestKey(t)
	dir := t.TempDir()
	sock := dir + "/nested/signer.sock"
	srv, err := NewServer(kl, sock)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if srv == nil {
		t.Fatal("nil server")
	}
}
