// Package events provides Redis pub/sub for real-time Aether events.
package events

import "time"

// Channel names for Redis pub/sub.
const (
	ChannelBundlesNew   = "aether:bundles:new"
	ChannelPnLUpdate    = "aether:pnl:update"
	ChannelBreaker      = "aether:status:breaker"
	ChannelSignerHealth = "aether:signer:health"
)

// BundleEvent is published when a new bundle is submitted.
type BundleEvent struct {
	BundleHash string    `json:"bundle_hash"`
	Builder    string    `json:"builder"`
	Profit     float64   `json:"profit"`
	Gas        float64   `json:"gas"`
	Timestamp  time.Time `json:"timestamp"`
}

// PnLEvent is published when cumulative PnL changes.
type PnLEvent struct {
	TotalProfit float64   `json:"total_profit"`
	WinRate     float64   `json:"winrate"`
	Timestamp   time.Time `json:"timestamp"`
}

// BreakerEvent is published when circuit breaker state changes.
type BreakerEvent struct {
	Open      bool      `json:"open"`
	Reason    string    `json:"reason"`
	Timestamp time.Time `json:"timestamp"`
}

// SignerHealthEvent is published when signer health changes.
type SignerHealthEvent struct {
	Healthy   bool      `json:"healthy"`
	Timestamp time.Time `json:"timestamp"`
}
