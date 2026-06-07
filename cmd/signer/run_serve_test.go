package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aether-arb/aether/internal/signer"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestRunServeContext_ShortRun(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "signer.sock")
	keyPath := filepath.Join(dir, "key.bin")

	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	raw := crypto.FromECDSA(priv)
	blob, err := signer.Encrypt(raw, "serve-test-pass", 1000)
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

	t.Setenv("AETHER_SIGNER_PASSPHRASE", "serve-test-pass")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- runServeContext(ctx, []string{"-config", cfgPath})
	}()

	deadline := time.Now().Add(1500 * time.Millisecond)
	var ready bool
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(sock); statErr == nil {
			client := signer.Dial(sock)
			if pingErr := client.Ping(); pingErr == nil {
				addr, addrErr := client.Address()
				if addrErr == nil && addr != "" {
					ready = true
					break
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("signer socket never became ready")
	}

	err = <-errCh
	if err != nil {
		t.Logf("runServeContext returned: %v", err)
	}
}
