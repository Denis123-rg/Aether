package main

import (
	"context"
	"errors"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/aether-arb/aether/internal/db"
	pb "github.com/aether-arb/aether/internal/pb"
	"github.com/aether-arb/aether/internal/risk"
	"github.com/aether-arb/aether/internal/strategy"
)

type unavailableSigner struct {
	addr common.Address
}

func (u unavailableSigner) Address() common.Address { return u.addr }
func (u unavailableSigner) SignTx(*types.Transaction) (*types.Transaction, error) {
	return nil, errSignerUnavailable
}

func TestProcessArb_SignerUnavailable_Pauses(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	nm := NewNonceManager(0)
	go_ := NewGasOracle(300.0)
	bundler := NewBundleConstructor(nm, go_, unavailableSigner{addr: common.HexToAddress("0x1")}, 1)
	submitter, _ := NewSubmitter(defaultBuilderConfigs(), "")
	submitter.submitFn = func(ctx context.Context, builder BuilderConfig, bundle *Bundle) SubmissionResult {
		return SubmissionResult{Builder: builder.Name, Success: true}
	}

	arb := newValidArb("arb-signer-fail", 0.01, 5.0)
	_, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter, db.NewNoopLedger(),
		"0x0000000000000000000000000000000000000001", 0.5)
	if err == nil {
		t.Fatal("expected build bundle error")
	}
	if !errors.Is(err, errSignerUnavailable) {
		t.Fatalf("err = %v, want errSignerUnavailable", err)
	}
	if rm.State() != risk.StatePaused {
		t.Fatalf("expected paused, got %s", rm.State())
	}
}

func TestProcessArb_ShadowMode_SkipsSubmission(t *testing.T) {
	t.Setenv("AETHER_SHADOW", "1")
	defer os.Unsetenv("AETHER_SHADOW")

	rm, bundler, submitter := newTestComponents()
	ledger := &recordingLedger{}
	arb := newValidArb("arb-shadow-001", 0.01, 5.0)

	submitted, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter, ledger,
		"0x0000000000000000000000000000000000000001", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if !submitted {
		t.Fatal("shadow mode should return submitted=true (bundle built)")
	}
	if !ledger.bundleInserted {
		t.Fatal("shadow mode must persist bundle row")
	}
}

func TestProcessArb_MempoolMissingVictimRawTx(t *testing.T) {
	rm, bundler, submitter := newTestComponents()
	arb := newValidArb("arb-mempool-no-victim", 0.01, 5.0)
	arb.Source = pb.ArbSource_MEMPOOL_BACKRUN
	arb.VictimTxHash = []byte{0xde, 0xad}
	arb.VictimRawTx = nil

	submitted, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter, db.NewNoopLedger(),
		"0x0000000000000000000000000000000000000001", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if submitted {
		t.Fatal("expected not submitted without victim raw tx")
	}
}

func TestProcessArb_MempoolRiskRejection(t *testing.T) {
	prevCfg := mempoolRiskCfg
	prevInflight := mempoolInflight
	defer func() {
		mempoolRiskCfg = prevCfg
		mempoolInflight = prevInflight
	}()

	mempoolRiskCfg = MempoolRiskConfig{
		MinProfitWei:              new(big.Int).SetUint64(1_000_000_000_000_000_000), // 1 ETH — very high
		MaxTipShareBps:            9500,
		MaxVictimFreshnessMs:      500,
		MaxInflightPerTargetBlock: 5,
	}
	mempoolInflight = NewMempoolInflightTracker()

	rm, bundler, submitter := newTestComponents()
	arb := newValidArb("arb-mempool-reject", 0.01, 5.0)
	arb.Source = pb.ArbSource_MEMPOOL_BACKRUN
	arb.VictimTxHash = []byte{0x01, 0x02, 0x03}
	arb.VictimRawTx = []byte{0xf8, 0x01, 0x01}
	arb.TimestampNs = time.Now().UnixNano()

	submitted, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter, db.NewNoopLedger(),
		"0x0000000000000000000000000000000000000001", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if submitted {
		t.Fatal("expected mempool gate rejection")
	}
}

func TestProcessArb_SelectRoutingMode(t *testing.T) {
	prevMode := routingMode
	prevSelector := builderSelector
	defer func() {
		routingMode = prevMode
		builderSelector = prevSelector
	}()

	routingMode = "select"
	builderSelector = strategy.New([]string{"mock"}, strategy.Config{})

	builders := []BuilderConfig{{Name: "mock", Enabled: true, TimeoutMs: 1000}}
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	nm := NewNonceManager(0)
	go_ := NewGasOracle(300.0)
	bundler := NewBundleConstructor(nm, go_, nil, 1)
	submitter, _ := NewSubmitter(builders, "")
	var picked string
	submitter.submitFn = func(ctx context.Context, builder BuilderConfig, bundle *Bundle) SubmissionResult {
		picked = builder.Name
		return SubmissionResult{Builder: builder.Name, Success: true, BundleHash: "h"}
	}

	arb := newValidArb("arb-select-route", 0.01, 5.0)
	submitted, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter, db.NewNoopLedger(),
		"0x0000000000000000000000000000000000000001", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if !submitted {
		t.Fatal("expected submission")
	}
	if picked != "mock" {
		t.Fatalf("picked builder = %q, want mock", picked)
	}
}

type recordingLedger struct {
	bundleInserted bool
}

func (r *recordingLedger) InsertBundle(_ db.NewBundle) { r.bundleInserted = true }
func (r *recordingLedger) InsertInclusion(_ db.NewInclusion) {}
func (r *recordingLedger) UpsertPnLDaily(_ db.PnLDailyDelta) {}
