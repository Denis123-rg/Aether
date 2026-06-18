package main

import (
	"os"
	"testing"
)

func TestRunEncrypt_NoKeyNoEnv_Coverage(t *testing.T) {
	os.Unsetenv(envPrivateKey)
	err := runEncrypt([]string{"-out", "/tmp/test-encrypt.bin"})
	if err == nil {
		t.Error("expected error for no key")
	}
}

func TestRunEncrypt_NoOutput_Coverage(t *testing.T) {
	err := runEncrypt([]string{})
	if err == nil {
		t.Error("expected error for no -out flag")
	}
}

func TestRunEncrypt_InvalidKey_Coverage(t *testing.T) {
	err := runEncrypt([]string{"-key", "not-hex", "-out", "/tmp/test-invalid.bin"})
	if err == nil {
		t.Error("expected error for invalid hex key")
	}
}

func TestRunEncrypt_EmptyPassphrase_Coverage(t *testing.T) {
	os.Setenv(envPassphrase, "")
	os.Unsetenv(envPassphrase)
	err := runEncrypt([]string{"-key", "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80", "-out", t.TempDir() + "/key.bin"})
	if err == nil {
		t.Error("expected error for empty passphrase")
	}
}

func TestZeroBytes_Coverage(t *testing.T) {
	b := []byte{1, 2, 3, 4, 5}
	zeroBytes(b)
	for i, v := range b {
		if v != 0 {
			t.Errorf("byte %d not zeroed: %d", i, v)
		}
	}
}

func TestZeroBytes_Empty_Coverage(t *testing.T) {
	zeroBytes([]byte{})
	zeroBytes(nil)
}

func TestReadPassphrase_Env_Coverage(t *testing.T) {
	os.Setenv(envPassphrase, "test-passphrase")
	p, err := readPassphrase()
	if err != nil {
		t.Fatal(err)
	}
	if p != "test-passphrase" {
		t.Errorf("expected test-passphrase, got %q", p)
	}
	// Should be unset after reading
	if os.Getenv(envPassphrase) != "" {
		t.Error("expected env var to be unset")
	}
}

func TestReadPassphrase_EmptyEnv_Coverage(t *testing.T) {
	os.Unsetenv(envPassphrase)
	// This will try to read stdin, which is empty in tests
	// The function reads to EOF, which returns empty
	p, err := readPassphrase()
	if err != nil {
		t.Skipf("stdin read failed (expected in test): %v", err)
	}
	if p != "" {
		t.Errorf("expected empty, got %q", p)
	}
}

func TestRunServe_UnknownSubcommand_Coverage(t *testing.T) {
	err := runServe([]string{"-flag"})
	if err == nil {
		t.Error("expected error for invalid flags")
	}
}

func TestRunServe_InvalidFlags_Coverage(t *testing.T) {
	err := runServeContext(t.Context(), []string{"-invalid-flag"})
	if err == nil {
		t.Error("expected error for invalid flags")
	}
}
