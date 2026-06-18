package main

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aether-arb/aether/internal/metrics"
	"github.com/aether-arb/aether/internal/risk"
)

// --- mock types for testing admin handlers ---

type mockEngineCtrl struct {
	setStateCalled bool
	err            error
}

func (m *mockEngineCtrl) SetEngineState(ctx context.Context, paused bool) error {
	m.setStateCalled = true
	return m.err
}

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

func TestHandleAdminPause_EngineCtrlError(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	globalAdminDeps.riskMgr = rm
	globalAdminDeps.engineCtrl = &mockEngineCtrl{err: errors.New("engine error")}

	req := httptest.NewRequest(http.MethodPost, "/admin/pause?reason=enginefail", nil)
	w := httptest.NewRecorder()
	handleAdminPause(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
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

func TestHandleAdminReset_EngineCtrlError(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.ForceStateForTest(risk.StateHalted)
	globalAdminDeps.riskMgr = rm
	globalAdminDeps.engineCtrl = &mockEngineCtrl{err: errors.New("engine error")}

	req := httptest.NewRequest(http.MethodPost, "/admin/reset", nil)
	w := httptest.NewRecorder()
	handleAdminReset(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
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

func TestHandleAdminResume_EngineCtrlError(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	_ = rm.Pause("test")
	globalAdminDeps.riskMgr = rm
	globalAdminDeps.engineCtrl = &mockEngineCtrl{err: errors.New("engine error")}

	req := httptest.NewRequest(http.MethodPost, "/admin/resume", nil)
	w := httptest.NewRecorder()
	handleAdminResume(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
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

func TestAddBigIntCounter_LargeValue(t *testing.T) {
	// value > 2^53 to trigger precision loss path
	val := new(big.Int).Lsh(big.NewInt(1), 60)
	addBigIntCounter(profitTotalWei, val)
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

func TestRun_NilConfig(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deps := &Dependencies{}
	err := run(ctx, nil, deps)
	if err == nil {
		t.Fatal("expected error for nil config")
	}
}
