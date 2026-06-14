package signer

import (
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/crypto"
)

func startPooledTestClient(t *testing.T, kl *KeyLoader) (*PooledSignerClient, func()) {
	t.Helper()
	sock := shortSocketPath(t)
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
	client := NewPooledSignerClient(sock)
	return client, func() {
		cancel()
		<-done
		_ = client.Close()
	}
}

func TestPooledSignerClient_SignDigestRoundTrip(t *testing.T) {
	kl, addrHex := loadedTestKey(t)
	client, stop := startPooledTestClient(t, kl)
	defer stop()

	digest := crypto.Keccak256([]byte("pooled-sign-digest"))
	sig, err := client.SignDigest(digest)
	if err != nil {
		t.Fatalf("SignDigest: %v", err)
	}
	pub, err := crypto.SigToPub(digest, sig)
	if err != nil {
		t.Fatal(err)
	}
	if got := crypto.PubkeyToAddress(*pub).Hex(); got != addrHex {
		t.Fatalf("recovered %s want %s", got, addrHex)
	}
	if client.ReuseCount() < 1 {
		t.Fatal("expected connection reuse")
	}
}

func TestPooledSignerClient_Address(t *testing.T) {
	kl, addrHex := loadedTestKey(t)
	client, stop := startPooledTestClient(t, kl)
	defer stop()
	got, err := client.Address()
	if err != nil {
		t.Fatal(err)
	}
	if got != addrHex {
		t.Fatalf("got %s", got)
	}
}

func TestPooledSignerClient_Ping(t *testing.T) {
	kl, _ := loadedTestKey(t)
	client, stop := startPooledTestClient(t, kl)
	defer stop()
	if err := client.Ping(); err != nil {
		t.Fatal(err)
	}
}

func TestPooledSignerClient_SignFlashbotsPayload(t *testing.T) {
	kl, addrHex := loadedTestKey(t)
	client, stop := startPooledTestClient(t, kl)
	defer stop()
	payload := []byte("flashbots-bundle-payload")
	signed, err := client.SignFlashbotsPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	if signed == "" || !containsSubstr(signed, addrHex) {
		t.Fatalf("signed %q", signed)
	}
}

func TestPooledSignerClient_Close(t *testing.T) {
	c := NewPooledSignerClient("/nonexistent.sock")
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPooledSignerClient_ReconnectOnError(t *testing.T) {
	kl, _ := loadedTestKey(t)
	client, stop := startPooledTestClient(t, kl)
	defer stop()
	if _, err := client.Address(); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	client.resetLocked()
	client.mu.Unlock()
	if _, err := client.Address(); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
}

func TestPooledSignerClient_DialFailure(t *testing.T) {
	c := NewPooledSignerClient("/tmp/definitely-not-a-signer.sock")
	_, err := c.SignDigest(accounts.TextHash([]byte("x")))
	if err == nil {
		t.Fatal("expected dial error")
	}
}

func TestUseConnectionPool_InvalidValue(t *testing.T) {
	t.Setenv("SIGNER_USE_CONNECTION_POOL", "not-a-bool")
	if useConnectionPool() {
		t.Fatal("invalid env should be false")
	}
}

func TestPooledSignerClient_MultipleCallsReuse(t *testing.T) {
	kl, _ := loadedTestKey(t)
	client, stop := startPooledTestClient(t, kl)
	defer stop()
	for i := 0; i < 5; i++ {
		if _, err := client.Address(); err != nil {
			t.Fatal(err)
		}
	}
	if client.ReuseCount() < 4 {
		t.Fatalf("reuse %d", client.ReuseCount())
	}
}

func containsSubstr(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexSubstr(s, sub) >= 0)
}

func indexSubstr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
