package signer

import (
	"crypto/ecdsa"
	"errors"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

func testKeyBytes() []byte {
	key, _ := crypto.GenerateKey()
	return crypto.FromECDSA(key)
}

func TestEncrypt_EmptyPassphrase_Coverage(t *testing.T) {
	_, err := Encrypt(testKeyBytes(), "", 1000)
	if !errors.Is(err, ErrEmptyPassphrase) {
		t.Errorf("expected ErrEmptyPassphrase, got %v", err)
	}
}

func TestEncrypt_WrongKeyLength_Coverage(t *testing.T) {
	_, err := Encrypt([]byte{0x01, 0x02}, "pass", 1000)
	if err == nil {
		t.Error("expected error for wrong key length")
	}
}

func TestEncrypt_InvalidKey_Coverage(t *testing.T) {
	_, err := Encrypt(make([]byte, 32), "pass", 1000)
	if err == nil {
		t.Error("expected error for invalid key bytes")
	}
}

func TestEncrypt_DefaultIters_Coverage(t *testing.T) {
	raw := testKeyBytes()
	blob, err := Encrypt(raw, "pass", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(blob) < 40 {
		t.Error("blob too short")
	}
}

func TestLoadKey_EmptyPassphrase_Coverage(t *testing.T) {
	_, err := LoadKey([]byte("garbage"), "")
	if !errors.Is(err, ErrEmptyPassphrase) {
		t.Errorf("expected ErrEmptyPassphrase, got %v", err)
	}
}

func TestLoadKey_BadMagic_Coverage(t *testing.T) {
	blob := make([]byte, 50)
	copy(blob, "XXXX")
	_, err := LoadKey(blob, "pass")
	if err == nil {
		t.Error("expected error for bad magic")
	}
}

func TestLoadKey_TooShort_Coverage(t *testing.T) {
	_, err := LoadKey([]byte("ABC"), "pass")
	if err == nil {
		t.Error("expected error for short blob")
	}
}

func TestLoadKey_WrongPassphrase_Coverage(t *testing.T) {
	raw := testKeyBytes()
	blob, err := Encrypt(raw, "correct", 1000)
	if err != nil {
		t.Fatal(err)
	}
	_, err = LoadKey(blob, "wrong")
	if err == nil {
		t.Error("expected error for wrong passphrase")
	}
}

func TestEncryptLoadRoundTrip_Coverage(t *testing.T) {
	raw := testKeyBytes()
	passphrase := "test-passphrase-123"
	blob, err := Encrypt(raw, passphrase, 10000)
	if err != nil {
		t.Fatal(err)
	}
	kl, err := LoadKey(blob, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	defer kl.Destroy()

	expectedAddr := crypto.PubkeyToAddress(func() ecdsa.PublicKey {
		k, _ := crypto.ToECDSA(raw)
		return k.PublicKey
	}())
	if kl.Address() != expectedAddr {
		t.Errorf("address mismatch: %s vs %s", kl.Address().Hex(), expectedAddr.Hex())
	}
}

func TestSignDigest_NilKey_Coverage(t *testing.T) {
	kl := &KeyLoader{}
	_, err := kl.SignDigest(make([]byte, 32))
	if err == nil {
		t.Error("expected error for nil key")
	}
}

func TestSignDigest_WrongLength_Coverage(t *testing.T) {
	raw := testKeyBytes()
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

func TestSignDigest_CorrectLength_Coverage(t *testing.T) {
	raw := testKeyBytes()
	blob, _ := Encrypt(raw, "pass", 1000)
	kl, err := LoadKey(blob, "pass")
	if err != nil {
		t.Fatal(err)
	}
	defer kl.Destroy()

	sig, err := kl.SignDigest(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != 65 {
		t.Errorf("expected 65-byte signature, got %d", len(sig))
	}
}

func TestDestroy_NilLoader_Coverage(t *testing.T) {
	var kl *KeyLoader
	kl.Destroy()
}

func TestDestroy_CalledTwice_Coverage(t *testing.T) {
	raw := testKeyBytes()
	blob, _ := Encrypt(raw, "pass", 1000)
	kl, _ := LoadKey(blob, "pass")
	kl.Destroy()
	kl.Destroy()
}

func TestParseHexKey_InvalidHex_Coverage(t *testing.T) {
	_, err := ParseHexKey("not-hex")
	if err == nil {
		t.Error("expected error for invalid hex")
	}
}

func TestParseHexKey_WrongLength_Coverage(t *testing.T) {
	_, err := ParseHexKey("aabb")
	if err == nil {
		t.Error("expected error for wrong length")
	}
}

func TestParseHexKey_InvalidKey_Coverage(t *testing.T) {
	_, err := ParseHexKey("0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Error("expected error for zero key")
	}
}

func TestNewServer_NilKeyLoader_Coverage(t *testing.T) {
	_, err := NewServer(nil, "/tmp/test.sock")
	if err == nil {
		t.Error("expected error for nil key loader")
	}
}

func TestNewServer_EmptySocketPath_Coverage(t *testing.T) {
	kl := &KeyLoader{}
	_, err := NewServer(kl, "")
	if err == nil {
		t.Error("expected error for empty socket path")
	}
}

func TestNewServer_Valid_Coverage(t *testing.T) {
	raw := testKeyBytes()
	blob, _ := Encrypt(raw, "pass", 1000)
	kl, err := LoadKey(blob, "pass")
	if err != nil {
		t.Fatal(err)
	}
	defer kl.Destroy()

	srv, err := NewServer(kl, t.TempDir()+"/test.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if srv.Addr() == "" {
		t.Error("expected non-empty addr")
	}
}

func TestNewServer_StaleSocketRemoved_Coverage(t *testing.T) {
	dir := t.TempDir()
	sockPath := dir + "/stale.sock"
	f, _ := os.Create(sockPath)
	f.Write([]byte("stale"))
	f.Close()

	raw := testKeyBytes()
	blob, _ := Encrypt(raw, "pass", 1000)
	kl, _ := LoadKey(blob, "pass")
	defer kl.Destroy()

	_, err := NewServer(kl, sockPath)
	if err == nil {
		t.Error("expected error for non-socket file at path")
	}
}

func TestSignService_Address_Coverage(t *testing.T) {
	raw := testKeyBytes()
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

func TestSignService_SignDigest_Coverage(t *testing.T) {
	raw := testKeyBytes()
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

func TestNewGCM_Coverage(t *testing.T) {
	_, err := newGCM(make([]byte, 32))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewGCM_WrongKeyLength_Coverage(t *testing.T) {
	_, err := newGCM([]byte{0x01})
	if err == nil {
		t.Error("expected error for wrong key length")
	}
}

func TestParseBlob_HeaderTooShort_Coverage(t *testing.T) {
	_, _, _, _, err := parseBlob([]byte("ABC"))
	if err == nil {
		t.Error("expected error for short blob")
	}
}

func TestParseBlob_BadMagic_Coverage(t *testing.T) {
	blob := make([]byte, 50)
	copy(blob, "XXXX")
	_, _, _, _, err := parseBlob(blob)
	if err == nil {
		t.Error("expected error for bad magic")
	}
}

func TestParseBlob_BadVersion_Coverage(t *testing.T) {
	blob := make([]byte, 50)
	copy(blob, "AETK")
	blob[4] = 99
	_, _, _, _, err := parseBlob(blob)
	if err == nil {
		t.Error("expected error for bad version")
	}
}

func TestParseBlob_BadKDF_Coverage(t *testing.T) {
	blob := make([]byte, 50)
	copy(blob, "AETK")
	blob[4] = 1
	blob[5] = 99
	_, _, _, _, err := parseBlob(blob)
	if err == nil {
		t.Error("expected error for bad KDF")
	}
}

func TestParseBlob_NegativeIters_Coverage(t *testing.T) {
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

func TestEncodeDecodeBlob_Coverage(t *testing.T) {
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
	if len(salt2) != 16 {
		t.Errorf("expected 16-byte salt, got %d", len(salt2))
	}
	if len(nonce2) != 12 {
		t.Errorf("expected 12-byte nonce, got %d", len(nonce2))
	}
	if len(ciphertext2) != 32 {
		t.Errorf("expected 32-byte ciphertext, got %d", len(ciphertext2))
	}
}

func TestRemoveStaleSocket_NotExists_Coverage(t *testing.T) {
	err := removeStaleSocket(t.TempDir() + "/nonexistent.sock")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRemoveStaleSocket_IsDir_Coverage(t *testing.T) {
	dir := t.TempDir()
	err := removeStaleSocket(dir)
	if err == nil {
		t.Error("expected error for directory")
	}
}
