package main

import (
	"context"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aether-arb/aether/internal/signer"
	"github.com/ethereum/go-ethereum/crypto"
)

func signerHelperProcess(t *testing.T, extraEnv ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1")
	cmd.Env = append(os.Environ(), extraEnv...)
	return cmd
}

func TestMain_EncryptSuccess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "encrypt" {
		t.Setenv(envPassphrase, "main-encrypt-pass")
		os.Args = []string{
			"aether-signer", "encrypt",
			"-key", os.Getenv("TEST_HEX_KEY"),
			"-out", os.Getenv("TEST_OUT_PATH"),
			"-iters", "1000",
		}
		main()
		os.Exit(0)
	}

	dir := t.TempDir()
	outPath := filepath.Join(dir, "encrypted.bin")
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	hexKey := "0x" + hex.EncodeToString(crypto.FromECDSA(priv))

	cmd := signerHelperProcess(t,
		"GO_WANT_HELPER_PROCESS=encrypt",
		"TEST_HEX_KEY="+hexKey,
		"TEST_OUT_PATH="+outPath,
	)
	if err := cmd.Run(); err != nil {
		t.Fatalf("encrypt subprocess: %v", err)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("encrypted file missing: %v", err)
	}
}

func TestMain_UnknownSubcommandExits(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "unknown" {
		os.Args = []string{"aether-signer", "not-a-command"}
		main()
		os.Exit(0)
	}

	cmd := signerHelperProcess(t, "GO_WANT_HELPER_PROCESS=unknown")
	if err := cmd.Run(); err == nil {
		t.Fatal("expected non-zero exit for unknown subcommand")
	}
}

func TestMain_ServeFailsOnMissingConfig(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "serve-fail" {
		t.Setenv(envPassphrase, "x")
		os.Args = []string{"aether-signer", "serve", "-config", filepath.Join(t.TempDir(), "missing.yaml")}
		main()
		os.Exit(0)
	}

	cmd := signerHelperProcess(t, "GO_WANT_HELPER_PROCESS=serve-fail")
	if err := cmd.Run(); err == nil {
		t.Fatal("expected non-zero exit when config is missing")
	}
}

func TestMain_DefaultSubcommandIsServe(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "default-serve" {
		t.Setenv(envPassphrase, "x")
		// No subcommand → main() defaults to "serve".
		os.Args = []string{"aether-signer", "-config", filepath.Join(t.TempDir(), "missing.yaml")}
		main()
		os.Exit(0)
	}

	cmd := signerHelperProcess(t, "GO_WANT_HELPER_PROCESS=default-serve")
	if err := cmd.Run(); err == nil {
		t.Fatal("expected non-zero exit for default serve with missing config")
	}
}

func TestRunServeContext_InvalidFlag(t *testing.T) {
	err := runServeContext(context.Background(), []string{"-not-a-real-flag"})
	if err == nil {
		t.Fatal("expected flag parse error")
	}
}

func TestRunEncrypt_InvalidHexKey(t *testing.T) {
	t.Setenv(envPassphrase, "pw")
	outPath := filepath.Join(t.TempDir(), "out.bin")
	err := runEncrypt([]string{"-key", "0xZZ", "-out", outPath})
	if err == nil {
		t.Fatal("expected hex parse error")
	}
}

func TestRunEncrypt_InvalidFlag(t *testing.T) {
	err := runEncrypt([]string{"-not-a-flag"})
	if err == nil {
		t.Fatal("expected flag parse error")
	}
}

func TestRunEncrypt_OutPathIsDirectory(t *testing.T) {
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	hexKey := "0x" + hex.EncodeToString(crypto.FromECDSA(priv))
	t.Setenv(envPassphrase, "pw")
	err = runEncrypt([]string{"-key", hexKey, "-out", t.TempDir()})
	if err == nil {
		t.Fatal("expected error when -out is a directory")
	}
}

func TestRunEncrypt_KeyFromEnv(t *testing.T) {
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	hexKey := "0x" + hex.EncodeToString(crypto.FromECDSA(priv))
	outPath := filepath.Join(t.TempDir(), "from-env.bin")
	t.Setenv(envPrivateKey, hexKey)
	t.Setenv(envPassphrase, "env-key-pass")

	if err := runEncrypt([]string{"-out", outPath, "-iters", "1000"}); err != nil {
		t.Fatalf("runEncrypt: %v", err)
	}
	if os.Getenv(envPrivateKey) != "" {
		t.Fatal("env private key should be unset after read")
	}
}

func TestRunServeContext_EmptyPassphrase(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "signer.yaml")
	cfg := "socket_path: " + filepath.Join(dir, "s.sock") + "\nkey_file: " + filepath.Join(dir, "k.bin") + "\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	os.Unsetenv(envPassphrase)
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	w.Close()
	defer func() { os.Stdin = oldStdin }()

	err := runServeContext(context.Background(), []string{"-config", cfgPath})
	if err == nil || !strings.Contains(err.Error(), "empty passphrase") {
		t.Fatalf("expected empty passphrase error, got %v", err)
	}
}

func TestRunServeContext_WrongPassphrase(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key.bin")
	raw, _ := signer.ParseHexKey("ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80")
	blob, err := signer.Encrypt(raw, "right-pass", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "signer.yaml")
	if err := os.WriteFile(cfgPath, []byte("socket_path: "+filepath.Join(dir, "s.sock")+"\nkey_file: "+keyPath+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envPassphrase, "wrong-pass")
	err = runServeContext(context.Background(), []string{"-config", cfgPath})
	if err == nil || !strings.Contains(err.Error(), "load key") {
		t.Fatalf("expected load key error, got %v", err)
	}
}

func TestRunServe_SignalShutdown(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "signal" {
		t.Setenv(envPassphrase, os.Getenv("TEST_SIGNER_PASS"))
		if err := runServe([]string{"-config", os.Getenv("TEST_SIGNER_CONFIG")}); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}

	dir := t.TempDir()
	sock := filepath.Join(dir, "signer.sock")
	keyPath := filepath.Join(dir, "key.bin")

	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	raw := crypto.FromECDSA(priv)
	blob, err := signer.Encrypt(raw, "signal-pass", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, blob, 0o600); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(dir, "signer.yaml")
	cfgContent := "socket_path: " + sock + "\nkey_file: " + keyPath + "\n"
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := signerHelperProcess(t,
		"GO_WANT_HELPER_PROCESS=signal",
		"TEST_SIGNER_PASS=signal-pass",
		"TEST_SIGNER_CONFIG="+cfgPath,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(sock); statErr == nil {
			client := signer.Dial(sock)
			if pingErr := client.Ping(); pingErr == nil {
				ready = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		_ = cmd.Process.Kill()
		t.Fatal("signer socket never became ready")
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("signal: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("runServe did not exit after SIGTERM")
	}
}
