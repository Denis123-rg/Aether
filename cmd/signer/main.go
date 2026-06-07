// Command aether-signer is the local in-memory bundle signer.
//
// It runs as its own process so the searcher private key never lives inside the
// executor's address space. The key is stored encrypted at rest; at startup the
// signer is handed a passphrase (env var or stdin pipe, e.g. from
// systemd-ask-password), decrypts the key into mlock'd memory, and exposes
// signing over a 0600 unix-domain socket. On SIGTERM it zeroes the key.
//
// Usage:
//
//	aether-signer serve                 # default; loads config/signer.yaml
//	aether-signer encrypt -key 0x.. -out encrypted_key.bin
//
// Passphrase sources (checked in order): AETHER_SIGNER_PASSPHRASE env, then
// stdin (read to EOF, trailing newline trimmed). The env var is unset after
// reading. Private-key sources for `encrypt`: -key flag, then AETHER_PRIVATE_KEY
// env (also unset after reading).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/aether-arb/aether/internal/config"
	"github.com/aether-arb/aether/internal/signer"
)

const (
	envPassphrase = "AETHER_SIGNER_PASSPHRASE"
	envPrivateKey = "AETHER_PRIVATE_KEY"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	args := os.Args[1:]
	sub := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}

	var err error
	switch sub {
	case "serve":
		err = runServe(args)
	case "encrypt":
		err = runEncrypt(args)
	default:
		err = fmt.Errorf("unknown subcommand %q (want 'serve' or 'encrypt')", sub)
	}
	if err != nil {
		slog.Error("aether-signer failed", "subcommand", sub, "err", err)
		os.Exit(1)
	}
}

// runServe loads the encrypted key and serves signing requests until ctx is
// cancelled or a SIGINT/SIGTERM is received.
func runServe(argv []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case s := <-sigCh:
			slog.Info("signer received signal, shutting down", "signal", s.String())
			cancel()
		case <-ctx.Done():
		}
	}()

	return runServeContext(ctx, argv)
}

// runServeContext is the testable core of runServe; it exits when ctx is cancelled.
func runServeContext(ctx context.Context, argv []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := fs.String("config", config.ConfigPath("signer.yaml"), "path to signer.yaml")
	if err := fs.Parse(argv); err != nil {
		return err
	}

	cfg, err := config.LoadSignerConfig(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	passphrase, err := readPassphrase()
	if err != nil {
		return fmt.Errorf("read passphrase: %w", err)
	}
	if passphrase == "" {
		return fmt.Errorf("empty passphrase (set %s or pipe it on stdin)", envPassphrase)
	}

	kl, err := signer.LoadKeyFile(cfg.KeyFile, passphrase)
	// Scrub the passphrase from our own stack copy as soon as the key is loaded.
	passphrase = ""
	if err != nil {
		return fmt.Errorf("load key: %w", err)
	}
	defer kl.Destroy()
	slog.Info("signer key loaded", "address", kl.Address().Hex(), "key_file", cfg.KeyFile)

	srv, err := signer.NewServer(kl, cfg.SocketPath)
	if err != nil {
		return fmt.Errorf("start server: %w", err)
	}
	slog.Info("signer listening", "socket", srv.Addr())

	// Serve blocks until ctx is cancelled or the listener errors.
	if err := srv.Serve(ctx); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	slog.Info("signer stopped")
	return nil
}

// runEncrypt encrypts a raw hex private key into the on-disk format the signer
// reads at startup.
func runEncrypt(argv []string) error {
	fs := flag.NewFlagSet("encrypt", flag.ContinueOnError)
	keyFlag := fs.String("key", "", "hex private key (0x-optional); falls back to "+envPrivateKey+" env")
	out := fs.String("out", "", "output path for the encrypted key blob (required)")
	iters := fs.Int("iters", signer.DefaultPBKDF2Iters, "PBKDF2 iteration count")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("-out is required")
	}

	hexKey := *keyFlag
	if hexKey == "" {
		hexKey = os.Getenv(envPrivateKey)
		os.Unsetenv(envPrivateKey)
	}
	if hexKey == "" {
		return fmt.Errorf("no private key (pass -key or set %s)", envPrivateKey)
	}

	raw, err := signer.ParseHexKey(hexKey)
	hexKey = "" // scrub
	if err != nil {
		return err
	}

	passphrase, err := readPassphrase()
	if err != nil {
		return fmt.Errorf("read passphrase: %w", err)
	}
	if passphrase == "" {
		return fmt.Errorf("empty passphrase (set %s or pipe it on stdin)", envPassphrase)
	}

	blob, err := signer.Encrypt(raw, passphrase, *iters)
	passphrase = ""
	zeroBytes(raw) // scrub the plaintext key from our buffer
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}

	// 0600: the encrypted key file is still sensitive (passphrase-protected,
	// but defense in depth). O_EXCL refuses to silently overwrite an existing
	// key — deleting/rotating must be a deliberate act.
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create %s (already exists? remove it to re-encrypt): %w", *out, err)
	}
	defer f.Close()
	if _, err := f.Write(blob); err != nil {
		return fmt.Errorf("write %s: %w", *out, err)
	}
	slog.Info("wrote encrypted key", "path", *out, "bytes", len(blob), "pbkdf2_iters", *iters)
	return nil
}

// readPassphrase pulls the passphrase from the env var (preferred, then unset)
// or, failing that, reads stdin to EOF. The trailing newline a pipe adds is
// trimmed; interior characters (including spaces) are preserved.
func readPassphrase() (string, error) {
	if p := os.Getenv(envPassphrase); p != "" {
		os.Unsetenv(envPassphrase)
		return p, nil
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
