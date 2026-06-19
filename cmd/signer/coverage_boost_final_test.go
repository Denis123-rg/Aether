package main

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

func TestRunEncrypt_KeyFromFlagAndEnv(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "out.bin")
	priv, _ := crypto.GenerateKey()
	raw := crypto.FromECDSA(priv)
	hexKey := "0x" + hex.EncodeToString(raw)
	t.Setenv(envPassphrase, "flag-pass")

	err := runEncrypt([]string{"-key", hexKey, "-out", outPath, "-iters", "1000"})
	if err != nil {
		t.Fatalf("runEncrypt: %v", err)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("file missing: %v", err)
	}
}

func TestRunEncrypt_InvalidHexFromFlag(t *testing.T) {
	t.Setenv(envPassphrase, "pw")
	err := runEncrypt([]string{"-key", "not-hex-at-all", "-out", t.TempDir() + "/x.bin"})
	if err == nil {
		t.Fatal("expected error for invalid hex")
	}
}

func TestRunEncrypt_WrongKeyLengthFromFlag(t *testing.T) {
	t.Setenv(envPassphrase, "pw")
	shortHex := hex.EncodeToString([]byte{0x01, 0x02})
	err := runEncrypt([]string{"-key", shortHex, "-out", t.TempDir() + "/x.bin"})
	if err == nil {
		t.Fatal("expected error for wrong key length")
	}
}

func TestRunEncrypt_NoKeyNoEnv(t *testing.T) {
	os.Unsetenv(envPrivateKey)
	err := runEncrypt([]string{"-out", t.TempDir() + "/x.bin"})
	if err == nil {
		t.Fatal("expected error for no key")
	}
}

func TestRunEncrypt_FileExists(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "existing.bin")
	os.WriteFile(outPath, []byte("existing"), 0o600)
	priv, _ := crypto.GenerateKey()
	hexKey := "0x" + hex.EncodeToString(crypto.FromECDSA(priv))
	t.Setenv(envPassphrase, "pass")

	err := runEncrypt([]string{"-key", hexKey, "-out", outPath})
	if err == nil {
		t.Fatal("expected error for existing file")
	}
}

func TestRunEncrypt_EmptyPassphraseFromStdin(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	hexKey := "0x" + hex.EncodeToString(crypto.FromECDSA(priv))
	os.Unsetenv(envPassphrase)
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	w.Close()
	defer func() { os.Stdin = oldStdin }()

	err := runEncrypt([]string{"-key", hexKey, "-out", t.TempDir() + "/x.bin"})
	if err == nil {
		t.Fatal("expected error for empty passphrase")
	}
}

func TestRunServe_InvalidFlags(t *testing.T) {
	err := runServe([]string{"-invalid-flag"})
	if err == nil {
		t.Fatal("expected error for invalid flags")
	}
}

func TestRunServeContext_InvalidFlags(t *testing.T) {
	err := runServeContext(context.Background(), []string{"-not-a-flag"})
	if err == nil {
		t.Fatal("expected error for invalid flags")
	}
}

func TestRunEncrypt_DirAsOutput(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	hexKey := "0x" + hex.EncodeToString(crypto.FromECDSA(priv))
	t.Setenv(envPassphrase, "pw")
	err := runEncrypt([]string{"-key", hexKey, "-out", t.TempDir()})
	if err == nil {
		t.Fatal("expected error when -out is directory")
	}
}

func TestZeroBytes_Nil(t *testing.T) {
	zeroBytes(nil)
}

func TestZeroBytes_Empty(t *testing.T) {
	zeroBytes([]byte{})
}

func TestReadPassphrase_EmptyEnv(t *testing.T) {
	os.Unsetenv(envPassphrase)
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	w.Close()
	defer func() { os.Stdin = oldStdin }()

	p, err := readPassphrase()
	if err != nil {
		t.Skipf("stdin read failed: %v", err)
	}
	if p != "" {
		t.Errorf("expected empty, got %q", p)
	}
}

func TestMain_UnknownSubcommandLogic(t *testing.T) {
	err := errUnknownSubcommand("unknown")
	if err == nil {
		t.Fatal("expected error")
	}
}
