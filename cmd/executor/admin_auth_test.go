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
	prev := configuredAdminToken
	setAdminTokenForTest(token)
	return func() {
		setAdminTokenForTest(prev)
	}
}

func TestInitAdminAuth_ProductionWithoutToken_Fatal(t *testing.T) {
	t.Setenv("AETHER_ENV", "production")
	t.Setenv("AETHER_ADMIN_TOKEN", "")
	setAdminTokenForTest("")
	if err := initAdminAuth(); err == nil {
		t.Fatal("expected error in production without token")
	}
}

func TestInitAdminAuth_DevWithoutToken_Warns(t *testing.T) {
	t.Setenv("AETHER_ENV", "development")
	t.Setenv("AETHER_ADMIN_TOKEN", "")
	setAdminTokenForTest("")
	if err := initAdminAuth(); err != nil {
		t.Fatalf("dev mode should start: %v", err)
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

func TestRequireAdminAuth_NoTokenConfigured_401(t *testing.T) {
	resetAdminGlobals()
	cleanup := withAdminToken(t, "")
	defer cleanup()

	handler := requireAdminAuth(handleAdminPause)
	req := httptest.NewRequest(http.MethodPost, "/admin/pause", nil)
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusUnauthorized {
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

func TestMetricsJSON_RemainsUnauthenticated(t *testing.T) {
	resetAdminGlobals()
	cleanup := withAdminToken(t, "secret")
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/metrics/json", nil)
	w := httptest.NewRecorder()
	handleMetricsJSON(w, req)
	if w.Code != http.StatusOK {
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

func TestInitAdminAuth_ProductionWithToken_OK(t *testing.T) {
	t.Setenv("AETHER_ENV", "production")
	t.Setenv("AETHER_ADMIN_TOKEN", "prod-secret")
	if err := initAdminAuth(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if configuredAdminToken != "prod-secret" {
		t.Fatalf("token not stored")
	}
	os.Unsetenv("AETHER_ADMIN_TOKEN")
}
