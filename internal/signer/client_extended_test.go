package signer

import (
	"testing"
)

func TestClient_PingFailsWithoutServer(t *testing.T) {
	sock := shortSocketPath(t)
	c := Dial(sock)
	if err := c.Ping(); err == nil {
		t.Fatal("expected ping error when server absent")
	}
}

func TestClient_SignFlashbotsPayloadEmptyBody(t *testing.T) {
	sock, stop := startClientTestSigner(t)
	defer stop()
	c := Dial(sock)

	got, err := c.SignFlashbotsPayload(nil)
	if err != nil {
		t.Fatalf("SignFlashbotsPayload(nil): %v", err)
	}
	if got == "" {
		t.Fatal("expected non-empty signature for nil payload")
	}
}

func TestLoadKey_CorruptedCiphertext(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pass", 1000)
	if err != nil {
		t.Fatal(err)
	}
	blob[len(blob)-1] ^= 0xff
	if _, err := LoadKey(blob, "pass"); err == nil {
		t.Fatal("corrupted ciphertext must fail decryption")
	}
}

func TestLoadKey_TruncatedBlob(t *testing.T) {
	if _, err := LoadKey([]byte{1, 2, 3}, "pass"); err == nil {
		t.Fatal("truncated blob must fail")
	}
}

func TestEncrypt_WrongPassphraseRoundTripFails(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "alpha", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKey(blob, "beta"); err == nil {
		t.Fatal("wrong passphrase must fail")
	}
}
