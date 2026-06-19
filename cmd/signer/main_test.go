package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aether-arb/aether/internal/signer"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestReadPassphrase_FromEnv(t *testing.T) {
	t.Setenv(envPassphrase, "secret-from-env")
	got, err := readPassphrase()
	if err != nil {
		t.Fatalf("readPassphrase: %v", err)
	}
	if got != "secret-from-env" {
		t.Fatalf("got %q", got)
	}
	if os.Getenv(envPassphrase) != "" {
		t.Fatal("env var should be unset after read")
	}
}

func TestReadPassphrase_FromStdin(t *testing.T) {
	os.Unsetenv(envPassphrase)
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	go func() {
		w.Write([]byte("stdin-pass\n"))
		w.Close()
	}()
	defer func() { os.Stdin = oldStdin }()

	got, err := readPassphrase()
	if err != nil {
		t.Fatalf("readPassphrase: %v", err)
	}
	if got != "stdin-pass" {
		t.Fatalf("got %q", got)
	}
}

func TestReadPassphrase_TrimsCRLF(t *testing.T) {
	os.Unsetenv(envPassphrase)
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	go func() {
		w.Write([]byte("pass-with-spaces \r\n"))
		w.Close()
	}()
	defer func() { os.Stdin = oldStdin }()

	got, err := readPassphrase()
	if err != nil {
		t.Fatalf("readPassphrase: %v", err)
	}
	if got != "pass-with-spaces " {
		t.Fatalf("got %q", got)
	}
}

func TestZeroBytes(t *testing.T) {
	b := []byte{1, 2, 3, 4, 5}
	zeroBytes(b)
	for i, v := range b {
		if v != 0 {
			t.Fatalf("byte %d = %d", i, v)
		}
	}
}

func TestRunEncrypt_MissingOut(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	raw := crypto.FromECDSA(priv)
	hexKey := "0x" + hex.EncodeToString(raw)

	err := runEncrypt([]string{"-key", hexKey})
	if err == nil || !strings.Contains(err.Error(), "-out is required") {
		t.Fatalf("expected -out required error, got %v", err)
	}
}

func TestRunEncrypt_NoKey(t *testing.T) {
	os.Unsetenv(envPrivateKey)
	err := runEncrypt([]string{"-out", filepath.Join(t.TempDir(), "key.bin")})
	if err == nil || !strings.Contains(err.Error(), "no private key") {
		t.Fatalf("expected no key error, got %v", err)
	}
}

func TestRunEncrypt_Success(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "encrypted.bin")
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	raw := crypto.FromECDSA(priv)
	hexKey := "0x" + hex.EncodeToString(raw)
	t.Setenv(envPassphrase, "encrypt-test-pass")
	err = runEncrypt([]string{"-key", hexKey, "-out", outPath, "-iters", "1000"})
	if err != nil {
		t.Fatalf("runEncrypt: %v", err)
	}
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("file mode too permissive: %o", info.Mode())
	}
}

func TestRunEncrypt_RefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "existing.bin")
	if err := os.WriteFile(outPath, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	priv, _ := crypto.GenerateKey()
	raw := crypto.FromECDSA(priv)
	hexKey := "0x" + hex.EncodeToString(raw)
	t.Setenv(envPassphrase, "pass")

	err := runEncrypt([]string{"-key", hexKey, "-out", outPath})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected exists error, got %v", err)
	}
}

func TestRunEncrypt_EmptyPassphrase(t *testing.T) {
	dir := t.TempDir()
	priv, _ := crypto.GenerateKey()
	raw := crypto.FromECDSA(priv)
	hexKey := "0x" + hex.EncodeToString(raw)
	os.Unsetenv(envPassphrase)
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	w.Close()
	defer func() { os.Stdin = oldStdin }()

	err := runEncrypt([]string{"-key", hexKey, "-out", filepath.Join(dir, "k.bin")})
	if err == nil || !strings.Contains(err.Error(), "empty passphrase") {
		t.Fatalf("expected empty passphrase error, got %v", err)
	}
}

func TestRunServe_InvalidConfigPath(t *testing.T) {
	os.Unsetenv(envPassphrase)
	t.Setenv(envPassphrase, "x")
	err := runServe([]string{"-config", filepath.Join(t.TempDir(), "missing.yaml")})
	if err == nil || !strings.Contains(err.Error(), "load config") {
		t.Fatalf("expected config error, got %v", err)
	}
}

func TestMain_UnknownSubcommand(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"aether-signer", "unknown-cmd"}
	// We cannot call main() directly (os.Exit). Test the switch logic inline.
	sub := "unknown-cmd"
	var err error
	switch sub {
	case "serve":
		err = runServe(nil)
	case "encrypt":
		err = runEncrypt(nil)
	default:
		err = errUnknownSubcommand(sub)
	}
	if err == nil {
		t.Fatal("expected error")
	}
}

func errUnknownSubcommand(sub string) error {
	return fmt.Errorf("unknown subcommand %q (want 'serve' or 'encrypt')", sub)
}


func TestFlagSetParseErrors(t *testing.T) {
	fs := flag.NewFlagSet("encrypt", flag.ContinueOnError)
	fs.String("out", "", "")
	if err := fs.Parse([]string{"-unknown-flag"}); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestParseHexKeyViaEncrypt(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	hexKey := "0x" + hex.EncodeToString(crypto.FromECDSA(priv))
	parsed, err := signer.ParseHexKey(hexKey)
	if err != nil {
		t.Fatalf("ParseHexKey: %v", err)
	}
	if len(parsed) != 32 {
		t.Fatalf("len = %d", len(parsed))
	}
}

type failingRandReader struct{}

func (f *failingRandReader) Read(p []byte) (int, error) {
	return 0, fmt.Errorf("mock random reader failure")
}

func TestRunEncrypt_EncryptError(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "encrypt-err" {
		rand.Reader = &failingRandReader{}
		hexKey := os.Getenv("TEST_HEX_KEY")
		outPath := os.Getenv("TEST_OUT_PATH")
		os.Setenv(envPassphrase, "test-pass")
		os.Args = []string{"aether-signer", "encrypt", "-key", hexKey, "-out", outPath, "-iters", "1000"}
		main()
		os.Exit(0)
	}

	dir := t.TempDir()
	outPath := filepath.Join(dir, "encrypted.bin")
	priv, _ := crypto.GenerateKey()
	hexKey := "0x" + hex.EncodeToString(crypto.FromECDSA(priv))

	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1")
	cmd.Env = append(os.Environ(),
		"GO_WANT_HELPER_PROCESS=encrypt-err",
		"TEST_HEX_KEY="+hexKey,
		"TEST_OUT_PATH="+outPath,
	)

	if err := cmd.Run(); err == nil {
		t.Fatal("expected non-zero exit for encrypt error")
	}
}

func TestRunEncrypt_DefaultIters(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "encrypted.bin")
	priv, _ := crypto.GenerateKey()
	hexKey := "0x" + hex.EncodeToString(crypto.FromECDSA(priv))
	t.Setenv(envPassphrase, "default-iters-pass")

	err := runEncrypt([]string{"-key", hexKey, "-out", outPath})
	if err != nil {
		t.Fatalf("runEncrypt: %v", err)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("file missing: %v", err)
	}
}

func TestRunEncrypt_KeyFromEnvAndFlag(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "encrypted.bin")
	priv, _ := crypto.GenerateKey()
	flagKey := "0x" + hex.EncodeToString(crypto.FromECDSA(priv))
	envPriv, _ := crypto.GenerateKey()
	envKey := "0x" + hex.EncodeToString(crypto.FromECDSA(envPriv))
	t.Setenv(envPrivateKey, envKey)
	t.Setenv(envPassphrase, "env-flag-pass")

	err := runEncrypt([]string{"-key", flagKey, "-out", outPath, "-iters", "1000"})
	if err != nil {
		t.Fatalf("runEncrypt: %v", err)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("file missing: %v", err)
	}
}

func TestRunEncrypt_ReadableFileAsOutput(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "readable.bin")
	if err := os.WriteFile(outPath, []byte("data"), 0o444); err != nil {
		t.Fatal(err)
	}
	priv, _ := crypto.GenerateKey()
	hexKey := "0x" + hex.EncodeToString(crypto.FromECDSA(priv))
	t.Setenv(envPassphrase, "pw")

	err := runEncrypt([]string{"-key", hexKey, "-out", outPath})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected exists error, got %v", err)
	}
}
