package main

import (
	"context"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/prometheus/client_golang/prometheus/testutil"

	pb "github.com/aether-arb/aether/internal/pb"
	"github.com/aether-arb/aether/internal/risk"
)

func TestTargetBlockForArb_Table(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		blockNumber uint64
		targetBlock uint64
		want        uint64
	}{
		{"explicit target wins", 18_000_000, 18_000_005, 18_000_005},
		{"zero target falls back to block+1", 18_000_000, 0, 18_000_001},
		{"block zero with zero target", 0, 0, 1},
		{"large block number", 21_000_000, 0, 21_000_001},
		{"mempool stamped target", 19_500_000, 19_500_002, 19_500_002},
		{"target equals block not treated as fallback", 100, 100, 100},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			arb := &pb.ValidatedArb{
				BlockNumber: tc.blockNumber,
				TargetBlock: tc.targetBlock,
			}
			if got := targetBlockForArb(arb); got != tc.want {
				t.Fatalf("targetBlockForArb() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestArbSourceLabel_Table(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source pb.ArbSource
		want   string
	}{
		{"mempool backrun", pb.ArbSource_MEMPOOL_BACKRUN, SourceMempoolBackrun},
		{"block driven explicit", pb.ArbSource_BLOCK_DRIVEN, SourceBlockDriven},
		{"unset defaults block driven", pb.ArbSource(0), SourceBlockDriven},
		{"unknown enum defaults block driven", pb.ArbSource(99), SourceBlockDriven},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			arb := &pb.ValidatedArb{Source: tc.source}
			if got := arbSourceLabel(arb); got != tc.want {
				t.Fatalf("arbSourceLabel() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRecordRiskRejection_IncrementsCounter(t *testing.T) {
	t.Parallel()
	base := testutil.ToFloat64(riskRejections)
	recordRiskRejection()
	recordRiskRejection()
	got := testutil.ToFloat64(riskRejections)
	if got != base+2 {
		t.Fatalf("risk_rejections: got %.0f, want %.0f", got, base+2)
	}
}

func TestWeiToGwei_Table(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		wei  *big.Int
		want float64
	}{
		{"30 gwei", big.NewInt(30_000_000_000), 30.0},
		{"1 gwei", big.NewInt(1_000_000_000), 1.0},
		{"zero", big.NewInt(0), 0.0},
		{"sub-gwei fraction", big.NewInt(500_000_000), 0.5},
		{"large value beyond int64", new(big.Int).Mul(big.NewInt(1_000), big.NewInt(1_000_000_000)), 1000.0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := weiToGwei(tc.wei); got != tc.want {
				t.Fatalf("weiToGwei() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMempoolGasTipFloorGwei_Table(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want float64
	}{
		{"unset defaults 2", "", 2.0},
		{"valid override", "15", 15.0},
		{"invalid string falls back", "not-a-number", 2.0},
		{"zero or negative falls back", "0", 2.0},
		{"negative falls back", "-5", 2.0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env == "" {
				os.Unsetenv("AETHER_MEMPOOL_GAS_TIP_MIN_GWEI")
			} else {
				t.Setenv("AETHER_MEMPOOL_GAS_TIP_MIN_GWEI", tc.env)
			}
			if got := mempoolGasTipFloorGwei(); got != tc.want {
				t.Fatalf("mempoolGasTipFloorGwei() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRequireAdminAuth_Table(t *testing.T) {
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})

	tests := []struct {
		name       string
		tokenEnv   string
		header     string
		queryToken string
		wantCode   int
	}{
		{"no token configured unauthorized", "", "", "", http.StatusUnauthorized},
		{"valid header token", "secret", "secret", "", http.StatusTeapot},
		{"valid query token", "secret", "", "secret", http.StatusTeapot},
		{"missing token unauthorized", "secret", "", "", http.StatusUnauthorized},
		{"wrong token unauthorized", "secret", "wrong", "", http.StatusUnauthorized},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setAdminTokenForTest(tc.tokenEnv)

			req := httptest.NewRequest(http.MethodPost, "/admin/pause", nil)
			if tc.header != "" {
				req.Header.Set("X-Aether-Admin-Token", tc.header)
			}
			if tc.queryToken != "" {
				req.URL.RawQuery = "token=" + tc.queryToken
			}
			w := httptest.NewRecorder()
			requireAdminAuth(okHandler)(w, req)
			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", w.Code, tc.wantCode)
			}
		})
	}
}

func TestHandleAdminPause_DefaultReason(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	globalAdminDeps.riskMgr = rm

	req := httptest.NewRequest(http.MethodPost, "/admin/pause", nil)
	w := httptest.NewRecorder()
	handleAdminPause(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	snap := globalSnapshotStore.Get()
	if snap.BreakerReason != "admin_pause" {
		t.Fatalf("reason = %q, want admin_pause", snap.BreakerReason)
	}
}

func TestHandleAdminResume_ConflictWhenNotPaused(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	globalAdminDeps.riskMgr = rm

	req := httptest.NewRequest(http.MethodPost, "/admin/resume", nil)
	w := httptest.NewRecorder()
	handleAdminResume(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
}

func TestGasOracleFetchOnce_ZeroBaseFeeKeepsLast(t *testing.T) {
	t.Parallel()

	mock := &mockFeeHistoryProvider{
		result: &ethereum.FeeHistory{
			BaseFee: []*big.Int{big.NewInt(0)},
			Reward:  [][]*big.Int{{big.NewInt(2e9)}},
		},
	}
	go_ := NewGasOracle(300.0)
	go_.Update(big.NewInt(40e9), big.NewInt(3e9))
	go_.SetClient(mock)

	fees, err := go_.FetchOnce(context.Background())
	if err != nil {
		t.Fatalf("FetchOnce: %v", err)
	}
	if fees.BaseFee.Cmp(big.NewInt(40e9)) != 0 {
		t.Fatalf("BaseFee = %s, want 40 gwei retained", fees.BaseFee)
	}
}

func TestGasOracleFetchOnce_NilBaseFeeKeepsLast(t *testing.T) {
	t.Parallel()

	mock := &mockFeeHistoryProvider{
		result: &ethereum.FeeHistory{
			BaseFee: nil,
			Reward:  [][]*big.Int{{big.NewInt(2e9)}},
		},
	}
	go_ := NewGasOracle(300.0)
	go_.Update(big.NewInt(55e9), big.NewInt(4e9))
	go_.SetClient(mock)

	fees, err := go_.FetchOnce(context.Background())
	if err != nil {
		t.Fatalf("FetchOnce: %v", err)
	}
	if fees.BaseFee.Cmp(big.NewInt(55e9)) != 0 {
		t.Fatalf("BaseFee = %s, want 55 gwei retained", fees.BaseFee)
	}
}

func TestGasOracleFetchOnce_ZeroPriorityUsesDefault(t *testing.T) {
	t.Parallel()

	mock := &mockFeeHistoryProvider{
		result: &ethereum.FeeHistory{
			BaseFee: []*big.Int{big.NewInt(20e9)},
			Reward:  [][]*big.Int{{big.NewInt(0)}},
		},
	}
	go_ := NewGasOracle(300.0)
	go_.SetClient(mock)

	fees, err := go_.FetchOnce(context.Background())
	if err != nil {
		t.Fatalf("FetchOnce: %v", err)
	}
	if fees.MaxPriorityFee.Cmp(big.NewInt(2e9)) != 0 {
		t.Fatalf("MaxPriorityFee = %s, want 2 gwei default", fees.MaxPriorityFee)
	}
}

func TestGasOracleMempoolFees_Table(t *testing.T) {
	tests := []struct {
		name         string
		envFloor     string
		baseGwei     int64
		priorityGwei int64
		wantPriGwei  int64
	}{
		{"ten percent of base wins", "", 80, 2, 8},
		{"env floor wins over ten percent", "12", 80, 2, 12},
		{"suggested priority wins when highest", "2", 30, 7, 7},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.envFloor == "" {
				os.Unsetenv("AETHER_MEMPOOL_GAS_TIP_MIN_GWEI")
			} else {
				t.Setenv("AETHER_MEMPOOL_GAS_TIP_MIN_GWEI", tc.envFloor)
			}
			go_ := NewGasOracle(300.0)
			go_.Update(big.NewInt(tc.baseGwei*1e9), big.NewInt(tc.priorityGwei*1e9))
			mp := go_.MempoolFees()
			want := big.NewInt(tc.wantPriGwei * 1e9)
			if mp.MaxPriorityFee.Cmp(want) != 0 {
				t.Fatalf("priority = %s, want %s", mp.MaxPriorityFee, want)
			}
		})
	}
}

func TestNonceSyncFromChain_NoAddressIsNoOp(t *testing.T) {
	t.Parallel()

	mock := &mockNonceProvider{nonce: 99}
	nm := NewNonceManager(10)
	nm.client = mock

	if err := nm.SyncFromChain(context.Background()); err != nil {
		t.Fatalf("SyncFromChain: %v", err)
	}
	if mock.calls != 0 {
		t.Fatalf("expected no RPC calls, got %d", mock.calls)
	}
	if nm.Current() != 10 {
		t.Fatalf("nonce = %d, want 10", nm.Current())
	}
}

func TestNonceSyncFromChain_NoClientIsNoOp(t *testing.T) {
	t.Parallel()

	nm := NewNonceManager(7)
	nm.SetSyncSource(common.HexToAddress("0xabc"), nil)
	if err := nm.SyncFromChain(context.Background()); err != nil {
		t.Fatalf("SyncFromChain: %v", err)
	}
	if nm.Current() != 7 {
		t.Fatalf("nonce = %d, want 7", nm.Current())
	}
}

func TestNonceSync_Table(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		start      uint64
		onChain    uint64
		pendingOps int
		want       uint64
		wantPend   int32
	}{
		{"higher on-chain advances", 5, 20, 3, 20, 0},
		{"lower on-chain ignored", 30, 10, 2, 32, 2},
		{"equal on-chain ignored", 15, 15, 1, 16, 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			nm := NewNonceManager(tc.start)
			for i := 0; i < tc.pendingOps; i++ {
				nm.Next()
			}
			nm.Sync(tc.onChain)
			if got := nm.Current(); got != tc.want {
				t.Fatalf("Current() = %d, want %d", got, tc.want)
			}
			if got := nm.PendingCount(); got != tc.wantPend {
				t.Fatalf("PendingCount() = %d, want %d", got, tc.wantPend)
			}
		})
	}
}

func TestHandleSetMinProfit_FromBody(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	globalAdminDeps.riskMgr = rm

	req := httptest.NewRequest(http.MethodPost, "/admin/set_min_profit", strings.NewReader("0.007"))
	w := httptest.NewRecorder()
	handleSetMinProfit(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	if rm.MinProfitETH() != 0.007 {
		t.Fatalf("min profit = %f", rm.MinProfitETH())
	}
}
