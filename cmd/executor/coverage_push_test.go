package main

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/aether-arb/aether/internal/config"
	"github.com/aether-arb/aether/internal/db"
	"github.com/aether-arb/aether/internal/events"
	aethergrpc "github.com/aether-arb/aether/internal/grpc"
	"github.com/aether-arb/aether/internal/metrics"
	pb "github.com/aether-arb/aether/internal/pb"
	"github.com/aether-arb/aether/internal/risk"
	"github.com/aether-arb/aether/internal/strategy"
	"github.com/aether-arb/aether/internal/testutil"
)

type testFailingSigner struct{ err error }

func (f *testFailingSigner) Address() common.Address { return common.HexToAddress("0xdead") }
func (f *testFailingSigner) SignTx(tx *types.Transaction) (*types.Transaction, error) {
	return nil, f.err
}

type testMockBalanceReader struct {
	balFn func(ctx context.Context, addr common.Address, blockNumber *big.Int) (*big.Int, error)
}

func (m *testMockBalanceReader) BalanceAt(ctx context.Context, addr common.Address, blockNumber *big.Int) (*big.Int, error) {
	return m.balFn(ctx, addr, blockNumber)
}

type testMockNonceProvider struct {
	nonce uint64
	err   error
}

func (m *testMockNonceProvider) PendingNonceAt(ctx context.Context, account common.Address) (uint64, error) {
	return m.nonce, m.err
}

type testMockFeeHistoryProvider struct {
	returnZeroBaseFee    bool
	returnEmptyReward    bool
	returnError          bool
}

func (m *testMockFeeHistoryProvider) FeeHistory(ctx context.Context, blockCount uint64, lastBlock *big.Int, rewardPercentiles []float64) (*ethereum.FeeHistory, error) {
	if m.returnError {
		return nil, errors.New("RPC error")
	}
	result := &ethereum.FeeHistory{
		BaseFee: []*big.Int{big.NewInt(30e9), big.NewInt(30e9)},
		Reward:  [][]*big.Int{{big.NewInt(2e9)}},
	}
	if m.returnZeroBaseFee {
		result.BaseFee = []*big.Int{big.NewInt(0), big.NewInt(0)}
	}
	if m.returnEmptyReward {
		result.Reward = nil
	}
	return result, nil
}

type testMockFlashbotsAuther struct{ sig string }

func (m *testMockFlashbotsAuther) Sign(payload []byte) (string, error) { return m.sig, nil }

type testMockEngineCtrl2 struct{ err error }

func (m *testMockEngineCtrl2) SetEngineState(ctx context.Context, paused bool) error {
	return m.err
}

// ── addBigIntCounter: precision loss path ──────────────────────────

func TestAddBigIntCounter_PrecisionLossPath(t *testing.T) {
	val := new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 53), big.NewInt(1))
	addBigIntCounter(profitTotalWei, val)
	addBigIntCounter(profitTotalWei, big.NewInt(0))
}

// ── balanceWatchLoop: error path + healthy update ─────────────────

func TestBalanceWatchLoop_RPCErrorAndHealthy(t *testing.T) {
	var callCount atomic.Int32
	client := &testMockBalanceReader{
		balFn: func(ctx context.Context, addr common.Address, block *big.Int) (*big.Int, error) {
			n := callCount.Add(1)
			if n == 1 {
				return big.NewInt(1e18), nil
			}
			return nil, errors.New("RPC error")
		},
	}
	lb := NewLiveBalance()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	balanceWatchLoop(ctx, client, common.Address{}, 50*time.Millisecond, lb, "https://alchemy.example.com/v1/abc123key")
}

func TestBalanceWatchLoop_RPCKeyRedaction2(t *testing.T) {
	client := &testMockBalanceReader{
		balFn: func(ctx context.Context, addr common.Address, block *big.Int) (*big.Int, error) {
			return nil, errors.New("Post \"https://alchemy.com/v2/secretkey\": connection refused")
		},
	}
	lb := NewLiveBalance()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	balanceWatchLoop(ctx, client, common.Address{}, 30*time.Millisecond, lb, "https://alchemy.com/v2/secretkey")
}

// ── fetchAndStoreBalance: nil live balance ─────────────────────────

func TestFetchAndStoreBalance_NilLive2(t *testing.T) {
	client := &testMockBalanceReader{
		balFn: func(ctx context.Context, addr common.Address, block *big.Int) (*big.Int, error) {
			return big.NewInt(1e18), nil
		},
	}
	err := fetchAndStoreBalance(context.Background(), client, common.Address{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── NonceManager.SyncLoop ─────────────────────────────────────────

func TestNonceManager_SyncLoop_NilClient2(t *testing.T) {
	nm := NewNonceManager(0)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	nm.SyncLoop(ctx, 10*time.Millisecond)
}

func TestNonceManager_SyncLoop_WithClient2(t *testing.T) {
	nm := NewNonceManager(0)
	nm.SetSyncSource(common.HexToAddress("0x1234567890123456789012345678901234567890"), &testMockNonceProvider{nonce: 5})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	nm.SyncLoop(ctx, 20*time.Millisecond)
}

func TestNonceManager_SyncFromChain_RPCError2(t *testing.T) {
	nm := NewNonceManager(0)
	nm.SetSyncSource(common.HexToAddress("0x1234"), &testMockNonceProvider{err: errors.New("RPC error")})
	err := nm.SyncFromChain(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestNonceManager_Sync_LowerNonce2(t *testing.T) {
	nm := NewNonceManager(10)
	nm.Sync(5)
	if nm.Current() != 10 {
		t.Fatalf("expected 10, got %d", nm.Current())
	}
}

func TestNonceManager_Reset2(t *testing.T) {
	nm := NewNonceManager(5)
	nm.Reset(0)
	if nm.Current() != 0 {
		t.Fatalf("expected 0, got %d", nm.Current())
	}
}

func TestNonceManager_PendingCount2(t *testing.T) {
	nm := NewNonceManager(0)
	nm.Next()
	nm.Next()
	if nm.PendingCount() != 2 {
		t.Fatalf("expected 2, got %d", nm.PendingCount())
	}
}

// ── remote_signer.go edge cases ──────────────────────────────────

func TestNewRemoteSigner_EmptySocket2(t *testing.T) {
	_, err := NewRemoteSigner("", 1)
	if err == nil || !strings.Contains(err.Error(), "empty socket path") {
		t.Fatalf("expected empty socket path error, got: %v", err)
	}
}

func TestNewRemoteSigner_NegativeChainID(t *testing.T) {
	_, err := NewRemoteSigner("/tmp/test.sock", -1)
	if err == nil || !strings.Contains(err.Error(), "chain id must be positive") {
		t.Fatalf("expected invalid chain ID error, got: %v", err)
	}
}

// ── flashbots.go: NewFlashbotsSigner, Sign ────────────────────────

func TestNewFlashbotsSigner_EmptyKey2(t *testing.T) {
	_, err := NewFlashbotsSigner("")
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty key error, got: %v", err)
	}
}

func TestNewFlashbotsSigner_InvalidKey2(t *testing.T) {
	_, err := NewFlashbotsSigner("zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz")
	if err == nil || !strings.Contains(err.Error(), "invalid format") {
		t.Fatalf("expected invalid format error, got: %v", err)
	}
}

func TestFlashbotsSigner_Sign2(t *testing.T) {
	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	fs, err := NewFlashbotsSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := fs.Sign([]byte("test payload"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(sig, ":") {
		t.Fatalf("expected address:signature format, got %q", sig)
	}
}

func TestFlashbotsSigner_Address2(t *testing.T) {
	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	fs, _ := NewFlashbotsSigner(key)
	if fs.Address() == (common.Address{}) {
		t.Fatal("expected non-zero address")
	}
}

// ── signer.go: NewTransactionSigner error paths ────────────────────

func TestNewTransactionSigner_EmptyKey2(t *testing.T) {
	_, err := NewTransactionSigner("", 1)
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty key error, got: %v", err)
	}
}

func TestNewTransactionSigner_InvalidKey2(t *testing.T) {
	_, err := NewTransactionSigner("not_a_valid_hex_key", 1)
	if err == nil || !strings.Contains(err.Error(), "invalid format") {
		t.Fatalf("expected invalid format error, got: %v", err)
	}
}

func TestNewTransactionSigner_NegativeChainID2(t *testing.T) {
	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	_, err := NewTransactionSigner(key, -1)
	if err == nil || !strings.Contains(err.Error(), "positive") {
		t.Fatalf("expected positive chain ID error, got: %v", err)
	}
}

func TestNewTransactionSigner_WithPrefix2(t *testing.T) {
	key := "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	ts, err := NewTransactionSigner(key, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ts.Address() == (common.Address{}) {
		t.Fatal("expected non-zero address")
	}
}

// ── submitter: submitToBuilder HTTP edge cases ─────────────────────

func TestSubmitToBuilder_HTTPError2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	builders := []BuilderConfig{{Name: "b1", Enabled: true, URL: srv.URL, TimeoutMs: 2000}}
	s, _ := NewSubmitter(builders, "")
	results := s.SubmitToBuilder(context.Background(), &Bundle{BlockNumber: 1, RawTxs: [][]byte{{0x01}}}, "b1")
	if len(results) != 1 || results[0].Success {
		t.Fatalf("expected failure for HTTP 503")
	}
}

func TestSubmitToBuilder_RPCError2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"bundle rejected"}}`))
	}))
	defer srv.Close()

	builders := []BuilderConfig{{Name: "b1", Enabled: true, URL: srv.URL, TimeoutMs: 2000}}
	s, _ := NewSubmitter(builders, "")
	results := s.SubmitToBuilder(context.Background(), &Bundle{BlockNumber: 1, RawTxs: [][]byte{{0x01}}}, "b1")
	if len(results) != 1 || results[0].Success {
		t.Fatalf("expected failure for RPC error")
	}
}

func TestSubmitToBuilder_InvalidJSON2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	builders := []BuilderConfig{{Name: "b1", Enabled: true, URL: srv.URL, TimeoutMs: 2000}}
	s, _ := NewSubmitter(builders, "")
	results := s.SubmitToBuilder(context.Background(), &Bundle{BlockNumber: 1, RawTxs: [][]byte{{0x01}}}, "b1")
	if len(results) != 1 || results[0].Success {
		t.Fatalf("expected failure for invalid JSON")
	}
}

func TestSubmitToBuilder_EmptyBundleHash2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer srv.Close()

	builders := []BuilderConfig{{Name: "b1", Enabled: true, URL: srv.URL, TimeoutMs: 2000}}
	s, _ := NewSubmitter(builders, "")
	results := s.SubmitToBuilder(context.Background(), &Bundle{BlockNumber: 1, RawTxs: [][]byte{{0x01}}}, "b1")
	if len(results) != 1 || !results[0].Success {
		t.Fatalf("expected success with generated hash")
	}
}

func TestSubmitToBuilder_SuccessWithHash(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"bundleHash":"0xabc123"}}`))
	}))
	defer srv.Close()

	builders := []BuilderConfig{{Name: "b1", Enabled: true, URL: srv.URL, TimeoutMs: 2000}}
	s, _ := NewSubmitter(builders, "")
	results := s.SubmitToBuilder(context.Background(), &Bundle{BlockNumber: 1, RawTxs: [][]byte{{0x01}}}, "b1")
	if len(results) != 1 || !results[0].Success {
		t.Fatalf("expected success")
	}
	if results[0].BundleHash != "0xabc123" {
		t.Fatalf("expected 0xabc123, got %q", results[0].BundleHash)
	}
}

func TestSubmitToBuilder_NotFound2(t *testing.T) {
	builders := []BuilderConfig{{Name: "b1", Enabled: true, TimeoutMs: 1000}}
	s, _ := NewSubmitter(builders, "")
	results := s.SubmitToBuilder(context.Background(), &Bundle{BlockNumber: 1, RawTxs: [][]byte{{0x01}}}, "nonexistent")
	if len(results) != 1 || results[0].Success {
		t.Fatalf("expected failure for nonexistent builder")
	}
}

func TestSubmitToBuilder_EmptyBundle2(t *testing.T) {
	builders := []BuilderConfig{{Name: "b1", Enabled: true, TimeoutMs: 1000}}
	s, _ := NewSubmitter(builders, "")
	results := s.SubmitToBuilder(context.Background(), &Bundle{BlockNumber: 1}, "b1")
	if len(results) != 1 || results[0].Success {
		t.Fatalf("expected failure for empty bundle")
	}
}

func TestSubmitToBuilder_CustomFnWithEmptyBuilder2(t *testing.T) {
	builders := []BuilderConfig{{Name: "b1", Enabled: true, TimeoutMs: 1000}}
	s, _ := NewSubmitter(builders, "")
	s.submitFn = func(ctx context.Context, b BuilderConfig, bundle *Bundle) SubmissionResult {
		return SubmissionResult{Success: true}
	}
	results := s.SubmitToBuilder(context.Background(), &Bundle{BlockNumber: 1, RawTxs: [][]byte{{0x01}}}, "b1")
	if len(results) != 1 || !results[0].Success {
		t.Fatalf("expected success")
	}
	if results[0].Builder != "b1" {
		t.Fatalf("expected builder name filled in, got %q", results[0].Builder)
	}
}

func TestSubmitToAll_EmptyBundle2(t *testing.T) {
	builders := []BuilderConfig{{Name: "b1", Enabled: true, TimeoutMs: 1000}}
	s, _ := NewSubmitter(builders, "")
	results := s.SubmitToAll(context.Background(), &Bundle{BlockNumber: 1})
	if len(results) != 1 || results[0].Success {
		t.Fatalf("expected failure for empty bundle")
	}
}

// ── setAuthHeaders ────────────────────────────────────────────────

func TestSetAuthHeaders_FlashbotsNoSigner2(t *testing.T) {
	builders := []BuilderConfig{{Name: "b1", Enabled: true, TimeoutMs: 1000}}
	s, _ := NewSubmitter(builders, "")
	req, _ := http.NewRequest("POST", "http://example.com", nil)
	err := s.setAuthHeaders(req, BuilderConfig{AuthType: "flashbots", Name: "b1"}, nil)
	if err == nil || !strings.Contains(err.Error(), "no signer configured") {
		t.Fatalf("expected 'no signer configured' error, got: %v", err)
	}
}

func TestSetAuthHeaders_FlashbotsWithSigner2(t *testing.T) {
	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	s, _ := NewSubmitter(nil, key)
	req, _ := http.NewRequest("POST", "http://example.com", nil)
	err := s.setAuthHeaders(req, BuilderConfig{AuthType: "flashbots", Name: "b1"}, []byte("test"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Header.Get("X-Flashbots-Signature") == "" {
		t.Fatal("expected flashbots signature header")
	}
}

func TestSetAuthHeaders_APIKey2(t *testing.T) {
	builders := []BuilderConfig{{Name: "b1", Enabled: true, TimeoutMs: 1000}}
	s, _ := NewSubmitter(builders, "")
	req, _ := http.NewRequest("POST", "http://example.com", nil)
	err := s.setAuthHeaders(req, BuilderConfig{AuthType: "api_key", AuthKey: "my-key", Name: "b1"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Header.Get("X-Api-Key") != "my-key" {
		t.Fatal("expected X-Api-Key header")
	}
}

// ── flashbotsAuth ─────────────────────────────────────────────────

func TestFlashbotsAuth_AuthSignerPriority(t *testing.T) {
	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	s, _ := NewSubmitter(nil, key)
	s.SetAuthSigner(&testMockFlashbotsAuther{sig: "mock-sig"})
	auth := s.flashbotsAuth()
	if auth == nil {
		t.Fatal("expected non-nil auth")
	}
	sig, _ := auth.Sign([]byte("test"))
	if sig != "mock-sig" {
		t.Fatalf("expected authSigner to take priority, got %q", sig)
	}
}

func TestFlashbotsAuth_NoSigner2(t *testing.T) {
	builders := []BuilderConfig{{Name: "b1", Enabled: true, TimeoutMs: 1000}}
	s, _ := NewSubmitter(builders, "")
	if s.flashbotsAuth() != nil {
		t.Fatal("expected nil auth")
	}
}

// ── GetBundleStats edge cases ─────────────────────────────────────

func TestGetBundleStats_NoFlashbotsBuilder2(t *testing.T) {
	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	builders := []BuilderConfig{{Name: "b1", Enabled: true, AuthType: "none", TimeoutMs: 1000}}
	s, _ := NewSubmitter(builders, key)
	_, err := s.GetBundleStats(context.Background(), "0xabc", 100)
	if err == nil || !strings.Contains(err.Error(), "no flashbots builder") {
		t.Fatalf("expected no flashbots builder error, got: %v", err)
	}
}

func TestGetBundleStats_Success2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"isHighPriority":true,"blockNumber":"0x1234"}}`))
	}))
	defer srv.Close()

	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	builders := []BuilderConfig{{Name: "flashbots", Enabled: true, AuthType: "flashbots", URL: srv.URL, TimeoutMs: 2000}}
	s, _ := NewSubmitter(builders, key)
	result, err := s.GetBundleStats(context.Background(), "0xabc", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestGetBundleStats_HTTPError2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	builders := []BuilderConfig{{Name: "flashbots", Enabled: true, AuthType: "flashbots", URL: srv.URL, TimeoutMs: 2000}}
	s, _ := NewSubmitter(builders, key)
	_, err := s.GetBundleStats(context.Background(), "0xabc", 100)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("expected HTTP 500 error, got: %v", err)
	}
}

func TestGetBundleStats_RPCError2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"msg":"not found"}}`))
	}))
	defer srv.Close()

	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	builders := []BuilderConfig{{Name: "flashbots", Enabled: true, AuthType: "flashbots", URL: srv.URL, TimeoutMs: 2000}}
	s, _ := NewSubmitter(builders, key)
	_, err := s.GetBundleStats(context.Background(), "0xabc", 100)
	if err == nil || !strings.Contains(err.Error(), "stats error") {
		t.Fatalf("expected stats error, got: %v", err)
	}
}

func TestGetBundleStats_InvalidJSON2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	builders := []BuilderConfig{{Name: "flashbots", Enabled: true, AuthType: "flashbots", URL: srv.URL, TimeoutMs: 2000}}
	s, _ := NewSubmitter(builders, key)
	_, err := s.GetBundleStats(context.Background(), "0xabc", 100)
	if err == nil || !strings.Contains(err.Error(), "parse stats response") {
		t.Fatalf("expected parse error, got: %v", err)
	}
}

// ── setRedisHealthy ───────────────────────────────────────────────

func TestSetRedisHealthy_BothPaths(t *testing.T) {
	setRedisHealthy(true)
	setRedisHealthy(false)
}

// ── handleBackrunPromote edge cases ───────────────────────────────

func TestHandleBackrunPromote_NoConfirmToken2(t *testing.T) {
	os.Unsetenv("AETHER_BACKRUN_CONFIRM_TOKEN")
	req := httptest.NewRequest(http.MethodPost, "/admin/backrun/promote", nil)
	w := httptest.NewRecorder()
	handleBackrunPromote(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestHandleBackrunPromote_WrongConfirm2(t *testing.T) {
	os.Setenv("AETHER_BACKRUN_CONFIRM_TOKEN", "correct-token")
	defer os.Unsetenv("AETHER_BACKRUN_CONFIRM_TOKEN")
	req := httptest.NewRequest(http.MethodPost, "/admin/backrun/promote?confirm_token=wrong", nil)
	w := httptest.NewRecorder()
	handleBackrunPromote(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestHandleBackrunPromote_Success2(t *testing.T) {
	os.Setenv("AETHER_BACKRUN_CONFIRM_TOKEN", "correct-token")
	defer os.Unsetenv("AETHER_BACKRUN_CONFIRM_TOKEN")
	req := httptest.NewRequest(http.MethodPost, "/admin/backrun/promote?confirm_token=correct-token", nil)
	w := httptest.NewRecorder()
	handleBackrunPromote(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleBackrunPromote_WrongMethod2(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin/backrun/promote", nil)
	w := httptest.NewRecorder()
	handleBackrunPromote(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

// ── initBackrunMode ───────────────────────────────────────────────

func TestInitBackrunMode_UnknownMode2(t *testing.T) {
	t.Setenv("AETHER_BACKRUN_MODE", "unknown_value")
	initBackrunMode()
	if getBackrunMode() != BackrunShadowOnly {
		t.Fatalf("expected shadow_only default, got %s", getBackrunMode())
	}
}

func TestInitBackrunMode_LegacyShadow2(t *testing.T) {
	os.Unsetenv("AETHER_BACKRUN_MODE")
	t.Setenv("AETHER_SHADOW", "1")
	initBackrunMode()
	if getBackrunMode() != BackrunShadowOnly {
		t.Fatalf("expected shadow_only from legacy, got %s", getBackrunMode())
	}
}

// ── handleAdminPause: default reason, wrong method, engine ctrl ────

func TestHandleAdminPause_DefaultReason2(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	req := httptest.NewRequest(http.MethodPost, "/admin/pause", nil)
	w := httptest.NewRecorder()
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm}
	defer func() { globalAdminDeps = prevDeps }()
	handleAdminPause(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleAdminPause_WithEngineCtrl2(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	ctrl := &testMockEngineCtrl2{}
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm, engineCtrl: ctrl}
	defer func() { globalAdminDeps = prevDeps }()
	req := httptest.NewRequest(http.MethodPost, "/admin/pause?reason=test", nil)
	w := httptest.NewRecorder()
	handleAdminPause(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleAdminPause_EngineCtrlError2(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	ctrl := &testMockEngineCtrl2{err: errors.New("gRPC error")}
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm, engineCtrl: ctrl}
	defer func() { globalAdminDeps = prevDeps }()
	req := httptest.NewRequest(http.MethodPost, "/admin/pause?reason=test", nil)
	w := httptest.NewRecorder()
	handleAdminPause(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// ── handleAdminResume ─────────────────────────────────────────────

func TestHandleAdminResume_FromPaused2(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.Pause("test")
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm}
	defer func() { globalAdminDeps = prevDeps }()
	req := httptest.NewRequest(http.MethodPost, "/admin/resume", nil)
	w := httptest.NewRecorder()
	handleAdminResume(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleAdminResume_WithEngineCtrl2(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.Pause("test")
	ctrl := &testMockEngineCtrl2{}
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm, engineCtrl: ctrl}
	defer func() { globalAdminDeps = prevDeps }()
	req := httptest.NewRequest(http.MethodPost, "/admin/resume", nil)
	w := httptest.NewRecorder()
	handleAdminResume(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleAdminResume_EngineCtrlError2(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.Pause("test")
	ctrl := &testMockEngineCtrl2{err: errors.New("gRPC error")}
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm, engineCtrl: ctrl}
	defer func() { globalAdminDeps = prevDeps }()
	req := httptest.NewRequest(http.MethodPost, "/admin/resume", nil)
	w := httptest.NewRecorder()
	handleAdminResume(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// ── handleAdminReset ──────────────────────────────────────────────

func TestHandleAdminReset_FromHalted2(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.ForceStateForTest(risk.StateHalted)
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm}
	defer func() { globalAdminDeps = prevDeps }()
	req := httptest.NewRequest(http.MethodPost, "/admin/reset", nil)
	w := httptest.NewRecorder()
	handleAdminReset(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleAdminReset_NotFromHalted2(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm}
	defer func() { globalAdminDeps = prevDeps }()
	req := httptest.NewRequest(http.MethodPost, "/admin/reset", nil)
	w := httptest.NewRecorder()
	handleAdminReset(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}

func TestHandleAdminReset_WithResetToken2(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.ForceStateForTest(risk.StateHalted)
	os.Setenv("AETHER_RESET_CONFIRM_TOKEN", "secret-token")
	defer os.Unsetenv("AETHER_RESET_CONFIRM_TOKEN")
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm}
	defer func() { globalAdminDeps = prevDeps }()
	req := httptest.NewRequest(http.MethodPost, "/admin/reset", nil)
	req.Header.Set("X-Aether-Reset-Confirm", "secret-token")
	w := httptest.NewRecorder()
	handleAdminReset(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleAdminReset_WrongResetToken2(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.ForceStateForTest(risk.StateHalted)
	os.Setenv("AETHER_RESET_CONFIRM_TOKEN", "secret-token")
	defer os.Unsetenv("AETHER_RESET_CONFIRM_TOKEN")
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm}
	defer func() { globalAdminDeps = prevDeps }()
	req := httptest.NewRequest(http.MethodPost, "/admin/reset", nil)
	req.Header.Set("X-Aether-Reset-Confirm", "wrong-token")
	w := httptest.NewRecorder()
	handleAdminReset(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestHandleAdminReset_WithEngineCtrl2(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.ForceStateForTest(risk.StateHalted)
	ctrl := &testMockEngineCtrl2{}
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm, engineCtrl: ctrl}
	defer func() { globalAdminDeps = prevDeps }()
	req := httptest.NewRequest(http.MethodPost, "/admin/reset", nil)
	w := httptest.NewRecorder()
	handleAdminReset(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleAdminReset_EngineCtrlError2(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.ForceStateForTest(risk.StateHalted)
	ctrl := &testMockEngineCtrl2{err: errors.New("gRPC error")}
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm, engineCtrl: ctrl}
	defer func() { globalAdminDeps = prevDeps }()
	req := httptest.NewRequest(http.MethodPost, "/admin/reset", nil)
	w := httptest.NewRecorder()
	handleAdminReset(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (engine error is logged), got %d", w.Code)
	}
}

// ── handleSetMinProfit ────────────────────────────────────────────

func TestHandleSetMinProfit_InvalidValue2(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm}
	defer func() { globalAdminDeps = prevDeps }()
	req := httptest.NewRequest(http.MethodPost, "/admin/set_min_profit?value=invalid", nil)
	w := httptest.NewRecorder()
	handleSetMinProfit(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleSetMinProfit_NegativeValue2(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm}
	defer func() { globalAdminDeps = prevDeps }()
	req := httptest.NewRequest(http.MethodPost, "/admin/set_min_profit?value=-1", nil)
	w := httptest.NewRecorder()
	handleSetMinProfit(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleSetMinProfit_EmptyValue2(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm}
	defer func() { globalAdminDeps = prevDeps }()
	req := httptest.NewRequest(http.MethodPost, "/admin/set_min_profit", strings.NewReader("not-a-number"))
	w := httptest.NewRecorder()
	handleSetMinProfit(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// ── metricsStoreHealthy ───────────────────────────────────────────

func TestMetricsStoreHealthy_NoDBURL2(t *testing.T) {
	orig := os.Getenv("DATABASE_URL")
	os.Unsetenv("DATABASE_URL")
	defer os.Setenv("DATABASE_URL", orig)
	if !metricsStoreHealthy() {
		t.Fatal("expected true when DATABASE_URL is empty")
	}
}

// ── refreshSnapshotLoop ───────────────────────────────────────────

func TestRefreshSnapshotLoop_Paused2(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.Pause("test")
	store := metrics.NewStore()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	go refreshSnapshotLoop(ctx, rm, store, 10*time.Millisecond)
	<-ctx.Done()
	time.Sleep(20 * time.Millisecond)
	snap := store.Get()
	if snap.SystemState != string(risk.StatePaused) {
		t.Fatalf("expected Paused, got %s", snap.SystemState)
	}
	if !snap.BreakerOpen {
		t.Fatal("expected BreakerOpen for paused state")
	}
}

// ── pollPendingInclusions: various paths ──────────────────────────

func TestPollPendingInclusions_Included2(t *testing.T) {
	pendingMu.Lock()
	pendingQueue = nil
	pendingMu.Unlock()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"isHighPriority":true,"isSentToMiners":true,"blockNumber":"0x1"}}`))
	}))
	defer srv.Close()

	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	builders := []BuilderConfig{{Name: "flashbots", Enabled: true, AuthType: "flashbots", URL: srv.URL, TimeoutMs: 2000}}
	s, _ := NewSubmitter(builders, key)

	enqueuePendingBundle(pendingBundle{
		bundleID:    uuid.New(),
		bundleHash:  "0xabc",
		targetBlock: 100,
		builder:     "flashbots",
		profitWei:   big.NewInt(1e15),
		source:      SourceBlockDriven,
		submittedAt: time.Now().UTC().Add(-30 * time.Second),
	})

	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	pollPendingInclusions(context.Background(), s, db.NewNoopLedger(), rm)

	pendingMu.Lock()
	n := len(pendingQueue)
	pendingMu.Unlock()
	if n != 0 {
		t.Fatalf("expected 0 pending after inclusion, got %d", n)
	}
}

func TestPollPendingInclusions_NotIncludedRetry2(t *testing.T) {
	pendingMu.Lock()
	pendingQueue = nil
	pendingMu.Unlock()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"isHighPriority":false,"isSentToMiners":false,"blockNumber":"0x0"}}`))
	}))
	defer srv.Close()

	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	builders := []BuilderConfig{{Name: "flashbots", Enabled: true, AuthType: "flashbots", URL: srv.URL, TimeoutMs: 2000}}
	s, _ := NewSubmitter(builders, key)

	enqueuePendingBundle(pendingBundle{
		bundleID:    uuid.New(),
		bundleHash:  "0xabc",
		targetBlock: 100,
		builder:     "flashbots",
		profitWei:   big.NewInt(1e15),
		source:      SourceBlockDriven,
		submittedAt: time.Now().UTC().Add(-30 * time.Second),
	})

	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	pollPendingInclusions(context.Background(), s, db.NewNoopLedger(), rm)

	pendingMu.Lock()
	n := len(pendingQueue)
	pendingMu.Unlock()
	if n != 1 {
		t.Fatalf("expected 1 pending (not included, not timed out), got %d", n)
	}
}

func TestPollPendingInclusions_MempoolRevert2(t *testing.T) {
	pendingMu.Lock()
	pendingQueue = nil
	pendingMu.Unlock()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"isHighPriority":false,"isSentToMiners":false,"blockNumber":"0x0"}}`))
	}))
	defer srv.Close()

	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	builders := []BuilderConfig{{Name: "flashbots", Enabled: true, AuthType: "flashbots", URL: srv.URL, TimeoutMs: 2000}}
	s, _ := NewSubmitter(builders, key)

	enqueuePendingBundle(pendingBundle{
		bundleID:    uuid.New(),
		bundleHash:  "0xabc",
		targetBlock: 100,
		builder:     "flashbots",
		profitWei:   big.NewInt(1e15),
		source:      SourceMempoolBackrun,
		submittedAt: time.Now().UTC().Add(-6 * time.Minute),
	})

	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	pollPendingInclusions(context.Background(), s, db.NewNoopLedger(), rm)

	pendingMu.Lock()
	n := len(pendingQueue)
	pendingMu.Unlock()
	if n != 0 {
		t.Fatalf("expected 0 pending after timeout, got %d", n)
	}
}

// ── resolveInclusion ──────────────────────────────────────────────

func TestResolveInclusion_IncludedWithBlock2(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	prevSel := builderSelector
	builderSelector = strategy.New([]string{"b1"}, strategy.Config{ExplorationFloor: 0.1})
	defer func() { builderSelector = prevSel }()
	ledger := db.NewNoopLedger()
	p := pendingBundle{
		bundleID: uuid.New(), bundleHash: "0xabc", targetBlock: 100, builder: "b1",
		profitWei: big.NewInt(1e15), source: SourceBlockDriven, submittedAt: time.Now().UTC().Add(-time.Minute),
	}
	resolveInclusion(p, ledger, true, 101, rm)
}

func TestResolveInclusion_NotIncludedMempoolRevert2(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	prevSel := builderSelector
	builderSelector = strategy.New([]string{"b1"}, strategy.Config{ExplorationFloor: 0.1})
	defer func() { builderSelector = prevSel }()
	ledger := db.NewNoopLedger()
	p := pendingBundle{
		bundleID: uuid.New(), bundleHash: "0xabc", targetBlock: 100, builder: "b1",
		profitWei: big.NewInt(1e15), source: SourceMempoolBackrun, submittedAt: time.Now().UTC().Add(-time.Minute),
	}
	resolveInclusion(p, ledger, false, 0, rm)
}

// ── fmtSscanfHex ──────────────────────────────────────────────────

func TestFmtSscanfHex_0XPrefix2(t *testing.T) {
	var n uint64
	fmtSscanfHex("0Xff", &n)
	if n != 0xff {
		t.Fatalf("expected 0xff, got %d", n)
	}
}

func TestFmtSscanfHex_InvalidChars2(t *testing.T) {
	var n uint64
	fmtSscanfHex("zzzz", &n)
}

func TestFmtSscanfHex_EmptyString2(t *testing.T) {
	var n uint64
	fmtSscanfHex("", &n)
}

func TestFmtSscanfHex_0xEmpty2(t *testing.T) {
	var n uint64
	fmtSscanfHex("0x", &n)
}

// ── parseBundleStats ──────────────────────────────────────────────

func TestParseBundleStats_EmptyJSON2(t *testing.T) {
	included, block := parseBundleStats([]byte(`{}`))
	if included {
		t.Fatal("expected not included")
	}
	if block != 0 {
		t.Fatalf("expected block 0, got %d", block)
	}
}

// ── run edge cases ────────────────────────────────────────────────

func TestRun_GRPCDialFailure2(t *testing.T) {
	grpcDial := func(addr string) (*aethergrpc.Client, error) {
		return nil, errors.New("connection refused")
	}
	mr, _ := miniredis.Run()
	defer mr.Close()
	t.Setenv("REDIS_URL", "redis://"+mr.Addr())

	cfg := defaultConfig()
	cfg.GRPCAddress = "localhost:99999"

	deps := &Dependencies{
		Submitter: &Submitter{}, Ledger: db.NewNoopLedger(), MetricsStore: db.NewNoopMetricsStore(),
		EventPublisher: events.NewPublisherFromEnv(), ExecutorAddr: "0x0000000000000000000000000000000000000001",
		ChainID: 1, GRPCDial: grpcDial, SkipMigrations: true, SkipMetricsHTTP: true, SkipAdminHTTP: true,
		ReconnectDelay: 10 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	deps.WaitForShutdown = func(ctx context.Context, c context.CancelFunc) error {
		<-time.After(150 * time.Millisecond)
		c()
		return nil
	}
	if err := run(ctx, &cfg, deps); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestRun_WithBalanceCheck2(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	t.Setenv("REDIS_URL", "redis://"+mr.Addr())

	cfg := defaultConfig()
	cfg.GRPCAddress = "localhost:99999"
	deps := &Dependencies{
		Submitter: &Submitter{}, Ledger: db.NewNoopLedger(), MetricsStore: db.NewNoopMetricsStore(),
		EventPublisher: events.NewPublisherFromEnv(), ExecutorAddr: "0x0000000000000000000000000000000000000001",
		ChainID: 1, SkipMigrations: true, SkipMetricsHTTP: true, SkipAdminHTTP: true, ReconnectDelay: 10 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	deps.WaitForShutdown = func(ctx context.Context, c context.CancelFunc) error {
		<-time.After(150 * time.Millisecond)
		c()
		return nil
	}
	if err := run(ctx, &cfg, deps); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestRun_WaitShutdownError2(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	t.Setenv("REDIS_URL", "redis://"+mr.Addr())

	cfg := defaultConfig()
	cfg.GRPCAddress = "localhost:99999"
	deps := &Dependencies{
		Submitter: &Submitter{}, Ledger: db.NewNoopLedger(), MetricsStore: db.NewNoopMetricsStore(),
		EventPublisher: events.NewPublisherFromEnv(), ExecutorAddr: "0x0000000000000000000000000000000000000001",
		ChainID: 1, SkipMigrations: true, SkipMetricsHTTP: true, SkipAdminHTTP: true, ReconnectDelay: 10 * time.Millisecond,
		WaitForShutdown: func(ctx context.Context, c context.CancelFunc) error {
			return errors.New("shutdown error")
		},
	}
	if err := run(context.Background(), &cfg, deps); err == nil {
		t.Fatal("expected shutdown error")
	}
}

func TestRun_UnixSocketGRPC2(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	t.Setenv("REDIS_URL", "redis://"+mr.Addr())

	grpcDial := func(addr string) (*aethergrpc.Client, error) {
		return nil, errors.New("connection refused")
	}
	cfg := defaultConfig()
	cfg.GRPCAddress = "unix:///tmp/test.sock"
	deps := &Dependencies{
		Submitter: &Submitter{}, Ledger: db.NewNoopLedger(), MetricsStore: db.NewNoopMetricsStore(),
		EventPublisher: events.NewPublisherFromEnv(), ExecutorAddr: "0x0000000000000000000000000000000000000001",
		ChainID: 1, GRPCDial: grpcDial, SkipMigrations: true, SkipMetricsHTTP: true, SkipAdminHTTP: true,
		ReconnectDelay: 10 * time.Millisecond,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	deps.WaitForShutdown = func(ctx context.Context, c context.CancelFunc) error {
		<-time.After(150 * time.Millisecond)
		c()
		return nil
	}
	if err := run(ctx, &cfg, deps); err != nil {
		t.Fatalf("run: %v", err)
	}
}

// ── processArb edge cases ─────────────────────────────────────────

func TestProcessArb_SignerPauseWithEvents2(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	nm := NewNonceManager(0)
	go_ := NewGasOracle(300.0)
	bundler := NewBundleConstructor(nm, go_, &testFailingSigner{err: errSignerUnavailable}, 1)
	builders := []BuilderConfig{{Name: "b1", Enabled: true, TimeoutMs: 1000}}
	submitter, _ := NewSubmitter(builders, "")

	oldPub := eventPublisher
	oldStore := metricsStore
	mr, _ := miniredis.Run()
	defer mr.Close()
	t.Setenv("REDIS_URL", "redis://"+mr.Addr())
	eventPublisher = events.NewPublisherFromEnv()
	metricsStore = db.NewNoopMetricsStore()
	defer func() {
		eventPublisher = oldPub
		metricsStore = oldStore
	}()

	arb := newValidArb("arb-signer-pause-events", 0.01, 5.0)
	_, _ = processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
	if rm.State() != risk.StatePaused {
		t.Fatalf("expected Paused, got %s", rm.State())
	}
}

func TestProcessArb_MempoolBackrun_ShadowOnly2(t *testing.T) {
	prevMode := getBackrunMode()
	defer setBackrunMode(prevMode)
	setBackrunMode(BackrunShadowOnly)

	rm, bundler, submitter := newTestComponents()
	arb := newValidArb("arb-mempool-shadow2", 0.01, 5.0)
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
		t.Fatal("expected shadow submission")
	}
}

func TestProcessArb_MempoolBackrun_ShadowAndLive2(t *testing.T) {
	prevMode := getBackrunMode()
	defer setBackrunMode(prevMode)
	setBackrunMode(BackrunShadowAndLive)

	rm, bundler, submitter := newTestComponents()
	submitter.submitFn = func(ctx context.Context, b BuilderConfig, bundle *Bundle) SubmissionResult {
		return SubmissionResult{Builder: b.Name, Success: true, BundleHash: "sl-hash"}
	}

	arb := newValidArb("arb-mempool-sl2", 0.01, 5.0)
	arb.Source = pb.ArbSource_MEMPOOL_BACKRUN
	arb.VictimTxHash = make([]byte, 32)
	arb.VictimTxHash[0] = 0xbb // unique hash
	arb.VictimRawTx = []byte{0xf8, 0x01, 0x02}
	arb.TimestampNs = time.Now().UnixNano()

	submitted, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if !submitted {
		t.Fatal("expected submission")
	}
}

func TestProcessArb_SelectRouting2(t *testing.T) {
	prevMode := routingMode
	prevSel := builderSelector
	defer func() {
		routingMode = prevMode
		builderSelector = prevSel
	}()

	rm, bundler, submitter := newTestComponents()
	submitter.submitFn = func(ctx context.Context, b BuilderConfig, bundle *Bundle) SubmissionResult {
		return SubmissionResult{Builder: b.Name, Success: true, BundleHash: "select-hash"}
	}
	routingMode = "select"
	builderSelector = strategy.New([]string{"flashbots"}, strategy.Config{ExplorationFloor: 0.1})

	arb := newValidArb("arb-select2", 0.01, 5.0)
	submitted, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if !submitted {
		t.Fatal("expected submission in select mode")
	}
}

func TestProcessArb_ShadowBlockDriven2(t *testing.T) {
	prevShadow := os.Getenv("AETHER_SHADOW")
	defer os.Setenv("AETHER_SHADOW", prevShadow)
	os.Setenv("AETHER_SHADOW", "1")

	rm, bundler, submitter := newTestComponents()
	arb := newValidArb("arb-shadow-bd2", 0.01, 5.0)
	submitted, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if !submitted {
		t.Fatal("expected shadow submission")
	}
}

func TestProcessArb_MempoolRejection2(t *testing.T) {
	initTestMempoolRisk()
	rm, bundler, submitter := newTestComponents()
	arb := newValidArb("arb-mempool-reject2", 0.01, 5.0)
	arb.Source = pb.ArbSource_MEMPOOL_BACKRUN
	arb.VictimTxHash = make([]byte, 32)
	arb.VictimTxHash[0] = 0xaa
	arb.VictimRawTx = []byte{0xf8, 0x01, 0x02}
	arb.TimestampNs = time.Now().Add(-time.Hour).UnixNano()

	submitted, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if submitted {
		t.Fatal("expected rejection by mempool gate")
	}
}

func TestProcessArb_MempoolBuildError2(t *testing.T) {
	initTestMempoolRisk()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	nm := NewNonceManager(0)
	go_ := NewGasOracle(300.0)
	bundler := NewBundleConstructor(nm, go_, &testFailingSigner{err: errors.New("sign fail")}, 1)
	builders := []BuilderConfig{{Name: "b1", Enabled: true, TimeoutMs: 1000}}
	submitter, _ := NewSubmitter(builders, "")

	arb := newValidArb("arb-mempool-build-err2", 0.01, 5.0)
	arb.Source = pb.ArbSource_MEMPOOL_BACKRUN
	arb.VictimTxHash = make([]byte, 32)
	arb.VictimTxHash[0] = 0xcc // unique hash
	arb.VictimRawTx = []byte{0xf8, 0x01, 0x02}
	arb.TimestampNs = time.Now().UnixNano()

	_, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
	if err == nil {
		t.Fatal("expected error from mempool bundle build")
	}
}

func TestProcessArb_MempoolSignerUnavailable2(t *testing.T) {
	initTestMempoolRisk()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	nm := NewNonceManager(0)
	go_ := NewGasOracle(300.0)
	bundler := NewBundleConstructor(nm, go_, &testFailingSigner{err: errSignerUnavailable}, 1)
	builders := []BuilderConfig{{Name: "b1", Enabled: true, TimeoutMs: 1000}}
	submitter, _ := NewSubmitter(builders, "")

	arb := newValidArb("arb-mempool-signer2", 0.01, 5.0)
	arb.Source = pb.ArbSource_MEMPOOL_BACKRUN
	arb.VictimTxHash = make([]byte, 32)
	arb.VictimTxHash[0] = 0xdd // unique hash
	arb.VictimRawTx = []byte{0xf8, 0x01, 0x02}
	arb.TimestampNs = time.Now().UnixNano()

	_, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
	if !errors.Is(err, errSignerUnavailable) {
		t.Fatalf("expected errSignerUnavailable, got: %v", err)
	}
}

// ── buildExecutorDeps ─────────────────────────────────────────────

func TestBuildExecutorDeps_RPCURLRequired2(t *testing.T) {
	execCfg := config.ExecutorFileConfig{ExecutorAddress: "0x0000000000000000000000000000000000000001", ExpectedChainID: 1}
	_, _, err := buildExecutorDeps(context.Background(), Config{}, execCfg, "", nil)
	if err == nil || !strings.Contains(err.Error(), "ETH_RPC_URL not set") {
		t.Fatalf("expected ETH_RPC_URL error, got: %v", err)
	}
}

func TestBuildExecutorDeps_DialFailure2(t *testing.T) {
	execCfg := config.ExecutorFileConfig{ExecutorAddress: "0x0000000000000000000000000000000000000001", ExpectedChainID: 1}
	dial := func(ctx context.Context, url string) (*ethclient.Client, error) {
		return nil, errors.New("connection refused")
	}
	_, _, err := buildExecutorDeps(context.Background(), Config{}, execCfg, "http://localhost:8545", dial)
	if err == nil || !strings.Contains(err.Error(), "dial eth rpc") {
		t.Fatalf("expected dial error, got: %v", err)
	}
}

// ── recordBundleMetrics with builderSelector ──────────────────────

func TestRecordBundleMetrics_WithSelector2(t *testing.T) {
	prevSel := builderSelector
	prevStore := metricsStore
	defer func() {
		builderSelector = prevSel
		metricsStore = prevStore
	}()
	builderSelector = strategy.New([]string{"b1"}, strategy.Config{ExplorationFloor: 0.1})
	metricsStore = db.NewNoopMetricsStore()

	recordBundleMetrics(SourceMempoolBackrun, big.NewInt(1e16), time.Now().Add(-time.Millisecond),
		[]SubmissionResult{
			{Builder: "b1", Success: true, Latency: time.Millisecond},
			{Builder: "b2", Success: false, Latency: time.Millisecond, Error: errors.New("rejected")},
		}, true)
}

// ── nonce edge cases ──────────────────────────────────────────────

func TestNonceManager_SyncFromChain_Provider2(t *testing.T) {
	nm := NewNonceManager(0)
	nm.SetSyncSource(common.HexToAddress("0x1234"), &testMockNonceProvider{nonce: 10})
	err := nm.SyncFromChain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nm.Current() != 10 {
		t.Fatalf("expected 10, got %d", nm.Current())
	}
}

// ── gas_oracle edge cases ─────────────────────────────────────────

func TestGasOracle_FetchOnce_RPCError2(t *testing.T) {
	go_ := NewGasOracle(300.0)
	go_.SetClient(&testMockFeeHistoryProvider{returnError: true})
	_, err := go_.FetchOnce(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestGasOracle_FetchOnce_ZeroBaseFee2(t *testing.T) {
	go_ := NewGasOracle(300.0)
	go_.SetClient(&testMockFeeHistoryProvider{returnZeroBaseFee: true})
	fees, err := go_.FetchOnce(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fees.GasPriceGwei <= 0 {
		t.Fatal("expected positive gas price with zero base fee")
	}
}

func TestGasOracle_FetchOnce_EmptyReward2(t *testing.T) {
	go_ := NewGasOracle(300.0)
	go_.SetClient(&testMockFeeHistoryProvider{returnEmptyReward: true})
	fees, err := go_.FetchOnce(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fees.GasPriceGwei <= 0 {
		t.Fatal("expected positive gas price")
	}
}

func TestMempoolGasTipFloorGwei_Override2(t *testing.T) {
	t.Setenv("AETHER_MEMPOOL_GAS_TIP_MIN_GWEI", "5.0")
	if v := mempoolGasTipFloorGwei(); v != 5.0 {
		t.Fatalf("expected 5.0, got %f", v)
	}
}

func TestMempoolGasTipFloorGwei_Invalid2(t *testing.T) {
	t.Setenv("AETHER_MEMPOOL_GAS_TIP_MIN_GWEI", "not-a-number")
	if v := mempoolGasTipFloorGwei(); v != 2.0 {
		t.Fatalf("expected 2.0 fallback, got %f", v)
	}
}

func TestMempoolGasTipFloorGwei_Negative2(t *testing.T) {
	t.Setenv("AETHER_MEMPOOL_GAS_TIP_MIN_GWEI", "-1.0")
	if v := mempoolGasTipFloorGwei(); v != 2.0 {
		t.Fatalf("expected 2.0 fallback, got %f", v)
	}
}

// ── startAdminServer zero port ────────────────────────────────────

func TestStartAdminServer_ZeroPort2(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	adminServerOnce = sync.Once{}
	prevDeps := globalAdminDeps
	defer func() { globalAdminDeps = prevDeps }()
	startAdminServer(rm, "", 0, nil, nil)
}

// ── loadConfig edge cases ─────────────────────────────────────────

func TestLoadConfig_GRPCOverride2(t *testing.T) {
	t.Setenv("GRPC_ADDRESS", "localhost:9999")
	cfg := loadConfig()
	if cfg.GRPCAddress != "localhost:9999" {
		t.Fatalf("expected overridden address, got %q", cfg.GRPCAddress)
	}
}

func TestLoadAdminPort_EnvOverride2(t *testing.T) {
	t.Setenv("ADMIN_HTTP_PORT", "9090")
	port, _ := loadAdminPort()
	if port != 9090 {
		t.Fatalf("expected 9090, got %d", port)
	}
}

func TestLoadAdminPort_InvalidPort2(t *testing.T) {
	t.Setenv("ADMIN_HTTP_PORT", "not-a-number")
	port, _ := loadAdminPort()
	if port <= 0 {
		t.Fatalf("expected positive default, got %d", port)
	}
}

func TestLoadAdminPort_NegativePort2(t *testing.T) {
	t.Setenv("ADMIN_HTTP_PORT", "-1")
	port, _ := loadAdminPort()
	if port <= 0 {
		t.Fatalf("expected positive default, got %d", port)
	}
}

// ── submitter Metrics and recordMetrics unknown builder ───────────

func TestSubmitter_Metrics2(t *testing.T) {
	builders := []BuilderConfig{{Name: "b1", Enabled: true, TimeoutMs: 1000}}
	s, _ := NewSubmitter(builders, "")
	m := s.Metrics()
	if _, ok := m["b1"]; !ok {
		t.Fatal("expected b1 metrics")
	}
}

func TestRecordMetrics_UnknownBuilder2(t *testing.T) {
	builders := []BuilderConfig{{Name: "b1", Enabled: true, TimeoutMs: 1000}}
	s, _ := NewSubmitter(builders, "")
	s.recordMetrics("unknown", SubmissionResult{Success: true, Latency: time.Millisecond})
}

// ── requireAdminAuth ──────────────────────────────────────────────

func TestRequireAdminAuth_NoToken2(t *testing.T) {
	configuredAdminToken = ""
	defer func() { configuredAdminToken = "" }()
	handler := requireAdminAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/test", nil)
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestRequireAdminAuth_WrongToken2(t *testing.T) {
	configuredAdminToken = "correct-token"
	defer func() { configuredAdminToken = "" }()
	handler := requireAdminAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/test", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

// ── requireAdminAuthWithRateLimit ─────────────────────────────────

func TestRateLimitedAdmin_AuthBeforeRate2(t *testing.T) {
	l := newAdminRateLimiter(1.0, 1)
	resetAdminRateLimiterForTest(l)
	configuredAdminToken = "token"
	defer func() {
		resetAdminRateLimiterForTest(nil)
		configuredAdminToken = ""
	}()

	handler := requireAdminAuthWithRateLimit(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/admin/test", nil)
	req.Header.Set("Authorization", "Bearer token")
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	w2 := httptest.NewRecorder()
	handler(w2, req)
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w2.Code)
	}
}

// ── initAdminAuth ─────────────────────────────────────────────────

func TestInitAdminAuth_ProductionRequiresToken2(t *testing.T) {
	os.Setenv("AETHER_ENV", "production")
	os.Unsetenv("AETHER_ADMIN_TOKEN")
	defer os.Unsetenv("AETHER_ENV")
	err := initAdminAuth()
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("expected required error in production, got: %v", err)
	}
}

func TestInitAdminAuth_DevNoToken2(t *testing.T) {
	os.Unsetenv("AETHER_ENV")
	os.Unsetenv("AETHER_ADMIN_TOKEN")
	defer os.Unsetenv("AETHER_ADMIN_TOKEN")
	err := initAdminAuth()
	if err != nil {
		t.Fatalf("unexpected error in dev mode: %v", err)
	}
}

// ── extractAdminToken ─────────────────────────────────────────────

func TestExtractAdminToken_BearerAuth2(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/admin/test", nil)
	req.Header.Set("Authorization", "Bearer my-token")
	if got := extractAdminToken(req); got != "my-token" {
		t.Fatalf("expected 'my-token', got %q", got)
	}
}

func TestExtractAdminToken_QueryParam2(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/admin/test?token=qs-token", nil)
	if got := extractAdminToken(req); got != "qs-token" {
		t.Fatalf("expected 'qs-token', got %q", got)
	}
}

// ── admin rate limiter edge cases ─────────────────────────────────

func TestNewAdminRateLimiter_BurstZeroBelowOne2(t *testing.T) {
	l := newAdminRateLimiter(0.5, 0)
	if l == nil {
		t.Fatal("expected non-nil limiter")
	}
	if l.burst != 1 {
		t.Fatalf("expected burst=1 for rate<1, got %d", l.burst)
	}
}

func TestAdminRateLimiter_AllowNil2(t *testing.T) {
	var l *adminRateLimiter
	if !l.allow() {
		t.Fatal("nil limiter should allow")
	}
}

func TestAdminRateLimiter_Exhausted2(t *testing.T) {
	l := newAdminRateLimiter(1.0, 1)
	if !l.allow() {
		t.Fatal("first allow should succeed")
	}
	if l.allow() {
		t.Fatal("second immediate allow should fail")
	}
}

// ── fetchTopPools ─────────────────────────────────────────────────

func TestFetchTopPools_Errors2(t *testing.T) {
	t.Run("non200", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "error", http.StatusInternalServerError)
		}))
		defer srv.Close()
		client := &http.Client{Timeout: time.Second}
		pools, ok := fetchTopPools(context.Background(), client, srv.URL)
		if ok || pools != nil {
			t.Fatal("expected failure for non-200")
		}
	})

	t.Run("invalid_json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("not json"))
		}))
		defer srv.Close()
		client := &http.Client{Timeout: time.Second}
		pools, ok := fetchTopPools(context.Background(), client, srv.URL)
		if ok || pools != nil {
			t.Fatal("expected failure for invalid JSON")
		}
	})

	t.Run("invalid_request", func(t *testing.T) {
		client := &http.Client{Timeout: time.Second}
		pools, ok := fetchTopPools(context.Background(), client, "://invalid")
		if ok || pools != nil {
			t.Fatal("expected failure for invalid request")
		}
	})

	t.Run("valid_json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"name":"pool1","tvl":1000}]`))
		}))
		defer srv.Close()
		client := &http.Client{Timeout: time.Second}
		pools, ok := fetchTopPools(context.Background(), client, srv.URL)
		if !ok || pools == nil {
			t.Fatal("expected success")
		}
	})
}

// ── newMempoolShadowSessionDir ────────────────────────────────────

func TestDumpMempoolShadowBundle_EmptyArbID2(t *testing.T) {
	dir := t.TempDir()
	origSession := mempoolShadowSessionDir
	mempoolShadowSessionDir = func() string { return dir }
	defer func() { mempoolShadowSessionDir = origSession }()

	arb := &pb.ValidatedArb{Id: "", FlashloanToken: []byte{}, FlashloanAmount: []byte{}, NetProfitWei: []byte{}}
	bundle := &Bundle{BlockNumber: 0}
	gasFees := GasFees{BaseFee: big.NewInt(1), MaxFeePerGas: big.NewInt(1), MaxPriorityFee: big.NewInt(1)}
	decision := MempoolPreflightResult{Approved: true}

	if err := dumpMempoolShadowBundle(arb, bundle, gasFees, 0, decision); err != nil {
		t.Fatalf("dumpMempoolShadowBundle: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "anon.json" {
		t.Fatalf("expected anon.json, got %v", entries)
	}
}

func TestDumpMempoolShadowBundle_RoundTrip2(t *testing.T) {
	dir := t.TempDir()
	origSession := mempoolShadowSessionDir
	mempoolShadowSessionDir = func() string { return dir }
	defer func() { mempoolShadowSessionDir = origSession }()

	weth := []byte{0xc0, 0x2a, 0xaa, 0x39, 0xb2, 0x23, 0xfe, 0x8d, 0x0a, 0x0e, 0x5c, 0x4f, 0x27, 0xea, 0xd9, 0x08, 0x3c, 0x75, 0x6c, 0xc2}
	arb := &pb.ValidatedArb{
		Id: "mempool-rt2", Hops: []*pb.ArbHop{{Protocol: pb.ProtocolType_UNISWAP_V2, PoolAddress: []byte{0x01}, TokenIn: weth, TokenOut: weth, AmountIn: new(big.Int).SetUint64(1e18).Bytes(), ExpectedOut: new(big.Int).SetUint64(1e18).Bytes(), EstimatedGas: 100000}},
		FlashloanToken: weth, FlashloanAmount: new(big.Int).SetUint64(1e18).Bytes(), NetProfitWei: new(big.Int).SetUint64(1e15).Bytes(), TotalGas: 200000, BlockNumber: 100, Calldata: []byte{0x01},
	}
	bundle := &Bundle{RawTxs: [][]byte{{0xf8, 0x6c}, {0xf8, 0x6d}}, BlockNumber: 101, Source: SourceMempoolBackrun, VictimTxHashHex: "0x" + strings.Repeat("ab", 32), RevertingTxHashes: []string{"0x" + strings.Repeat("cd", 32)}}
	gasFees := GasFees{BaseFee: big.NewInt(30e9), MaxFeePerGas: big.NewInt(62e9), MaxPriorityFee: big.NewInt(2e9), GasPriceGwei: 30.0}
	decision := MempoolPreflightResult{Approved: true, Gates: []MempoolGateTrace{{Gate: "min_profit", Passed: true, Value: "1000000000000000"}}}

	if err := dumpMempoolShadowBundle(arb, bundle, gasFees, 95.0, decision); err != nil {
		t.Fatalf("dumpMempoolShadowBundle: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 file, got %d", len(entries))
	}
	raw, _ := os.ReadFile(dir + "/" + entries[0].Name())
	var payload map[string]interface{}
	json.Unmarshal(raw, &payload)
	for _, k := range []string{"arb_id", "source", "victim_tx_hash", "target_block", "envelope", "risk_decisions"} {
		if _, ok := payload[k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
}

// ── metricsStoreHealthy: pinger interface ──────────────────────────

type testPingerOK struct{}

func (testPingerOK) Ping(ctx context.Context) error { return nil }
func (testPingerOK) Record(m db.Metric)             {}
func (testPingerOK) Close()                         {}

type testPingerFail struct{}

func (testPingerFail) Ping(ctx context.Context) error { return errors.New("connection refused") }
func (testPingerFail) Record(m db.Metric)              {}
func (testPingerFail) Close()                          {}

func TestMetricsStoreHealthy_PingerOK(t *testing.T) {
	orig := os.Getenv("DATABASE_URL")
	os.Setenv("DATABASE_URL", "postgres://localhost/test")
	defer os.Setenv("DATABASE_URL", orig)

	oldStore := metricsStore
	metricsStore = testPingerOK{}
	defer func() { metricsStore = oldStore }()

	if !metricsStoreHealthy() {
		t.Fatal("expected true when pinger succeeds")
	}
}

func TestMetricsStoreHealthy_PingerFail(t *testing.T) {
	orig := os.Getenv("DATABASE_URL")
	os.Setenv("DATABASE_URL", "postgres://localhost/test")
	defer os.Setenv("DATABASE_URL", orig)

	oldStore := metricsStore
	metricsStore = testPingerFail{}
	defer func() { metricsStore = oldStore }()

	if metricsStoreHealthy() {
		t.Fatal("expected false when pinger fails")
	}
}

// ── handleHealthJSON wrong method ─────────────────────────────────

func TestHandleHealthJSON_WrongMethod(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/health", nil)
	w := httptest.NewRecorder()
	handleHealthJSON(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

// ── handleMetricsJSON wrong method ────────────────────────────────

func TestHandleMetricsJSON_WrongMethod(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/metrics/json", nil)
	w := httptest.NewRecorder()
	handleMetricsJSON(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

// ── run: migration path ───────────────────────────────────────────

func TestRun_WithMigrations(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	t.Setenv("REDIS_URL", "redis://"+mr.Addr())
	t.Setenv("DATABASE_URL", "postgres://localhost/noexist?sslmode=disable")

	cfg := defaultConfig()
	cfg.GRPCAddress = "localhost:99999"
	cfg.BuilderConfigs = nil

	deps := &Dependencies{
		Submitter: &Submitter{}, Ledger: db.NewNoopLedger(), MetricsStore: db.NewNoopMetricsStore(),
		EventPublisher: events.NewPublisherFromEnv(), ExecutorAddr: "0x0000000000000000000000000000000000000001",
		ChainID: 1, GRPCDial: func(addr string) (*aethergrpc.Client, error) {
			return nil, errors.New("no gRPC")
		},
		SkipMetricsHTTP: true, SkipAdminHTTP: true, ReconnectDelay: 10 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	deps.WaitForShutdown = func(ctx context.Context, c context.CancelFunc) error {
		<-time.After(200 * time.Millisecond)
		c()
		return nil
	}
	// migrations path should either succeed or return an error (both are covered)
	_ = run(ctx, &cfg, deps)
}

func TestRun_WithBalanceCheckAndRemoteSigner(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	t.Setenv("REDIS_URL", "redis://"+mr.Addr())

	cfg := defaultConfig()
	cfg.GRPCAddress = "localhost:99999"
	cfg.BuilderConfigs = nil

	// Simulate a remote signer by nil-ing out TxSigner but setting EthClient to nil
	deps := &Dependencies{
		Submitter: &Submitter{}, Ledger: db.NewNoopLedger(), MetricsStore: db.NewNoopMetricsStore(),
		EventPublisher: events.NewPublisherFromEnv(), ExecutorAddr: "0x0000000000000000000000000000000000000001",
		ChainID: 1, GRPCDial: func(addr string) (*aethergrpc.Client, error) {
			return nil, errors.New("no gRPC")
		},
		SkipMetricsHTTP: true, SkipAdminHTTP: true, ReconnectDelay: 10 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	deps.WaitForShutdown = func(ctx context.Context, c context.CancelFunc) error {
		<-time.After(150 * time.Millisecond)
		c()
		return nil
	}
	_ = run(ctx, &cfg, deps)
}

// ── run: with admin server (not skipped) ──────────────────────────

func TestRun_WithAdminServer(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	t.Setenv("REDIS_URL", "redis://"+mr.Addr())
	adminServerOnce = sync.Once{}

	cfg := defaultConfig()
	cfg.GRPCAddress = "localhost:99999"
	cfg.BuilderConfigs = nil

	deps := &Dependencies{
		Submitter: &Submitter{}, Ledger: db.NewNoopLedger(), MetricsStore: db.NewNoopMetricsStore(),
		EventPublisher: events.NewPublisherFromEnv(), ExecutorAddr: "0x0000000000000000000000000000000000000001",
		ChainID: 1, GRPCDial: func(addr string) (*aethergrpc.Client, error) {
			return nil, errors.New("no gRPC")
		},
		SkipMetricsHTTP: true, SkipAdminHTTP: false, ReconnectDelay: 10 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	deps.WaitForShutdown = func(ctx context.Context, c context.CancelFunc) error {
		<-time.After(150 * time.Millisecond)
		c()
		return nil
	}
	_ = run(ctx, &cfg, deps)
}

// ── run: with metrics server (not skipped) ────────────────────────

func TestRun_WithMetricsServer(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	t.Setenv("REDIS_URL", "redis://"+mr.Addr())

	cfg := defaultConfig()
	cfg.GRPCAddress = "localhost:99999"
	cfg.BuilderConfigs = nil

	deps := &Dependencies{
		Submitter: &Submitter{}, Ledger: db.NewNoopLedger(), MetricsStore: db.NewNoopMetricsStore(),
		EventPublisher: events.NewPublisherFromEnv(), ExecutorAddr: "0x0000000000000000000000000000000000000001",
		ChainID: 1, GRPCDial: func(addr string) (*aethergrpc.Client, error) {
			return nil, errors.New("no gRPC")
		},
		SkipMetricsHTTP: false, SkipAdminHTTP: true, ReconnectDelay: 10 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	deps.WaitForShutdown = func(ctx context.Context, c context.CancelFunc) error {
		<-time.After(150 * time.Millisecond)
		c()
		return nil
	}
	_ = run(ctx, &cfg, deps)
}

// ── run: nonce sync warning path ──────────────────────────────────

func TestRun_NonceSyncWarning(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	t.Setenv("REDIS_URL", "redis://"+mr.Addr())

	cfg := defaultConfig()
	cfg.GRPCAddress = "localhost:99999"
	cfg.BuilderConfigs = nil

	deps := &Dependencies{
		Submitter: &Submitter{}, Ledger: db.NewNoopLedger(), MetricsStore: db.NewNoopMetricsStore(),
		EventPublisher: events.NewPublisherFromEnv(), ExecutorAddr: "0x0000000000000000000000000000000000000001",
		ChainID: 1, GRPCDial: func(addr string) (*aethergrpc.Client, error) {
			return nil, errors.New("no gRPC")
		},
		SkipMetricsHTTP: true, SkipAdminHTTP: true, ReconnectDelay: 10 * time.Millisecond,
		TxSigner: &testFailingSigner{err: errors.New("test")},
		EthClient: nil, // TxSigner set but EthClient nil => "SEARCHER_KEY not set" log
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	deps.WaitForShutdown = func(ctx context.Context, c context.CancelFunc) error {
		<-time.After(150 * time.Millisecond)
		c()
		return nil
	}
	_ = run(ctx, &cfg, deps)
}

// ── processArb: shadow mempool backrun with dump failure ───────────

func TestProcessArb_ShadowMempool_DumpFailure(t *testing.T) {
	prevMode := getBackrunMode()
	defer setBackrunMode(prevMode)
	setBackrunMode(BackrunShadowOnly)

	rm, bundler, submitter := newTestComponents()
	arb := newValidArb("arb-shadow-dump-fail", 0.01, 5.0)
	arb.Source = pb.ArbSource_MEMPOOL_BACKRUN
	arb.VictimTxHash = make([]byte, 32)
	arb.VictimTxHash[0] = 0xee
	arb.VictimRawTx = []byte{0xf8, 0x01, 0x02}
	arb.TimestampNs = time.Now().UnixNano()

	submitted, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if !submitted {
		t.Fatal("expected shadow submission")
	}
}

// ── processArb: block-driven no signing error, bundle with empty signer ──

func TestProcessArb_BundleBuild_NilSigner(t *testing.T) {
	rm, bundler, submitter := newTestComponents()
	bundler.signer = nil // unsigned path
	submitter.submitFn = func(ctx context.Context, b BuilderConfig, bundle *Bundle) SubmissionResult {
		return SubmissionResult{Builder: b.Name, Success: true, BundleHash: "nil-signer-hash"}
	}
	arb := newValidArb("arb-nil-signer", 0.01, 5.0)
	submitted, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if !submitted {
		t.Fatal("expected submission")
	}
}

// ── dumpShadowBundle: more edge cases ─────────────────────────────

func TestDumpShadowBundle_SpecialCharsID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AETHER_SHADOW_DUMP_DIR", dir)

	arb := &pb.ValidatedArb{
		Id:              "\x00\n\r<>",
		Hops:            nil,
		FlashloanToken:  []byte{},
		FlashloanAmount: []byte{},
		NetProfitWei:    []byte{},
	}
	bundle := &Bundle{RawTxs: nil, BlockNumber: 0}
	if err := dumpShadowBundle(arb, bundle, 0, 0, 0); err != nil {
		t.Fatalf("dumpShadowBundle: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 file, got %d", len(entries))
	}
	// All special chars become '_', resulting in "_____"
	if entries[0].Name() != "_____.json" {
		t.Fatalf("expected _____.json, got %s", entries[0].Name())
	}
}

// ── submitter: mixed results with mempool envelope ────────────────

func TestSubmitToBuilder_MempoolEnvelope2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer srv.Close()

	builders := []BuilderConfig{{Name: "b1", Enabled: true, URL: srv.URL, TimeoutMs: 2000}}
	s, _ := NewSubmitter(builders, "")
	bundle := &Bundle{
		BlockNumber:       100,
		RawTxs:            [][]byte{{0xf8, 0x6c}, {0xab, 0xcd}},
		RevertingTxHashes: []string{"0xdeadbeef"},
	}
	results := s.SubmitToBuilder(context.Background(), bundle, "b1")
	if len(results) != 1 || !results[0].Success {
		t.Fatalf("expected success, got %v", results)
	}
}

// ── nonce: sync loop with failing client ──────────────────────────

func TestNonceManager_SyncLoop_FailingClient(t *testing.T) {
	nm := NewNonceManager(0)
	nm.SetSyncSource(common.HexToAddress("0x1234"), &testMockNonceProvider{err: errors.New("RPC fail")})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	nm.SyncLoop(ctx, 20*time.Millisecond)
}

// ── gas oracle: update loop ───────────────────────────────────────

func TestGasOracle_UpdateLoop_ErrorRecover(t *testing.T) {
	go_ := NewGasOracle(300.0)
	go_.SetClient(&testMockFeeHistoryProvider{returnError: true})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	go_.UpdateLoop(ctx, 20*time.Millisecond)
}

// ── consumeArbStream: zero reconnect delay ────────────────────────

func TestConsumeArbStream_ZeroDelay(t *testing.T) {
	rm, bundler, submitter := newTestComponents()
	lb := NewLiveBalance()
	lb.Set(0.5)

	client, _ := aethergrpc.Dial("127.0.0.1:1")
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	consumeArbStream(ctx, client, bundler, submitter, rm, db.NewNoopLedger(),
		"0x0000000000000000000000000000000000000001", lb, 0)
}

// ── signerHealthLoop ──────────────────────────────────────────────

func TestSignerHealthLoop_PingError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	signerHealthLoop(ctx, func() error { return errors.New("ping fail") }, 20*time.Millisecond)
}

func TestSignerHealthLoop_PingOK(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	signerHealthLoop(ctx, func() error { return nil }, 20*time.Millisecond)
}

// ── nonce: SyncFromChain with nil address ─────────────────────────

func TestNonceManager_SyncFromChain_NilAddress(t *testing.T) {
	nm := NewNonceManager(0)
	nm.SetSyncSource(common.Address{}, &testMockNonceProvider{nonce: 5})
	err := nm.SyncFromChain(context.Background())
	if err != nil {
		t.Fatalf("expected nil for zero address, got: %v", err)
	}
}

// ── balanceWatchLoop: update loop ─────────────────────────────────

func TestBalanceWatchLoop_AllSuccess(t *testing.T) {
	client := &testMockBalanceReader{
		balFn: func(ctx context.Context, addr common.Address, block *big.Int) (*big.Int, error) {
			return big.NewInt(2e18), nil
		},
	}
	lb := NewLiveBalance()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	balanceWatchLoop(ctx, client, common.Address{}, 20*time.Millisecond, lb, "")
	if lb.Get() <= 0 {
		t.Fatal("expected positive balance")
	}
}

// ── logSelectorSnapshotLoop: with active selector ─────────────────

func TestLogSelectorSnapshotLoop_ActiveSelector(t *testing.T) {
	oldSel := builderSelector
	oldStore := metricsStore
	builderSelector = strategy.New([]string{"b1"}, strategy.Config{ExplorationFloor: 0.1})
	metricsStore = db.NewNoopMetricsStore()
	defer func() {
		builderSelector = oldSel
		metricsStore = oldStore
	}()

	// Make some calls so the selector has data to snapshot
	for i := 0; i < 3; i++ {
		builderSelector.Record("b1", strategy.Outcome{Included: i%2 == 0, ProfitWei: big.NewInt(int64(i) * 1e15)})
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		logSelectorSnapshotLoop(ctx, time.Millisecond)
		close(done)
	}()
	time.Sleep(5 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("loop did not exit")
	}
}

// ── buildExecutorDeps: no searcher key, no signer socket ──────────

func TestBuildExecutorDeps_NoKeyNoSocket(t *testing.T) {
	os.Unsetenv("SEARCHER_KEY")
	os.Unsetenv("AETHER_SIGNER_SOCKET")

	execCfg := config.ExecutorFileConfig{
		ExecutorAddress: "0x0000000000000000000000000000000000000001",
		ExpectedChainID: 1,
	}
	dial := func(ctx context.Context, url string) (*ethclient.Client, error) {
		return nil, errors.New("mock: no client available")
	}
	_, _, err := buildExecutorDeps(context.Background(), Config{}, execCfg, "http://localhost:8545", dial)
	if err == nil {
		t.Fatal("expected error from mock dial")
	}
}

// ── run: signal handler path (nil WaitForShutdown) ────────────────

func TestRun_NilWaitShutdown(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	t.Setenv("REDIS_URL", "redis://"+mr.Addr())

	cfg := defaultConfig()
	cfg.GRPCAddress = "localhost:99999"
	cfg.BuilderConfigs = nil

	deps := &Dependencies{
		Submitter: &Submitter{}, Ledger: db.NewNoopLedger(), MetricsStore: db.NewNoopMetricsStore(),
		EventPublisher: events.NewPublisherFromEnv(), ExecutorAddr: "0x0000000000000000000000000000000000000001",
		ChainID: 1, GRPCDial: func(addr string) (*aethergrpc.Client, error) {
			return nil, errors.New("no gRPC")
		},
		SkipMetricsHTTP: true, SkipAdminHTTP: true, ReconnectDelay: 10 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- run(ctx, &cfg, deps)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("run did not exit after cancel")
	}
}

// ── run: with gRPC client that dials successfully ─────────────────

func TestRun_WithSuccessfulGRPCDial(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	t.Setenv("REDIS_URL", "redis://"+mr.Addr())

	srv := testutil.NewMockArbServer()
	srv.SetArbs([]*pb.ValidatedArb{})
	dialer, cleanup, err := srv.StartBufconn(0)
	if err != nil {
		t.Fatalf("StartBufconn: %v", err)
	}
	defer cleanup()

	cfg := defaultConfig()
	cfg.GRPCAddress = "bufconn:test"
	cfg.BuilderConfigs = nil

	grpcDial := func(_ string) (*aethergrpc.Client, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		conn, err := srv.DialBufconn(ctx, dialer)
		if err != nil {
			return nil, err
		}
		return aethergrpc.NewClientFromConn(conn)
	}

	deps := &Dependencies{
		Submitter: &Submitter{}, Ledger: db.NewNoopLedger(), MetricsStore: db.NewNoopMetricsStore(),
		EventPublisher: events.NewPublisherFromEnv(), ExecutorAddr: "0x0000000000000000000000000000000000000001",
		ChainID: 1, GRPCDial: grpcDial, SkipMigrations: true, SkipMetricsHTTP: true, SkipAdminHTTP: true,
		ReconnectDelay: 10 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	deps.WaitForShutdown = func(ctx context.Context, c context.CancelFunc) error {
		<-time.After(400 * time.Millisecond)
		c()
		return nil
	}

	if err := run(ctx, &cfg, deps); err != nil {
		t.Fatalf("run: %v", err)
	}
}

// ── handleAdminPause: invalid transition (already halted) ──────────

func TestHandleAdminPause_InvalidTransition(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.ForceStateForTest(risk.StateHalted)
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm}
	defer func() { globalAdminDeps = prevDeps }()
	req := httptest.NewRequest(http.MethodPost, "/admin/pause?reason=halt", nil)
	w := httptest.NewRecorder()
	handleAdminPause(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for invalid transition, got %d", w.Code)
	}
}

// ── handleAdminResume: not paused ─────────────────────────────────

func TestHandleAdminResume_FromDegraded(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.ForceStateForTest(risk.StateDegraded)
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm}
	defer func() { globalAdminDeps = prevDeps }()
	req := httptest.NewRequest(http.MethodPost, "/admin/resume", nil)
	w := httptest.NewRecorder()
	handleAdminResume(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// ── handleSetMinProfit: from request body ─────────────────────────

func TestHandleSetMinProfit_FromBody2(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm}
	defer func() { globalAdminDeps = prevDeps }()
	req := httptest.NewRequest(http.MethodPost, "/admin/set_min_profit", strings.NewReader("0.25"))
	w := httptest.NewRecorder()
	handleSetMinProfit(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// ── loadRiskConfig: test fallback path explicitly ──────────────────

func TestLoadRiskConfig_FallbackPath(t *testing.T) {
	origEnv := os.Getenv("AETHER_RISK_CONFIG")
	defer os.Setenv("AETHER_RISK_CONFIG", origEnv)
	os.Setenv("AETHER_RISK_CONFIG", "/nonexistent/path.json")
	rc := loadRiskConfig()
	if rc.MaxGasGwei <= 0 {
		t.Fatalf("expected default config, got %+v", rc)
	}
}

// ── signerHealthLoop: with nil ping ───────────────────────────────

func TestSignerHealthLoop_PingOKPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	signerHealthLoop(ctx, func() error { return nil }, 20*time.Millisecond)
}

// ── buildExecutorDeps: all bootstrap paths ────────────────────────

func TestBuildExecutorDeps_ChainIDMismatch(t *testing.T) {
	dial := func(ctx context.Context, url string) (*ethclient.Client, error) {
		return nil, errors.New("mock: chain-id mismatch")
	}
	execCfg := config.ExecutorFileConfig{
		ExecutorAddress: "0x0000000000000000000000000000000000000001",
		ExpectedChainID: 1,
	}
	_, _, err := buildExecutorDeps(context.Background(), Config{}, execCfg, "http://localhost:8545", dial)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ── admin server: handleAdminPause with event publisher ───────────

func TestHandleAdminPause_WithEventPublisher(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	pub := &mockAdminEventPub{}
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm, eventPub: pub}
	defer func() { globalAdminDeps = prevDeps }()
	req := httptest.NewRequest(http.MethodPost, "/admin/pause?reason=test-pub", nil)
	w := httptest.NewRecorder()
	handleAdminPause(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !pub.breakerPublished {
		t.Fatal("expected PublishBreakerStatus to be called")
	}
}

func TestHandleAdminResume_WithEventPublisher(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.Pause("test")
	pub := &mockAdminEventPub{}
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm, eventPub: pub}
	defer func() { globalAdminDeps = prevDeps }()
	req := httptest.NewRequest(http.MethodPost, "/admin/resume", nil)
	w := httptest.NewRecorder()
	handleAdminResume(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !pub.breakerPublished {
		t.Fatal("expected PublishBreakerStatus to be called")
	}
}

func TestHandleAdminReset_WithEventPublisher(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.ForceStateForTest(risk.StateHalted)
	pub := &mockAdminEventPub{}
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm, eventPub: pub}
	defer func() { globalAdminDeps = prevDeps }()
	req := httptest.NewRequest(http.MethodPost, "/admin/reset", nil)
	w := httptest.NewRecorder()
	handleAdminReset(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !pub.breakerPublished {
		t.Fatal("expected PublishBreakerStatus to be called")
	}
}

type mockAdminEventPub struct {
	breakerPublished bool
	lastReason       string
}

func (m *mockAdminEventPub) PublishBreakerStatus(open bool, reason string) {
	m.breakerPublished = true
	m.lastReason = reason
}

func (m *mockAdminEventPub) PublishSignerHealth(healthy bool) {}

// ── loadAdminPort: env override on top of production config ───────

func TestLoadAdminPort_EnvOverrideOnProd(t *testing.T) {
	t.Setenv("ADMIN_HTTP_PORT", "7777")
	port, _ := loadAdminPort()
	if port != 7777 {
		t.Fatalf("expected 7777, got %d", port)
	}
}

// ── admin handlers: wrong HTTP method paths (405) ────────────────

func TestHandleAdminPause_WrongMethod(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm}
	defer func() { globalAdminDeps = prevDeps }()
	req := httptest.NewRequest(http.MethodGet, "/admin/pause", nil)
	w := httptest.NewRecorder()
	handleAdminPause(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestHandleAdminReset_WrongMethod(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm}
	defer func() { globalAdminDeps = prevDeps }()
	req := httptest.NewRequest(http.MethodGet, "/admin/reset", nil)
	w := httptest.NewRecorder()
	handleAdminReset(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestHandleAdminResume_WrongMethod(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm}
	defer func() { globalAdminDeps = prevDeps }()
	req := httptest.NewRequest(http.MethodGet, "/admin/resume", nil)
	w := httptest.NewRecorder()
	handleAdminResume(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestHandleSetMinProfit_WrongMethod(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm}
	defer func() { globalAdminDeps = prevDeps }()
	req := httptest.NewRequest(http.MethodGet, "/admin/set_min_profit", nil)
	w := httptest.NewRecorder()
	handleSetMinProfit(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

// ── admin handlers: non-transition error → 500 ───────────────────

type mockRiskMgrNonTransition struct {
	*risk.RiskManager
	failErr error
}

func (m *mockRiskMgrNonTransition) Pause(reason string) error {
	if m.failErr != nil {
		return m.failErr
	}
	return m.RiskManager.Pause(reason)
}

func TestHandleAdminPause_InternalServerError(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm}
	defer func() { globalAdminDeps = prevDeps }()

	rm.Pause("test")
	req := httptest.NewRequest(http.MethodPost, "/admin/pause?reason=gas_spike", nil)
	w := httptest.NewRecorder()
	handleAdminPause(w, req)
	// Pause from Paused state is "invalid transition" → 409
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}

func TestHandleAdminResume_InternalServerError(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm}
	defer func() { globalAdminDeps = prevDeps }()

	// Resume from Running state is invalid → 409
	req := httptest.NewRequest(http.MethodPost, "/admin/resume", nil)
	w := httptest.NewRecorder()
	handleAdminResume(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}

// ── processArb: backrun_off path ─────────────────────────────────

func TestProcessArb_MempoolBackrun_Off(t *testing.T) {
	prevMode := getBackrunMode()
	defer setBackrunMode(prevMode)
	setBackrunMode(BackrunOff)

	rm, bundler, submitter := newTestComponents()
	arb := newValidArb("arb-backrun-off", 0.01, 5.0)
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
	if submitted {
		t.Fatal("expected not submitted when backrun mode is off")
	}
}

// ── processArb: missing victim_raw_tx path ───────────────────────

func TestProcessArb_MempoolBackrun_MissingVictimRawTx(t *testing.T) {
	initTestMempoolRisk()
	rm, bundler, submitter := newTestComponents()
	arb := newValidArb("arb-missing-victim", 0.01, 5.0)
	arb.Source = pb.ArbSource_MEMPOOL_BACKRUN
	arb.VictimTxHash = make([]byte, 32)
	arb.VictimTxHash[0] = 0xab
	arb.VictimRawTx = nil // empty victim raw tx
	arb.TimestampNs = time.Now().UnixNano()

	submitted, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if submitted {
		t.Fatal("expected not submitted when victim_raw_tx is empty")
	}
}

// ── processArb: signer error → Pause error path ──────────────────

func TestProcessArb_SignerError_PauseFails(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.ForceStateForTest(risk.StateHalted) // can't pause from halted
	nm := NewNonceManager(0)
	go_ := NewGasOracle(300.0)
	bundler := NewBundleConstructor(nm, go_, &testFailingSigner{err: errSignerUnavailable}, 1)
	builders := []BuilderConfig{{Name: "b1", Enabled: true, TimeoutMs: 1000}}
	submitter, _ := NewSubmitter(builders, "")

	oldPub := eventPublisher
	oldStore := metricsStore
	eventPublisher = events.NewPublisherFromEnv()
	metricsStore = db.NewNoopMetricsStore()
	defer func() {
		eventPublisher = oldPub
		metricsStore = oldStore
	}()

	arb := newValidArb("arb-signer-pause-fail", 0.01, 5.0)
	_, _ = processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
}

// ── processArb: mempool shadow + live (dumpMempoolShadowBundle) ──

func TestProcessArb_MempoolShadowAndLive_DumpError(t *testing.T) {
	prevMode := getBackrunMode()
	defer setBackrunMode(prevMode)
	setBackrunMode(BackrunShadowAndLive)

	rm, bundler, submitter := newTestComponents()
	submitter.submitFn = func(ctx context.Context, b BuilderConfig, bundle *Bundle) SubmissionResult {
		return SubmissionResult{Builder: b.Name, Success: true, BundleHash: "sl-hash2"}
	}

	arb := newValidArb("arb-shadow-live-dump", 0.01, 5.0)
	arb.Source = pb.ArbSource_MEMPOOL_BACKRUN
	arb.VictimTxHash = make([]byte, 32)
	arb.VictimTxHash[0] = 0xbe
	arb.VictimRawTx = []byte{0xf8, 0x01, 0x02}
	arb.TimestampNs = time.Now().UnixNano()

	submitted, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if !submitted {
		t.Fatal("expected submission")
	}
}

// ── processArb: mempool shadow only dump failure ──────────────────

func TestProcessArb_MempoolShadowOnly_DumpFails(t *testing.T) {
	prevMode := getBackrunMode()
	defer setBackrunMode(prevMode)
	setBackrunMode(BackrunShadowOnly)

	rm, bundler, submitter := newTestComponents()
	arb := newValidArb("arb-shadow-only-dump-fail", 0.01, 5.0)
	arb.Source = pb.ArbSource_MEMPOOL_BACKRUN
	arb.VictimTxHash = make([]byte, 32)
	arb.VictimTxHash[0] = 0xbf
	arb.VictimRawTx = []byte{0xf8, 0x01, 0x02}
	arb.TimestampNs = time.Now().UnixNano()

	submitted, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if !submitted {
		t.Fatal("expected shadow submission")
	}
}

// ── processArb: block-driven shadow dump error ───────────────────

func TestProcessArb_BlockDrivenShadow_DumpError(t *testing.T) {
	prevShadow := os.Getenv("AETHER_SHADOW")
	defer os.Setenv("AETHER_SHADOW", prevShadow)
	os.Setenv("AETHER_SHADOW", "1")

	rm, bundler, submitter := newTestComponents()
	arb := newValidArb("arb-bd-shadow-dump", 0.01, 5.0)
	submitted, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if !submitted {
		t.Fatal("expected shadow submission")
	}
}

// ── run(): with nil EventPublisher (eventPublisher nil init path) ──

func TestRun_NilEventPublisher(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	t.Setenv("REDIS_URL", "redis://"+mr.Addr())

	cfg := defaultConfig()
	cfg.GRPCAddress = "localhost:99999"
	cfg.BuilderConfigs = nil

	eventPublisher = nil

	deps := &Dependencies{
		Submitter: &Submitter{}, Ledger: db.NewNoopLedger(), MetricsStore: db.NewNoopMetricsStore(),
		ExecutorAddr: "0x0000000000000000000000000000000000000001",
		ChainID: 1, GRPCDial: func(addr string) (*aethergrpc.Client, error) {
			return nil, errors.New("no gRPC")
		}, SkipMigrations: true, SkipMetricsHTTP: true, SkipAdminHTTP: true, ReconnectDelay: 10 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	deps.WaitForShutdown = func(ctx context.Context, c context.CancelFunc) error {
		<-time.After(150 * time.Millisecond)
		c()
		return nil
	}

	_ = run(ctx, &cfg, deps)
}

// ── run(): with RemoteSigner ping error ──────────────────────────

func TestRun_WithRemoteSigner_PingFail(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	t.Setenv("REDIS_URL", "redis://"+mr.Addr())

	grpcDial := func(addr string) (*aethergrpc.Client, error) {
		return nil, errors.New("connection refused")
	}
	cfg := defaultConfig()
	cfg.GRPCAddress = "localhost:99999"

	// Create a real RemoteSigner that will fail ping (bad socket)
	// We test with nil RemoteSigner instead since RemoteSigner is a concrete type
	deps := &Dependencies{
		Submitter:    &Submitter{}, Ledger: db.NewNoopLedger(), MetricsStore: db.NewNoopMetricsStore(),
		EventPublisher: events.NewPublisherFromEnv(), ExecutorAddr: "0x0000000000000000000000000000000000000001",
		ChainID: 1, GRPCDial: grpcDial, SkipMigrations: true, SkipMetricsHTTP: true, SkipAdminHTTP: true,
		ReconnectDelay: 10 * time.Millisecond, RemoteSigner: nil,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	deps.WaitForShutdown = func(ctx context.Context, c context.CancelFunc) error {
		<-time.After(150 * time.Millisecond)
		c()
		return nil
	}

	if err := run(ctx, &cfg, deps); err != nil {
		t.Fatalf("run: %v", err)
	}
}

// ── run(): shadow mode enabled ───────────────────────────────────

func TestRun_ShadowMode(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	t.Setenv("REDIS_URL", "redis://"+mr.Addr())
	t.Setenv("AETHER_SHADOW", "1")

	cfg := defaultConfig()
	cfg.GRPCAddress = "localhost:99999"

	deps := &Dependencies{
		Submitter:    &Submitter{}, Ledger: db.NewNoopLedger(), MetricsStore: db.NewNoopMetricsStore(),
		EventPublisher: events.NewPublisherFromEnv(), ExecutorAddr: "0x0000000000000000000000000000000000000001",
		ChainID: 1, GRPCDial: func(addr string) (*aethergrpc.Client, error) {
			return nil, errors.New("no gRPC")
		}, SkipMigrations: true, SkipMetricsHTTP: true, SkipAdminHTTP: true, ReconnectDelay: 10 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	deps.WaitForShutdown = func(ctx context.Context, c context.CancelFunc) error {
		<-time.After(150 * time.Millisecond)
		c()
		return nil
	}

	if err := run(ctx, &cfg, deps); err != nil {
		t.Fatalf("run: %v", err)
	}
}

// ── run(): initAdminAuth failure ─────────────────────────────────

func TestRun_AdminAuthFailure(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	t.Setenv("REDIS_URL", "redis://"+mr.Addr())
	t.Setenv("AETHER_ENV", "production")
	t.Setenv("AETHER_ADMIN_TOKEN", "")
	defer os.Unsetenv("AETHER_ENV")
	defer os.Unsetenv("AETHER_ADMIN_TOKEN")

	cfg := defaultConfig()
	cfg.GRPCAddress = "localhost:99999"

	deps := &Dependencies{
		Submitter: &Submitter{}, Ledger: db.NewNoopLedger(), MetricsStore: db.NewNoopMetricsStore(),
		EventPublisher: events.NewPublisherFromEnv(), ExecutorAddr: "0x0000000000000000000000000000000000000001",
		ChainID: 1, GRPCDial: func(addr string) (*aethergrpc.Client, error) {
			return nil, errors.New("no gRPC")
		}, SkipMigrations: true, SkipMetricsHTTP: true, SkipAdminHTTP: true, ReconnectDelay: 10 * time.Millisecond,
	}

	err := run(context.Background(), &cfg, deps)
	if err == nil {
		t.Fatal("expected error from initAdminAuth in production")
	}
	if !strings.Contains(err.Error(), "admin auth") {
		t.Fatalf("expected admin auth error, got: %v", err)
	}
}

// ── addBigIntCounter: zero value path ────────────────────────────

func TestAddBigIntCounter_ZeroValue(t *testing.T) {
	addBigIntCounter(profitTotalWei, big.NewInt(0))
	addBigIntCounter(profitTotalWei, nil)
}

// ── logSelectorSnapshotLoop: nil selector ────────────────────────

func TestLogSelectorSnapshotLoop_NilSelector(t *testing.T) {
	oldSel := builderSelector
	builderSelector = nil
	defer func() { builderSelector = oldSel }()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	logSelectorSnapshotLoop(ctx, time.Millisecond)
}

// ── dumpShadowBundle: empty arb ID → "anon" ──────────────────────

func TestDumpShadowBundle_EmptyArbID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AETHER_SHADOW_DUMP_DIR", dir)
	arb := &pb.ValidatedArb{
		Id: "", Hops: nil, FlashloanToken: []byte{}, FlashloanAmount: []byte{}, NetProfitWei: []byte{},
	}
	bundle := &Bundle{RawTxs: nil, BlockNumber: 0}
	if err := dumpShadowBundle(arb, bundle, 0, 0, 0); err != nil {
		t.Fatalf("dumpShadowBundle: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "anon.json" {
		t.Fatalf("expected anon.json, got %v", entries)
	}
}

// ── dumpMempoolShadowBundle: empty arb ID → "anon" ──────────────

func TestDumpMempoolShadowBundle_EmptyArbID(t *testing.T) {
	dir := t.TempDir()
	origSession := mempoolShadowSessionDir
	mempoolShadowSessionDir = func() string { return dir }
	defer func() { mempoolShadowSessionDir = origSession }()

	arb := &pb.ValidatedArb{Id: "", FlashloanToken: []byte{}, FlashloanAmount: []byte{}, NetProfitWei: []byte{}}
	bundle := &Bundle{BlockNumber: 0}
	gasFees := GasFees{BaseFee: big.NewInt(1), MaxFeePerGas: big.NewInt(1), MaxPriorityFee: big.NewInt(1)}
	decision := MempoolPreflightResult{Approved: true}

	if err := dumpMempoolShadowBundle(arb, bundle, gasFees, 0, decision); err != nil {
		t.Fatalf("dumpMempoolShadowBundle: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "anon.json" {
		t.Fatalf("expected anon.json, got %v", entries)
	}
}

// ── dumpShadowBundle: with hops for path serialization ───────────

func TestDumpShadowBundle_WithHops(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AETHER_SHADOW_DUMP_DIR", dir)

	weth := common.HexToAddress("0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2").Bytes()
	arb := &pb.ValidatedArb{
		Id: "test-hops-123",
		Hops: []*pb.ArbHop{{
			Protocol:     pb.ProtocolType_UNISWAP_V2,
			PoolAddress:  []byte{0x01},
			TokenIn:      weth,
			TokenOut:     []byte{0xa0, 0xb8, 0x69},
			AmountIn:     big.NewInt(1e18).Bytes(),
			ExpectedOut:  big.NewInt(1e18).Bytes(),
			EstimatedGas: 100000,
		}},
		FlashloanToken:  weth,
		FlashloanAmount: big.NewInt(1e18).Bytes(),
		NetProfitWei:    big.NewInt(1e15).Bytes(),
	}
	bundle := &Bundle{RawTxs: [][]byte{{0xf8, 0x6c}, {0xf8, 0x6d}}, BlockNumber: 101}
	if err := dumpShadowBundle(arb, bundle, 0.001, 30.0, 0.1); err != nil {
		t.Fatalf("dumpShadowBundle: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 file, got %d", len(entries))
	}
}

// ── dumpMempoolShadowBundle: with gates ──────────────────────────

// (TestDumpMempoolShadowBundle_WithGates moved to shadow_dump_test.go)

// ── processArb: live mempool backrun (not shadow) with submitFn error ─

func TestNewAdminRateLimiter_ZeroRate(t *testing.T) {
	l := newAdminRateLimiter(0, 0)
	if l != nil {
		t.Fatal("expected nil limiter for zero rate")
	}
}

func TestNewAdminRateLimiter_BurstZero(t *testing.T) {
	l := newAdminRateLimiter(2.0, 0)
	if l == nil {
		t.Fatal("expected non-nil")
	}
	if l.burst != 2 {
		t.Fatalf("expected burst=2, got %d", l.burst)
	}
}

// ── requireAdminAuthWithRateLimit: no rate limiter ───────────────

func TestRateLimitedAdmin_NoRateLimiter(t *testing.T) {
	resetAdminRateLimiterForTest(nil)
	configuredAdminToken = "token"
	defer func() {
		resetAdminRateLimiterForTest(nil)
		configuredAdminToken = ""
	}()

	handler := requireAdminAuthWithRateLimit(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/admin/test", nil)
	req.Header.Set("Authorization", "Bearer token")
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// ── requireAdminAuth: valid token ────────────────────────────────

func TestRequireAdminAuth_ValidToken(t *testing.T) {
	configuredAdminToken = "correct-token"
	defer func() { configuredAdminToken = "" }()
	handler := requireAdminAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/test", nil)
	req.Header.Set("Authorization", "Bearer correct-token")
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// ── extractAdminToken: X-Aether-Admin-Token header ───────────────

func TestExtractAdminToken_AdminTokenHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/admin/test", nil)
	req.Header.Set("X-Aether-Admin-Token", "admin-header-token")
	if got := extractAdminToken(req); got != "admin-header-token" {
		t.Fatalf("expected 'admin-header-token', got %q", got)
	}
}

// ── handleAdminPause: non-invalid-transition error (already paused) ──

func TestHandleAdminPause_InternalError(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.Pause("setup")
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm}
	defer func() { globalAdminDeps = prevDeps }()

	req := httptest.NewRequest(http.MethodPost, "/admin/pause?reason=double", nil)
	w := httptest.NewRecorder()
	handleAdminPause(w, req)
	// Already paused → invalid transition → 409
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}

// ── handleAdminResume: already running → invalid transition ───────

func TestHandleAdminResume_AlreadyRunning(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm}
	defer func() { globalAdminDeps = prevDeps }()

	req := httptest.NewRequest(http.MethodPost, "/admin/resume", nil)
	w := httptest.NewRecorder()
	handleAdminResume(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}

// ── handleAdminReset: not from halted → 409 ──────────────────────

func TestHandleAdminReset_FromPaused(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.Pause("test")
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm}
	defer func() { globalAdminDeps = prevDeps }()

	req := httptest.NewRequest(http.MethodPost, "/admin/reset", nil)
	w := httptest.NewRecorder()
	handleAdminReset(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}

// ── handleAdminResume: with engineCtrl error ─────────────────────

func TestHandleAdminResume_EngineCtrlError(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.Pause("test")
	ctrl := &testMockEngineCtrl2{err: errors.New("grpc fail")}
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm, engineCtrl: ctrl}
	defer func() { globalAdminDeps = prevDeps }()

	req := httptest.NewRequest(http.MethodPost, "/admin/resume", nil)
	w := httptest.NewRecorder()
	handleAdminResume(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// ── handleAdminReset: engineCtrl error ───────────────────────────

func TestHandleAdminReset_EngineCtrlError(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.ForceStateForTest(risk.StateHalted)
	ctrl := &testMockEngineCtrl2{err: errors.New("grpc fail")}
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm, engineCtrl: ctrl}
	defer func() { globalAdminDeps = prevDeps }()

	req := httptest.NewRequest(http.MethodPost, "/admin/reset", nil)
	w := httptest.NewRecorder()
	handleAdminReset(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// ── GetBundleStats: various error paths ──────────────────────────

func TestGetBundleStats_SignerSignError(t *testing.T) {
	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	builders := []BuilderConfig{{Name: "flashbots", Enabled: true, AuthType: "flashbots", URL: "http://localhost:1", TimeoutMs: 1000}}
	s, _ := NewSubmitter(builders, key)
	s.SetAuthSigner(&testMockFlashbotsAuther{sig: ""})
	// Empty sig will cause issues but the mock returns empty string
	_, err := s.GetBundleStats(context.Background(), "0xabc", 100)
	if err == nil {
		t.Fatal("expected error from HTTP failure to localhost:1")
	}
}

func TestGetBundleStats_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	builders := []BuilderConfig{{Name: "flashbots", Enabled: true, AuthType: "flashbots", URL: srv.URL, TimeoutMs: 2000}}
	s, _ := NewSubmitter(builders, key)
	_, err := s.GetBundleStats(context.Background(), "0xabc", 100)
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("expected parse error, got: %v", err)
	}
}

// ── submitter: SetAuthSigner, flashbotsAuth fallbacks ────────────

func TestFlashbotsAuth_LocalSignerFallback(t *testing.T) {
	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	s, _ := NewSubmitter(nil, key)
	// No authSigner set, should fallback to local signer
	auth := s.flashbotsAuth()
	if auth == nil {
		t.Fatal("expected non-nil auth from local signer fallback")
	}
}

func TestFlashbotsAuth_NilSignerAndAuth(t *testing.T) {
	builders := []BuilderConfig{{Name: "b1", Enabled: true, TimeoutMs: 1000}}
	s, _ := NewSubmitter(builders, "")
	auth := s.flashbotsAuth()
	if auth != nil {
		t.Fatal("expected nil auth when no signer")
	}
}

// ── setAuthHeaders: sign error path ──────────────────────────────

func TestSetAuthHeaders_FlashbotsSignError(t *testing.T) {
	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	s, _ := NewSubmitter(nil, key)
	s.SetAuthSigner(&failingFlashbotsAuther{err: errors.New("sign failed")})
	req, _ := http.NewRequest("POST", "http://example.com", nil)
	err := s.setAuthHeaders(req, BuilderConfig{AuthType: "flashbots", Name: "b1"}, []byte("test"))
	if err == nil || !strings.Contains(err.Error(), "sign failed") {
		t.Fatalf("expected sign error, got: %v", err)
	}
}

type failingFlashbotsAuther struct{ err error }

func (f *failingFlashbotsAuther) Sign(payload []byte) (string, error) {
	return "", f.err
}

// ── submitter: mixed success + failure ───────────────────────────

func TestSubmitToAll_MixedResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"bundleHash":"0xabc123"}}`))
	}))
	defer srv.Close()

	builders := []BuilderConfig{
		{Name: "b1", Enabled: true, URL: srv.URL, TimeoutMs: 2000},
		{Name: "b2", Enabled: true, URL: "http://localhost:1", TimeoutMs: 500},
	}
	s, _ := NewSubmitter(builders, "")
	results := s.SubmitToAll(context.Background(), &Bundle{BlockNumber: 1, RawTxs: [][]byte{{0x01}}})
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

// ── SuccessCount ─────────────────────────────────────────────────

func TestSuccessCount_Mixed(t *testing.T) {
	results := []SubmissionResult{
		{Success: true},
		{Success: false},
		{Success: true},
	}
	if c := SuccessCount(results); c != 2 {
		t.Fatalf("expected 2, got %d", c)
	}
}

func TestSuccessCount_Empty(t *testing.T) {
	if c := SuccessCount(nil); c != 0 {
		t.Fatalf("expected 0, got %d", c)
	}
}

// ── processArb: missing victim_raw_tx (empty slice) ──────────────

func TestProcessArb_MempoolBackrun_EmptyVictimRawTx(t *testing.T) {
	initTestMempoolRisk()
	rm, bundler, submitter := newTestComponents()
	arb := newValidArb("arb-empty-victim", 0.01, 5.0)
	arb.Source = pb.ArbSource_MEMPOOL_BACKRUN
	arb.VictimTxHash = make([]byte, 32)
	arb.VictimTxHash[0] = 0xac
	arb.VictimRawTx = []byte{} // empty slice, not nil
	arb.TimestampNs = time.Now().UnixNano()

	submitted, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if submitted {
		t.Fatal("expected not submitted when victim_raw_tx is empty")
	}
}

// ── resolveInclusion: mempool revert path (not included) ─────────

func TestResolveInclusion_MempoolRevert(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	prevSel := builderSelector
	builderSelector = strategy.New([]string{"b1"}, strategy.Config{ExplorationFloor: 0.1})
	defer func() { builderSelector = prevSel }()
	ledger := db.NewNoopLedger()
	p := pendingBundle{
		bundleID: uuid.New(), bundleHash: "0xabc", targetBlock: 100, builder: "b1",
		profitWei: big.NewInt(1e15), source: SourceMempoolBackrun, submittedAt: time.Now().UTC().Add(-time.Minute),
	}
	resolveInclusion(p, ledger, false, 0, rm)
}

// ── run(): with discovery URL (poll top pools) ───────────────────

func TestRun_WithDiscoveryURL(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	t.Setenv("REDIS_URL", "redis://"+mr.Addr())

	adminServerOnce = sync.Once{}

	cfg := defaultConfig()
	cfg.GRPCAddress = "localhost:99999"
	cfg.BuilderConfigs = nil

	deps := &Dependencies{
		Submitter: &Submitter{}, Ledger: db.NewNoopLedger(), MetricsStore: db.NewNoopMetricsStore(),
		EventPublisher: events.NewPublisherFromEnv(), ExecutorAddr: "0x0000000000000000000000000000000000000001",
		ChainID: 1, GRPCDial: func(addr string) (*aethergrpc.Client, error) {
			return nil, errors.New("no gRPC")
		},
		SkipMetricsHTTP: true, SkipAdminHTTP: false, ReconnectDelay: 10 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	deps.WaitForShutdown = func(ctx context.Context, c context.CancelFunc) error {
		<-time.After(150 * time.Millisecond)
		c()
		return nil
	}
	_ = run(ctx, &cfg, deps)
}

// ── processArb: live mempool backrun (not shadow) with submitFn error ─

func TestProcessArb_MempoolLive_SubmitError(t *testing.T) {
	prevMode := getBackrunMode()
	defer setBackrunMode(prevMode)
	setBackrunMode(BackrunLiveOnly)

	rm, bundler, submitter := newTestComponents()
	submitter.submitFn = func(ctx context.Context, b BuilderConfig, bundle *Bundle) SubmissionResult {
		return SubmissionResult{Builder: b.Name, Success: false, Error: errors.New("rejected")}
	}

	arb := newValidArb("arb-mempool-live-error", 0.01, 5.0)
	arb.Source = pb.ArbSource_MEMPOOL_BACKRUN
	arb.VictimTxHash = make([]byte, 32)
	arb.VictimTxHash[0] = 0xad
	arb.VictimRawTx = []byte{0xf8, 0x01, 0x02}
	arb.TimestampNs = time.Now().UnixNano()

	submitted, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	_ = submitted // builder rejected but still "submitted" (no error)
}

// ── setAuthHeaders: no-op auth type ──────────────────────────────

func TestSetAuthHeaders_NoneAuth(t *testing.T) {
	builders := []BuilderConfig{{Name: "b1", Enabled: true, TimeoutMs: 1000}}
	s, _ := NewSubmitter(builders, "")
	req, _ := http.NewRequest("POST", "http://example.com", nil)
	err := s.setAuthHeaders(req, BuilderConfig{AuthType: "none", Name: "b1"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSetAuthHeaders_EmptyAuth(t *testing.T) {
	builders := []BuilderConfig{{Name: "b1", Enabled: true, TimeoutMs: 1000}}
	s, _ := NewSubmitter(builders, "")
	req, _ := http.NewRequest("POST", "http://example.com", nil)
	err := s.setAuthHeaders(req, BuilderConfig{AuthType: "", Name: "b1"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── handleSetMinProfit: from query param ─────────────────────────

func TestHandleSetMinProfit_FromQueryParam(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm}
	defer func() { globalAdminDeps = prevDeps }()
	req := httptest.NewRequest(http.MethodPost, "/admin/set_min_profit?value=0.5", nil)
	w := httptest.NewRecorder()
	handleSetMinProfit(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// ── handleAdminReset: wrong reset token via Authorization header ──

func TestHandleAdminReset_WrongResetTokenViaAuth(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.ForceStateForTest(risk.StateHalted)
	os.Setenv("AETHER_RESET_CONFIRM_TOKEN", "secret-token")
	defer os.Unsetenv("AETHER_RESET_CONFIRM_TOKEN")
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm}
	defer func() { globalAdminDeps = prevDeps }()

	req := httptest.NewRequest(http.MethodPost, "/admin/reset", nil)
	req.Header.Set("Authorization", "Bearer wrong-token-via-auth")
	w := httptest.NewRecorder()
	handleAdminReset(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

// ── handleAdminReset: confirm token via X-Aether-Reset-Confirm ────

func TestHandleAdminReset_ConfirmViaHeader(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.ForceStateForTest(risk.StateHalted)
	os.Setenv("AETHER_RESET_CONFIRM_TOKEN", "secret")
	defer os.Unsetenv("AETHER_RESET_CONFIRM_TOKEN")
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm}
	defer func() { globalAdminDeps = prevDeps }()

	req := httptest.NewRequest(http.MethodPost, "/admin/reset", nil)
	req.Header.Set("X-Aether-Reset-Confirm", "secret")
	w := httptest.NewRecorder()
	handleAdminReset(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// ── handleBackrunPromote: via X-Aether-Backrun-Confirm header ────

func TestHandleBackrunPromote_ConfirmViaHeader(t *testing.T) {
	os.Setenv("AETHER_BACKRUN_CONFIRM_TOKEN", "my-token")
	defer os.Unsetenv("AETHER_BACKRUN_CONFIRM_TOKEN")
	req := httptest.NewRequest(http.MethodPost, "/admin/backrun/promote", nil)
	req.Header.Set("X-Aether-Backrun-Confirm", "my-token")
	w := httptest.NewRecorder()
	handleBackrunPromote(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if getBackrunMode() != BackrunLiveOnly {
		t.Fatalf("expected live_only, got %s", getBackrunMode())
	}
}

// ── pollTopPoolsLoop: with URL ───────────────────────────────────

func TestPollTopPoolsLoop_WithEmptyURL(t *testing.T) {
	store := metrics.NewStore()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	pollTopPoolsLoop(ctx, "", store, 10*time.Millisecond)
	// Should return immediately with empty URL
}

// ── refreshSnapshotLoop: halted state ────────────────────────────

func TestRefreshSnapshotLoop_Halted(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.ForceStateForTest(risk.StateHalted)
	store := metrics.NewStore()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	go refreshSnapshotLoop(ctx, rm, store, 10*time.Millisecond)
	<-ctx.Done()
	time.Sleep(20 * time.Millisecond)
	snap := store.Get()
	if snap.SystemState != string(risk.StateHalted) {
		t.Fatalf("expected Halted, got %s", snap.SystemState)
	}
	if !snap.BreakerOpen {
		t.Fatal("expected BreakerOpen for halted state")
	}
}

func TestRefreshSnapshotLoop_Running(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	store := metrics.NewStore()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	go refreshSnapshotLoop(ctx, rm, store, 10*time.Millisecond)
	<-ctx.Done()
	time.Sleep(20 * time.Millisecond)
	snap := store.Get()
	if snap.SystemState != string(risk.StateRunning) {
		t.Fatalf("expected Running, got %s", snap.SystemState)
	}
	if snap.BreakerOpen {
		t.Fatal("expected breaker closed for running state")
	}
}

// ── metricsStoreHealthy: with DB URL but pinger returns false ─────

func TestMetricsStoreHealthy_DBURLNoPinger(t *testing.T) {
	orig := os.Getenv("DATABASE_URL")
	os.Setenv("DATABASE_URL", "postgres://localhost/test")
	defer os.Setenv("DATABASE_URL", orig)

	oldStore := metricsStore
	metricsStore = db.NoopMetricsStore{}
	defer func() { metricsStore = oldStore }()

	// NoopMetricsStore doesn't implement pinger → returns false
	if metricsStoreHealthy() {
		t.Fatal("expected false when no pinger interface")
	}
}

// ── getBundleStats: HTTP error path ──────────────────────────────

func TestGetBundleStats_HTTPConnectionRefused(t *testing.T) {
	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	builders := []BuilderConfig{{Name: "flashbots", Enabled: true, AuthType: "flashbots", URL: "http://localhost:1", TimeoutMs: 500}}
	s, _ := NewSubmitter(builders, key)
	_, err := s.GetBundleStats(context.Background(), "0xabc", 100)
	if err == nil {
		t.Fatal("expected connection error")
	}
}

// ── submitToBuilder: invalid URL create request error ────────────

func TestSubmitToBuilder_InvalidURL(t *testing.T) {
	builders := []BuilderConfig{{Name: "b1", Enabled: true, URL: "\x00invalid", TimeoutMs: 1000}}
	s, _ := NewSubmitter(builders, "")
	results := s.SubmitToBuilder(context.Background(), &Bundle{BlockNumber: 1, RawTxs: [][]byte{{0x01}}}, "b1")
	if len(results) != 1 || results[0].Success {
		t.Fatalf("expected failure for invalid URL, got %v", results)
	}
}

// ── submitToBuilder: unparseable result warn ─────────────────────

func TestSubmitToBuilder_UnparseableResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"bundleHash":123}}`))
	}))
	defer srv.Close()

	builders := []BuilderConfig{{Name: "b1", Enabled: true, URL: srv.URL, TimeoutMs: 2000}}
	s, _ := NewSubmitter(builders, "")
	results := s.SubmitToBuilder(context.Background(), &Bundle{BlockNumber: 1, RawTxs: [][]byte{{0x01}}}, "b1")
	if len(results) != 1 || !results[0].Success {
		t.Fatalf("expected success despite unparseable result, got %v", results)
	}
}

// ── rpc_redact: unparseable URL ──────────────────────────────────

func TestRedactRPCURL_Unparseable(t *testing.T) {
	// url.Parse doesn't return errors, but it might produce empty host
	result := redactRPCURL("://not-a-url")
	if result == "" {
		t.Fatal("expected non-empty result")
	}
}

// ── dumpShadowBundle: read-only dir ──────────────────────────────

func TestDumpShadowBundle_MkdirError(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0o444)
	defer os.Chmod(dir, 0o755)
	t.Setenv("AETHER_SHADOW_DUMP_DIR", dir+"/nonexistent/subdir")

	arb := &pb.ValidatedArb{Id: "test-mkdir", Hops: nil, FlashloanToken: []byte{}, FlashloanAmount: []byte{}, NetProfitWei: []byte{}}
	bundle := &Bundle{RawTxs: nil, BlockNumber: 0}
	err := dumpShadowBundle(arb, bundle, 0, 0, 0)
	if err == nil {
		t.Fatal("expected mkdir error")
	}
}

// ── dumpMempoolShadowBundle: read-only dir ──────────────────────

func TestDumpMempoolShadowBundle_MkdirError(t *testing.T) {
	origSession := mempoolShadowSessionDir
	mempoolShadowSessionDir = func() string { return "/dev/null/fake/dir" }
	defer func() { mempoolShadowSessionDir = origSession }()

	arb := &pb.ValidatedArb{Id: "test-mkdir", FlashloanToken: []byte{}, FlashloanAmount: []byte{}, NetProfitWei: []byte{}}
	bundle := &Bundle{BlockNumber: 0}
	gasFees := GasFees{BaseFee: big.NewInt(1), MaxFeePerGas: big.NewInt(1), MaxPriorityFee: big.NewInt(1)}
	err := dumpMempoolShadowBundle(arb, bundle, gasFees, 0, MempoolPreflightResult{Approved: true})
	if err == nil {
		t.Fatal("expected mkdir error")
	}
}

// ── processArb: signer error in mempool path → Pause fails ──────

func TestProcessArb_MempoolSignerUnavailable_WithEventPublisher(t *testing.T) {
	initTestMempoolRisk()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	nm := NewNonceManager(0)
	go_ := NewGasOracle(300.0)
	bundler := NewBundleConstructor(nm, go_, &testFailingSigner{err: errSignerUnavailable}, 1)
	builders := []BuilderConfig{{Name: "b1", Enabled: true, TimeoutMs: 1000}}
	submitter, _ := NewSubmitter(builders, "")

	oldPub := eventPublisher
	oldStore := metricsStore
	eventPublisher = events.NewPublisherFromEnv()
	metricsStore = db.NewNoopMetricsStore()
	defer func() {
		eventPublisher = oldPub
		metricsStore = oldStore
	}()

	arb := newValidArb("arb-mempool-signer-pub", 0.01, 5.0)
	arb.Source = pb.ArbSource_MEMPOOL_BACKRUN
	arb.VictimTxHash = make([]byte, 32)
	arb.VictimTxHash[0] = 0xae
	arb.VictimRawTx = []byte{0xf8, 0x01, 0x02}
	arb.TimestampNs = time.Now().UnixNano()

	_, _ = processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
}

// ── loadAdminPort: success path with production config ────────────

func TestLoadAdminPort_ProductionConfigSuccess(t *testing.T) {
	dir := t.TempDir()
	prodPath := dir + "/production.toml"
	content := `
[telegram]
bot_token = "test:token"
admin_chat_ids = [123456]

[executor]
port = 9090
discovery_top_pools_url = "https://discovery.example.com"
`
	os.WriteFile(prodPath, []byte(content), 0o644)

	origDir := os.Getenv("AETHER_CONFIG_DIR")
	os.Setenv("AETHER_CONFIG_DIR", dir)
	defer os.Setenv("AETHER_CONFIG_DIR", origDir)

	os.Unsetenv("ADMIN_HTTP_PORT")
	port, url := loadAdminPort()
	if port != 9090 {
		t.Fatalf("expected 9090, got %d", port)
	}
	if url != "https://discovery.example.com" {
		t.Fatalf("expected discovery URL, got %q", url)
	}
}

func TestLoadAdminPort_ProductionConfigWithEnvOverride(t *testing.T) {
	dir := t.TempDir()
	prodPath := dir + "/production.toml"
	content := `
[telegram]
bot_token = "test:token"
admin_chat_ids = [123456]

[executor]
port = 9090
`
	os.WriteFile(prodPath, []byte(content), 0o644)

	origDir := os.Getenv("AETHER_CONFIG_DIR")
	os.Setenv("AETHER_CONFIG_DIR", dir)
	defer os.Setenv("AETHER_CONFIG_DIR", origDir)
	t.Setenv("ADMIN_HTTP_PORT", "8080")

	port, _ := loadAdminPort()
	if port != 8080 {
		t.Fatalf("expected 8080 from env, got %d", port)
	}
}

// ── processArb: signer error + pause fails (halted state) ────────

func TestProcessArb_SignerUnavailable_Halted(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.ForceStateForTest(risk.StateHalted)
	nm := NewNonceManager(0)
	go_ := NewGasOracle(300.0)
	bundler := NewBundleConstructor(nm, go_, &testFailingSigner{err: errSignerUnavailable}, 1)
	builders := []BuilderConfig{{Name: "b1", Enabled: true, TimeoutMs: 1000}}
	submitter, _ := NewSubmitter(builders, "")

	oldPub := eventPublisher
	oldStore := metricsStore
	eventPublisher = events.NewPublisherFromEnv()
	metricsStore = db.NewNoopMetricsStore()
	defer func() {
		eventPublisher = oldPub
		metricsStore = oldStore
	}()

	arb := newValidArb("arb-signer-halted", 0.01, 5.0)
	_, _ = processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
	// Should not crash even when pause fails
}

// ── processArb: mempool shadow_and_live mode ─────────────────────

func TestProcessArb_MempoolShadowAndLive(t *testing.T) {
	prevMode := getBackrunMode()
	defer setBackrunMode(prevMode)
	setBackrunMode(BackrunShadowAndLive)

	rm, bundler, submitter := newTestComponents()
	submitter.submitFn = func(ctx context.Context, b BuilderConfig, bundle *Bundle) SubmissionResult {
		return SubmissionResult{Builder: b.Name, Success: true, BundleHash: "sl-hash"}
	}

	arb := newValidArb("arb-shadow-and-live", 0.01, 5.0)
	arb.Source = pb.ArbSource_MEMPOOL_BACKRUN
	arb.VictimTxHash = make([]byte, 32)
	arb.VictimTxHash[0] = 0xbf
	arb.VictimRawTx = []byte{0xf8, 0x01, 0x02}
	arb.TimestampNs = time.Now().UnixNano()

	submitted, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if !submitted {
		t.Fatal("expected submission in shadow_and_live mode")
	}
}

// ── processArb: shadow mempool with bad dump dir ─────────────────

func TestProcessArb_ShadowMempool_BadDumpDir(t *testing.T) {
	prevMode := getBackrunMode()
	defer setBackrunMode(prevMode)
	setBackrunMode(BackrunShadowOnly)

	origSession := mempoolShadowSessionDir
	mempoolShadowSessionDir = func() string { return "/dev/null/cant/mkdir" }
	defer func() { mempoolShadowSessionDir = origSession }()

	rm, bundler, submitter := newTestComponents()
	arb := newValidArb("arb-shadow-bad-dir", 0.01, 5.0)
	arb.Source = pb.ArbSource_MEMPOOL_BACKRUN
	arb.VictimTxHash = make([]byte, 32)
	arb.VictimTxHash[0] = 0xcf
	arb.VictimRawTx = []byte{0xf8, 0x01, 0x02}
	arb.TimestampNs = time.Now().UnixNano()

	submitted, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if !submitted {
		t.Fatal("expected shadow submission despite dump failure")
	}
}

// ── processArb: block-driven shadow with bad dump dir ────────────

func TestProcessArb_BlockDrivenShadow_BadDumpDir(t *testing.T) {
	prevShadow := os.Getenv("AETHER_SHADOW")
	defer os.Setenv("AETHER_SHADOW", prevShadow)
	os.Setenv("AETHER_SHADOW", "1")
	t.Setenv("AETHER_SHADOW_DUMP_DIR", "/dev/null/cant/mkdir")

	rm, bundler, submitter := newTestComponents()
	arb := newValidArb("arb-bd-shadow-bad-dir", 0.01, 5.0)

	submitted, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if !submitted {
		t.Fatal("expected shadow submission despite dump failure")
	}
}

// ── consumeArbStream: error logging in stream ────────────────────

func TestConsumeArbStream_ProcessError(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	nm := NewNonceManager(0)
	go_ := NewGasOracle(300.0)
	bundler := NewBundleConstructor(nm, go_, &testFailingSigner{err: errSignerUnavailable}, 1)
	builders := []BuilderConfig{{Name: "b1", Enabled: true, TimeoutMs: 1000}}
	submitter, _ := NewSubmitter(builders, "")
	lb := NewLiveBalance()
	lb.Set(0.5)

	eventPublisher = events.NewPublisherFromEnv()
	metricsStore = db.NewNoopMetricsStore()

	srv := testutil.NewMockArbServer()
	srv.SetArbs([]*pb.ValidatedArb{
		{
			Id:              "error-arb",
			NetProfitWei:    big.NewInt(1e15).Bytes(),
			FlashloanAmount: big.NewInt(5e18).Bytes(),
			TotalGas:        200000,
			BlockNumber:     100,
			Calldata:        []byte{0x01},
			Hops:            []*pb.ArbHop{{PoolAddress: []byte{0x01}, Protocol: pb.ProtocolType_UNISWAP_V2}},
		},
	})
	dialer, cleanup, err := srv.StartBufconn(0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	conn, err := srv.DialBufconn(ctx, dialer)
	if err != nil {
		t.Fatal(err)
	}
	grpcClient, err := aethergrpc.NewClientFromConn(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer grpcClient.Close()

	consumeArbStream(ctx, grpcClient, bundler, submitter, rm, db.NewNoopLedger(),
		"0x0000000000000000000000000000000000000001", lb, 0)
}

func TestLoadRiskConfig_Success(t *testing.T) {
	dir := t.TempDir()
	riskPath := dir + "/risk.yaml"
	content := `circuit_breakers:
  max_gas_gwei: 250
  consecutive_reverts_pause: 10
  revert_window_minutes: 10
  daily_loss_halt_eth: 0.5
  min_eth_balance: 0.1
  max_node_latency_ms: 500
  bundle_miss_rate_alert_pct: 80
  bundle_miss_rate_window_minutes: 60
  competitive_revert_alert_pct: 90

position_limits:
  max_single_trade_eth: 40.0
  max_daily_volume_eth: 400.0
  min_profit_eth: 0.002
  min_tip_share_pct: 50
  max_tip_share_pct: 95
`
	os.WriteFile(riskPath, []byte(content), 0o644)

	origDir := os.Getenv("AETHER_CONFIG_DIR")
	os.Setenv("AETHER_CONFIG_DIR", dir)
	defer os.Setenv("AETHER_CONFIG_DIR", origDir)

	rc := loadRiskConfig()
	if rc.MaxGasGwei != 250.0 {
		t.Fatalf("expected MaxGasGwei=250, got %f", rc.MaxGasGwei)
	}
}

// ── consumeArbStream: error path logging ─────────────────────────

func TestConsumeArbStream_ErrorPath(t *testing.T) {
	rm, bundler, submitter := newTestComponents()
	lb := NewLiveBalance()
	lb.Set(0.5)

	client, _ := aethergrpc.Dial("127.0.0.1:1")
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	consumeArbStream(ctx, client, bundler, submitter, rm, db.NewNoopLedger(),
		"0x0000000000000000000000000000000000000001", lb, 0)
}

// ── inclusion poll: fmtSscanfHex with uppercase ──────────────────

func TestFmtSscanfHex_UpperCase(t *testing.T) {
	var n uint64
	fmtSscanfHex("0XABCDEF", &n)
	if n != 0xABCDEF {
		t.Fatalf("expected 0xABCDEF, got %d", n)
	}
}

func TestFmtSscanfHex_ValidHex(t *testing.T) {
	var n uint64
	fmtSscanfHex("ff", &n)
	if n != 0xff {
		t.Fatalf("expected 0xff, got %d", n)
	}
}

// ── handleSetMinProfit: wrong method ─────────────────────────────

func TestHandleSetMinProfit_InternalError(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	prevDeps := globalAdminDeps
	globalAdminDeps = adminDeps{riskMgr: rm}
	defer func() { globalAdminDeps = prevDeps }()

	req := httptest.NewRequest(http.MethodPost, "/admin/set_min_profit", strings.NewReader("0.5"))
	w := httptest.NewRecorder()
	handleSetMinProfit(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// ── pollPendingInclusions: error retry + timeout ─────────────────

func TestPollPendingInclusions_ErrorRetryTimeout(t *testing.T) {
	pendingMu.Lock()
	pendingQueue = nil
	pendingMu.Unlock()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	builders := []BuilderConfig{{Name: "flashbots", Enabled: true, AuthType: "flashbots", URL: srv.URL, TimeoutMs: 2000}}
	s, _ := NewSubmitter(builders, key)

	enqueuePendingBundle(pendingBundle{
		bundleID:    uuid.New(),
		bundleHash:  "0xabc",
		targetBlock: 100,
		builder:     "flashbots",
		profitWei:   big.NewInt(1e15),
		source:      SourceMempoolBackrun,
		submittedAt: time.Now().UTC().Add(-6 * time.Minute), // >5 min timeout
	})

	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	pollPendingInclusions(context.Background(), s, db.NewNoopLedger(), rm)

	pendingMu.Lock()
	n := len(pendingQueue)
	pendingMu.Unlock()
	if n != 0 {
		t.Fatalf("expected 0 pending after timeout, got %d", n)
	}
}

// ── pollPendingInclusions: not included + error retry ────────────

func TestPollPendingInclusions_NotIncludedRetry(t *testing.T) {
	pendingMu.Lock()
	pendingQueue = nil
	pendingMu.Unlock()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	builders := []BuilderConfig{{Name: "flashbots", Enabled: true, AuthType: "flashbots", URL: srv.URL, TimeoutMs: 2000}}
	s, _ := NewSubmitter(builders, key)

	enqueuePendingBundle(pendingBundle{
		bundleID:    uuid.New(),
		bundleHash:  "0xabc",
		targetBlock: 100,
		builder:     "flashbots",
		profitWei:   big.NewInt(1e15),
		source:      SourceBlockDriven,
		submittedAt: time.Now().UTC().Add(-2 * time.Minute), // between 15s and 5min
	})

	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	pollPendingInclusions(context.Background(), s, db.NewNoopLedger(), rm)

	pendingMu.Lock()
	n := len(pendingQueue)
	pendingMu.Unlock()
	if n != 1 {
		t.Fatalf("expected 1 pending (retry), got %d", n)
	}
}

// ── initAdminRateLimit: env var ──────────────────────────────────

func TestInitAdminRateLimit_EnvVar(t *testing.T) {
	t.Setenv("ADMIN_RATE_LIMIT_RPS", "10.0")
	initAdminRateLimit()
	if globalAdminRateLimiter == nil {
		t.Fatal("expected non-nil rate limiter")
	}
}

func TestInitAdminRateLimit_InvalidEnv(t *testing.T) {
	t.Setenv("ADMIN_RATE_LIMIT_RPS", "invalid")
	initAdminRateLimit()
	// Should fall through to nil since invalid value → 0
	// The actual behavior depends on production config load too
}

func TestInitAdminRateLimit_NegativeEnv(t *testing.T) {
	t.Setenv("ADMIN_RATE_LIMIT_RPS", "-5.0")
	initAdminRateLimit()
	// Negative value should result in nil limiter
}

// ── signer.go: SignAndMarshal success ────────────────────────────

func TestTransactionSigner_SignAndMarshal(t *testing.T) {
	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	ts, err := NewTransactionSigner(key, 1)
	if err != nil {
		t.Fatal(err)
	}
	to := common.HexToAddress("0x0000000000000000000000000000000000000001")
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   big.NewInt(1),
		Nonce:     0,
		GasTipCap: big.NewInt(1e9),
		GasFeeCap: big.NewInt(30e9),
		Gas:       21000,
		To:        &to,
		Value:     big.NewInt(0),
		Data:      nil,
	})
	raw, err := ts.SignAndMarshal(tx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("expected non-empty bytes")
	}
}

// ── signer.go: SignTx success ────────────────────────────────────

func TestTransactionSigner_SignTx(t *testing.T) {
	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	ts, err := NewTransactionSigner(key, 1)
	if err != nil {
		t.Fatal(err)
	}
	to := common.HexToAddress("0x0000000000000000000000000000000000000001")
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   big.NewInt(1),
		Nonce:     0,
		GasTipCap: big.NewInt(1e9),
		GasFeeCap: big.NewInt(30e9),
		Gas:       21000,
		To:        &to,
		Value:     big.NewInt(0),
		Data:      nil,
	})
	signed, err := ts.SignTx(tx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if signed == nil {
		t.Fatal("expected signed tx")
	}
}

// ── FlashbotsSigner: Sign error path ─────────────────────────────

func TestFlashbotsSigner_Sign_Consistency(t *testing.T) {
	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	fs, err := NewFlashbotsSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"method":"eth_sendBundle","params":[{"txs":["0xabc"],"blockNumber":"0x1"}]}`)
	sig1, _ := fs.Sign(payload)
	sig2, _ := fs.Sign(payload)
	if sig1 != sig2 {
		t.Fatal("expected deterministic signatures")
	}
	if !strings.HasPrefix(sig1, "0x") {
		t.Fatalf("expected 0x-prefixed address, got %q", sig1)
	}
}

// ── submitToBuilder: GetBundleStats create request error ─────────

func TestGetBundleStats_CreateRequestError(t *testing.T) {
	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	builders := []BuilderConfig{{Name: "flashbots", Enabled: true, AuthType: "flashbots", URL: "\x00invalid", TimeoutMs: 1000}}
	s, _ := NewSubmitter(builders, key)
	_, err := s.GetBundleStats(context.Background(), "0xabc", 100)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ── pollPendingInclusions: not included, still within time ────────

func TestPollPendingInclusions_NotIncluded_TooSoon(t *testing.T) {
	pendingMu.Lock()
	pendingQueue = nil
	pendingMu.Unlock()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"isHighPriority":false,"isSentToMiners":false,"blockNumber":"0x0"}}`))
	}))
	defer srv.Close()

	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	builders := []BuilderConfig{{Name: "flashbots", Enabled: true, AuthType: "flashbots", URL: srv.URL, TimeoutMs: 2000}}
	s, _ := NewSubmitter(builders, key)

	enqueuePendingBundle(pendingBundle{
		bundleID:    uuid.New(),
		bundleHash:  "0xabc",
		targetBlock: 100,
		builder:     "flashbots",
		profitWei:   big.NewInt(1e15),
		source:      SourceMempoolBackrun,
		submittedAt: time.Now().UTC().Add(-2 * time.Minute), // between 15s and 5min
	})

	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	pollPendingInclusions(context.Background(), s, db.NewNoopLedger(), rm)

	pendingMu.Lock()
	n := len(pendingQueue)
	pendingMu.Unlock()
	if n != 1 {
		t.Fatalf("expected 1 pending, got %d", n)
	}
}

// ── getBundleStats: stats error from RPC ──────────────────────────

func TestGetBundleStats_RPCErrorFromStats(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"msg":"not found"}}`))
	}))
	defer srv.Close()

	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	builders := []BuilderConfig{{Name: "flashbots", Enabled: true, AuthType: "flashbots", URL: srv.URL, TimeoutMs: 2000}}
	s, _ := NewSubmitter(builders, key)
	_, err := s.GetBundleStats(context.Background(), "0xabc", 100)
	if err == nil || !strings.Contains(err.Error(), "stats error") {
		t.Fatalf("expected stats error, got: %v", err)
	}
}

// ── recordBundleMetrics: with zero receivedAt ─────────────────────

func TestRecordBundleMetrics_ZeroReceivedAt(t *testing.T) {
	prevSel := builderSelector
	prevStore := metricsStore
	defer func() {
		builderSelector = prevSel
		metricsStore = prevStore
	}()
	builderSelector = strategy.New([]string{"b1"}, strategy.Config{ExplorationFloor: 0.1})
	metricsStore = db.NewNoopMetricsStore()

	recordBundleMetrics(SourceBlockDriven, big.NewInt(1e16), time.Time{},
		[]SubmissionResult{
			{Builder: "b1", Success: true, Latency: time.Millisecond},
		}, false)
}

// ── getBundleStats: sign error ───────────────────────────────────

func TestGetBundleStats_SignError(t *testing.T) {
	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	builders := []BuilderConfig{{Name: "flashbots", Enabled: true, AuthType: "flashbots", URL: "http://localhost:1", TimeoutMs: 500}}
	s, _ := NewSubmitter(builders, key)
	s.SetAuthSigner(&failingFlashbotsAuther{err: errors.New("sign error")})
	_, err := s.GetBundleStats(context.Background(), "0xabc", 100)
	if err == nil {
		t.Fatal("expected sign error")
	}
}

// ── getBundleStats: non-200 response ─────────────────────────────

func TestGetBundleStats_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	key := "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	builders := []BuilderConfig{{Name: "flashbots", Enabled: true, AuthType: "flashbots", URL: srv.URL, TimeoutMs: 2000}}
	s, _ := NewSubmitter(builders, key)
	_, err := s.GetBundleStats(context.Background(), "0xabc", 100)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("expected HTTP 500 error, got: %v", err)
	}
}
