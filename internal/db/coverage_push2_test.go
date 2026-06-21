package db

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
)

func strPtr(s string) *string { return &s }

func TestPgMempoolReconciliation_InsertSuccessOptionalFields(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	recon, err := NewPgMempoolReconciliation(ctx, url, NewMempoolReconciliationMetrics(reg))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer recon.Close()

	predID := uuid.New()
	var txHash [32]byte
	copy(txHash[:], predID[:])
	router := [20]byte{1}
	tokenIn := [20]byte{2}
	tokenOut := [20]byte{3}
	poolAddr := [20]byte{4}

	_, err = recon.pool.Exec(ctx, `
		INSERT INTO mempool_predictions (
			prediction_id, decoded_at, pending_tx_hash, router_address, protocol,
			token_in, token_out, amount_in, pool_address,
			predicted_target_block, predicted_post_state
		) VALUES (
			$1, now(), $2, $3, 'uni_v2', $4, $5, 1000000, $6,
			100, '{}'::jsonb
		)
		ON CONFLICT (pending_tx_hash) DO NOTHING
	`, predID, txHash[:], router[:], tokenIn[:], tokenOut[:], poolAddr[:])
	if err != nil {
		t.Fatalf("seed prediction: %v", err)
	}
	t.Cleanup(func() {
		_, _ = recon.pool.Exec(context.Background(),
			`DELETE FROM mempool_predictions WHERE prediction_id = $1`, predID)
	})

	actualBlock := uint64(101)
	actualIdx := 3
	blockDelta := 1
	poolPathCorrect := true
	orderingCorrect := false
	replaced := txHash
	recon.InsertReconciliation(NewReconciliation{
		PredictionID:      predID,
		ResolutionTs:      time.Now().UTC(),
		Outcome:           OutcomeConfirmed,
		ActualTargetBlock: &actualBlock,
		ActualTxIndex:     &actualIdx,
		BlockDelta:        &blockDelta,
		OrderingCorrect:   &orderingCorrect,
		PoolPathCorrect:   &poolPathCorrect,
		ReplacedByTxHash:  &replaced,
		FailureReason:     strPtr("none"),
	})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var outcome string
		err := recon.pool.QueryRow(ctx,
			`SELECT outcome FROM mempool_reconciliation WHERE prediction_id = $1`, predID,
		).Scan(&outcome)
		if err == nil && outcome == OutcomeConfirmed {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("reconciliation row did not land")
}

func TestEnsureMigrationsTable(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	if err := ensureMigrationsTable(ctx, conn); err != nil {
		t.Fatalf("ensureMigrationsTable: %v", err)
	}
}

func TestPgLedger_InsertInclusionWithErrorString(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	ledger, err := NewPgLedger(ctx, url, getTestLedgerMetrics())
	if err != nil {
		t.Fatalf("NewPgLedger: %v", err)
	}
	defer ledger.Close()

	errMsg := "builder rejected"
	ledger.InsertInclusion(NewInclusion{
		BundleID: uuid.New(),
		Builder:  "flashbots",
		Included: false,
		Error:    &errMsg,
	})
	waitForLedgerWrite(t, ledger, 2*time.Second)
}

func TestPgLedger_InsertBundleFullFields(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	ledger, err := NewPgLedger(ctx, url, getTestLedgerMetrics())
	if err != nil {
		t.Fatalf("NewPgLedger: %v", err)
	}
	defer ledger.Close()

	arbID := uuid.New()
	bundleID := uuid.New()
	ledger.InsertBundle(NewBundle{
		BundleID:    bundleID,
		ArbID:       arbID,
		SubmittedAt: time.Now().UTC(),
		TargetBlock: 123,
		SignedTxHex: "0xdead",
		IsShadow:    true,
		Builders:    []string{"flashbots", "titan"},
	})
	waitForLedgerWrite(t, ledger, 2*time.Second)
}

func TestPgLedger_UpsertPnLDailyWithValues(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	ledger, err := NewPgLedger(ctx, url, getTestLedgerMetrics())
	if err != nil {
		t.Fatalf("NewPgLedger: %v", err)
	}
	defer ledger.Close()

	profit := big.NewInt(1e18)
	gas := big.NewInt(1e15)
	ledger.UpsertPnLDaily(PnLDailyDelta{
		Day:               time.Now().UTC(),
		RealizedProfitWei: profit,
		GasSpentWei:       gas,
		BundleCount:       2,
		InclusionCount:    1,
	})
	waitForLedgerWrite(t, ledger, 2*time.Second)
}
