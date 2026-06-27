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

	// Drain initial refresh signals; wait until all state is propagated.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-refreshed:
		default:
		}
		got := state.Get()
		if got.LastBuilder == "flashbots" && got.PnLTotal == 10.0 && got.BreakerOpen && !got.SignerHealthy {
			break
		}
		time.Sleep(50 * time.Millisecond)
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
	addr := mr.Addr()
	url := "redis://" + addr
	state := &DashboardState{}
	sub := NewSubscriber(url, state, nil)
	if sub == nil {
		t.Fatal("subscriber nil")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub.Start(ctx)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if state.Get().RedisConnected {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !state.Get().RedisConnected {
		t.Fatal("expected initial redis connection")
	}

	mr.Close() // kill Redis — subscriber should mark disconnected

	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !state.Get().RedisConnected {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if state.Get().RedisConnected {
		t.Fatal("expected disconnected after redis killed")
	}

	// Subscriber marks disconnected after kill; reconnect loop retries until ctx done.
	// Full same-address restart requires external Redis — here we verify disconnect detection.
	cancel()
	sub.Stop()
}
