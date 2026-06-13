package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aether-arb/aether/internal/risk"
)

type mockEngineCtrl struct {
	pauseCalls  int
	resumeCalls int
	err         error
}

func (m *mockEngineCtrl) SetEngineState(_ context.Context, paused bool) error {
	if paused {
		m.pauseCalls++
	} else {
		m.resumeCalls++
	}
	return m.err
}

func TestAdminPause_PausesRiskManager(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	globalAdminDeps.riskMgr = rm
	setAdminTokenForTest("tok")

	handler := requireAdminAuth(handleAdminPause)
	req := httptest.NewRequest(http.MethodPost, "/admin/pause", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if rm.State() != risk.StatePaused {
		t.Fatalf("state %s", rm.State())
	}
}

func TestAdminPause_CallsEngineSetState(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	mock := &mockEngineCtrl{}
	globalAdminDeps.riskMgr = rm
	globalAdminDeps.engineCtrl = mock
	setAdminTokenForTest("tok")

	handler := requireAdminAuth(handleAdminPause)
	req := httptest.NewRequest(http.MethodPost, "/admin/pause", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	handler(w, req)
	if mock.pauseCalls != 1 {
		t.Fatalf("pause calls %d", mock.pauseCalls)
	}
}

func TestAdminResume_CallsEngineRunning(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	_ = rm.Pause("test")
	mock := &mockEngineCtrl{}
	globalAdminDeps.riskMgr = rm
	globalAdminDeps.engineCtrl = mock
	setAdminTokenForTest("tok")

	handler := requireAdminAuth(handleAdminResume)
	req := httptest.NewRequest(http.MethodPost, "/admin/resume", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	handler(w, req)
	if mock.resumeCalls != 1 {
		t.Fatalf("resume calls %d", mock.resumeCalls)
	}
}

func TestAdminPause_Idempotent(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	globalAdminDeps.riskMgr = rm
	setAdminTokenForTest("tok")
	handler := requireAdminAuth(handleAdminPause)

	req := httptest.NewRequest(http.MethodPost, "/admin/pause", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("first pause status %d", w.Code)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/admin/pause", nil)
	req2.Header.Set("Authorization", "Bearer tok")
	w2 := httptest.NewRecorder()
	handler(w2, req2)
	if w2.Code != http.StatusConflict {
		t.Fatalf("second pause should be 409, got %d", w2.Code)
	}
}

func TestAdminPause_GRPCFailure_StillPausesLocal(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	mock := &mockEngineCtrl{err: errors.New("grpc down")}
	globalAdminDeps.riskMgr = rm
	globalAdminDeps.engineCtrl = mock
	setAdminTokenForTest("tok")

	handler := requireAdminAuth(handleAdminPause)
	req := httptest.NewRequest(http.MethodPost, "/admin/pause", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	handler(w, req)
	if rm.State() != risk.StatePaused {
		t.Fatal("local pause should succeed")
	}
}
