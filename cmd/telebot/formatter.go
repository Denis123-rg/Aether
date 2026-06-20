package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/aether-arb/aether/internal/events"
	"github.com/aether-arb/aether/internal/metrics"
)

// FormatDashboard renders the live dashboard message with emoji and alignment.
func FormatDashboard(snap metrics.Snapshot, redisState events.DashboardData, redisActive bool) string {
	var b strings.Builder

	if !snap.ExecutorReachable {
		b.WriteString("⚠️ *Executor unreachable*\n")
		b.WriteString("_Polling will retry automatically._\n\n")
	}

	b.WriteString("📊 *Aether Dashboard*\n")
	b.WriteString(fmt.Sprintf("_Updated: %s_\n\n", time.Now().UTC().Format("15:04:05 UTC")))

	// PnL section
	b.WriteString("💰 *PnL*\n")
	b.WriteString(fmt.Sprintf("  Today:  `%.6f ETH`\n", snap.PnLToday))
	pnlTotal := snap.PnLTotal
	if redisActive {
		pnlTotal = redisState.PnLTotal
	}
	b.WriteString(fmt.Sprintf("  Total:  `%.6f ETH`\n\n", pnlTotal))

	// Win rate
	winRate := snap.WinRate
	if redisActive && redisState.WinRate > 0 {
		winRate = redisState.WinRate
	}
	b.WriteString(fmt.Sprintf("🎯 Win rate (last 100): `%.1f%%`\n\n", winRate))

	// Last bundle
	lastProfit := snap.LastBundleProfit
	lastGas := snap.LastBundleGas
	lastBuilder := snap.LastBuilder
	if redisActive && redisState.LastBuilder != "" {
		lastProfit = redisState.LastBundleProfit
		lastGas = redisState.LastBundleGas
		lastBuilder = redisState.LastBuilder
	}
	b.WriteString("📦 *Last Bundle*\n")
	b.WriteString(fmt.Sprintf("  Profit:  `%.6f ETH`\n", lastProfit))
	b.WriteString(fmt.Sprintf("  Gas:     `%.6f ETH`\n", lastGas))
	b.WriteString(fmt.Sprintf("  Builder: `%s`\n\n", lastBuilder))

	// Circuit breaker
	breakerOpen := snap.BreakerOpen
	breakerReason := snap.BreakerReason
	if redisActive {
		breakerOpen = redisState.BreakerOpen
		breakerReason = redisState.BreakerReason
	}
	if breakerOpen {
		b.WriteString(fmt.Sprintf("🔴 Breaker: *OPEN* (%s)\n\n", breakerReason))
	} else {
		b.WriteString("🟢 Breaker: *CLOSED*\n\n")
	}

	// Health
	signerOK := snap.SignerHealthy
	if redisActive {
		signerOK = redisState.SignerHealthy
	}
	b.WriteString("🏥 *Health*\n")
	b.WriteString(fmt.Sprintf("  Signer:     %s\n", healthEmoji(signerOK)))
	b.WriteString(fmt.Sprintf("  RPC:        %s\n", healthEmoji(snap.RPCHealthy)))
	b.WriteString(fmt.Sprintf("  Discovery:  %s\n", healthEmoji(snap.DiscoveryHealthy)))
	b.WriteString(fmt.Sprintf("  TimescaleDB: %s\n", healthEmoji(snap.TimescaleHealthy)))
	if redisActive {
		b.WriteString(fmt.Sprintf("  Redis:      %s (live)\n", healthEmoji(redisState.RedisConnected)))
	} else {
		b.WriteString(fmt.Sprintf("  Redis:      %s (polling fallback)\n", healthEmoji(snap.RedisHealthy)))
	}
	b.WriteString(fmt.Sprintf("  State:      `%s`\n\n", snap.SystemState))

	// Top pools
	b.WriteString("🔥 *Top Hot Pools*\n")
	if len(snap.TopPools) == 0 {
		b.WriteString("  _No pool data_\n")
	} else {
		limit := 5
		if len(snap.TopPools) < limit {
			limit = len(snap.TopPools)
		}
		for i := 0; i < limit; i++ {
			p := snap.TopPools[i]
			addr := p.Address
			if len(addr) > 10 {
				addr = addr[:6] + "…" + addr[len(addr)-4:]
			}
			b.WriteString(fmt.Sprintf("  %d. `%s` score=%.4f %s\n", i+1, addr, p.Score, p.Protocol))
		}
	}

	b.WriteString("\n_Min profit: ")
	b.WriteString(fmt.Sprintf("%.6f ETH_", snap.MinProfitETH))

	return b.String()
}

// FormatPools renders the top-20 pools list.
func FormatPools(pools []metrics.TopPool) string {
	var b strings.Builder
	b.WriteString("🏊 *Top Hot Pools*\n\n")
	if len(pools) == 0 {
		b.WriteString("_No pools available. Check discovery service._")
		return b.String()
	}
	limit := 20
	if len(pools) < limit {
		limit = len(pools)
	}
	for i := 0; i < limit; i++ {
		p := pools[i]
		b.WriteString(fmt.Sprintf("%2d. `%s`\n", i+1, p.Address))
		b.WriteString(fmt.Sprintf("    score=%.4f  %s  TVL=$%.0f\n", p.Score, p.Protocol, p.TVLUSD))
	}
	return b.String()
}

// FormatHealth renders the /health command output.
func FormatHealth(snap metrics.Snapshot) string {
	var b strings.Builder
	b.WriteString("🏥 *System Health*\n\n")
	b.WriteString(fmt.Sprintf("Signer:      %s\n", healthLabel(snap.SignerHealthy)))
	b.WriteString(fmt.Sprintf("RPC:         %s\n", healthLabel(snap.RPCHealthy)))
	b.WriteString(fmt.Sprintf("Discovery:   %s\n", healthLabel(snap.DiscoveryHealthy)))
	b.WriteString(fmt.Sprintf("TimescaleDB: %s\n", healthLabel(snap.TimescaleHealthy)))
	b.WriteString(fmt.Sprintf("Redis:       %s\n", healthLabel(snap.RedisHealthy)))
	b.WriteString(fmt.Sprintf("System:      `%s`\n", snap.SystemState))
	b.WriteString(fmt.Sprintf("Breaker:     %s\n", breakerLabel(snap.BreakerOpen)))
	return b.String()
}

// FormatTrades renders the last 10 trades.
func FormatTrades(trades []metrics.TradeRecord) string {
	var b strings.Builder
	b.WriteString("📈 *Recent Trades*\n\n")
	if len(trades) == 0 {
		b.WriteString("_No trades recorded yet._")
		return b.String()
	}
	for i, t := range trades {
		b.WriteString(fmt.Sprintf("%d. `%s`\n", i+1, t.Timestamp.Format("2006-01-02 15:04:05")))
		b.WriteString(fmt.Sprintf("   profit=%.6f ETH  gas=%.6f  builder=%s\n", t.ProfitETH, t.GasETH, t.Builder))
	}
	return b.String()
}

func healthEmoji(ok bool) string {
	if ok {
		return "✅ healthy"
	}
	return "❌ unhealthy"
}

func healthLabel(ok bool) string {
	if ok {
		return "✅ healthy"
	}
	return "❌ unhealthy"
}

func breakerLabel(open bool) string {
	if open {
		return "🔴 OPEN"
	}
	return "🟢 CLOSED"
}
