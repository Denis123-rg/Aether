package main

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aether-arb/aether/internal/signer"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestReadPassphrase_ReadError(t *testing.T) {
	os.Unsetenv(envPassphrase)
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	r.Close()
	w.Close()
	defer func() { os.Stdin = oldStdin }()

	_, err := readPassphrase()
	if err == nil {
		t.Fatal("expected error from readPassphrase with closed stdin")
	}
}

func TestRunServeContext_ReadPassphraseError(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key.bin")
	raw, _ := signer.ParseHexKey("ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80")
	blob, err := signer.Encrypt(raw, "pass", 1000)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(keyPath, blob, 0o600)

	cfgPath := filepath.Join(dir, "signer.yaml")
	sockPath := filepath.Join(dir, "s.sock")
	cfgContent := "socket_path: " + sockPath + "\nkey_file: " + keyPath + "\n"
	os.WriteFile(cfgPath, []byte(cfgContent), 0o644)

	os.Unsetenv(envPassphrase)
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	r.Close()
	w.Close()
	defer func() { os.Stdin = oldStdin }()

	err = runServeContext(context.Background(), []string{"-config", cfgPath})
	if err == nil || !strings.Contains(err.Error(), "read passphrase") {
		t.Fatalf("expected read passphrase error, got %v", err)
	}
}

func TestRunEncrypt_ReadPassphraseError(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "encrypted.bin")
	priv, _ := crypto.GenerateKey()
	raw := crypto.FromECDSA(priv)
	hexKey := "0x" + hex.EncodeToString(raw)

	os.Unsetenv(envPassphrase)
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	r.Close()
	w.Close()
	defer func() { os.Stdin = oldStdin }()

	err := runEncrypt([]string{"-key", hexKey, "-out", outPath})
	if err == nil || !strings.Contains(err.Error(), "read passphrase") {
		t.Fatalf("expected read passphrase error, got %v", err)
	}
}

func TestRunServeContext_NewServerFails(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key.bin")
	raw, _ := signer.ParseHexKey("ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80")
	blob, err := signer.Encrypt(raw, "pass", 1000)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(keyPath, blob, 0o600)

	badSocketPath := filepath.Join(dir, "socket.txt")
	os.WriteFile(badSocketPath, []byte("not a socket"), 0o644)

	cfgPath := filepath.Join(dir, "signer.yaml")
	cfgContent := "socket_path: " + badSocketPath + "\nkey_file: " + keyPath + "\n"
	os.WriteFile(cfgPath, []byte(cfgContent), 0o644)

	t.Setenv(envPassphrase, "pass")
	err = runServeContext(context.Background(), []string{"-config", cfgPath})
	if err == nil || !strings.Contains(err.Error(), "start server") {
		t.Fatalf("expected start server error, got %v", err)
	}
}

func TestRunEncrypt_WriteToDirectory(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	raw := crypto.FromECDSA(priv)
	hexKey := "0x" + hex.EncodeToString(raw)
	t.Setenv(envPassphrase, "pw")

	err := runEncrypt([]string{"-key", hexKey, "-out", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "create") {
		t.Fatalf("expected create error, got %v", err)
	}
}

func TestRunServeContext_ServeError(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key.bin")
	raw, _ := signer.ParseHexKey("ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80")
	blob, err := signer.Encrypt(raw, "pass", 1000)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(keyPath, blob, 0o600)

	sockPath := filepath.Join(dir, "signer.sock")
	cfgPath := filepath.Join(dir, "signer.yaml")
	cfgContent := "socket_path: " + sockPath + "\nkey_file: " + keyPath + "\n"
	os.WriteFile(cfgPath, []byte(cfgContent), 0o644)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	t.Setenv(envPassphrase, "pass")
	err = runServeContext(ctx, []string{"-config", cfgPath})
	if err != nil {
		t.Logf("runServeContext returned: %v (acceptable for cancelled context)", err)
	}
}

func TestRunEncrypt_EmptyKeyFromEnv(t *testing.T) {
	os.Unsetenv(envPrivateKey)
	os.Setenv(envPrivateKey, "")
	defer os.Unsetenv(envPrivateKey)

	dir := t.TempDir()
	outPath := filepath.Join(dir, "out.bin")

	t.Setenv(envPassphrase, "pw")
	err := runEncrypt([]string{"-out", outPath})
	if err == nil || !strings.Contains(err.Error(), "no private key") {
		t.Fatalf("expected no private key error, got %v", err)
	}
}
