package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/aether-arb/aether/internal/config"
	"github.com/aether-arb/aether/internal/db"
	"github.com/aether-arb/aether/internal/events"
)

// buildExecutorDeps bootstraps the ETH node, configures signing, and wires
// ledger/metrics/events. Returns a cleanup func that must run on shutdown.
// Extracted from main() for unit tests (mock RPC dial, signer socket).
func buildExecutorDeps(
	ctx context.Context,
	cfg Config,
	execCfg config.ExecutorFileConfig,
	rpcURL string,
	dial ethDialFunc,
) (*Dependencies, func(), error) {
	dialCtx, dialCancel := context.WithTimeout(ctx, 10*time.Second)
	defer dialCancel()

	boot, err := bootstrap(dialCtx, execCfg, rpcURL, dial)
	if err != nil {
		return nil, nil, err
	}

	var (
		txSigner     TxSigner
		submitter    *Submitter
		remoteSigner *RemoteSigner
	)

	if signerSocket := resolveSignerSocket(); signerSocket != "" {
		rs, rsErr := NewRemoteSigner(signerSocket, boot.ChainID)
		if rsErr != nil {
			boot.Client.Close()
			return nil, nil, fmt.Errorf("remote signer connect: %w", rsErr)
		}
		if pingErr := rs.Ping(); pingErr != nil {
			boot.Client.Close()
			return nil, nil, fmt.Errorf("remote signer ping: %w", pingErr)
		}
		txSigner = rs
		remoteSigner = rs
		submitter, err = NewSubmitter(cfg.BuilderConfigs, "")
		if err != nil {
			boot.Client.Close()
			return nil, nil, fmt.Errorf("create submitter: %w", err)
		}
		submitter.SetAuthSigner(remoteFlashbotsAuth{rs: rs})
		os.Unsetenv("SEARCHER_KEY")
		slog.Info("remote signer connected", "addr", rs.Address().Hex(), "socket", signerSocket)
	} else {
		searcherKey := os.Getenv("SEARCHER_KEY")
		submitter, err = NewSubmitter(cfg.BuilderConfigs, searcherKey)
		if err != nil {
			boot.Client.Close()
			return nil, nil, fmt.Errorf("create submitter: %w", err)
		}
		if searcherKey != "" {
			local, signerErr := NewTransactionSigner(searcherKey, boot.ChainID)
			if signerErr != nil {
				boot.Client.Close()
				return nil, nil, fmt.Errorf("load SEARCHER_KEY: %w", signerErr)
			}
			txSigner = local
			slog.Info("searcher signer loaded (in-process key)", "addr", local.Address().Hex())
		} else {
			slog.Warn("AETHER_SIGNER_SOCKET unset — using in-process SEARCHER_KEY or unsigned txs; not recommended for production")
		}
		os.Unsetenv("SEARCHER_KEY")
	}

	ledgerMetrics := db.NewLedgerMetrics()
	ledger := db.LedgerFromEnv(ctx, os.Getenv("DATABASE_URL"), ledgerMetrics)

	ms := db.MetricsStoreFromEnv(ctx, os.Getenv("DATABASE_URL"))
	ep := events.NewPublisherFromEnv()

	cleanup := func() {
		if pg, ok := ledger.(*db.PgLedger); ok {
			pg.Close()
		}
		ms.Close()
		ep.Close()
		boot.Client.Close()
	}

	deps := &Dependencies{
		EthClient:      boot.Client,
		TxSigner:       txSigner,
		Submitter:      submitter,
		RemoteSigner:   remoteSigner,
		Ledger:         ledger,
		MetricsStore:   ms,
		EventPublisher: ep,
		ExecutorAddr:   execCfg.ExecutorAddress,
		ChainID:        boot.ChainID,
		RPCURL:         rpcURL,
		RequireBalance: true,
	}
	return deps, cleanup, nil
}

// logBootstrapFailure maps bootstrap errors to operator-facing log lines.
func logBootstrapFailure(err error, rpcURL, executorAddr string) {
	if rpcURL == "" {
		slog.Error("ETH_RPC_URL not set — required for chain-id / bytecode / balance checks")
		return
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "dial eth rpc"):
		slog.Error("failed to connect to ETH_RPC_URL", "url", redactRPCURL(rpcURL), "err", redactRPCError(err, rpcURL))
	case strings.Contains(msg, "chain id"):
		slog.Error("eth_chainId failed", "err", redactRPCError(err, rpcURL))
	case strings.Contains(msg, "chain-id mismatch"):
		slog.Error("chain-id mismatch", "err", err)
	case strings.Contains(msg, "get code"):
		slog.Error("eth_getCode failed", "executor_address", executorAddr, "err", redactRPCError(err, rpcURL))
	case strings.Contains(msg, "no bytecode"):
		slog.Error("executor address has no bytecode on chain", "executor_address", executorAddr)
	default:
		slog.Error("bootstrap failed", "err", err)
	}
}
