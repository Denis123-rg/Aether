package signer

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

func TestNewTestEncrypt_EmptyPassphrase(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	_, err := Encrypt(raw, "", 1000)
	if !errors.Is(err, ErrEmptyPassphrase) {
		t.Errorf("expected ErrEmptyPassphrase, got %v", err)
	}
}

func TestNewTestEncrypt_WrongKeyLength(t *testing.T) {
	_, err := Encrypt([]byte{0x01, 0x02}, "pass", 1000)
	if err == nil {
		t.Error("expected error for wrong key length")
	}
}

func TestNewTestEncrypt_InvalidKeyBytes(t *testing.T) {
	_, err := Encrypt(make([]byte, 32), "pass", 1000)
	if err == nil {
		t.Error("expected error for invalid key bytes (zero key)")
	}
}

func TestNewTestEncrypt_DefaultIters(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pass", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(blob) < 40 {
		t.Error("blob too short")
	}
}

func TestNewTestEncrypt_NegativeIters(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pass", -100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(blob) < 40 {
		t.Error("blob too short")
	}
}

func TestNewTestLoadKey_EmptyPassphrase(t *testing.T) {
	_, err := LoadKey([]byte("garbage-data-padding-padding-padding"), "")
	if !errors.Is(err, ErrEmptyPassphrase) {
		t.Errorf("expected ErrEmptyPassphrase, got %v", err)
	}
}

func TestNewTestLoadKey_WrongPassphrase(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "correct", 1000)
	if err != nil {
		t.Fatal(err)
	}
	_, err = LoadKey(blob, "wrong")
	if err == nil {
		t.Error("expected error for wrong passphrase")
	}
}

func TestNewTestLoadKey_TooShort(t *testing.T) {
	_, err := LoadKey([]byte("ABC"), "pass")
	if err == nil {
		t.Error("expected error for short blob")
	}
}

func TestNewTestLoadKey_BadMagic(t *testing.T) {
	blob := make([]byte, 50)
	copy(blob, "XXXX")
	_, err := LoadKey(blob, "pass")
	if err == nil {
		t.Error("expected error for bad magic")
	}
}

func TestNewTestLoadKey_BadVersion(t *testing.T) {
	blob := make([]byte, 50)
	copy(blob, "AETK")
	blob[4] = 99 // invalid version
	_, _, _, _, err := parseBlob(blob)
	if err == nil {
		t.Error("expected error for bad version")
	}
}

func TestNewTestLoadKey_BadKDF(t *testing.T) {
	blob := make([]byte, 50)
	copy(blob, "AETK")
	blob[4] = 1
	blob[5] = 99 // invalid KDF
	_, _, _, _, err := parseBlob(blob)
	if err == nil {
		t.Error("expected error for bad KDF")
	}
}

func TestNewTestLoadKey_ZeroIters(t *testing.T) {
	blob := make([]byte, 50)
	copy(blob, "AETK")
	blob[4] = 1
	blob[5] = 1
	blob[6] = 0
	blob[7] = 0
	blob[8] = 0
	blob[9] = 0
	_, _, _, _, err := parseBlob(blob)
	if err == nil {
		t.Error("expected error for zero iters")
	}
}

func TestNewTestNewGCM_ValidKey(t *testing.T) {
	_, err := newGCM(make([]byte, 32))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewTestNewGCM_WrongKeyLength(t *testing.T) {
	_, err := newGCM([]byte{0x01, 0x02, 0x03})
	if err == nil {
		t.Error("expected error for wrong key length")
	}
}

func TestNewTestNewGCM_EmptyKey(t *testing.T) {
	_, err := newGCM([]byte{})
	if err == nil {
		t.Error("expected error for empty key")
	}
}

func TestNewTestEncryptLoadRoundTrip(t *testing.T) {
	raw, addrHex := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "test-passphrase", 1000)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	kl, err := LoadKey(blob, "test-passphrase")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	defer kl.Destroy()

	if kl.Address().Hex() != addrHex {
		t.Fatalf("address mismatch: got %s want %s", kl.Address().Hex(), addrHex)
	}
}

func TestNewTestSignDigest_NilKey(t *testing.T) {
	kl := &KeyLoader{}
	_, err := kl.SignDigest(make([]byte, 32))
	if err == nil {
		t.Error("expected error for nil key")
	}
}

func TestNewTestSignDigest_WrongLength(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, _ := Encrypt(raw, "pass", 1000)
	kl, err := LoadKey(blob, "pass")
	if err != nil {
		t.Fatal(err)
	}
	defer kl.Destroy()
	_, err = kl.SignDigest(make([]byte, 16))
	if err == nil {
		t.Error("expected error for wrong digest length")
	}
}

func TestNewTestDestroy_NilLoader(t *testing.T) {
	var kl *KeyLoader
	kl.Destroy() // should not panic
}

func TestNewTestDestroy_CalledTwice(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, _ := Encrypt(raw, "pass", 1000)
	kl, _ := LoadKey(blob, "pass")
	kl.Destroy()
	kl.Destroy() // idempotent
}

func TestNewTestParseHexKey_InvalidHex(t *testing.T) {
	_, err := ParseHexKey("not-hex")
	if err == nil {
		t.Error("expected error for invalid hex")
	}
}

func TestNewTestParseHexKey_WrongLength(t *testing.T) {
	_, err := ParseHexKey("aabb")
	if err == nil {
		t.Error("expected error for wrong length")
	}
}

func TestNewTestParseHexKey_ZeroKey(t *testing.T) {
	_, err := ParseHexKey("0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Error("expected error for zero key")
	}
}

func TestNewTestParseHexKey_With0xPrefix(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	hexKey := "0x" + bytesToHex(crypto.FromECDSA(priv))
	raw, err := ParseHexKey(hexKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("expected 32 bytes, got %d", len(raw))
	}
}

func TestNewTestParseBlob_HeaderTooShort(t *testing.T) {
	_, _, _, _, err := parseBlob([]byte("ABC"))
	if err == nil {
		t.Error("expected error for short blob")
	}
}

func TestNewTestParseBlob_EncodeDecodeRoundTrip(t *testing.T) {
	salt := make([]byte, 16)
	nonce := make([]byte, 12)
	ciphertext := make([]byte, 32)
	blob := encodeBlob(salt, 600000, nonce, ciphertext)
	salt2, iters, nonce2, ciphertext2, err := parseBlob(blob)
	if err != nil {
		t.Fatal(err)
	}
	if iters != 600000 {
		t.Errorf("expected 600000, got %d", iters)
	}
	if len(salt2) != 16 || len(nonce2) != 12 || len(ciphertext2) != 32 {
		t.Errorf("wrong lengths: salt=%d nonce=%d ct=%d", len(salt2), len(nonce2), len(ciphertext2))
	}
}

func TestNewTestNewServer_NilKeyLoader(t *testing.T) {
	_, err := NewServer(nil, "/tmp/test.sock")
	if err == nil {
		t.Error("expected error for nil key loader")
	}
}

func TestNewTestNewServer_EmptySocketPath(t *testing.T) {
	kl := &KeyLoader{}
	_, err := NewServer(kl, "")
	if err == nil {
		t.Error("expected error for empty socket path")
	}
}

func TestNewTestNewServer_TooLongSocketPath(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, _ := Encrypt(raw, "pass", 1000)
	kl, _ := LoadKey(blob, "pass")
	defer kl.Destroy()

	// Unix socket paths are limited to ~108 bytes (sun_path)
	longPath := filepath.Join(t.TempDir(), strings.Repeat("a", 200)+".sock")
	_, err := NewServer(kl, longPath)
	if err == nil {
		t.Error("expected error for too-long socket path")
	}
}

func TestNewTestNewServer_NonSocketPath(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, _ := Encrypt(raw, "pass", 1000)
	kl, _ := LoadKey(blob, "pass")
	defer kl.Destroy()

	regular := filepath.Join(t.TempDir(), "not-a-socket")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewServer(kl, regular)
	if err == nil {
		t.Error("expected NewServer to refuse clobbering a regular file")
	}
}

func TestNewTestRemoveStaleSocket_NotExists(t *testing.T) {
	err := removeStaleSocket(t.TempDir() + "/nonexistent.sock")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNewTestRemoveStaleSocket_IsDir(t *testing.T) {
	dir := t.TempDir()
	err := removeStaleSocket(dir)
	if err == nil {
		t.Error("expected error for directory")
	}
}

func TestNewTestSignService_Address(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, _ := Encrypt(raw, "pass", 1000)
	kl, _ := LoadKey(blob, "pass")
	defer kl.Destroy()

	svc := &SignService{kl: kl}
	var reply AddressReply
	err := svc.Address(nil, &reply)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Address == "" {
		t.Error("expected non-empty address")
	}
}

func TestNewTestSignService_SignDigest(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, _ := Encrypt(raw, "pass", 1000)
	kl, _ := LoadKey(blob, "pass")
	defer kl.Destroy()

	svc := &SignService{kl: kl}
	var reply SignDigestReply
	err := svc.SignDigest(&SignDigestArgs{Digest: make([]byte, 32)}, &reply)
	if err != nil {
		t.Fatal(err)
	}
	if len(reply.Signature) != 65 {
		t.Errorf("expected 65-byte sig, got %d", len(reply.Signature))
	}
}

func TestNewTestSignService_SignDigest_WrongLength(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, _ := Encrypt(raw, "pass", 1000)
	kl, _ := LoadKey(blob, "pass")
	defer kl.Destroy()

	svc := &SignService{kl: kl}
	var reply SignDigestReply
	err := svc.SignDigest(&SignDigestArgs{Digest: make([]byte, 16)}, &reply)
	if err == nil {
		t.Error("expected error for wrong digest length")
	}
}
