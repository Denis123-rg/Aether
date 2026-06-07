package signer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

func TestEncrypt_InvalidScalar(t *testing.T) {
	zeros := make([]byte, privLen)
	if _, err := Encrypt(zeros, "pw", 1000); err == nil {
		t.Fatal("expected invalid scalar error")
	}
}

func TestLoadKey_TruncatedBlobHeader(t *testing.T) {
	if _, err := LoadKey([]byte("AETK\x01\x01\x00\x00\x00\x00"), "pw"); err == nil {
		t.Fatal("expected truncated blob error")
	}
}

func TestNewServer_RefusesRegularFileAtSocketPath(t *testing.T) {
	kl, _ := loadedTestKey(t)
	dir, err := os.MkdirTemp("", "aeth")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	regular := filepath.Join(dir, "not-a-socket")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewServer(kl, regular); err == nil {
		t.Fatal("expected error when socket path is a regular file")
	}
}

func TestParseHexKey_TrimsWhitespace(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	raw := crypto.FromECDSA(priv)
	hexKey := "  0x" + bytesToHex(raw) + "  "
	got, err := ParseHexKey(hexKey)
	if err != nil {
		t.Fatalf("ParseHexKey: %v", err)
	}
	if len(got) != privLen {
		t.Fatalf("len = %d", len(got))
	}
}

func TestSignService_SignDigestSuccess(t *testing.T) {
	kl, addrHex := loadedTestKey(t)
	svc := &SignService{kl: kl}
	digest := crypto.Keccak256([]byte("digest"))
	var reply SignDigestReply
	if err := svc.SignDigest(&SignDigestArgs{Digest: digest}, &reply); err != nil {
		t.Fatalf("SignDigest: %v", err)
	}
	if len(reply.Signature) != 65 {
		t.Fatalf("sig len = %d", len(reply.Signature))
	}
	var addrReply AddressReply
	if err := svc.Address(&AddressArgs{}, &addrReply); err != nil {
		t.Fatal(err)
	}
	if addrReply.Address != addrHex {
		t.Fatalf("address = %s", addrReply.Address)
	}
}

func TestLoadKey_WrongPassphrase(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "correct", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKey(blob, "wrong"); err == nil {
		t.Fatal("expected decryption failure")
	}
}

func TestLoadKeyFile_ReadError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unreadable")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Skip("cannot chmod for read-deny test")
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	_, err := LoadKeyFile(path, "pw")
	if err == nil {
		t.Fatal("expected read error")
	}
}
