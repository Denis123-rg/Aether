package signer

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// newTestKeyBytes returns a fresh, valid 32-byte secp256k1 scalar and its
// derived address hex for assertions.
func newTestKeyBytes(t *testing.T) (raw []byte, addrHex string) {
	t.Helper()
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return crypto.FromECDSA(priv), crypto.PubkeyToAddress(priv.PublicKey).Hex()
}

func TestEncryptLoadRoundTrip(t *testing.T) {
	raw, addrHex := newTestKeyBytes(t)
	const pass = "correct horse battery staple"

	// Low iteration count keeps the test fast; production uses the default.
	blob, err := Encrypt(raw, pass, 1000)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if bytes.Contains(blob, raw) {
		t.Fatal("ciphertext must not contain the plaintext key bytes")
	}

	kl, err := LoadKey(blob, pass)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	defer kl.Destroy()

	if kl.Address().Hex() != addrHex {
		t.Fatalf("address mismatch: got %s want %s", kl.Address().Hex(), addrHex)
	}
}

func TestLoadWrongPassphraseFails(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "right-pass", 1000)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := LoadKey(blob, "wrong-pass"); err == nil {
		t.Fatal("expected decryption failure with wrong passphrase")
	}
}

func TestEmptyPassphraseRejected(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	if _, err := Encrypt(raw, "", 1000); err != ErrEmptyPassphrase {
		t.Fatalf("Encrypt empty passphrase: got %v want ErrEmptyPassphrase", err)
	}
	if _, err := LoadKey([]byte("whatever-blob-bytes-padding-padding"), ""); err != ErrEmptyPassphrase {
		t.Fatalf("LoadKey empty passphrase: got %v want ErrEmptyPassphrase", err)
	}
}

func TestSignDigestRecoversToAddress(t *testing.T) {
	raw, addrHex := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pw", 1000)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	kl, err := LoadKey(blob, "pw")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	defer kl.Destroy()

	digest := crypto.Keccak256([]byte("aether bundle digest"))
	sig, err := kl.SignDigest(digest)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if len(sig) != 65 {
		t.Fatalf("signature length = %d, want 65", len(sig))
	}
	pub, err := crypto.SigToPub(digest, sig)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if got := crypto.PubkeyToAddress(*pub).Hex(); got != addrHex {
		t.Fatalf("recovered address %s != signer address %s", got, addrHex)
	}
}

func TestSignDigestRejectsWrongLength(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, _ := Encrypt(raw, "pw", 1000)
	kl, _ := LoadKey(blob, "pw")
	defer kl.Destroy()

	if _, err := kl.SignDigest([]byte("too short")); err == nil {
		t.Fatal("expected error for non-32-byte digest")
	}
}

func TestDestroyDisablesSigning(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, _ := Encrypt(raw, "pw", 1000)
	kl, _ := LoadKey(blob, "pw")

	kl.Destroy()
	kl.Destroy() // idempotent

	digest := crypto.Keccak256([]byte("x"))
	if _, err := kl.SignDigest(digest); err == nil {
		t.Fatal("expected signing to fail after Destroy")
	}
}

func TestParseHexKey(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	hexKey := "0x" + bytesToHex(crypto.FromECDSA(priv))

	raw, err := ParseHexKey(hexKey)
	if err != nil {
		t.Fatalf("parse 0x-prefixed: %v", err)
	}
	if !bytes.Equal(raw, crypto.FromECDSA(priv)) {
		t.Fatal("parsed bytes differ from source key")
	}

	if _, err := ParseHexKey("0xnothex"); err == nil {
		t.Fatal("expected error for non-hex input")
	}
	if _, err := ParseHexKey("0x1234"); err == nil {
		t.Fatal("expected error for wrong-length key")
	}
}

func TestParseBlobRejectsGarbage(t *testing.T) {
	if _, _, _, _, err := parseBlob([]byte("short")); err == nil {
		t.Fatal("expected error for short blob")
	}
	bad := make([]byte, 64)
	copy(bad, "XXXX")
	if _, _, _, _, err := parseBlob(bad); err == nil {
		t.Fatal("expected error for bad magic")
	}
}

func bytesToHex(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexdigits[v>>4]
		out[i*2+1] = hexdigits[v&0x0f]
	}
	return string(out)
}
