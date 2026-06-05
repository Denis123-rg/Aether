package main

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/aether-arb/aether/internal/signer"
)

// startTestSigner spins up a real internal/signer.Server backed by the
// well-known test key (testPrivateKeyHex, shared with signer_test.go) over a
// temp unix socket. It returns the socket path, the signer address, and a stop
// func. This exercises the actual JSON-RPC-over-UDS transport rather than a
// mock, so the RemoteSigner tests double as an integration test of the
// executor↔signer boundary.
func startTestSigner(t *testing.T) (sock, addrHex string, stop func()) {
	t.Helper()

	raw, err := signer.ParseHexKey(testPrivateKeyHex)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	blob, err := signer.Encrypt(raw, "pw", 1000)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	kl, err := signer.LoadKey(blob, "pw")
	if err != nil {
		t.Fatalf("load key: %v", err)
	}
	t.Cleanup(kl.Destroy)

	dir, err := os.MkdirTemp("", "aeth-rs")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock = filepath.Join(dir, "s.sock")

	srv, err := signer.NewServer(kl, sock)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = srv.Serve(ctx)
		close(done)
	}()
	return sock, kl.Address().Hex(), func() {
		cancel()
		<-done
	}
}

func TestRemoteSignerAddressAndPing(t *testing.T) {
	sock, addrHex, stop := startTestSigner(t)
	defer stop()

	rs, err := NewRemoteSigner(sock, 1)
	if err != nil {
		t.Fatalf("NewRemoteSigner: %v", err)
	}
	if got := rs.Address().Hex(); got != addrHex {
		t.Fatalf("Address() = %s, want %s", got, addrHex)
	}
	if err := rs.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

// TestRemoteSignerSignTxMatchesLocal asserts the remote signer produces a
// byte-identical signed transaction to the in-process signer for the same key
// (go-ethereum uses deterministic RFC-6979 ECDSA) and that the sender recovers
// to the signer address.
func TestRemoteSignerSignTxMatchesLocal(t *testing.T) {
	sock, _, stop := startTestSigner(t)
	defer stop()

	rs, err := NewRemoteSigner(sock, 1)
	if err != nil {
		t.Fatalf("NewRemoteSigner: %v", err)
	}
	local, err := NewTransactionSigner(testPrivateKeyHex, 1)
	if err != nil {
		t.Fatalf("NewTransactionSigner: %v", err)
	}

	to := common.HexToAddress("0x000000000000000000000000000000000000dEaD")
	mk := func() *types.Transaction {
		return types.NewTx(&types.DynamicFeeTx{
			ChainID:   big.NewInt(1),
			Nonce:     7,
			GasTipCap: big.NewInt(2_000_000_000),
			GasFeeCap: big.NewInt(20_000_000_000),
			Gas:       210000,
			To:        &to,
			Value:     big.NewInt(0),
			Data:      []byte{0xde, 0xad, 0xbe, 0xef},
		})
	}

	remoteSigned, err := rs.SignTx(mk())
	if err != nil {
		t.Fatalf("remote SignTx: %v", err)
	}
	localSigned, err := local.SignTx(mk())
	if err != nil {
		t.Fatalf("local SignTx: %v", err)
	}

	rb, err := remoteSigned.MarshalBinary()
	if err != nil {
		t.Fatalf("marshal remote: %v", err)
	}
	lb, err := localSigned.MarshalBinary()
	if err != nil {
		t.Fatalf("marshal local: %v", err)
	}
	if !bytes.Equal(rb, lb) {
		t.Fatalf("remote-signed tx bytes differ from local-signed tx bytes")
	}

	sender, err := types.Sender(types.LatestSignerForChainID(big.NewInt(1)), remoteSigned)
	if err != nil {
		t.Fatalf("recover sender: %v", err)
	}
	if sender != rs.Address() {
		t.Fatalf("sender = %s, want %s", sender.Hex(), rs.Address().Hex())
	}
}

// TestRemoteSignerFlashbotsAuthMatchesLocal verifies the remote-signer
// X-Flashbots-Signature header is identical to the local FlashbotsSigner's, both
// directly and through the submitter auth adapter.
func TestRemoteSignerFlashbotsAuthMatchesLocal(t *testing.T) {
	sock, _, stop := startTestSigner(t)
	defer stop()

	rs, err := NewRemoteSigner(sock, 1)
	if err != nil {
		t.Fatalf("NewRemoteSigner: %v", err)
	}
	fb, err := NewFlashbotsSigner(testPrivateKeyHex)
	if err != nil {
		t.Fatalf("NewFlashbotsSigner: %v", err)
	}

	payload := []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_sendBundle","params":[{}]}`)
	want, err := fb.Sign(payload)
	if err != nil {
		t.Fatalf("local flashbots sign: %v", err)
	}
	got, err := rs.SignFlashbotsPayload(payload)
	if err != nil {
		t.Fatalf("remote flashbots sign: %v", err)
	}
	if got != want {
		t.Fatalf("flashbots header mismatch:\n remote=%s\n  local=%s", got, want)
	}

	var auth flashbotsAuther = remoteFlashbotsAuth{rs: rs}
	got2, err := auth.Sign(payload)
	if err != nil || got2 != want {
		t.Fatalf("adapter sign = %q err=%v, want %q", got2, err, want)
	}
}

// TestRemoteSignerUnavailable asserts a down signer surfaces as
// errSignerUnavailable so processArb can pause the executor.
func TestRemoteSignerUnavailable(t *testing.T) {
	sock, _, stop := startTestSigner(t)

	rs, err := NewRemoteSigner(sock, 1)
	if err != nil {
		t.Fatalf("NewRemoteSigner: %v", err)
	}
	stop() // bring the signer down (closes listener, removes socket)

	to := common.HexToAddress("0x000000000000000000000000000000000000dEaD")
	_, err = rs.SignTx(types.NewTx(&types.DynamicFeeTx{
		ChainID:   big.NewInt(1),
		Nonce:     1,
		GasTipCap: big.NewInt(1),
		GasFeeCap: big.NewInt(1),
		Gas:       21000,
		To:        &to,
		Value:     big.NewInt(0),
	}))
	if err == nil {
		t.Fatal("expected error when signer is down")
	}
	if !errors.Is(err, errSignerUnavailable) {
		t.Fatalf("error = %v, want errSignerUnavailable", err)
	}
}

func TestNewRemoteSignerDownAtStartup(t *testing.T) {
	dir, err := os.MkdirTemp("", "aeth-rs")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	_, err = NewRemoteSigner(filepath.Join(dir, "missing.sock"), 1)
	if err == nil {
		t.Fatal("expected error dialing a missing socket")
	}
	if !errors.Is(err, errSignerUnavailable) {
		t.Fatalf("error = %v, want errSignerUnavailable", err)
	}
}

func TestResolveSignerSocket(t *testing.T) {
	t.Setenv("AETHER_SIGNER_SOCKET", "")
	if got := resolveSignerSocket(); got != "" {
		t.Fatalf("empty env: got %q, want \"\"", got)
	}
	t.Setenv("AETHER_SIGNER_SOCKET", "unix:///run/aether/signer.sock")
	if got := resolveSignerSocket(); got != "/run/aether/signer.sock" {
		t.Fatalf("unix:// strip: got %q", got)
	}
	t.Setenv("AETHER_SIGNER_SOCKET", "  /tmp/x.sock  ")
	if got := resolveSignerSocket(); got != "/tmp/x.sock" {
		t.Fatalf("trim: got %q", got)
	}
}
