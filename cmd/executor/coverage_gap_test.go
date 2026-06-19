package main

import (
	"context"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aether-arb/aether/internal/risk"
)

type mockEventPub struct {
	breakerStatusCalled bool
	signerHealthCalled  bool
}

func (m *mockEventPub) PublishBreakerStatus(open bool, reason string) {
	m.breakerStatusCalled = true
}
func (m *mockEventPub) PublishSignerHealth(healthy bool) {
	m.signerHealthCalled = true
}

// --- admin_server remaining coverage: engineCtrl & eventPub non-nil paths ---

func TestHandleAdminPause_WithEngineCtrlAndEventPub(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	globalAdminDeps.riskMgr = rm
	globalAdminDeps.engineCtrl = &mockEngineCtrl{}
	globalAdminDeps.eventPub = &mockEventPub{}

	req := httptest.NewRequest(http.MethodPost, "/admin/pause?reason=test2", nil)
	w := httptest.NewRecorder()
	handleAdminPause(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	if rm.State() != risk.StatePaused {
		t.Fatalf("state: %s", rm.State())
	}
}

func TestHandleAdminReset_WithEngineCtrlAndEventPub(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.ForceStateForTest(risk.StateHalted)
	globalAdminDeps.riskMgr = rm
	globalAdminDeps.engineCtrl = &mockEngineCtrl{}
	globalAdminDeps.eventPub = &mockEventPub{}

	req := httptest.NewRequest(http.MethodPost, "/admin/reset", nil)
	w := httptest.NewRecorder()
	handleAdminReset(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	if rm.State() != risk.StateRunning {
		t.Fatalf("state: %s", rm.State())
	}
}

func TestHandleAdminResume_WithEngineCtrlAndEventPub(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	_ = rm.Pause("test")
	globalAdminDeps.riskMgr = rm
	globalAdminDeps.engineCtrl = &mockEngineCtrl{}
	globalAdminDeps.eventPub = &mockEventPub{}

	req := httptest.NewRequest(http.MethodPost, "/admin/resume", nil)
	w := httptest.NewRecorder()
	handleAdminResume(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	if rm.State() != risk.StateRunning {
		t.Fatalf("state: %s", rm.State())
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
}

// --- flashbots.go edge cases ---

func TestFlashbotsSigner_Sign_EdgeCases(t *testing.T) {
	signer, err := NewFlashbotsSigner(testSearcherKey)
	if err != nil {
		t.Fatalf("NewFlashbotsSigner: %v", err)
	}

	// Sign empty payload
	_, err = signer.Sign([]byte{})
	if err != nil {
		t.Fatalf("Sign empty payload: %v", err)
	}

	// Sign large payload
	largePayload := make([]byte, 1024*1024)
	_, err = signer.Sign(largePayload)
	if err != nil {
		t.Fatalf("Sign large payload: %v", err)
	}
}

// --- signer.go edge cases ---

func TestNewTransactionSigner_InvalidChainID(t *testing.T) {
	_, err := NewTransactionSigner(testSearcherKey, 0)
	if err == nil {
		t.Fatal("expected error for chainID=0")
	}
}

func TestNewTransactionSigner_ECDSAParseFailure(t *testing.T) {
	// key that's too short to be valid
	_, err := NewTransactionSigner("deadbeef", 1)
	if err == nil {
		t.Fatal("expected error for too-short key")
	}
}

// --- metrics.go addBigIntCounter edge cases ---

func TestAddBigIntCounter_NilAndZero(t *testing.T) {
	// nil value
	addBigIntCounter(profitTotalWei, nil)

	// zero value
	addBigIntCounter(profitTotalWei, big.NewInt(0))
}

// --- remote_signer.go edge cases ---

func TestNewRemoteSigner_EmptySocket(t *testing.T) {
	_, err := NewRemoteSigner("", 1)
	if err == nil {
		t.Fatal("expected error for empty socket path")
	}
}

func TestNewRemoteSigner_InvalidChainID(t *testing.T) {
	_, err := NewRemoteSigner("/tmp/test.sock", 0)
	if err == nil {
		t.Fatal("expected error for chainID=0")
	}
}

// --- run.go error paths ---

func TestRun_NilDeps(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := defaultConfig()
	err := run(ctx, &cfg, nil)
	if err == nil {
		t.Fatal("expected error for nil deps")
	}
}


