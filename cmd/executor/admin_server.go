package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/aether-arb/aether/internal/config"
	"github.com/aether-arb/aether/internal/metrics"
	"github.com/aether-arb/aether/internal/risk"
)

// adminDeps holds references wired into the admin HTTP server.
type adminDeps struct {
	riskMgr       *risk.RiskManager
	snapshotStore *metrics.Store
	discoveryURL  string
	eventPub      adminEventPublisher
}

// adminEventPublisher is the minimal surface the admin server needs from the
// events package (avoids import cycles in tests).
type adminEventPublisher interface {
	PublishBreakerStatus(open bool, reason string)
	PublishSignerHealth(healthy bool)
}

var (
	globalSnapshotStore = metrics.NewStore()
	globalAdminDeps     adminDeps
	adminServerOnce     sync.Once
)

// startAdminServer starts the executor admin/metrics HTTP server on the
// configured port (default 8080). Idempotent — only the first call binds.
func startAdminServer(rm *risk.RiskManager, discoveryURL string, port int, pub adminEventPublisher) {
	adminServerOnce.Do(func() {
		globalAdminDeps = adminDeps{
			riskMgr:       rm,
			snapshotStore: globalSnapshotStore,
			discoveryURL:  discoveryURL,
			eventPub:      pub,
		}
		if port <= 0 {
			port = 8080
		}
		addr := ":" + strconv.Itoa(port)

		mux := http.NewServeMux()
		mux.HandleFunc("/metrics/json", handleMetricsJSON)
		mux.HandleFunc("/admin/pause", requireAdminAuth(handleAdminPause))
		mux.HandleFunc("/admin/resume", requireAdminAuth(handleAdminResume))
		mux.HandleFunc("/admin/set_min_profit", requireAdminAuth(handleSetMinProfit))
		mux.HandleFunc("/health", handleHealthJSON)

		go func() {
			slog.Info("admin HTTP server listening", "addr", addr)
			if err := http.ListenAndServe(addr, mux); err != nil && err != http.ErrServerClosed {
				slog.Error("admin HTTP server error", "err", err)
			}
		}()

		go pollTopPoolsLoop(context.Background(), discoveryURL, globalSnapshotStore, 5*time.Second)
		go refreshSnapshotLoop(context.Background(), rm, globalSnapshotStore, 1*time.Second)
	})
}

func loadAdminPort() (int, string) {
	path := config.ProductionConfigPath()
	cfg, err := config.LoadProductionConfig(path)
	if err != nil {
		slog.Warn("production.toml not loaded, using admin defaults", "path", path, "err", err)
		return 8080, ""
	}
	return cfg.Executor.Port, cfg.Executor.DiscoveryTopPoolsURL
}

func handleMetricsJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snap := globalSnapshotStore.Get()
	snap.ExecutorReachable = true
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}

func handleHealthJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snap := globalSnapshotStore.Get()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"signer_healthy":    snap.SignerHealthy,
		"rpc_healthy":       snap.RPCHealthy,
		"discovery_healthy": snap.DiscoveryHealthy,
		"timescale_healthy": snap.TimescaleHealthy,
		"redis_healthy":     snap.RedisHealthy,
		"system_state":      snap.SystemState,
		"breaker_open":      snap.BreakerOpen,
	})
}

func handleAdminPause(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	reason := r.URL.Query().Get("reason")
	if reason == "" {
		reason = "admin_pause"
	}
	if err := globalAdminDeps.riskMgr.Pause(reason); err != nil {
		slog.Error("admin pause transition failed", "reason", reason, "err", err)
	}
	globalSnapshotStore.Update(func(s *metrics.Snapshot) {
		s.BreakerOpen = true
		s.BreakerReason = reason
		s.SystemState = string(risk.StatePaused)
	})
	if globalAdminDeps.eventPub != nil {
		globalAdminDeps.eventPub.PublishBreakerStatus(true, reason)
	}
	slog.Info("admin pause requested", "reason", reason)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"paused"}`))
}

func handleAdminResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := globalAdminDeps.riskMgr.Resume(); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	globalSnapshotStore.Update(func(s *metrics.Snapshot) {
		s.BreakerOpen = false
		s.BreakerReason = ""
		s.SystemState = string(risk.StateRunning)
	})
	if globalAdminDeps.eventPub != nil {
		globalAdminDeps.eventPub.PublishBreakerStatus(false, "")
	}
	slog.Info("admin resume requested")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"running"}`))
}

func handleSetMinProfit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	valStr := r.URL.Query().Get("value")
	if valStr == "" {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1024))
		valStr = string(body)
	}
	val, err := strconv.ParseFloat(valStr, 64)
	if err != nil || val <= 0 {
		http.Error(w, "invalid value", http.StatusBadRequest)
		return
	}
	globalAdminDeps.riskMgr.SetMinProfitETH(val)
	globalSnapshotStore.Update(func(s *metrics.Snapshot) {
		s.MinProfitETH = val
	})
	slog.Info("min profit updated", "eth", val)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func refreshSnapshotLoop(ctx context.Context, rm *risk.RiskManager, store *metrics.Store, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			state := rm.State()
			winRate := rm.WinRate()
			minProfit := rm.MinProfitETH()
			store.Update(func(s *metrics.Snapshot) {
				s.SystemState = string(state)
				s.WinRate = winRate
				s.MinProfitETH = minProfit
				s.BreakerOpen = state == risk.StatePaused || state == risk.StateHalted
				if state == risk.StatePaused {
					s.BreakerReason = "paused"
				} else if state == risk.StateHalted {
					s.BreakerReason = "halted"
				} else {
					s.BreakerReason = ""
				}
				s.TimescaleHealthy = metricsStoreHealthy()
			})
		}
	}
}

func metricsStoreHealthy() bool {
	if os.Getenv("DATABASE_URL") == "" {
		return true
	}
	type pinger interface {
		Ping(context.Context) error
	}
	if p, ok := metricsStore.(pinger); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return p.Ping(ctx) == nil
	}
	return false
}

func pollTopPoolsLoop(ctx context.Context, url string, store *metrics.Store, interval time.Duration) {
	if url == "" {
		return
	}
	client := &http.Client{Timeout: 3 * time.Second}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pools, ok := fetchTopPools(ctx, client, url)
			store.Update(func(s *metrics.Snapshot) {
				s.DiscoveryHealthy = ok
				if ok {
					s.TopPools = pools
				}
			})
		}
	}
}

func fetchTopPools(ctx context.Context, client *http.Client, url string) ([]metrics.TopPool, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var pools []metrics.TopPool
	if err := json.NewDecoder(resp.Body).Decode(&pools); err != nil {
		return nil, false
	}
	return pools, true
}

// updateSnapshotFromBundle records bundle outcome into the metrics snapshot.
func updateSnapshotFromBundle(profitETH, gasETH float64, builder, bundleHash string) {
	globalSnapshotStore.Update(func(s *metrics.Snapshot) {
		s.LastBundleProfit = profitETH
		s.LastBundleGas = gasETH
		s.LastBuilder = builder
		s.PnLToday += profitETH - gasETH
		s.PnLTotal += profitETH - gasETH
	})
	globalSnapshotStore.RecordTrade(metrics.TradeRecord{
		Timestamp:  time.Now().UTC(),
		ProfitETH:  profitETH,
		GasETH:     gasETH,
		Builder:    builder,
		BundleHash: bundleHash,
	})
}

func setSignerHealthy(healthy bool) {
	globalSnapshotStore.Update(func(s *metrics.Snapshot) {
		s.SignerHealthy = healthy
	})
}

func setRPCHealthy(healthy bool) {
	globalSnapshotStore.Update(func(s *metrics.Snapshot) {
		s.RPCHealthy = healthy
	})
}

func setRedisHealthy(healthy bool) {
	globalSnapshotStore.Update(func(s *metrics.Snapshot) {
		s.RedisHealthy = healthy
	})
}

// requireAdminAuth wraps admin POST handlers with token auth when
// AETHER_ADMIN_TOKEN is set. Unauthenticated requests receive 401.
func requireAdminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := os.Getenv("AETHER_ADMIN_TOKEN")
		if token == "" {
			next(w, r)
			return
		}
		got := r.Header.Get("X-Aether-Admin-Token")
		if got == "" {
			got = r.URL.Query().Get("token")
		}
		if got != token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// signerHealthLoop periodically probes the remote signer and updates /health.
func signerHealthLoop(ctx context.Context, ping func() error, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := ping(); err != nil {
				setSignerHealthy(false)
				slog.Warn("signer health probe failed", "err", err)
			} else {
				setSignerHealthy(true)
			}
		}
	}
}
