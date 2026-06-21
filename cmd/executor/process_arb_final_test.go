package main

import (
	"context"
	"errors"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"

	"github.com/aether-arb/aether/internal/db"
	"github.com/aether-arb/aether/internal/events"
	aethergrpc "github.com/aether-arb/aether/internal/grpc"
	pb "github.com/aether-arb/aether/internal/pb"
	"github.com/aether-arb/aether/internal/risk"
)

func TestProcessArb_PreflightRejection(t *testing.T) {
	rm, bundler, submitter := newTestComponents()
	arb := newValidArb("arb-low-profit", 0.0001, 5.0)
	submitted, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if submitted {
		t.Fatal("expected preflight rejection")
	}
}

func TestProcessArb_FanoutSubmission(t *testing.T) {
	prevMode := routingMode
	prevSel := builderSelector
	defer func() {
		routingMode = prevMode
		builderSelector = prevSel
	}()
	routingMode = "fanout"
	builderSelector = nil

	rm, bundler, submitter := newTestComponents()
	arb := newValidArb("arb-fanout", 0.01, 5.0)
	submitted, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if !submitted {
		t.Fatal("expected fanout submission")
	}
}

func TestProcessArb_SubmissionFailureRecordsError(t *testing.T) {
	rm, bundler, submitter := newTestComponents()
	submitter.submitFn = func(ctx context.Context, b BuilderConfig, bundle *Bundle) SubmissionResult {
		return SubmissionResult{Builder: b.Name, Success: false, Error: errors.New("builder rejected")}
	}

	arb := newValidArb("arb-fail-submit", 0.01, 5.0)
	submitted, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if submitted {
		t.Fatal("expected no successful submission")
	}
}

func TestProcessArb_WithEventPublisher(t *testing.T) {
	oldPub := eventPublisher
	oldStore := metricsStore
	defer func() {
		eventPublisher = oldPub
		metricsStore = oldStore
	}()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	t.Setenv("REDIS_URL", "redis://"+mr.Addr())
	eventPublisher = events.NewPublisherFromEnv()
	metricsStore = db.NewNoopMetricsStore()

	rm, bundler, submitter := newTestComponents()
	arb := newValidArb("arb-events", 0.01, 5.0)
	submitted, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if !submitted {
		t.Fatal("expected submission")
	}
}

func TestProcessArb_MempoolBackrunHappyPath(t *testing.T) {
	rm, bundler, submitter := newTestComponents()
	arb := newValidArb("arb-mempool-ok", 0.01, 5.0)
	arb.Source = pb.ArbSource_MEMPOOL_BACKRUN
	arb.VictimTxHash = make([]byte, 32)
	arb.VictimTxHash[0] = 0xaa
	arb.VictimRawTx = []byte{0xf8, 0x01, 0x02}
	arb.TimestampNs = time.Now().UnixNano()

	submitted, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if !submitted {
		t.Fatal("expected mempool backrun submission")
	}
}

func TestTokenLabel_KnownAndTruncated(t *testing.T) {
	weth := []byte{0xc0, 0x2a, 0xaa, 0x39, 0xb2, 0x23, 0xfe, 0x8d, 0x0a, 0x0e, 0x5c, 0x4f, 0x27, 0xea, 0xd9, 0x08, 0x3c, 0x75, 0x6c, 0xc2}
	if tokenLabel(weth) != "WETH" {
		t.Fatalf("got %q", tokenLabel(weth))
	}
	short := []byte{0x01, 0x02, 0x03}
	if tokenLabel(short) == "?" {
		t.Fatal("short addr should truncate, not ?")
	}
}

func TestDumpShadowBundle_ReadOnlyDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AETHER_SHADOW_DUMP_DIR", dir)
	arb := newValidArb("arb-ro", 0.01, 5.0)
	bundle := &Bundle{BlockNumber: 1, RawTxs: [][]byte{{0x01}}}
	if err := dumpShadowBundle(arb, bundle, 0.01, 30.0, 90.0); err == nil {
		t.Fatal("expected mkdir/write error on read-only dir")
	}
	_ = os.Chmod(dir, 0o755)
}

func TestConsumeArbStream_DialErrorRetries(t *testing.T) {
	client, err := aethergrpc.Dial("127.0.0.1:1")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	rm, bundler, submitter := newTestComponents()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	lb := NewLiveBalance()
	lb.Set(0.5)
	consumeArbStream(ctx, client, bundler, submitter, rm, db.NewNoopLedger(),
		"0x0000000000000000000000000000000000000001", lb, 30*time.Millisecond)
}

func TestPollPendingInclusions_StatsErrorGiveUp(t *testing.T) {
	pendingMu.Lock()
	pendingQueue = nil
	pendingMu.Unlock()

	enqueuePendingBundle(pendingBundle{
		bundleID:    uuid.New(),
		bundleHash:  "0xcafe",
		submittedAt: time.Now().UTC().Add(-6 * time.Minute),
	})

	submitter, _ := NewSubmitter([]BuilderConfig{{Name: "mock", Enabled: true, TimeoutMs: 100}}, "")
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	pollPendingInclusions(context.Background(), submitter, db.NewNoopLedger(), rm)

	pendingMu.Lock()
	n := len(pendingQueue)
	pendingMu.Unlock()
	if n != 0 {
		t.Fatalf("stale bundle should be dropped after give-up, remaining=%d", n)
	}
}

func TestParseBundleStats_FallbackFlags(t *testing.T) {
	raw := []byte(`{"isHighPriority":true,"isSentToMiners":false,"blockNumber":"0x0"}`)
	included, block := parseBundleStats(raw)
	if included {
		t.Fatalf("high priority without confirmed block must not count as included; block=%d", block)
	}
}

func TestAddBigIntCounter_NilAndValue(t *testing.T) {
	addBigIntCounter(profitTotalWei, nil)
	addBigIntCounter(profitTotalWei, big.NewInt(42))
}
