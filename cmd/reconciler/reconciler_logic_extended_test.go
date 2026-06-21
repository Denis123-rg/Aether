package main

import (
	"context"
	"math/big"
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/aether-arb/aether/internal/db"
)

type fakeSubscription struct {
	errCh chan error
}

func (f *fakeSubscription) Unsubscribe()      {}
func (f *fakeSubscription) Err() <-chan error { return f.errCh }

type fakeEthClient struct {
	block      *types.Block
	blockErr   error
	receipt    *types.Receipt
	receiptErr error
	headNum    uint64
	headErr    error
	subErr     error
}

func (f *fakeEthClient) SubscribeNewHead(_ context.Context, _ chan<- *types.Header) (ethereum.Subscription, error) {
	if f.subErr != nil {
		return nil, f.subErr
	}
	return &fakeSubscription{errCh: make(chan error)}, nil
}

func (f *fakeEthClient) BlockByHash(_ context.Context, _ common.Hash) (*types.Block, error) {
	if f.blockErr != nil {
		return nil, f.blockErr
	}
	return f.block, nil
}

func (f *fakeEthClient) TransactionReceipt(_ context.Context, _ common.Hash) (*types.Receipt, error) {
	if f.receiptErr != nil {
		return nil, f.receiptErr
	}
	return f.receipt, nil
}

func (f *fakeEthClient) BlockNumber(_ context.Context) (uint64, error) {
	if f.headErr != nil {
		return 0, f.headErr
	}
	return f.headNum, nil
}

type fakeRecon struct {
	predictions map[common.Hash]db.PendingPrediction
	lookupErr   error
	inserted    []db.NewReconciliation
	markRows    int64
	markErr     error
}

func (f *fakeRecon) LookupPredictionByTxHash(_ context.Context, txHash [32]byte) (db.PendingPrediction, bool, error) {
	if f.lookupErr != nil {
		return db.PendingPrediction{}, false, f.lookupErr
	}
	h := common.BytesToHash(txHash[:])
	p, ok := f.predictions[h]
	return p, ok, nil
}

func (f *fakeRecon) InsertReconciliation(r db.NewReconciliation) {
	f.inserted = append(f.inserted, r)
}

func (f *fakeRecon) MarkStaleAsDropped(_ context.Context, _ uint64) (int64, error) {
	if f.markErr != nil {
		return 0, f.markErr
	}
	return f.markRows, nil
}

func signedLegacyTx(t *testing.T) *types.Transaction {
	t.Helper()
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	tx := types.NewTx(&types.LegacyTx{
		Nonce:    1,
		GasPrice: big.NewInt(1),
		Gas:      21000,
	})
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(big.NewInt(1)), priv)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func blockWithTx(t *testing.T, tx *types.Transaction, number uint64) *types.Block {
	t.Helper()
	header := &types.Header{Number: big.NewInt(int64(number))}
	body := &types.Body{Transactions: []*types.Transaction{tx}}
	return types.NewBlock(header, body, nil, trie.NewStackTrie(nil))
}

func TestReceiptHitsPool_MatchingLog(t *testing.T) {
	t.Parallel()
	pool := common.HexToAddress("0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")
	client := &fakeEthClient{
		receipt: &types.Receipt{Logs: []*types.Log{{Address: pool}}},
	}
	var poolBytes [20]byte
	copy(poolBytes[:], pool.Bytes())
	ok, err := receiptHitsPool(context.Background(), client, common.Hash{1}, poolBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected pool match")
	}
}

func TestReceiptHitsPool_NoMatchingLog(t *testing.T) {
	t.Parallel()
	client := &fakeEthClient{
		receipt: &types.Receipt{Logs: []*types.Log{{Address: common.HexToAddress("0x1")}}},
	}
	var poolBytes [20]byte
	copy(poolBytes[:], common.HexToAddress("0x2").Bytes())
	ok, err := receiptHitsPool(context.Background(), client, common.Hash{2}, poolBytes)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected no match")
	}
}

func TestReceiptHitsPool_Error(t *testing.T) {
	t.Parallel()
	client := &fakeEthClient{receiptErr: context.DeadlineExceeded}
	_, err := receiptHitsPool(context.Background(), client, common.Hash{3}, [20]byte{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleHeader_ConfirmsPrediction(t *testing.T) {
	t.Parallel()
	tx := signedLegacyTx(t)
	block := blockWithTx(t, tx, 100)
	pool := common.HexToAddress("0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2")
	var poolBytes [20]byte
	copy(poolBytes[:], pool.Bytes())

	recon := &fakeRecon{
		predictions: map[common.Hash]db.PendingPrediction{
			tx.Hash(): {
				PredictionID:         uuid.New(),
				Protocol:             "uniswap_v2",
				PoolAddress:          &poolBytes,
				PredictedTargetBlock: 99,
			},
		},
	}
	client := &fakeEthClient{
		block:   block,
		receipt: &types.Receipt{Logs: []*types.Log{{Address: pool}}},
	}
	reg := prometheus.NewRegistry()
	metrics := newLoopMetrics(reg)

	handleHeader(context.Background(), client, recon, metrics, block.Header())

	if len(recon.inserted) != 1 {
		t.Fatalf("inserted %d", len(recon.inserted))
	}
	r := recon.inserted[0]
	if r.Outcome != db.OutcomeConfirmed {
		t.Fatalf("outcome %s", r.Outcome)
	}
	if r.BlockDelta == nil || *r.BlockDelta != 1 {
		t.Fatalf("delta %v", r.BlockDelta)
	}
	if r.PoolPathCorrect == nil || !*r.PoolPathCorrect {
		t.Fatal("pool path should be correct")
	}
}

func TestHandleHeader_BlockFetchError(t *testing.T) {
	t.Parallel()
	client := &fakeEthClient{blockErr: context.DeadlineExceeded}
	reg := prometheus.NewRegistry()
	metrics := newLoopMetrics(reg)
	before := testutil.ToFloat64(metrics.HeaderFetchErrors)
	handleHeader(context.Background(), client, &fakeRecon{}, metrics, &types.Header{})
	after := testutil.ToFloat64(metrics.HeaderFetchErrors)
	if after != before+1 {
		t.Fatalf("metrics before=%v after=%v", before, after)
	}
}

func TestHandleHeader_LookupErrorContinues(t *testing.T) {
	t.Parallel()
	tx := signedLegacyTx(t)
	block := blockWithTx(t, tx, 50)
	recon := &fakeRecon{lookupErr: context.DeadlineExceeded}
	client := &fakeEthClient{block: block}
	reg := prometheus.NewRegistry()
	metrics := newLoopMetrics(reg)
	before := testutil.ToFloat64(metrics.LookupErrors)
	handleHeader(context.Background(), client, recon, metrics, block.Header())
	after := testutil.ToFloat64(metrics.LookupErrors)
	if after != before+1 {
		t.Fatalf("metrics before=%v after=%v", before, after)
	}
}

func TestHandleHeader_ReceiptErrorStillInserts(t *testing.T) {
	t.Parallel()
	tx := signedLegacyTx(t)
	block := blockWithTx(t, tx, 200)
	pool := common.HexToAddress("0xdAC17F958D2ee523a2206206994597C13D831ec7")
	var poolBytes [20]byte
	copy(poolBytes[:], pool.Bytes())
	recon := &fakeRecon{
		predictions: map[common.Hash]db.PendingPrediction{
			tx.Hash(): {
				PredictionID:         uuid.New(),
				Protocol:             "curve",
				PoolAddress:          &poolBytes,
				PredictedTargetBlock: 200,
			},
		},
	}
	client := &fakeEthClient{block: block, receiptErr: context.DeadlineExceeded}
	reg := prometheus.NewRegistry()
	metrics := newLoopMetrics(reg)
	handleHeader(context.Background(), client, recon, metrics, block.Header())
	if len(recon.inserted) != 1 {
		t.Fatal("expected insert despite receipt error")
	}
	if recon.inserted[0].PoolPathCorrect != nil {
		t.Fatal("pool path should be nil on receipt error")
	}
}

func TestHandleHeader_NoPredictionSkips(t *testing.T) {
	t.Parallel()
	tx := signedLegacyTx(t)
	block := blockWithTx(t, tx, 10)
	recon := &fakeRecon{predictions: map[common.Hash]db.PendingPrediction{}}
	client := &fakeEthClient{block: block}
	reg := prometheus.NewRegistry()
	metrics := newLoopMetrics(reg)
	handleHeader(context.Background(), client, recon, metrics, block.Header())
	if len(recon.inserted) != 0 {
		t.Fatal("expected no insert")
	}
}

func TestHandleHeader_NilPoolAddressSkipsReceipt(t *testing.T) {
	t.Parallel()
	tx := signedLegacyTx(t)
	block := blockWithTx(t, tx, 77)
	recon := &fakeRecon{
		predictions: map[common.Hash]db.PendingPrediction{
			tx.Hash(): {
				PredictionID:         uuid.New(),
				Protocol:             "balancer",
				PredictedTargetBlock: 77,
			},
		},
	}
	client := &fakeEthClient{block: block}
	reg := prometheus.NewRegistry()
	metrics := newLoopMetrics(reg)
	handleHeader(context.Background(), client, recon, metrics, block.Header())
	if len(recon.inserted) != 1 {
		t.Fatal("expected insert")
	}
	if recon.inserted[0].PoolPathCorrect != nil {
		t.Fatal("expected nil pool path")
	}
}

func TestRunStaleSweepLoop_MarksDropped(t *testing.T) {
	recon := &fakeRecon{markRows: 3}
	client := &fakeEthClient{headNum: 1_000_000}
	ctx, cancel := context.WithCancel(context.Background())
	go runStaleSweepLoop(ctx, client, recon)
	time.Sleep(staleSweepInterval + 300*time.Millisecond)
	cancel()
}

func TestRunStaleSweepLoop_BlockNumberError(t *testing.T) {
	client := &fakeEthClient{headErr: context.DeadlineExceeded}
	ctx, cancel := context.WithCancel(context.Background())
	go runStaleSweepLoop(ctx, client, &fakeRecon{})
	time.Sleep(staleSweepInterval + 300*time.Millisecond)
	cancel()
}

func TestRunStaleSweepLoop_MarkError(t *testing.T) {
	recon := &fakeRecon{markErr: context.DeadlineExceeded}
	client := &fakeEthClient{headNum: 100}
	ctx, cancel := context.WithCancel(context.Background())
	go runStaleSweepLoop(ctx, client, recon)
	time.Sleep(staleSweepInterval + 300*time.Millisecond)
	cancel()
}

func TestStartMetricsServer_ServesPrometheus(t *testing.T) {
	reg := prometheus.NewRegistry()
	newLoopMetrics(reg)
	srv := startMetricsServer("127.0.0.1:19094", reg)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get("http://127.0.0.1:19094/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestRunHeaderLoop_SubscribeError(t *testing.T) {
	client := &fakeEthClient{subErr: context.DeadlineExceeded}
	reg := prometheus.NewRegistry()
	metrics := newLoopMetrics(reg)
	runHeaderLoop(context.Background(), client, &fakeRecon{}, metrics)
}

func TestRunHeaderLoop_ContextCancel(t *testing.T) {
	client := &fakeEthClient{}
	reg := prometheus.NewRegistry()
	metrics := newLoopMetrics(reg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runHeaderLoop(ctx, client, &fakeRecon{}, metrics)
}
