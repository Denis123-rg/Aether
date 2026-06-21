package signer

import (
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

func TestEncrypt_EmptyPassphrase(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	_, err := Encrypt(raw, "", 1000)
	if !errors.Is(err, ErrEmptyPassphrase) {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadKey_EmptyPassphrase(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pw", 1000)
	if err != nil {
		t.Fatal(err)
	}
	_, err = LoadKey(blob, "")
	if !errors.Is(err, ErrEmptyPassphrase) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseBlob_MoreErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		blob []byte
	}{
		{name: "too short", blob: []byte("AETK")},
		{name: "bad magic", blob: append([]byte("XXXX"), make([]byte, 32)...)},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, _, _, err := parseBlob(tc.blob)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestSignDigest_WrongLength(t *testing.T) {
	kl, _ := loadedTestKey(t)
	if _, err := kl.SignDigest([]byte{1, 2, 3}); err == nil {
		t.Fatal("expected digest length error")
	}
}

func TestClient_Ping(t *testing.T) {
	kl, _ := loadedTestKey(t)
	client, stop := startTestServer(t, kl)
	defer stop()
	if err := client.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestSignService_SignDigestError(t *testing.T) {
	kl, _ := loadedTestKey(t)
	svc := &SignService{kl: kl}
	var reply SignDigestReply
	if err := svc.SignDigest(&SignDigestArgs{Digest: []byte{1}}, &reply); err == nil {
		t.Fatal("expected digest length error")
	}
}

func TestMemoryGuard_EmptySliceNoOp(t *testing.T) {
	if err := mlock(nil); err != nil {
		t.Fatalf("mlock nil: %v", err)
	}
	if err := munlock([]byte{}); err != nil {
		t.Fatalf("munlock empty: %v", err)
	}
}

func TestLoadKey_DecryptFailure(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pw", 1000)
	if err != nil {
		t.Fatal(err)
	}
	_, err = LoadKey(blob, "wrong-pass")
	if err == nil {
		t.Fatal("expected decryption failure")
	}
}

func TestParseHexKey_InvalidHex(t *testing.T) {
	if _, err := ParseHexKey("0xZZ"); err == nil {
		t.Fatal("expected hex decode error")
	}
}

func TestParseHexKey_WrongLength(t *testing.T) {
	short := make([]byte, 16)
	if _, err := ParseHexKey("0x" + bytesToHex(short)); err == nil {
		t.Fatal("expected length error")
	}
}

func TestEncrypt_DefaultIterations(t *testing.T) {
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	raw := crypto.FromECDSA(priv)
	blob, err := Encrypt(raw, "pw", 0)
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
