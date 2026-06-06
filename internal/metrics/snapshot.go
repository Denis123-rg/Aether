// Package metrics defines the JSON snapshot contract shared between the
// executor admin HTTP server and the Telegram dashboard (telebot).
package metrics

import (
	"sync"
	"time"
)

// TopPool is a ranked hot pool entry for dashboard display.
type TopPool struct {
	Address string  `json:"address"`
	Protocol string `json:"protocol"`
	Score   float64 `json:"score"`
	TVLUSD  float64 `json:"tvl_usd,omitempty"`
}

// TradeRecord is a recent trade for the /trades command.
type TradeRecord struct {
	Timestamp time.Time `json:"timestamp"`
	ProfitETH float64   `json:"profit_eth"`
	GasETH    float64   `json:"gas_eth"`
	Builder   string    `json:"builder"`
	BundleHash string   `json:"bundle_hash,omitempty"`
}

// Snapshot is the GET /metrics/json response body.
type Snapshot struct {
	PnLToday           float64     `json:"pnl_today"`
	PnLTotal           float64     `json:"pnl_total"`
	WinRate            float64     `json:"winrate"`
	LastBundleProfit   float64     `json:"last_bundle_profit"`
	LastBundleGas      float64     `json:"last_bundle_gas"`
	LastBuilder        string      `json:"last_builder"`
	BreakerOpen        bool        `json:"breaker_open"`
	BreakerReason      string      `json:"breaker_reason,omitempty"`
	SignerHealthy      bool        `json:"signer_healthy"`
	RPCHealthy         bool        `json:"rpc_healthy"`
	DiscoveryHealthy   bool        `json:"discovery_healthy"`
	TimescaleHealthy   bool        `json:"timescale_healthy"`
	RedisHealthy       bool        `json:"redis_healthy"`
	SystemState        string      `json:"system_state"`
	MinProfitETH       float64     `json:"min_profit_eth"`
	TopPools           []TopPool   `json:"top_pools"`
	RecentTrades       []TradeRecord `json:"recent_trades,omitempty"`
	ExecutorReachable  bool        `json:"executor_reachable"`
	UpdatedAt          time.Time   `json:"updated_at"`
}

// Store holds the live executor metrics snapshot (thread-safe).
type Store struct {
	mu       sync.RWMutex
	snapshot Snapshot
}

// NewStore creates a store with healthy defaults.
func NewStore() *Store {
	return &Store{
		snapshot: Snapshot{
			SignerHealthy:    true,
			RPCHealthy:       true,
			DiscoveryHealthy: true,
			TimescaleHealthy: true,
			ExecutorReachable: true,
			TopPools:         []TopPool{},
			RecentTrades:     []TradeRecord{},
		},
	}
}

// Get returns a copy of the current snapshot.
func (s *Store) Get() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot
}

// Update applies a mutation function to the snapshot.
func (s *Store) Update(fn func(*Snapshot)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.snapshot)
	s.snapshot.UpdatedAt = time.Now().UTC()
}

// SetTopPools replaces the top pools list.
func (s *Store) SetTopPools(pools []TopPool) {
	s.Update(func(sn *Snapshot) {
		sn.TopPools = pools
	})
}

// RecordTrade prepends a trade to the recent trades ring (max 10).
func (s *Store) RecordTrade(t TradeRecord) {
	s.Update(func(sn *Snapshot) {
		sn.RecentTrades = append([]TradeRecord{t}, sn.RecentTrades...)
		if len(sn.RecentTrades) > 10 {
			sn.RecentTrades = sn.RecentTrades[:10]
		}
	})
}
