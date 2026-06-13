package main

import (
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aether-arb/aether/internal/config"
)

// adminRateLimiter is a simple token-bucket limiter for admin POST endpoints.
// Disabled when ratePerSec <= 0.
type adminRateLimiter struct {
	mu         sync.Mutex
	ratePerSec float64
	burst      int
	tokens     float64
	last       time.Time
}

func newAdminRateLimiter(ratePerSec float64, burst int) *adminRateLimiter {
	if ratePerSec <= 0 {
		return nil
	}
	if burst <= 0 {
		burst = int(ratePerSec)
		if burst < 1 {
			burst = 1
		}
	}
	return &adminRateLimiter{
		ratePerSec: ratePerSec,
		burst:      burst,
		tokens:     float64(burst),
		last:       time.Now(),
	}
}

func (l *adminRateLimiter) allow() bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(l.last).Seconds()
	l.last = now
	l.tokens += elapsed * l.ratePerSec
	if l.tokens > float64(l.burst) {
		l.tokens = float64(l.burst)
	}
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}

var globalAdminRateLimiter *adminRateLimiter

func initAdminRateLimit() {
	rate := adminRateLimitFromEnv()
	if rate <= 0 {
		path := config.ProductionConfigPath()
		if cfg, err := config.LoadProductionConfig(path); err == nil {
			rate = cfg.Executor.AdminRateLimitRPS
		}
	}
	globalAdminRateLimiter = newAdminRateLimiter(rate, 0)
	if globalAdminRateLimiter != nil {
		slog.Info("admin rate limit enabled", "rps", rate)
	}
}

func adminRateLimitFromEnv() float64 {
	raw := strings.TrimSpace(os.Getenv("ADMIN_RATE_LIMIT_RPS"))
	if raw == "" {
		return 0
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < 0 {
		slog.Warn("invalid ADMIN_RATE_LIMIT_RPS; rate limit disabled", "value", raw)
		return 0
	}
	return v
}

func resetAdminRateLimiterForTest(l *adminRateLimiter) {
	globalAdminRateLimiter = l
}

// requireAdminAuthWithRateLimit wraps admin handlers with Bearer auth and optional rate limiting.
func requireAdminAuthWithRateLimit(next http.HandlerFunc) http.HandlerFunc {
	auth := requireAdminAuth(next)
	return func(w http.ResponseWriter, r *http.Request) {
		if globalAdminRateLimiter != nil && !globalAdminRateLimiter.allow() {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		auth(w, r)
	}
}
