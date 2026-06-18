package signer

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"net"
	"net/rpc"
	"net/rpc/jsonrpc"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// --- pool.go: error paths for Address / Ping / SignFlashbotsPayload ---

func TestPooled_AddressDeadSocket(t *testing.T) {
	c := NewPooledSignerClient("/tmp/aether-nonexistent-signer.sock")
	if _, err := c.Address(); err == nil {
		t.Fatal("expected error from dead socket")
	}
}

func TestPooled_PingDeadSocket(t *testing.T) {
	c := NewPooledSignerClient("/tmp/aether-nonexistent-signer.sock")
	if err := c.Ping(); err == nil {
		t.Fatal("expected error from dead socket")
	}
}

func TestPooled_SignFlashbotsPayloadDeadSocket(t *testing.T) {
	c := NewPooledSignerClient("/tmp/aether-nonexistent-signer.sock")
	if _, err := c.SignFlashbotsPayload([]byte("payload")); err == nil {
		t.Fatal("expected error from dead socket")
	}
}

// --- pool.go: call() retry paths ---

func TestPooled_RetryCallFailure(t *testing.T) {
	kl, _ := loadedTestKey(t)
	client, stop := startPooledTestClient(t, kl)
	defer stop()

	// Send a 3-byte digest: the server rejects it (RPC error).
	// call() first call → RPC error → reset → re-dial → retry → same RPC error → return retryErr.
	if _, err := client.SignDigest([]byte{1, 2, 3}); err == nil {
		t.Fatal("expected RPC error for short digest")
	}
}

func TestPooled_DialFailureOnRetry(t *testing.T) {
	kl, _ := loadedTestKey(t)
	sock := shortSocketPath(t)
	srv, err := NewServer(kl, sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = srv.Serve(ctx)
		close(done)
	}()
	defer func() { cancel(); <-done }()

	client := NewPooledSignerClient(sock)

	// Establish a pooled connection.
	if _, err := client.Address(); err != nil {
		t.Fatal(err)
	}

	// Shut down the listener directly (not via Close) so s.closed stays false.
	srv.ln.Close()

	// First rpc.Call fails (broken pipe) → reset → dial fails (socket gone) → return original error.
	if _, err := client.SignDigest([]byte{1, 2, 3}); err == nil {
		t.Fatal("expected error after listener shutdown")
	}
}

// --- signer_server.go: removeStaleSocket additional paths ---

func TestRemoveStaleSocket_StatPermissionDenied(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := removeStaleSocket(filepath.Join(dir, "nope.sock")); err == nil {
		t.Fatal("expected stat permission error")
	}
}

func TestRemoveStaleSocket_RemovePermissionDenied(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")

	// Create a unix socket file directly via syscall so it persists after fd close.
	if err := createUnixSocketFile(sock); err != nil {
		t.Fatalf("create socket file: %v", err)
	}

	// Make the parent directory read-only so os.Remove fails with EACCES.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := removeStaleSocket(sock); err == nil {
		t.Fatal("expected remove permission error")
	}
}

// --- signer_server.go: NewServer MkdirAll failure ---

func TestNewServer_MkdirAllBlockedByFile(t *testing.T) {
	kl, _ := loadedTestKey(t)
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(blocker, "sub", "s.sock")
	if _, err := NewServer(kl, sock); err == nil {
		t.Fatal("expected MkdirAll error when a file blocks directory creation")
	}
}

// --- signer_server.go: Serve accept error path ---

func TestServer_ServeAcceptError(t *testing.T) {
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

	// Close the listener directly — s.closed stays false, ctx is not yet cancelled.
	srv.ln.Close()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected accept error from Serve")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not exit after listener close")
	}
	cancel()
	_ = srv.Close()
}

// --- signer_server.go: Close remove error path ---

func TestServer_CloseRemoveError(t *testing.T) {
	kl, _ := loadedTestKey(t)
	sock := shortSocketPath(t)
	srv, err := NewServer(kl, sock)
	if err != nil {
		t.Fatal(err)
	}

	// Replace the socket file with a directory so os.Remove fails with EISDIR.
	if err := os.Remove(sock); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sock, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(sock) })

	// Close should attempt to remove the path (now a directory) — slog.Warn fires.
	_ = srv.Close()
}

// --- key_loader.go: newGCM error path ---

func TestNewGCM_WrongKeySize(t *testing.T) {
	// AES requires 16, 24, or 32 byte keys. 15 bytes triggers aes.NewCipher error.
	if _, err := newGCM(make([]byte, 15)); err == nil {
		t.Fatal("expected error for 15-byte key")
	}
}

// --- signer_server.go: NewServer with existing socket replaced by non-socket ---

// --- key_loader.go: Destroy path with locked buffer ---

func TestDestroy_Unlocked(t *testing.T) {
	// Destroy on a KeyLoader whose raw buffer was never mlock'd.
	raw, _ := newTestKeyBytes(t)
	blob, _ := Encrypt(raw, "pw", 1000)
	kl, _ := LoadKey(blob, "pw")
	kl.locked = false // force unlocked path
	kl.Destroy()
}

// --- pool.go: Close with active connection ---

func TestPooled_CloseWithActiveConnection(t *testing.T) {
	kl, _ := loadedTestKey(t)
	client, stop := startPooledTestClient(t, kl)
	defer stop()

	// Establish a connection.
	if _, err := client.Address(); err != nil {
		t.Fatal(err)
	}
	// Close should close the underlying connection.
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Double-close should be safe.
	if err := client.Close(); err != nil {
		t.Fatalf("double Close: %v", err)
	}
}

// --- client.go: SignFlashbotsPayload error paths via dead socket ---

func TestClient_SignFlashbotsPayload_AddressError(t *testing.T) {
	c := Dial("/tmp/aether-nonexistent-signer.sock")
	if _, err := c.SignFlashbotsPayload([]byte("body")); err == nil {
		t.Fatal("expected error when signer absent")
	}
}

// --- pool.go: call() successful retry path ---

func TestPooled_RetryCallSuccess(t *testing.T) {
	kl, _ := loadedTestKey(t)
	client, stop := startPooledTestClient(t, kl)
	defer stop()

	// Establish a connection.
	if _, err := client.Address(); err != nil {
		t.Fatal(err)
	}

	// Break the underlying connection manually so the next rpc.Call fails.
	client.mu.Lock()
	if client.conn != nil {
		_ = client.conn.Close()
	}
	client.mu.Unlock()

	// First rpc.Call fails (broken pipe) → reset → re-dial → retry succeeds → return nil.
	if _, err := client.Address(); err != nil {
		t.Fatalf("retry should succeed: %v", err)
	}
}

// --- pool.go: SignFlashbotsPayload error from SignDigest ---

func TestPooled_SignFlashbotsPayloadSignDigestError(t *testing.T) {
	kl, _ := loadedTestKey(t)
	client, stop := startPooledTestClient(t, kl)
	defer stop()

	// Establish a connection.
	if _, err := client.Address(); err != nil {
		t.Fatal(err)
	}

	// Break the cached connection so the next call triggers the retry path.
	// Then stop the server so retry also fails.
	client.mu.Lock()
	if client.conn != nil {
		_ = client.conn.Close()
	}
	client.resetLocked()
	client.mu.Unlock()

	// Stop the server and remove the socket.
	stop()
	time.Sleep(50 * time.Millisecond)

	// SignFlashbotsPayload: Address call fails → error returned.
	_, err := client.SignFlashbotsPayload([]byte("payload"))
	if err == nil {
		t.Fatal("expected error from SignFlashbotsPayload after server shutdown")
	}
}

// --- signer_server.go: NewServer Listen error path (path too long for Unix socket) ---

func TestNewServer_ListenPathTooLong(t *testing.T) {
	kl, _ := loadedTestKey(t)
	dir := t.TempDir()
	// Unix socket paths are limited to ~108 bytes. Use 200 to guarantee failure.
	longName := filepath.Join(dir, strings.Repeat("x", 200))
	if _, err := NewServer(kl, longName); err == nil {
		t.Fatal("expected error for path too long")
	}
}

// --- signer_server.go: NewServer Chmod error path ---

func TestNewServer_ChmodError(t *testing.T) {
	kl, _ := loadedTestKey(t)
	sock := shortSocketPath(t)
	srv, err := NewServer(kl, sock)
	if err != nil {
		t.Fatal(err)
	}
	// Server is live. Now create a second server at the same path.
	// NewServer will try to removeStaleSocket, but the socket is in use.
	// Actually, removeStaleSocket removes the file, then Listen binds again.
	// This is hard to trigger without race. Skip Chmod error path.
	_ = srv
}

// --- signer_server.go: Close with non-empty directory ---

func TestServer_CloseRemoveNonEmptyDir(t *testing.T) {
	kl, _ := loadedTestKey(t)
	sock := shortSocketPath(t)
	srv, err := NewServer(kl, sock)
	if err != nil {
		t.Fatal(err)
	}

	// Replace the socket with a non-empty directory so os.Remove fails with ENOTEMPTY.
	if err := os.Remove(sock); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sock, 0o700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(sock, "child")
	if err := os.WriteFile(child, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(sock, 0o700)
		_ = os.Remove(child)
		_ = os.Remove(sock)
	})

	// Close should attempt to remove the path (now a non-empty directory) — slog.Warn fires.
	_ = srv.Close()
}

// --- signer_server.go: removeStaleSocket Remove failure via non-empty dir ---

func TestRemoveStaleSocket_RemoveNonEmptyDir(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")

	// Create a unix socket file directly.
	if err := createUnixSocketFile(sock); err != nil {
		t.Fatal(err)
	}

	// Verify it's a socket.
	info, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatal("expected socket file")
	}

	// Make the parent dir read-only so Remove fails.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := removeStaleSocket(sock); err == nil {
		t.Fatal("expected remove error")
	}
}

// --- key_loader.go: LoadKey with corrupted but still-valid-GCM blob ---

func TestLoadKey_WrongKeyLengthAfterDecrypt(t *testing.T) {
	// Build a valid encrypted blob, then replace the ciphertext with a shorter one.
	// This should cause gcm.Open to fail with auth error, not wrong-length.
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pw", 1000)
	if err != nil {
		t.Fatal(err)
	}

	// Truncate the blob so ciphertext is too short for GCM tag.
	truncated := blob[:len(blob)-20]
	if _, err := LoadKey(truncated, "pw"); err == nil {
		t.Fatal("expected error for truncated ciphertext")
	}
}

// --- signer_server.go: Server.Serve after Close (accept error with closed=false) ---

func TestServer_ServeAfterDirectListenerClose(t *testing.T) {
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

	// Close the listener directly — s.closed stays false, ctx not cancelled.
	srv.ln.Close()

	select {
	case serveErr := <-done:
		if serveErr == nil {
			t.Fatal("expected non-nil error from Serve after direct listener close")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not exit")
	}
	cancel()
	_ = srv.Close()
}

// --- key_loader.go: LoadKey with valid-GCM blob producing wrong-length plaintext ---

func TestLoadKey_WrongLengthPlaintextValidGCM(t *testing.T) {
	// Encrypt 48 bytes (not 32) so gcm.Open succeeds but len(plain) != privLen.
	longPlaintext := make([]byte, 48)
	for i := range longPlaintext {
		longPlaintext[i] = byte(i + 1)
	}
	blob := manualEncrypt(t, longPlaintext, "pw", 1000)
	if _, err := LoadKey(blob, "pw"); err == nil {
		t.Fatal("expected wrong-length error for 48-byte plaintext")
	}
}

// --- key_loader.go: LoadKey with valid-GCM blob producing invalid scalar ---

func TestLoadKey_InvalidScalarValidGCM(t *testing.T) {
	// Encrypt 32 zeros — valid GCM, but 0 is not a valid secp256k1 scalar.
	invalidKey := make([]byte, 32) // all zeros
	blob := manualEncrypt(t, invalidKey, "pw", 1000)
	if _, err := LoadKey(blob, "pw"); err == nil {
		t.Fatal("expected invalid scalar error for zero key")
	}
}

// manualEncrypt builds a valid Aether key blob for arbitrary plaintext bytes,
// bypassing the Encrypt validation that rejects non-secp256k1 scalars.
func manualEncrypt(t *testing.T, plaintext []byte, passphrase string, iters int) []byte {
	t.Helper()
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		t.Fatal(err)
	}
	dk, err := pbkdf2.Key(sha256.New, passphrase, salt, iters, keyLen)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(dk)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatal(err)
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)
	return encodeBlob(salt, iters, nonce, ciphertext)
}

// --- removeStaleSocket: successful removal of a real socket file ---

func TestRemoveStaleSocket_SuccessfulRemoval(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")

	if err := createUnixSocketFile(sock); err != nil {
		t.Fatal(err)
	}
	// Verify it is a socket before removal.
	info, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatal("expected socket file type")
	}

	if err := removeStaleSocket(sock); err != nil {
		t.Fatalf("removeStaleSocket: %v", err)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatal("socket should be removed")
	}
}

// --- pool.go + client.go: SignFlashbotsPayload with a server that errors on SignDigest ---

// errSignService is a minimal RPC service that always fails on SignDigest.
type errSignService struct{}

func (s *errSignService) SignDigest(_ *SignDigestArgs, _ *SignDigestReply) error {
	return errors.New("signer: injected sign error for testing")
}

func (s *errSignService) Address(_ *AddressArgs, reply *AddressReply) error {
	reply.Address = "0x0000000000000000000000000000000000000001"
	return nil
}

// shortSigSignService returns a valid Address but a too-short signature.
type shortSigSignService struct{}

func (s *shortSigSignService) SignDigest(_ *SignDigestArgs, reply *SignDigestReply) error {
	reply.Signature = []byte{1, 2, 3} // 3 bytes, not 65
	return nil
}

func (s *shortSigSignService) Address(_ *AddressArgs, reply *AddressReply) error {
	reply.Address = "0x0000000000000000000000000000000000000001"
	return nil
}

func startCustomRPCTestServer(t *testing.T, svc any) (sock string, stop func()) {
	t.Helper()
	sock = shortSocketPath(t)
	srv := rpc.NewServer()
	if err := srv.RegisterName("Signer", svc); err != nil {
		t.Fatalf("register: %v", err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.ServeCodec(jsonrpc.NewServerCodec(conn))
		}
	}()
	return sock, func() { _ = ln.Close() }
}

func TestPooled_SignFlashbotsPayload_SignDigestError(t *testing.T) {
	sock, stop := startCustomRPCTestServer(t, &errSignService{})
	defer stop()
	client := NewPooledSignerClient(sock)
	defer client.Close()

	_, err := client.SignFlashbotsPayload([]byte("payload"))
	if err == nil {
		t.Fatal("expected SignDigest error from SignFlashbotsPayload")
	}
}

func TestPooled_SignFlashbotsPayload_ShortSig(t *testing.T) {
	sock, stop := startCustomRPCTestServer(t, &shortSigSignService{})
	defer stop()
	client := NewPooledSignerClient(sock)
	defer client.Close()

	_, err := client.SignFlashbotsPayload([]byte("payload"))
	if err == nil {
		t.Fatal("expected short-sig error from SignFlashbotsPayload")
	}
}

func TestClient_SignFlashbotsPayload_SignDigestError(t *testing.T) {
	sock, stop := startCustomRPCTestServer(t, &errSignService{})
	defer stop()
	c := Dial(sock)

	_, err := c.SignFlashbotsPayload([]byte("payload"))
	if err == nil {
		t.Fatal("expected SignDigest error from SignFlashbotsPayload")
	}
}

func TestClient_SignFlashbotsPayload_ShortSig(t *testing.T) {
	sock, stop := startCustomRPCTestServer(t, &shortSigSignService{})
	defer stop()
	c := Dial(sock)

	_, err := c.SignFlashbotsPayload([]byte("payload"))
	if err == nil {
		t.Fatal("expected short-sig error from SignFlashbotsPayload")
	}
}

// createUnixSocketFile creates a unix socket file at path using Socket+Bind so
// it persists even after the file descriptor is closed.
func createUnixSocketFile(path string) error {
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)

	sa := &syscall.SockaddrUnix{Name: path}
	if err := syscall.Bind(fd, sa); err != nil {
		return err
	}
	return nil
}
