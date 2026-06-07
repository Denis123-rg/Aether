package main

import (
	"fmt"
	"log"

	"github.com/aether-arb/aether/internal/risk"
)

func main() {
	fmt.Println("aether-risk: risk management and circuit breaker service")
	state, cfg := runRiskService()
	log.Printf("Risk manager initialized in state: %s", state)
	log.Printf("Max gas: %.0f gwei, Min profit: %.4f ETH, Max trade: %.1f ETH",
		cfg.MaxGasGwei, cfg.MinProfitETH, cfg.MaxSingleTradeETH)
}

// runRiskService initializes the risk manager from default config. Extracted for
// unit tests so cmd/risk coverage is not limited to untestable main().
func runRiskService() (risk.SystemState, risk.RiskConfig) {
	cfg := risk.DefaultRiskConfig()
	rm := risk.NewRiskManager(cfg)
	return rm.State(), cfg
}
