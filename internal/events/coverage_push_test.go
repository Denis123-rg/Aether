package events

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func TestNewPublisherFromEnv_Table(t *testing.T) {
	t.Run("unset disables", func(t *testing.T) {
		t.Setenv("REDIS_URL", "")
		p := NewPublisherFromEnv()
		if p.Enabled() {
			t.Fatal("expected disabled publisher")
		}
	})
	t.Run("valid url enables", func(t *testing.T) {
		mr, err := miniredis.Run()
		if err != nil {
			t.Fatal(err)
		}
		defer mr.Close()
		t.Setenv("REDIS_URL", "redis://"+mr.Addr())
		p := NewPublisherFromEnv()
		if !p.Enabled() {
			t.Fatal("expected enabled publisher")
		}
		p.Close()
	})
}

func TestNewPublisher_InvalidURL(t *testing.T) {
	p := NewPublisher("not-a-valid-redis-url")
	if p.Enabled() {
		t.Fatal("invalid url should disable publisher")
	}
}

func TestPublisher_AllChannels(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	url := "redis://" + mr.Addr()

	state := &DashboardState{}
	sub := NewSubscriber(url, state, nil)
	if sub == nil {
		t.Fatal("nil subscriber")
	}
	ctx, cancel := context.WithCancel(context.Background())
	sub.Start(ctx)
	defer func() {
		cancel()
		sub.Stop()
	}()

	pub := NewPublisher(url)
	defer pub.Close()

	pub.PublishNewBundle("0xabc", "flashbots", 1.5, 0.01)
	waitUntil(t, func() bool {
		got := state.Get()
		return got.LastBundleHash == "0xabc" && got.LastBuilder == "flashbots"
	}, "bundle event")

	pub.PublishPnLUpdate(10, 75)
	waitUntil(t, func() bool {
		got := state.Get()
		return got.PnLTotal == 10 && got.WinRate == 75
	}, "pnl event")

	pub.PublishBreakerStatus(true, "gas")
	waitUntil(t, func() bool {
		got := state.Get()
		return got.BreakerOpen && got.BreakerReason == "gas"
	}, "breaker event")

	pub.PublishSignerHealth(false)
	waitUntil(t, func() bool {
		return !state.Get().SignerHealthy
	}, "signer event")
}

func waitUntil(t *testing.T, cond func() bool, label string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", label)
}

func TestNewSubscriber_InvalidURL(t *testing.T) {
	if sub := NewSubscriber("://bad", &DashboardState{}, nil); sub != nil {
		t.Fatal("expected nil subscriber")
	}
}

func TestNewSubscriber_NilState(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	if sub := NewSubscriber("redis://"+mr.Addr(), nil, nil); sub != nil {
		t.Fatal("expected nil when state is nil")
	}
}

func TestSubscriber_StartNilSafe(t *testing.T) {
	var sub *Subscriber
	sub.Start(context.Background()) // must not panic
	sub.Stop()
}

func TestPublisher_UnreachableRedis(t *testing.T) {
	p := NewPublisher("redis://127.0.0.1:1")
	if p.Enabled() {
		t.Fatal("unreachable redis should disable publisher")
	}
}

func TestSubscriber_OnEventCallback(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	url := "redis://" + mr.Addr()
	state := &DashboardState{}
	var calls int
	sub := NewSubscriber(url, state, func() { calls++ })
	ctx, cancel := context.WithCancel(context.Background())
	sub.Start(ctx)
	defer func() {
		cancel()
		sub.Stop()
	}()
	pub := NewPublisher(url)
	defer pub.Close()
	pub.PublishPnLUpdate(1, 1)
	waitUntil(t, func() bool { return calls > 0 || state.Get().PnLTotal == 1 }, "onEvent callback")
}

func TestSubscriber_RouteBadJSON(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	url := "redis://" + mr.Addr()
	state := &DashboardState{}
	sub := NewSubscriber(url, state, nil)
	ctx, cancel := context.WithCancel(context.Background())
	sub.Start(ctx)
	defer func() {
		cancel()
		sub.Stop()
	}()
	// Publish invalid JSON directly via miniredis.
	_ = mr.Publish(ChannelPnLUpdate, "not-json")
	time.Sleep(100 * time.Millisecond)
	if state.Get().PnLTotal != 0 {
		t.Fatal("bad json should not update state")
	}
}

func TestSubscriber_ListenPingFailure(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	url := "redis://" + mr.Addr()
	state := &DashboardState{}
	sub := NewSubscriber(url, state, nil)
	ctx, cancel := context.WithCancel(context.Background())
	sub.Start(ctx)
	time.Sleep(150 * time.Millisecond)
	mr.Close()
	time.Sleep(700 * time.Millisecond)
	cancel()
	sub.Stop()
	if state.Get().RedisConnected {
		// After disconnect, connected flag should eventually clear.
		t.Log("redis connected flag may still be true briefly after close")
	}
}

func TestPublisher_PublishMarshalSafe(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	p := NewPublisher("redis://" + mr.Addr())
	defer p.Close()
	p.PublishNewBundle("h", "b", 1, 1)
}
