package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/aether-arb/aether/internal/risk"
)

func withAdminToken(t *testing.T, token string) func() {
	t.Helper()
	prev := os.Getenv("AETHER_ADMIN_TOKEN")
	t.Setenv("AETHER_ADMIN_TOKEN", token)
	return func() {
		if prev == "" {
			os.Unsetenv("AETHER_ADMIN_TOKEN")
		} else {
			os.Setenv("AETHER_ADMIN_TOKEN", prev)
		}
	}
}

func TestExtractAdminToken_XAetherHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/admin/pause", nil)
	req.Header.Set("X-Aether-Admin-Token", "secret")
	if got := extractAdminToken(req); got != "secret" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractAdminToken_BearerHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/admin/pause", nil)
	req.Header.Set("Authorization", "Bearer telebot-token")
	if got := extractAdminToken(req); got != "telebot-token" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractAdminToken_BearerCaseInsensitive(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/admin/pause", nil)
	req.Header.Set("Authorization", "bearer mixed-case")
	if got := extractAdminToken(req); got != "mixed-case" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractAdminToken_QueryParam(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/admin/pause?token=query-secret", nil)
	if got := extractAdminToken(req); got != "query-secret" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractAdminToken_HeaderPrecedence(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/admin/pause?token=query", nil)
	req.Header.Set("X-Aether-Admin-Token", "header-wins")
	if got := extractAdminToken(req); got != "header-wins" {
		t.Fatalf("got %q", got)
	}
}

func TestRequireAdminAuth_NoTokenConfigured_Allows(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	globalAdminDeps.riskMgr = rm
	cleanup := withAdminToken(t, "")
	defer cleanup()

	handler := requireAdminAuth(handleAdminPause)
	req := httptest.NewRequest(http.MethodPost, "/admin/pause", nil)
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
}

func TestRequireAdminAuth_ValidBearer_Allows(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	globalAdminDeps.riskMgr = rm
	cleanup := withAdminToken(t, "prod-token")
	defer cleanup()

	handler := requireAdminAuth(handleAdminPause)
	req := httptest.NewRequest(http.MethodPost, "/admin/pause", nil)
	req.Header.Set("Authorization", "Bearer prod-token")
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
}

func TestRequireAdminAuth_ValidXAether_Allows(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	_ = rm.Pause("auth-test")
	globalAdminDeps.riskMgr = rm
	cleanup := withAdminToken(t, "x-token")
	defer cleanup()

	handler := requireAdminAuth(handleAdminResume)
	req := httptest.NewRequest(http.MethodPost, "/admin/resume", nil)
	req.Header.Set("X-Aether-Admin-Token", "x-token")
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
}

func TestRequireAdminAuth_MissingToken_401(t *testing.T) {
	resetAdminGlobals()
	cleanup := withAdminToken(t, "required")
	defer cleanup()

	handler := requireAdminAuth(handleAdminPause)
	req := httptest.NewRequest(http.MethodPost, "/admin/pause", nil)
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", w.Code)
	}
}

func TestRequireAdminAuth_WrongToken_401(t *testing.T) {
	resetAdminGlobals()
	cleanup := withAdminToken(t, "correct")
	defer cleanup()

	handler := requireAdminAuth(handleAdminPause)
	req := httptest.NewRequest(http.MethodPost, "/admin/pause", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", w.Code)
	}
}

func TestRequireAdminAuth_SetMinProfit_WithBearer(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	globalAdminDeps.riskMgr = rm
	cleanup := withAdminToken(t, "admin")
	defer cleanup()

	handler := requireAdminAuth(handleSetMinProfit)
	req := httptest.NewRequest(http.MethodPost, "/admin/set_min_profit?value=0.002", nil)
	req.Header.Set("Authorization", "Bearer admin")
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if rm.MinProfitETH() != 0.002 {
		t.Fatalf("min profit %f", rm.MinProfitETH())
	}
}
