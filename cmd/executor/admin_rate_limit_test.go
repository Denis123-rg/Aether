package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAdminRateLimiter_AllowWithinBurst(t *testing.T) {
	t.Parallel()
	lim := newAdminRateLimiter(10, 5)
	for i := 0; i < 5; i++ {
		if !lim.allow() {
			t.Fatalf("request %d should be allowed within burst", i)
		}
	}
}

func TestAdminRateLimiter_BlocksWhenExhausted(t *testing.T) {
	t.Parallel()
	lim := newAdminRateLimiter(1, 1)
	if !lim.allow() {
		t.Fatal("first request should pass")
	}
	if lim.allow() {
		t.Fatal("second immediate request should be blocked")
	}
}

func TestAdminRateLimiter_RefillsOverTime(t *testing.T) {
	t.Parallel()
	lim := newAdminRateLimiter(100, 1)
	if !lim.allow() {
		t.Fatal("first request should pass")
	}
	time.Sleep(20 * time.Millisecond)
	if !lim.allow() {
		t.Fatal("token should refill after sleep")
	}
}

func TestAdminRateLimiter_DisabledWhenZeroRate(t *testing.T) {
	t.Parallel()
	if newAdminRateLimiter(0, 0) != nil {
		t.Fatal("zero rate should disable limiter")
	}
}

func TestRequireAdminAuthWithRateLimit_Returns429(t *testing.T) {
	setAdminTokenForTest("secret")
	resetAdminRateLimiterForTest(newAdminRateLimiter(1, 1))
	t.Cleanup(func() { resetAdminRateLimiterForTest(nil) })

	handler := requireAdminAuthWithRateLimit(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/admin/pause", nil)
	req.Header.Set("Authorization", "Bearer secret")

	w1 := httptest.NewRecorder()
	handler(w1, req)
	if w1.Code != http.StatusOK {
		t.Fatalf("first request: got %d", w1.Code)
	}

	w2 := httptest.NewRecorder()
	handler(w2, req)
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: got %d, want 429", w2.Code)
	}
}

func TestRequireAdminAuthWithRateLimit_AuthBeforeRate(t *testing.T) {
	setAdminTokenForTest("secret")
	resetAdminRateLimiterForTest(newAdminRateLimiter(100, 100))
	t.Cleanup(func() { resetAdminRateLimiterForTest(nil) })

	handler := requireAdminAuthWithRateLimit(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/admin/pause", nil)
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("missing token should 401 before rate limit, got %d", w.Code)
	}
}

func TestAdminRateLimitFromEnv_InvalidDisables(t *testing.T) {
	t.Setenv("ADMIN_RATE_LIMIT_RPS", "not-a-number")
	if got := adminRateLimitFromEnv(); got != 0 {
		t.Fatalf("invalid env should return 0, got %v", got)
	}
}

func TestAdminRateLimitFromEnv_ParsesFloat(t *testing.T) {
	t.Setenv("ADMIN_RATE_LIMIT_RPS", "12.5")
	if got := adminRateLimitFromEnv(); got != 12.5 {
		t.Fatalf("got %v, want 12.5", got)
	}
}
