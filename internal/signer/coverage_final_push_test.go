package signer

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Encrypt tests targeting specific branches ---

func TestEncrypt_EmptyPassphrase_Targeted(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	_, err := Encrypt(raw, "", 1000)
	if err != ErrEmptyPassphrase {
		t.Fatalf("got %v, want ErrEmptyPassphrase", err)
	}
}

func TestEncrypt_WrongKeyLength16_Targeted(t *testing.T) {
	key := make([]byte, 16)
	if _, err := Encrypt(key, "pass", 1000); err == nil {
		t.Fatal("expected error for 16-byte key")
	}
}

func TestEncrypt_InvalidKeyAllZeros_Targeted(t *testing.T) {
	zeros := make([]byte, 32)
	if _, err := Encrypt(zeros, "pass", 1000); err == nil {
		t.Fatal("expected error for all-zero key")
	}
}

func TestEncrypt_ZeroItersUsesDefault_Targeted(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pass", 0)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	_, iters, _, _, err := parseBlob(blob)
	if err != nil {
		t.Fatal(err)
	}
	if iters != DefaultPBKDF2Iters {
		t.Fatalf("iters = %d, want %d", iters, DefaultPBKDF2Iters)
	}
}

func TestEncrypt_NegativeItersUsesDefault_Targeted(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pass", -5)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	_, iters, _, _, err := parseBlob(blob)
	if err != nil {
		t.Fatal(err)
	}
	if iters != DefaultPBKDF2Iters {
		t.Fatalf("iters = %d, want %d", iters, DefaultPBKDF2Iters)
	}
}

func TestEncrypt_RoundTrip_Targeted(t *testing.T) {
	raw, addrHex := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pass", 1000)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	kl, err := LoadKey(blob, "pass")
	if err != nil {
		t.Fatalf("LoadKey: %v", err)
	}
	defer kl.Destroy()
	if kl.Address().Hex() != addrHex {
		t.Fatalf("address = %s, want %s", kl.Address().Hex(), addrHex)
	}
}

func TestEncrypt_ItersMinOne_Targeted(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pass", 1)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	_, iters, _, _, err := parseBlob(blob)
	if err != nil {
		t.Fatal(err)
	}
	if iters != 1 {
		t.Fatalf("iters = %d, want 1", iters)
	}
	kl, err := LoadKey(blob, "pass")
	if err != nil {
		t.Fatalf("LoadKey: %v", err)
	}
	kl.Destroy()
}

// --- newGCM tests targeting specific branches ---

func TestNewGCM_15ByteKey_Targeted(t *testing.T) {
	_, err := newGCM(make([]byte, 15))
	if err == nil {
		t.Fatal("expected aes.NewCipher error for 15-byte key")
	}
	if !strings.Contains(err.Error(), "aes init") {
		t.Fatalf("expected 'aes init' in error, got: %v", err)
	}
}

func TestNewGCM_17ByteKey_Targeted(t *testing.T) {
	_, err := newGCM(make([]byte, 17))
	if err == nil {
		t.Fatal("expected error for 17-byte key")
	}
}

func TestNewGCM_32ByteKey_Targeted(t *testing.T) {
	gcm, err := newGCM(make([]byte, 32))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gcm == nil {
		t.Fatal("expected non-nil GCM")
	}
}

// --- LoadKey tests targeting specific branches ---

func TestLoadKey_WrongPassphrase_Targeted(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "correct", 1000)
	if err != nil {
		t.Fatal(err)
	}
	_, err = LoadKey(blob, "wrong")
	if err == nil {
		t.Fatal("expected decryption failure")
	}
	if !strings.Contains(err.Error(), "decryption failed") {
		t.Fatalf("expected 'decryption failed', got: %v", err)
	}
}

func TestLoadKey_CorruptedCiphertextBytes_Targeted(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pass", 1000)
	if err != nil {
		t.Fatal(err)
	}
	blob[len(blob)-3] ^= 0xFF
	blob[len(blob)-5] ^= 0xAA
	_, err = LoadKey(blob, "pass")
	if err == nil {
		t.Fatal("expected error for corrupted ciphertext")
	}
}

func TestLoadKey_TooShortBlob_Targeted(t *testing.T) {
	_, err := LoadKey([]byte("short"), "pass")
	if err == nil {
		t.Fatal("expected error for short blob")
	}
	if !strings.Contains(err.Error(), "too short") {
		t.Fatalf("expected 'too short' in error, got: %v", err)
	}
}

func TestLoadKey_BadMagicBytes_Targeted(t *testing.T) {
	// Need enough bytes to pass the minimum length check (headerLen + 16 = 44)
	// before the magic check can be reached.
	blob := make([]byte, 64)
	copy(blob, "XXXX")
	_, err := LoadKey(blob, "pass")
	if err == nil {
		t.Fatal("expected bad magic error")
	}
	if !strings.Contains(err.Error(), "bad key file magic") {
		t.Fatalf("expected 'bad key file magic', got: %v", err)
	}
}

func TestLoadKey_UnsupportedVersionByte_Targeted(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pass", 1000)
	if err != nil {
		t.Fatal(err)
	}
	blob[4] = 99
	_, err = LoadKey(blob, "pass")
	if err == nil {
		t.Fatal("expected unsupported version error")
	}
	if !strings.Contains(err.Error(), "unsupported key file version") {
		t.Fatalf("expected 'unsupported key file version', got: %v", err)
	}
}

func TestLoadKey_UnsupportedKDFId_Targeted(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pass", 1000)
	if err != nil {
		t.Fatal(err)
	}
	blob[5] = 99
	_, err = LoadKey(blob, "pass")
	if err == nil {
		t.Fatal("expected unsupported kdf error")
	}
	if !strings.Contains(err.Error(), "unsupported kdf id") {
		t.Fatalf("expected 'unsupported kdf id', got: %v", err)
	}
}

func TestLoadKey_CorruptedButValidFormat_Targeted(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pass", 1000)
	if err != nil {
		t.Fatal(err)
	}
	// Flip the last byte of the ciphertext — GCM will reject it.
	blob[len(blob)-1] ^= 0xFF
	_, err = LoadKey(blob, "pass")
	if err == nil {
		t.Fatal("expected error for corrupted ciphertext")
	}
}

func TestLoadKey_WrongDecryptedLength_Targeted(t *testing.T) {
	passphrase := "test-targeted"
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		t.Fatal(err)
	}
	iters := 1000
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
	// Encrypt 16 bytes instead of 32 — gcm.Open succeeds but length check fails.
	ciphertext := gcm.Seal(nil, nonce, make([]byte, 16), nil)
	blob := encodeBlob(salt, iters, nonce, ciphertext)

	_, err = LoadKey(blob, passphrase)
	if err == nil {
		t.Fatal("expected wrong length error")
	}
	if !strings.Contains(err.Error(), "wrong length") {
		t.Fatalf("expected 'wrong length', got: %v", err)
	}
}

func TestLoadKey_InvalidScalarAfterDecrypt_Targeted(t *testing.T) {
	passphrase := "test-targeted"
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		t.Fatal(err)
	}
	iters := 1000
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
	// Encrypt 32 zeros — valid GCM, correct length, but not a valid secp256k1 scalar.
	ciphertext := gcm.Seal(nil, nonce, make([]byte, 32), nil)
	blob := encodeBlob(salt, iters, nonce, ciphertext)

	_, err = LoadKey(blob, passphrase)
	if err == nil {
		t.Fatal("expected invalid scalar error")
	}
	if !strings.Contains(err.Error(), "not a valid secp256k1") {
		t.Fatalf("expected 'not a valid secp256k1', got: %v", err)
	}
}

// --- NewServer tests targeting specific branches ---

func TestNewServer_NilKeyLoader_Targeted(t *testing.T) {
	_, err := NewServer(nil, "/tmp/test.sock")
	if err == nil {
		t.Fatal("expected nil key loader error")
	}
	if !strings.Contains(err.Error(), "nil key loader") {
		t.Fatalf("expected 'nil key loader', got: %v", err)
	}
}

func TestNewServer_EmptySocketPath_Targeted(t *testing.T) {
	kl := &KeyLoader{}
	_, err := NewServer(kl, "")
	if err == nil {
		t.Fatal("expected empty socket path error")
	}
	if !strings.Contains(err.Error(), "empty socket path") {
		t.Fatalf("expected 'empty socket path', got: %v", err)
	}
}

func TestNewServer_ValidAndSocketExists_Targeted(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pass", 1000)
	if err != nil {
		t.Fatal(err)
	}
	kl, err := LoadKey(blob, "pass")
	if err != nil {
		t.Fatal(err)
	}
	defer kl.Destroy()

	sock := shortSocketPath(t)
	srv, err := NewServer(kl, sock)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	// Verify socket file exists.
	info, err := os.Stat(sock)
	if err != nil {
		t.Fatalf("socket missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket perms = %o, want 600", info.Mode().Perm())
	}
}

func TestNewServer_StaleSocketCleanup_Targeted(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")

	// Create a real unix socket file directly via syscall so it persists
	// after the fd is closed (net.Listen/Close removes the socket file).
	if err := createUnixSocketFile(sock); err != nil {
		t.Fatalf("create socket file: %v", err)
	}

	// Verify the stale socket file exists.
	info, err := os.Stat(sock)
	if err != nil {
		t.Fatalf("stale socket missing: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatal("expected socket file type")
	}

	// NewServer should remove the stale socket and create a new one.
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pass", 1000)
	if err != nil {
		t.Fatal(err)
	}
	kl, err := LoadKey(blob, "pass")
	if err != nil {
		t.Fatal(err)
	}
	defer kl.Destroy()

	srv, err := NewServer(kl, sock)
	if err != nil {
		t.Fatalf("NewServer with stale socket: %v", err)
	}
	defer srv.Close()

	if srv.Addr() != sock {
		t.Fatalf("addr = %s, want %s", srv.Addr(), sock)
	}
}

func TestNewServer_CreatesNestedParentDir_Targeted(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pass", 1000)
	if err != nil {
		t.Fatal(err)
	}
	kl, err := LoadKey(blob, "pass")
	if err != nil {
		t.Fatal(err)
	}
	defer kl.Destroy()

	dir := t.TempDir()
	sock := filepath.Join(dir, "a", "b", "c", "signer.sock")
	srv, err := NewServer(kl, sock)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	parentDir := filepath.Dir(sock)
	if _, err := os.Stat(parentDir); err != nil {
		t.Fatalf("parent dir should exist: %v", err)
	}
}

func TestNewServer_MkdirBlockedByFile_Targeted(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pass", 1000)
	if err != nil {
		t.Fatal(err)
	}
	kl, err := LoadKey(blob, "pass")
	if err != nil {
		t.Fatal(err)
	}
	defer kl.Destroy()

	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(blocker, "sub", "s.sock")
	_, err = NewServer(kl, sock)
	if err == nil {
		t.Fatal("expected error when file blocks directory creation")
	}
}

func TestNewServer_ListenPathTooLong_Targeted(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pass", 1000)
	if err != nil {
		t.Fatal(err)
	}
	kl, err := LoadKey(blob, "pass")
	if err != nil {
		t.Fatal(err)
	}
	defer kl.Destroy()

	dir := t.TempDir()
	longPath := filepath.Join(dir, strings.Repeat("x", 200))
	_, err = NewServer(kl, longPath)
	if err == nil {
		t.Fatal("expected error for too-long socket path")
	}
}

func TestNewServer_RefusesClobberRegularFile_Targeted(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pass", 1000)
	if err != nil {
		t.Fatal(err)
	}
	kl, err := LoadKey(blob, "pass")
	if err != nil {
		t.Fatal(err)
	}
	defer kl.Destroy()

	dir := t.TempDir()
	regular := filepath.Join(dir, "not-a-socket")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = NewServer(kl, regular)
	if err == nil {
		t.Fatal("expected error for non-socket file at path")
	}
}

func TestNewServer_ServerLifecycle_Targeted(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pass", 1000)
	if err != nil {
		t.Fatal(err)
	}
	kl, err := LoadKey(blob, "pass")
	if err != nil {
		t.Fatal(err)
	}
	defer kl.Destroy()

	sock := shortSocketPath(t)
	srv, err := NewServer(kl, sock)
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()
	// Verify socket is cleaned up.
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatal("socket should be removed after Close")
	}
}
