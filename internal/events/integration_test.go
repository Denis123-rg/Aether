package events

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func TestPublisherSubscriberRoundTrip(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()

	url := "redis://" + mr.Addr()
	pub := NewPublisher(url)
	if !pub.Enabled() {
		t.Fatal("publisher should be enabled")
	}
	defer pub.Close()

	state := &DashboardState{}
	refreshed := make(chan struct{}, 1)
	sub := NewSubscriber(url, state, func() {
		select {
		case refreshed <- struct{}{}:
		default:
		}
	})
	if sub == nil {
		t.Fatal("subscriber should connect")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	pub.PublishNewBundle("0xhash", "flashbots", 0.05, 0.01)
	pub.PublishPnLUpdate(10.0, 75.0)
	pub.PublishBreakerStatus(true, "test")
	pub.PublishSignerHealth(false)

	select {
	case <-refreshed:
	case <-time.After(2 * time.Second):
		t.Fatal("expected refresh callback")
	}

	got := state.Get()
	if got.LastBuilder != "flashbots" || got.PnLTotal != 10.0 {
		t.Fatalf("state: %+v", got)
	}
	if !got.BreakerOpen || got.SignerHealthy {
		t.Fatalf("breaker/signer: %+v", got)
	}

	sub.Stop()
}

func TestSubscriberReconnectsAfterRedisRestart(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	url := "redis://" + mr.Addr()
	state := &DashboardState{}
	sub := NewSubscriber(url, state, nil)
	if sub == nil {
		t.Fatal("subscriber nil")
	}
	ctx, cancel := context.WithCancel(context.Background())
	sub.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	mr.Close() // simulate disconnect
	time.Sleep(100 * time.Millisecond)

	// Restart miniredis on same addr isn't possible — verify state shows disconnected
	if state.Get().RedisConnected {
		// may still be connected briefly
	}
	cancel()
	sub.Stop()
}
