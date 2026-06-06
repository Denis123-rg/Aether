package main

import (
	"strings"
	"testing"

	"github.com/aether-arb/aether/internal/events"
	"github.com/aether-arb/aether/internal/metrics"
)

func TestFormatDashboardContainsFields(t *testing.T) {
	snap := metrics.Snapshot{
		PnLToday:         0.123456,
		PnLTotal:         1.5,
		WinRate:          65.5,
		LastBundleProfit: 0.01,
		LastBundleGas:    0.002,
		LastBuilder:      "flashbots",
		BreakerOpen:      false,
		SignerHealthy:    true,
		RPCHealthy:       true,
		SystemState:      "Running",
		MinProfitETH:     0.001,
		TopPools: []metrics.TopPool{
			{Address: "0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2", Score: 0.95, Protocol: "uniswap_v2"},
		},
		ExecutorReachable: true,
	}
	text := FormatDashboard(snap, events.DashboardState{}, false)
	for _, want := range []string{"📊", "PnL", "0.123456", "65.5", "flashbots", "🟢", "✅", "Running", "0.001"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}

func TestFormatDashboardExecutorUnreachable(t *testing.T) {
	snap := metrics.Snapshot{ExecutorReachable: false}
	text := FormatDashboard(snap, events.DashboardState{}, false)
	if !strings.Contains(text, "unreachable") {
		t.Fatalf("text: %s", text)
	}
}

func TestFormatDashboardBreakerOpen(t *testing.T) {
	snap := metrics.Snapshot{BreakerOpen: true, BreakerReason: "signer_unavailable"}
	text := FormatDashboard(snap, events.DashboardState{}, false)
	if !strings.Contains(text, "🔴") || !strings.Contains(text, "signer_unavailable") {
		t.Fatalf("text: %s", text)
	}
}

func TestFormatDashboardRedisOverlay(t *testing.T) {
	snap := metrics.Snapshot{PnLTotal: 1.0, WinRate: 50.0}
	redis := events.DashboardState{PnLTotal: 99.0, WinRate: 80.0, LastBuilder: "titan", SignerHealthy: false}
	text := FormatDashboard(snap, redis, true)
	if !strings.Contains(text, "99.000000") || !strings.Contains(text, "80.0") {
		t.Fatalf("text: %s", text)
	}
}

func TestFormatPools(t *testing.T) {
	pools := make([]metrics.TopPool, 5)
	for i := range pools {
		pools[i] = metrics.TopPool{Address: "0xabc", Score: float64(i) / 10, Protocol: "v2", TVLUSD: 10000}
	}
	text := FormatPools(pools)
	if !strings.Contains(text, "🏊") || !strings.Contains(text, "0xabc") {
		t.Fatalf("text: %s", text)
	}
}

func TestFormatPoolsEmpty(t *testing.T) {
	text := FormatPools(nil)
	if !strings.Contains(text, "No pools") {
		t.Fatalf("text: %s", text)
	}
}

func TestFormatHealth(t *testing.T) {
	snap := metrics.Snapshot{
		SignerHealthy: true,
		RPCHealthy:    false,
		SystemState:   "Paused",
		BreakerOpen:   true,
	}
	text := FormatHealth(snap)
	if !strings.Contains(text, "healthy") || !strings.Contains(text, "unhealthy") {
		t.Fatalf("text: %s", text)
	}
}

func TestFormatTrades(t *testing.T) {
	trades := []metrics.TradeRecord{
		{ProfitETH: 0.01, GasETH: 0.001, Builder: "eden"},
	}
	text := FormatTrades(trades)
	if !strings.Contains(text, "0.010000") || !strings.Contains(text, "eden") {
		t.Fatalf("text: %s", text)
	}
}

func TestFormatTradesEmpty(t *testing.T) {
	text := FormatTrades(nil)
	if !strings.Contains(text, "No trades") {
		t.Fatalf("text: %s", text)
	}
}

func TestHealthEmoji(t *testing.T) {
	if healthEmoji(true) != "✅ healthy" {
		t.Fatal("healthy emoji")
	}
	if healthEmoji(false) != "❌ unhealthy" {
		t.Fatal("unhealthy emoji")
	}
}

func TestBreakerLabel(t *testing.T) {
	if breakerLabel(true) != "🔴 OPEN" {
		t.Fatal("open")
	}
	if breakerLabel(false) != "🟢 CLOSED" {
		t.Fatal("closed")
	}
}

func TestFormatPoolsLimit20(t *testing.T) {
	pools := make([]metrics.TopPool, 25)
	for i := range pools {
		pools[i] = metrics.TopPool{Address: "0xabc1234567890abcdef", Score: 0.5}
	}
	text := FormatPools(pools)
	lines := strings.Count(text, "0xabc")
	if lines != 20 {
		t.Fatalf("expected 20 entries, got %d", lines)
	}
}
