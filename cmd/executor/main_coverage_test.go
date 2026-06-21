package main

import (
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	pb "github.com/aether-arb/aether/internal/pb"
	"github.com/aether-arb/aether/internal/risk"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

type failingSigner struct{}

func (f *failingSigner) Address() common.Address { return common.Address{} }
func (f *failingSigner) SignTx(tx *types.Transaction) (*types.Transaction, error) {
	return nil, fmt.Errorf("signing failed")
}

// --- admin_server.go: engine ctrl paths ---

func TestAdminPause_WithEngineCtrl_NoError_Coverage(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	globalAdminDeps.riskMgr = rm
	globalAdminDeps.engineCtrl = &mockEngineCtrl{}
	req := httptest.NewRequest(http.MethodPost, "/admin/pause?reason=test", nil)
	w := httptest.NewRecorder()
	handleAdminPause(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
}

func TestAdminPause_WithEngineCtrl_Error_Coverage(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	globalAdminDeps.riskMgr = rm
	globalAdminDeps.engineCtrl = &mockEngineCtrl{err: fmt.Errorf("unavailable")}
	req := httptest.NewRequest(http.MethodPost, "/admin/pause?reason=test", nil)
	w := httptest.NewRecorder()
	handleAdminPause(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 even with engine err, got %d", w.Code)
	}
}

func TestAdminResume_WithEngineCtrl_NoError_Coverage(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	_ = rm.Pause("test")
	globalAdminDeps.riskMgr = rm
	globalAdminDeps.engineCtrl = &mockEngineCtrl{}
	globalAdminDeps.eventPub = nil
	req := httptest.NewRequest(http.MethodPost, "/admin/resume", nil)
	w := httptest.NewRecorder()
	handleAdminResume(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
}

func TestAdminResume_WithEngineCtrl_Error_Coverage(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	_ = rm.Pause("test")
	globalAdminDeps.riskMgr = rm
	globalAdminDeps.engineCtrl = &mockEngineCtrl{err: fmt.Errorf("unavailable")}
	globalAdminDeps.eventPub = nil
	req := httptest.NewRequest(http.MethodPost, "/admin/resume", nil)
	w := httptest.NewRecorder()
	handleAdminResume(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestAdminReset_ConfirmToken_Wrong_Coverage(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.ForceStateForTest(risk.StateHalted)
	globalAdminDeps.riskMgr = rm
	os.Setenv("AETHER_RESET_CONFIRM_TOKEN", "correct-token")
	defer os.Unsetenv("AETHER_RESET_CONFIRM_TOKEN")
	req := httptest.NewRequest(http.MethodPost, "/admin/reset", nil)
	req.Header.Set("X-Aether-Reset-Confirm", "wrong-token")
	w := httptest.NewRecorder()
	handleAdminReset(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestAdminReset_ConfirmToken_Correct_Coverage(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.ForceStateForTest(risk.StateHalted)
	globalAdminDeps.riskMgr = rm
	os.Setenv("AETHER_RESET_CONFIRM_TOKEN", "correct")
	defer os.Unsetenv("AETHER_RESET_CONFIRM_TOKEN")
	req := httptest.NewRequest(http.MethodPost, "/admin/reset", nil)
	req.Header.Set("X-Aether-Reset-Confirm", "correct")
	w := httptest.NewRecorder()
	handleAdminReset(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAdminReset_WithEngineCtrl_Coverage(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.ForceStateForTest(risk.StateHalted)
	globalAdminDeps.riskMgr = rm
	globalAdminDeps.engineCtrl = &mockEngineCtrl{}
	globalAdminDeps.eventPub = nil
	req := httptest.NewRequest(http.MethodPost, "/admin/reset", nil)
	w := httptest.NewRecorder()
	handleAdminReset(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
}

func TestAdminReset_WithEngineCtrl_Error_Coverage(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.ForceStateForTest(risk.StateHalted)
	globalAdminDeps.riskMgr = rm
	globalAdminDeps.engineCtrl = &mockEngineCtrl{err: fmt.Errorf("unavailable")}
	globalAdminDeps.eventPub = nil
	req := httptest.NewRequest(http.MethodPost, "/admin/reset", nil)
	w := httptest.NewRecorder()
	handleAdminReset(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
}

// --- metrics.go ---

func TestAddBigIntCounter_LargeValue_Coverage(t *testing.T) {
	counter := precisionLossTestCounter
	val := new(big.Int).Exp(big.NewInt(2), big.NewInt(60), nil)
	addBigIntCounter(counter, val)
}

func TestAddBigIntCounter_NilValue_Coverage(t *testing.T) {
	addBigIntCounter(precisionLossTestCounter, nil)
}

func TestAddBigIntCounter_ZeroValue_Coverage(t *testing.T) {
	addBigIntCounter(precisionLossTestCounter, big.NewInt(0))
}

// --- bundle.go ---

func TestBuildBundle_SigningError_Coverage(t *testing.T) {
	bc := &BundleConstructor{
		nonceManager: NewNonceManager(0),
		gasOracle:    NewGasOracle(300.0),
		signer:       &failingSigner{},
		chainID:      1,
	}
	_, err := bc.BuildBundle([]byte{0x01}, "0x0000000000000000000000000000000000000000", 21000, 100)
	if err == nil {
		t.Error("expected signing error")
	}
}

func TestBuildMempoolBackrunBundle_InvalidHash_Coverage(t *testing.T) {
	bc := &BundleConstructor{
		nonceManager: NewNonceManager(0),
		gasOracle:    NewGasOracle(300.0),
		chainID:      1,
	}
	_, err := bc.BuildMempoolBackrunBundle([]byte{0x01}, "0x0000000000000000000000000000000000000000", 21000, 100, "nothex", []byte{0x01})
	if err == nil {
		t.Error("expected error for invalid hash")
	}
}

func TestBuildMempoolBackrunBundle_EmptyRawTx_Coverage(t *testing.T) {
	bc := &BundleConstructor{
		nonceManager: NewNonceManager(0),
		gasOracle:    NewGasOracle(300.0),
		chainID:      1,
	}
	hash := "0x" + "0000000000000000000000000000000000000000000000000000000000000001"
	_, err := bc.BuildMempoolBackrunBundle([]byte{0x01}, "0x0000000000000000000000000000000000000000", 21000, 100, hash, nil)
	if err == nil {
		t.Error("expected error for empty raw tx")
	}
}

func TestBuildMempoolBackrunBundle_ShortHash_Coverage(t *testing.T) {
	bc := &BundleConstructor{
		nonceManager: NewNonceManager(0),
		gasOracle:    NewGasOracle(300.0),
		chainID:      1,
	}
	_, err := bc.BuildMempoolBackrunBundle([]byte{0x01}, "0x0000000000000000000000000000000000000000", 21000, 100, "0x01", []byte{0x01})
	if err == nil {
		t.Error("expected error for short hash")
	}
}

func TestBuildMempoolBackrunBundle_SigningError_Coverage(t *testing.T) {
	bc := &BundleConstructor{
		nonceManager: NewNonceManager(0),
		gasOracle:    NewGasOracle(300.0),
		signer:       &failingSigner{},
		chainID:      1,
	}
	hash := "0x" + "0000000000000000000000000000000000000000000000000000000000000001"
	_, err := bc.BuildMempoolBackrunBundle([]byte{0x01}, "0x0000000000000000000000000000000000000000", 21000, 100, hash, []byte{0x01})
	if err == nil {
		t.Error("expected signing error")
	}
}

// --- remote_signer.go ---

func TestResolveSignerSocket_Empty_Coverage(t *testing.T) {
	os.Unsetenv("AETHER_SIGNER_SOCKET")
	if got := resolveSignerSocket(); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestResolveSignerSocket_WithPrefix_Coverage(t *testing.T) {
	os.Setenv("AETHER_SIGNER_SOCKET", "unix:///tmp/signer.sock")
	defer os.Unsetenv("AETHER_SIGNER_SOCKET")
	if got := resolveSignerSocket(); got != "/tmp/signer.sock" {
		t.Errorf("got %q", got)
	}
}

func TestResolveSignerSocket_NoPrefix_Coverage(t *testing.T) {
	os.Setenv("AETHER_SIGNER_SOCKET", "/tmp/signer.sock")
	defer os.Unsetenv("AETHER_SIGNER_SOCKET")
	if got := resolveSignerSocket(); got != "/tmp/signer.sock" {
		t.Errorf("got %q", got)
	}
}

// --- startup.go ---

func TestLogBootstrapFailure_EmptyRPC_Coverage(t *testing.T) {
	logBootstrapFailure(fmt.Errorf("some error"), "", "0xaddr")
}

func TestLogBootstrapFailure_DialError_Coverage(t *testing.T) {
	logBootstrapFailure(fmt.Errorf("dial eth rpc failed"), "http://localhost:8545", "0xaddr")
}

func TestLogBootstrapFailure_ChainIDError_Coverage(t *testing.T) {
	logBootstrapFailure(fmt.Errorf("chain id failed"), "http://localhost:8545", "0xaddr")
}

func TestLogBootstrapFailure_ChainMismatch_Coverage(t *testing.T) {
	logBootstrapFailure(fmt.Errorf("chain-id mismatch"), "http://localhost:8545", "0xaddr")
}

func TestLogBootstrapFailure_GetCodeError_Coverage(t *testing.T) {
	logBootstrapFailure(fmt.Errorf("get code failed"), "http://localhost:8545", "0xaddr")
}

func TestLogBootstrapFailure_NoBytecode_Coverage(t *testing.T) {
	logBootstrapFailure(fmt.Errorf("no bytecode at address"), "http://localhost:8545", "0xaddr")
}

func TestLogBootstrapFailure_Default_Coverage(t *testing.T) {
	logBootstrapFailure(fmt.Errorf("unknown error"), "http://localhost:8545", "0xaddr")
}

// --- signer.go ---

func TestNewTransactionSigner_0xPrefix_Coverage(t *testing.T) {
	ts, err := NewTransactionSigner("0x"+testSearcherKey, 1)
	if err != nil {
		t.Fatal(err)
	}
	if ts.Address().Hex() != "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266" {
		t.Errorf("unexpected address: %s", ts.Address().Hex())
	}
}

func TestNewTransactionSigner_NegativeChainID_Coverage(t *testing.T) {
	_, err := NewTransactionSigner(testSearcherKey, -1)
	if err == nil {
		t.Error("expected error for negative chain ID")
	}
}

// --- main.go recordSubmissionReverts ---

func TestRecordSubmissionReverts_CompetitiveOnly_Coverage(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	// "execution reverted" has no competitive pattern match, so ClassifyRevert
	// classifies it as RevertBug (conservative). Use a known competitive reason.
	recordSubmissionReverts(rm, []SubmissionResult{
		{Builder: "a", Success: false, Error: fmt.Errorf("nonce too low")},
	})
	if rm.BugRevertTotal.Load() != 0 {
		t.Error("competitive should not count as bug")
	}
	if rm.CompRevertTotal.Load() != 1 {
		t.Error("expected 1 competitive revert")
	}
}

// --- main.go looksLikeRevert ---

func TestLooksLikeRevert_ExecutionReverted_Coverage(t *testing.T) {
	if !looksLikeRevert("execution reverted: reason") {
		t.Error("should detect")
	}
}

func TestLooksLikeRevert_Reverted_Coverage(t *testing.T) {
	if !looksLikeRevert("Transaction reverted") {
		t.Error("should detect")
	}
}

func TestLooksLikeRevert_Timeout_Coverage(t *testing.T) {
	if looksLikeRevert("context deadline exceeded") {
		t.Error("should not detect")
	}
}

func TestLooksLikeRevert_ConnectionRefused_Coverage(t *testing.T) {
	if looksLikeRevert("connection refused") {
		t.Error("should not detect")
	}
}

// --- main.go fetchTopPools ---

func TestFetchTopPools_BadStatus_Coverage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	_, ok := fetchTopPools(t.Context(), srv.Client(), srv.URL)
	if ok {
		t.Error("expected not ok")
	}
}

func TestFetchTopPools_BadJSON_Coverage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()
	_, ok := fetchTopPools(t.Context(), srv.Client(), srv.URL)
	if ok {
		t.Error("expected not ok")
	}
}

func TestFetchTopPools_ConnectionError_Coverage(t *testing.T) {
	_, ok := fetchTopPools(t.Context(), &http.Client{}, "http://localhost:1")
	if ok {
		t.Error("expected not ok")
	}
}

// --- main.go shadowBundleDumpDir ---

func TestShadowBundleDumpDir_Custom_Coverage(t *testing.T) {
	os.Setenv("AETHER_SHADOW_DUMP_DIR", "/custom/dir")
	defer os.Unsetenv("AETHER_SHADOW_DUMP_DIR")
	if got := shadowBundleDumpDir(); got != "/custom/dir" {
		t.Errorf("got %q", got)
	}
}

func TestShadowBundleDumpDir_Default_Coverage(t *testing.T) {
	os.Unsetenv("AETHER_SHADOW_DUMP_DIR")
	if got := shadowBundleDumpDir(); got != "reports/bundles" {
		t.Errorf("got %q", got)
	}
}

// --- main.go dumpShadowBundle ---

func TestDumpShadowBundle_NilHops_Coverage(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("AETHER_SHADOW_DUMP_DIR", dir)
	defer os.Unsetenv("AETHER_SHADOW_DUMP_DIR")
	arb := &pb.ValidatedArb{
		Id:              "nil-hops-cov",
		BlockNumber:     100,
		FlashloanToken:  []byte{0xC0, 0x2a},
		FlashloanAmount: big.NewInt(1000).Bytes(),
		NetProfitWei:    big.NewInt(100).Bytes(),
		TotalGas:        200000,
		Calldata:        []byte{0x01},
	}
	bundle := &Bundle{RawTxs: [][]byte{{0xde, 0xad}}, BlockNumber: 101}
	if err := dumpShadowBundle(arb, bundle, 0.01, 50.0, 75.0); err != nil {
		t.Fatal(err)
	}
}

func TestDumpShadowBundle_SpecialIDChars_Coverage(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("AETHER_SHADOW_DUMP_DIR", dir)
	defer os.Unsetenv("AETHER_SHADOW_DUMP_DIR")
	arb := &pb.ValidatedArb{Id: "with/special!chars", BlockNumber: 100}
	if err := dumpShadowBundle(arb, &Bundle{}, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
}

func TestDumpShadowBundle_EmptyID_Coverage(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("AETHER_SHADOW_DUMP_DIR", dir)
	defer os.Unsetenv("AETHER_SHADOW_DUMP_DIR")
	arb := &pb.ValidatedArb{Id: "", BlockNumber: 100}
	if err := dumpShadowBundle(arb, &Bundle{}, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
}

func TestDumpShadowBundle_CreatesFile_Coverage(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("AETHER_SHADOW_DUMP_DIR", dir)
	defer os.Unsetenv("AETHER_SHADOW_DUMP_DIR")
	arb := &pb.ValidatedArb{
		Id:          "creates-file",
		BlockNumber: 100,
		Hops: []*pb.ArbHop{
			{TokenIn: []byte{0xC0, 0x2a}, TokenOut: []byte{0xA0, 0xb8}},
		},
		FlashloanToken:  []byte{0xC0, 0x2a},
		FlashloanAmount: big.NewInt(1000).Bytes(),
		NetProfitWei:    big.NewInt(100).Bytes(),
		TotalGas:        200000,
		Calldata:        []byte{0x01},
	}
	bundle := &Bundle{RawTxs: [][]byte{{0xde, 0xad}}, BlockNumber: 101}
	if err := dumpShadowBundle(arb, bundle, 0.01, 50.0, 75.0); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 file, got %d", len(entries))
	}
}

// --- main.go extractAdminToken ---

func TestExtractAdminToken_Empty_Coverage(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	if got := extractAdminToken(req); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestExtractAdminToken_BearerCaseInsensitive_Coverage(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "bearer lower-tok")
	if got := extractAdminToken(req); got != "lower-tok" {
		t.Errorf("got %q", got)
	}
}

func TestExtractAdminToken_BearerNoSpace_Coverage(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "BearerNoSpace")
	if got := extractAdminToken(req); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

// --- main.go tokenLabel ---

func TestTokenLabel_WETH_Coverage(t *testing.T) {
	addr := common.FromHex("0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2")
	if got := tokenLabel(addr); got != "WETH" {
		t.Errorf("got %q", got)
	}
}

func TestTokenLabel_USDC_Coverage(t *testing.T) {
	addr := common.FromHex("0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")
	if got := tokenLabel(addr); got != "USDC" {
		t.Errorf("got %q", got)
	}
}

func TestTokenLabel_Nil_Coverage(t *testing.T) {
	if got := tokenLabel(nil); got != "?" {
		t.Errorf("got %q", got)
	}
}

// --- main.go mempoolShadowSessionDir ---

func TestMempoolShadowSessionDir_Coverage(t *testing.T) {
	dir := mempoolShadowSessionDir()
	if dir == "" {
		t.Error("expected non-empty dir")
	}
}

// --- main.go helpers ---

var precisionLossTestCounter = metricsPrecisionLoss
