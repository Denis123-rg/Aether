package signer

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
)

func TestEncrypt_Table(t *testing.T) {
	t.Parallel()
	raw, _ := newTestKeyBytes(t)

	tests := []struct {
		name    string
		key     []byte
		pass    string
		iters   int
		wantErr bool
	}{
		{name: "default iters", key: raw, pass: "pw", iters: 0},
		{name: "wrong key length", key: []byte{1, 2, 3}, pass: "pw", iters: 100, wantErr: true},
		{name: "invalid scalar", key: make([]byte, privLen), pass: "pw", iters: 100, wantErr: true},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Encrypt(tc.key, tc.pass, tc.iters)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Encrypt: %v", err)
			}
		})
	}
}

func TestParseBlob_Table(t *testing.T) {
	t.Parallel()
	raw, _ := newTestKeyBytes(t)
	valid, err := Encrypt(raw, "pw", 1000)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		blob    []byte
		wantErr bool
	}{
		{name: "unsupported version", blob: mutateBlob(valid, func(b []byte) { b[4] = 99 }), wantErr: true},
		{name: "unsupported kdf", blob: mutateBlob(valid, func(b []byte) { b[5] = 99 }), wantErr: true},
		{name: "zero iters", blob: mutateBlob(valid, func(b []byte) {
			binary.BigEndian.PutUint32(b[6:10], 0)
		}), wantErr: true},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, _, _, err := parseBlob(tc.blob)
			if tc.wantErr && err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func mutateBlob(src []byte, fn func([]byte)) []byte {
	out := bytes.Clone(src)
	fn(out)
	return out
}

func TestParseHexKey_InvalidScalar(t *testing.T) {
	zeros := make([]byte, privLen)
	hexKey := "0x" + bytesToHex(zeros)
	if _, err := ParseHexKey(hexKey); err == nil {
		t.Fatal("expected error for invalid scalar")
	}
}

func TestSignDigest_NilLoader(t *testing.T) {
	var kl *KeyLoader
	if _, err := kl.SignDigest(make([]byte, 32)); err == nil {
		t.Fatal("expected error for nil loader")
	}
}

func TestLoadKey_WrongLengthPlaintext(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	blob, err := Encrypt(raw, "pw", 1000)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt ciphertext so decrypted length != 32.
	blob[len(blob)-1] ^= 0xff
	if _, err := LoadKey(blob, "pw"); err == nil {
		t.Fatal("expected error for corrupt ciphertext")
	}
}

func TestLoadKeyFile_RoundTrip(t *testing.T) {
	raw, addrHex := newTestKeyBytes(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "key.bin")
	blob, err := Encrypt(raw, "pw", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	kl, err := LoadKeyFile(path, "pw")
	if err != nil {
		t.Fatalf("LoadKeyFile: %v", err)
	}
	defer kl.Destroy()
	if kl.Address().Hex() != addrHex {
		t.Fatalf("address = %s", kl.Address().Hex())
	}
}

func TestServer_RemoveStaleSocketAndServe(t *testing.T) {
	raw, _ := newTestKeyBytes(t)
	kl, _ := LoadKey(mustEncrypt(t, raw, "pw"), "pw")
	defer kl.Destroy()
	sock := shortSocketPath(t)
	srv, err := NewServer(kl, sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func mustEncrypt(t *testing.T, raw []byte, pass string) []byte {
	t.Helper()
	b, err := Encrypt(raw, pass, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRemoveStaleSocket_RemovesUnixSocket(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	if err := removeStaleSocket(sock); err != nil {
		t.Fatalf("removeStaleSocket: %v", err)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatal("socket should be removed")
	}
}

func TestClient_AddressAndSignDigest(t *testing.T) {
	sock, stop := startClientTestSigner(t)
	defer stop()
	c := Dial(sock)
	addr, err := c.Address()
	if err != nil {
		t.Fatalf("Address: %v", err)
	}
	if addr == "" {
		t.Fatal("empty address")
	}
	digest := crypto.Keccak256([]byte("probe"))
	sig, err := c.SignDigest(digest)
	if err != nil || len(sig) != 65 {
		t.Fatalf("SignDigest: err=%v len=%d", err, len(sig))
	}
}

func TestParseHexKey_ValidWithoutPrefix(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	raw := crypto.FromECDSA(priv)
	hexNoPrefix := bytesToHex(raw)
	got, err := ParseHexKey(hexNoPrefix)
	if err != nil {
		t.Fatalf("ParseHexKey: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatal("parsed bytes mismatch")
	}
}
