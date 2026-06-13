package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestBackrunMode_Off_SkipsProcessing(t *testing.T) {
	setBackrunMode(BackrunOff)
	if shouldProcessMempoolBackrun() {
		t.Fatal("off mode should not process")
	}
}

func TestBackrunMode_ShadowOnly_NoSubmit(t *testing.T) {
	setBackrunMode(BackrunShadowOnly)
	if !shouldShadowMempoolBackrun() {
		t.Fatal("shadow_only should shadow")
	}
	if shouldSubmitMempoolBackrun() {
		t.Fatal("shadow_only should not submit")
	}
}

func TestBackrunMode_ShadowAndLive_Both(t *testing.T) {
	setBackrunMode(BackrunShadowAndLive)
	if !shouldShadowMempoolBackrun() || !shouldSubmitMempoolBackrun() {
		t.Fatal("shadow_and_live should shadow and submit")
	}
}

func TestBackrunMode_LiveOnly_SubmitOnly(t *testing.T) {
	setBackrunMode(BackrunLiveOnly)
	if shouldShadowMempoolBackrun() {
		t.Fatal("live_only should not shadow-only")
	}
	if !shouldSubmitMempoolBackrun() {
		t.Fatal("live_only should submit")
	}
}

func TestInitBackrunMode_DefaultShadowOnly(t *testing.T) {
	os.Unsetenv("AETHER_BACKRUN_MODE")
	os.Unsetenv("AETHER_SHADOW")
	initBackrunMode()
	if getBackrunMode() != BackrunShadowOnly {
		t.Fatalf("got %s", getBackrunMode())
	}
}

func TestBackrunPromote_WithoutConfirmToken_403(t *testing.T) {
	resetAdminGlobals()
	setAdminTokenForTest("admin")
	t.Setenv("AETHER_BACKRUN_CONFIRM_TOKEN", "confirm-me")

	handler := requireAdminAuth(handleBackrunPromote)
	req := httptest.NewRequest(http.MethodPost, "/admin/backrun/promote", nil)
	req.Header.Set("Authorization", "Bearer admin")
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status %d", w.Code)
	}
}

func TestBackrunPromote_WithConfirmToken_OK(t *testing.T) {
	resetAdminGlobals()
	setAdminTokenForTest("admin")
	setBackrunMode(BackrunShadowOnly)
	t.Setenv("AETHER_BACKRUN_CONFIRM_TOKEN", "confirm-me")

	handler := requireAdminAuth(handleBackrunPromote)
	req := httptest.NewRequest(http.MethodPost, "/admin/backrun/promote", nil)
	req.Header.Set("Authorization", "Bearer admin")
	req.Header.Set("X-Aether-Backrun-Confirm", "confirm-me")
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	if getBackrunMode() != BackrunLiveOnly {
		t.Fatalf("mode %s", getBackrunMode())
	}
}

func TestBackrunShadowMetricIncrements(t *testing.T) {
	before := testutil.ToFloat64(backrunShadowTotal.WithLabelValues(SourceMempoolBackrun))
	recordBackrunShadow(SourceMempoolBackrun)
	after := testutil.ToFloat64(backrunShadowTotal.WithLabelValues(SourceMempoolBackrun))
	if after != before+1 {
		t.Fatalf("before=%v after=%v", before, after)
	}
}

func TestBackrunLiveMetricIncrements(t *testing.T) {
	before := testutil.ToFloat64(backrunLiveTotal.WithLabelValues(SourceMempoolBackrun))
	recordBackrunLive(SourceMempoolBackrun)
	after := testutil.ToFloat64(backrunLiveTotal.WithLabelValues(SourceMempoolBackrun))
	if after != before+1 {
		t.Fatalf("before=%v after=%v", before, after)
	}
}

func TestBackrunMode_RollbackViaSet(t *testing.T) {
	setBackrunMode(BackrunLiveOnly)
	setBackrunMode(BackrunShadowOnly)
	if getBackrunMode() != BackrunShadowOnly {
		t.Fatal("rollback failed")
	}
}
