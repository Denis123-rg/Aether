package signer

import (
	"crypto/aes"
	"crypto/cipher"
	"testing"
)

func TestEncrypt_NegativeIters(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pass", -1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(blob) == 0 {
		t.Fatal("expected non-empty blob")
	}
	kl, err := LoadKey(blob, "pass")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	defer kl.Destroy()
}

func TestEncrypt_ZeroIters(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pass", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(blob) == 0 {
		t.Fatal("expected non-empty blob")
	}
}

func TestNewGCM_Key16(t *testing.T) {
	key := make([]byte, 16)
	gcm, err := newGCM(key)
	if err != nil {
		t.Fatalf("unexpected error for 16-byte key: %v", err)
	}
	if gcm == nil {
		t.Fatal("expected non-nil GCM")
	}
}

func TestNewGCM_Key24(t *testing.T) {
	key := make([]byte, 24)
	gcm, err := newGCM(key)
	if err != nil {
		t.Fatalf("unexpected error for 24-byte key: %v", err)
	}
	if gcm == nil {
		t.Fatal("expected non-nil GCM")
	}
}

func TestNewGCM_Key32(t *testing.T) {
	key := make([]byte, 32)
	gcm, err := newGCM(key)
	if err != nil {
		t.Fatalf("unexpected error for 32-byte key: %v", err)
	}
	if gcm == nil {
		t.Fatal("expected non-nil GCM")
	}
}

func TestNewGCM_WrongKeySizes(t *testing.T) {
	sizes := []int{0, 1, 8, 15, 17, 31, 33, 64}
	for _, sz := range sizes {
		_, err := newGCM(make([]byte, sz))
		if err == nil {
			t.Errorf("expected error for %d-byte key", sz)
		}
	}
}

func TestNewServer_InvalidSocketDir(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, _ := Encrypt(raw, "pass", 1000)
	kl, _ := LoadKey(blob, "pass")
	defer kl.Destroy()

	_, err := NewServer(kl, "/proc/invalid/dir/test.sock")
	if err == nil {
		t.Error("expected error for invalid socket dir")
	}
}

func TestNewGCM_VerifyAEADInterface(t *testing.T) {
	key := make([]byte, 32)
	gcm, err := newGCM(key)
	if err != nil {
		t.Fatal(err)
	}

	nonce := make([]byte, gcm.NonceSize())
	plaintext := []byte("test data")
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)
	if len(ciphertext) == 0 {
		t.Fatal("expected non-empty ciphertext")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm2, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	decrypted, err := gcm2.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(decrypted) != "test data" {
		t.Fatalf("expected 'test data', got %q", decrypted)
	}
}

func TestEncrypt_KeyLen24(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pass24", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	kl, err := LoadKey(blob, "pass24")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	defer kl.Destroy()
	if kl.Address().Hex() == "" {
		t.Fatal("expected non-empty address")
	}
}

func TestEncrypt_RoundTripDifferentIters(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	for _, iters := range []int{1, 100, 1000, 5000} {
		blob, err := Encrypt(raw, "pass", iters)
		if err != nil {
			t.Fatalf("encrypt with iters=%d: %v", iters, err)
		}
		kl, err := LoadKey(blob, "pass")
		if err != nil {
			t.Fatalf("load with iters=%d: %v", iters, err)
		}
		kl.Destroy()
	}
}

func TestParseHexKey_Valid(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	hexKey := "0x" + bytesToHex(raw)
	parsed, err := ParseHexKey(hexKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parsed) != 32 {
		t.Fatalf("expected 32 bytes, got %d", len(parsed))
	}
}

func TestParseHexKey_NoPrefix(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	hexKey := bytesToHex(raw)
	parsed, err := ParseHexKey(hexKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parsed) != 32 {
		t.Fatalf("expected 32 bytes, got %d", len(parsed))
	}
}

func TestParseHexKey_TrimmedWhitespace(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	hexKey := "  0x" + bytesToHex(raw) + "  "
	parsed, err := ParseHexKey(hexKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parsed) != 32 {
		t.Fatalf("expected 32 bytes, got %d", len(parsed))
	}
}
