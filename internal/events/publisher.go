package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

// Publisher publishes Aether events to Redis channels. When REDIS_URL is
// unset or Redis is unreachable at construction time, all methods are no-ops.
type Publisher struct {
	client *redis.Client
}

// NewPublisherFromEnv creates a Publisher from REDIS_URL. Returns a no-op
// publisher when the URL is empty.
func NewPublisherFromEnv() *Publisher {
	url := os.Getenv("REDIS_URL")
	if url == "" {
		slog.Info("REDIS_URL unset — event publisher disabled (no-op)")
		return &Publisher{}
	}
	return NewPublisher(url)
}

// NewPublisher connects to Redis at the given URL. On connection failure
// returns a no-op publisher (no panic, no error).
func NewPublisher(url string) *Publisher {
	if url == "" {
		return &Publisher{}
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		slog.Warn("invalid REDIS_URL, event publisher disabled", "err", err)
		return &Publisher{}
	}
	client := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		slog.Warn("redis ping failed, event publisher disabled", "err", err)
		_ = client.Close()
		return &Publisher{}
	}
	slog.Info("redis event publisher connected")
	return &Publisher{client: client}
}

func (p *Publisher) publish(channel string, payload any) {
	if p == nil || p.client == nil {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("event marshal failed", "channel", channel, "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := p.client.Publish(ctx, channel, data).Err(); err != nil {
		slog.Warn("redis publish failed", "channel", channel, "err", err)
	}
}

// PublishNewBundle publishes a new bundle event.
func (p *Publisher) PublishNewBundle(bundleHash, builder string, profit, gas float64) {
	p.publish(ChannelBundlesNew, BundleEvent{
		BundleHash: bundleHash,
		Builder:    builder,
		Profit:     profit,
		Gas:        gas,
		Timestamp:  time.Now().UTC(),
	})
}

// PublishPnLUpdate publishes a PnL update event.
func (p *Publisher) PublishPnLUpdate(totalProfit, winrate float64) {
	p.publish(ChannelPnLUpdate, PnLEvent{
		TotalProfit: totalProfit,
		WinRate:     winrate,
		Timestamp:   time.Now().UTC(),
	})
}

// PublishBreakerStatus publishes a circuit breaker status change.
func (p *Publisher) PublishBreakerStatus(open bool, reason string) {
	p.publish(ChannelBreaker, BreakerEvent{
		Open:      open,
		Reason:    reason,
		Timestamp: time.Now().UTC(),
	})
}

// PublishSignerHealth publishes signer health status.
func (p *Publisher) PublishSignerHealth(healthy bool) {
	p.publish(ChannelSignerHealth, SignerHealthEvent{
		Healthy:   healthy,
		Timestamp: time.Now().UTC(),
	})
}

// Close releases the Redis client.
func (p *Publisher) Close() {
	if p != nil && p.client != nil {
		_ = p.client.Close()
	}
}

// Enabled reports whether the publisher has an active Redis connection.
func (p *Publisher) Enabled() bool {
	return p != nil && p.client != nil
}
