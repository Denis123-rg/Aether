package main

import (
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/aether-arb/aether/internal/config"
)

func validExecCfg() config.ExecutorFileConfig {
	return config.ExecutorFileConfig{
		ExecutorAddress: "0x0000000000000000000000000000000000000001",
		ExpectedChainID: 1,
	}
}

func TestBuildExecutorDeps_MockRPC(t *testing.T) {
	srv := mockEthRPC(t, 1, []byte{0x60, 0x80})
	defer srv.Close()

	t.Setenv("SEARCHER_KEY", testPrivateKeyHex)
	t.Setenv("AETHER_SIGNER_SOCKET", "")
	t.Setenv("DATABASE_URL", "")

	dial := func(ctx context.Context, url string) (*ethclient.Client, error) {
		return ethclient.DialContext(ctx, srv.URL)
	}

	deps, cleanup, err := buildExecutorDeps(
		context.Background(),
		defaultConfig(),
		validExecCfg(),
		srv.URL,
		dial,
	)
	if err != nil {
		t.Fatalf("buildExecutorDeps: %v", err)
	}
	defer cleanup()

	if deps.TxSigner == nil || deps.Submitter == nil {
		t.Fatal("expected signer and submitter")
	}
	if deps.ChainID != 1 {
		t.Fatalf("chain id = %d", deps.ChainID)
	}
}

func TestBuildExecutorDeps_RemoteSigner(t *testing.T) {
	srv := mockEthRPC(t, 1, []byte{0x60, 0x80})
	defer srv.Close()

	sock, _, stop := startTestSigner(t)
	defer stop()

	t.Setenv("AETHER_SIGNER_SOCKET", sock)
	t.Setenv("SEARCHER_KEY", "")
	t.Setenv("DATABASE_URL", "")

	dial := func(ctx context.Context, url string) (*ethclient.Client, error) {
		return ethclient.DialContext(ctx, srv.URL)
	}

	deps, cleanup, err := buildExecutorDeps(
		context.Background(),
		defaultConfig(),
		validExecCfg(),
		srv.URL,
		dial,
	)
	if err != nil {
		t.Fatalf("buildExecutorDeps: %v", err)
	}
	defer cleanup()

	if deps.RemoteSigner == nil {
		t.Fatal("expected remote signer")
	}
	if deps.TxSigner == nil {
		t.Fatal("expected tx signer from remote")
	}
}

func TestBuildExecutorDeps_MissingRPC(t *testing.T) {
	_, _, err := buildExecutorDeps(
		context.Background(),
		defaultConfig(),
		validExecCfg(),
		"",
		nil,
	)
	if err == nil {
		t.Fatal("expected error for empty RPC URL")
	}
}

func TestBuildExecutorDeps_InvalidSearcherKey(t *testing.T) {
	srv := mockEthRPC(t, 1, []byte{0x60})
	defer srv.Close()

	t.Setenv("SEARCHER_KEY", "not-a-key")
	t.Setenv("AETHER_SIGNER_SOCKET", "")

	dial := func(ctx context.Context, url string) (*ethclient.Client, error) {
		return ethclient.DialContext(ctx, srv.URL)
	}

	_, _, err := buildExecutorDeps(
		context.Background(),
		defaultConfig(),
		validExecCfg(),
		srv.URL,
		dial,
	)
	if err == nil {
		t.Fatal("expected invalid key error")
	}
}

func TestLogBootstrapFailure_Branches(t *testing.T) {
	logBootstrapFailure(fmtErr("ETH_RPC_URL not set"), "", "0x1")
	logBootstrapFailure(fmtErr("dial eth rpc: refused"), "http://x", "0x1")
	logBootstrapFailure(fmtErr("chain id: timeout"), "http://x", "0x1")
	logBootstrapFailure(fmtErr("chain-id mismatch"), "http://x", "0x1")
	logBootstrapFailure(fmtErr("get code: fail"), "http://x", "0x1")
	logBootstrapFailure(fmtErr("executor has no bytecode"), "http://x", "0x1")
	logBootstrapFailure(fmtErr("other"), "http://x", "0x1")
}

type fmtErr string

func (e fmtErr) Error() string { return string(e) }

func TestBuildExecutorDeps_RemoteSignerUnreachable(t *testing.T) {
	srv := mockEthRPC(t, 1, []byte{0x60})
	defer srv.Close()

	t.Setenv("AETHER_SIGNER_SOCKET", "/nonexistent/signer.sock")
	t.Setenv("SEARCHER_KEY", "")

	dial := func(ctx context.Context, url string) (*ethclient.Client, error) {
		return ethclient.DialContext(ctx, srv.URL)
	}

	_, _, err := buildExecutorDeps(
		context.Background(),
		defaultConfig(),
		validExecCfg(),
		srv.URL,
		dial,
	)
	if err == nil {
		t.Fatal("expected remote signer error")
	}
}

