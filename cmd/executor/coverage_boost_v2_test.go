package main

import (
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/aether-arb/aether/internal/risk"
)

func TestRemoteSigner_Ping_Down(t *testing.T) {
	sock, _, stop := startTestSigner(t)
	rs, err := NewRemoteSigner(sock, 1)
	if err != nil {
		t.Fatalf("NewRemoteSigner: %v", err)
	}
	stop()
	if err := rs.Ping(); err == nil {
		t.Fatal("expected error")
	} else if !errors.Is(err, errSignerUnavailable) {
		t.Fatalf("error = %v", err)
	}
}

func TestRemoteSigner_SignFlashbotsPayload_Down(t *testing.T) {
	sock, _, stop := startTestSigner(t)
	rs, err := NewRemoteSigner(sock, 1)
	if err != nil {
		t.Fatalf("NewRemoteSigner: %v", err)
	}
	stop()
	_, err = rs.SignFlashbotsPayload([]byte("test"))
	if err == nil {
		t.Fatal("expected error")
	} else if !errors.Is(err, errSignerUnavailable) {
		t.Fatalf("error = %v", err)
	}
}

func TestSignAndMarshal_WrongChainID(t *testing.T) {
	ts, err := NewTransactionSigner(testPrivateKeyHex, 1)
	if err != nil {
		t.Fatal(err)
	}
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID: big.NewInt(999), Nonce: 0, Gas: 21000,
		To: &common.Address{}, Value: big.NewInt(0),
	})
	_, err = ts.SignAndMarshal(tx)
	if err == nil {
		t.Fatal("expected error for wrong chain ID")
	}
}

func TestBuildBundle_NilSigner(t *testing.T) {
	bc := &BundleConstructor{
		nonceManager: NewNonceManager(0),
		gasOracle:    NewGasOracle(300.0),
		chainID:      1,
	}
	bundle, err := bc.BuildBundle([]byte{0x01}, "0x0000000000000000000000000000000000000000", 21000, 100)
	if err != nil {
		t.Fatal(err)
	}
	if bundle == nil || len(bundle.Transactions) != 1 {
		t.Fatal("expected bundle with 1 tx")
	}
}

func TestBuildMempoolBackrunBundle_NilSigner(t *testing.T) {
	bc := &BundleConstructor{
		nonceManager: NewNonceManager(0),
		gasOracle:    NewGasOracle(300.0),
		chainID:      1,
	}
	hash := "0x" + strings.Repeat("00", 32)
	bundle, err := bc.BuildMempoolBackrunBundle(
		[]byte{0x01}, "0x0000000000000000000000000000000000000000",
		21000, 100, hash, []byte{0xde, 0xad},
	)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Source != SourceMempoolBackrun {
		t.Fatalf("wrong source: %v", bundle.Source)
	}
}

func TestHandleAdminPause_EventPubPublish(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	globalAdminDeps.riskMgr = rm
	ep := &mockEventPub{}
	globalAdminDeps.eventPub = ep
	req := httptest.NewRequest(http.MethodPost, "/admin/pause?reason=test", nil)
	w := httptest.NewRecorder()
	handleAdminPause(w, req)
	if !ep.breakerStatusCalled {
		t.Fatal("expected PublishBreakerStatus to be called")
	}
}

func TestHandleAdminResume_EventPubPublish(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	_ = rm.Pause("test")
	globalAdminDeps.riskMgr = rm
	ep := &mockEventPub{}
	globalAdminDeps.eventPub = ep
	req := httptest.NewRequest(http.MethodPost, "/admin/resume", nil)
	w := httptest.NewRecorder()
	handleAdminResume(w, req)
	if !ep.breakerStatusCalled {
		t.Fatal("expected PublishBreakerStatus to be called")
	}
}

func TestHandleAdminReset_EventPubPublish(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.ForceStateForTest(risk.StateHalted)
	globalAdminDeps.riskMgr = rm
	ep := &mockEventPub{}
	globalAdminDeps.eventPub = ep
	req := httptest.NewRequest(http.MethodPost, "/admin/reset", nil)
	w := httptest.NewRecorder()
	handleAdminReset(w, req)
	if !ep.breakerStatusCalled {
		t.Fatal("expected PublishBreakerStatus to be called")
	}
}

func TestHandleAdminReset_ConfirmViaToken(t *testing.T) {
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
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleAdminReset_Forbidden(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.ForceStateForTest(risk.StateHalted)
	globalAdminDeps.riskMgr = rm
	os.Setenv("AETHER_RESET_CONFIRM_TOKEN", "secret")
	defer os.Unsetenv("AETHER_RESET_CONFIRM_TOKEN")
	req := httptest.NewRequest(http.MethodPost, "/admin/reset", nil)
	req.Header.Set("X-Aether-Reset-Confirm", "wrong")
	w := httptest.NewRecorder()
	handleAdminReset(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestHandleAdminPause_AlreadyPaused(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	_ = rm.Pause("test")
	globalAdminDeps.riskMgr = rm
	req := httptest.NewRequest(http.MethodPost, "/admin/pause", nil)
	w := httptest.NewRecorder()
	handleAdminPause(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}

func TestHandleAdminResume_HaltedConflict(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.ForceStateForTest(risk.StateHalted)
	globalAdminDeps.riskMgr = rm
	req := httptest.NewRequest(http.MethodPost, "/admin/resume", nil)
	w := httptest.NewRecorder()
	handleAdminResume(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}

func TestHandleAdminReset_NotHalted(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	globalAdminDeps.riskMgr = rm
	req := httptest.NewRequest(http.MethodPost, "/admin/reset", nil)
	w := httptest.NewRecorder()
	handleAdminReset(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}

func TestNewFlashbotsSigner_InvalidKey(t *testing.T) {
	_, err := NewFlashbotsSigner("zzzz")
	if err == nil {
		t.Fatal("expected error for invalid hex key")
	}
}
