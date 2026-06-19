package signer

import (
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"net/rpc"
	"os"
	"testing"
)

// errReader returns an error on any Read call. Used to trigger the
// io.ReadFull(rand.Reader, ...) error branches in Encrypt.
type errReader struct{}

func (errReader) Read(p []byte) (int, error) {
	return 0, errors.New("injected rand.Reader failure")
}

// partialReader returns 'ok' bytes on the first Read call, then errors.
// Used to pass the salt read but fail the nonce read in Encrypt.
type partialReader struct {
	remaining int
}

func (r *partialReader) Read(p []byte) (int, error) {
	n := len(p)
	if n > r.remaining {
		n = r.remaining
	}
	if n == 0 {
		return 0, errors.New("injected rand.Reader failure")
	}
	r.remaining -= n
	return n, nil
}

// badBlockCipher implements cipher.Block but with BlockSize != 16,
// causing cipher.NewGCM to fail and exercising that branch.
type badBlockCipher struct{}

func (badBlockCipher) BlockSize() int        { return 8 }
func (badBlockCipher) Encrypt(_, _ []byte)  {}
func (badBlockCipher) Decrypt(_, _ []byte)  {}

// --- Encrypt: io.ReadFull(rand.Reader, ...) error paths ---

func TestEncrypt_RandReadSaltError(t *testing.T) {
	raw, _ := newTestKeyBytes(t)

	old := rand.Reader
	rand.Reader = errReader{}
	defer func() { rand.Reader = old }()

	_, err := Encrypt(raw, "pw", 1000)
	if err == nil {
		t.Fatal("expected error when rand.Reader fails on salt generation")
	}
}

func TestEncrypt_RandReadNonceError(t *testing.T) {
	raw, _ := newTestKeyBytes(t)

	old := rand.Reader
	rand.Reader = &partialReader{remaining: saltLen}
	defer func() { rand.Reader = old }()

	_, err := Encrypt(raw, "pw", 1000)
	if err == nil {
		t.Fatal("expected error when rand.Reader fails on nonce generation")
	}
}

// --- Encrypt: pbkdf2.Key error path ---

func TestEncrypt_PBKDF2Error(t *testing.T) {
	raw, _ := newTestKeyBytes(t)

	old := pbkdf2KeyFn
	pbkdf2KeyFn = func(_ string, _ []byte, _ int) ([]byte, error) {
		return nil, errors.New("injected pbkdf2 error")
	}
	defer func() { pbkdf2KeyFn = old }()

	_, err := Encrypt(raw, "pw", 1000)
	if err == nil {
		t.Fatal("expected error when pbkdf2.Key fails in Encrypt")
	}
}

// --- Encrypt: newGCM error path (also covers cipher.NewGCM error in newGCM) ---

func TestEncrypt_NewGCMError(t *testing.T) {
	raw, _ := newTestKeyBytes(t)

	old := aesCipherFn
	aesCipherFn = func(_ []byte) (cipher.Block, error) {
		return badBlockCipher{}, nil
	}
	defer func() { aesCipherFn = old }()

	// Encrypt internally calls newGCM after pbkdf2 key derivation.
	// With the injected badBlockCipher, cipher.NewGCM fails because
	// BlockSize() != 16.
	_, err := Encrypt(raw, "pw", 1000)
	if err == nil {
		t.Fatal("expected error when newGCM fails due to bad cipher block")
	}
}

// --- LoadKey: pbkdf2.Key error path ---

func TestLoadKey_PBKDF2Error(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pw", 1000)
	if err != nil {
		t.Fatal(err)
	}

	old := pbkdf2KeyFn
	pbkdf2KeyFn = func(_ string, _ []byte, _ int) ([]byte, error) {
		return nil, errors.New("injected pbkdf2 error")
	}
	defer func() { pbkdf2KeyFn = old }()

	_, err = LoadKey(blob, "pw")
	if err == nil {
		t.Fatal("expected error when pbkdf2.Key fails in LoadKey")
	}
}

// --- LoadKey: newGCM error path (also covers cipher.NewGCM error in newGCM) ---

func TestLoadKey_NewGCMError(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pw", 1000)
	if err != nil {
		t.Fatal(err)
	}

	oldAes := aesCipherFn
	aesCipherFn = func(_ []byte) (cipher.Block, error) {
		return badBlockCipher{}, nil
	}
	defer func() { aesCipherFn = oldAes }()

	_, err = LoadKey(blob, "pw")
	if err == nil {
		t.Fatal("expected error when newGCM fails due to bad cipher block")
	}
}

// --- NewServer: rpc.RegisterName error path ---

func TestNewServer_RegisterNameError(t *testing.T) {
	kl, _ := loadedTestKey(t)
	sock := shortSocketPath(t)

	old := newRPCServerFn
	newRPCServerFn = func() *rpc.Server {
		srv := rpc.NewServer()
		// Pre-register "Signer" so NewServer's RegisterName call fails.
		if err := srv.RegisterName(serviceName, &SignService{}); err != nil {
			t.Fatalf("pre-register: %v", err)
		}
		return srv
	}
	defer func() { newRPCServerFn = old }()

	_, err := NewServer(kl, sock)
	if err == nil {
		t.Fatal("expected error from duplicate rpc.RegisterName")
	}
}

// --- NewServer: os.Chmod error path ---

func TestNewServer_ChmodErrorPath(t *testing.T) {
	kl, _ := loadedTestKey(t)
	sock := shortSocketPath(t)

	old := chmodFn
	chmodFn = func(_ string, _ os.FileMode) error {
		return errors.New("injected chmod error")
	}
	defer func() { chmodFn = old }()

	_, err := NewServer(kl, sock)
	if err == nil {
		t.Fatal("expected error from chmod failure")
	}
}
