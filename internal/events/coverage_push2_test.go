package events

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func TestPublisher_PublishAfterRedisClosed(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	url := "redis://" + mr.Addr()
	p := NewPublisher(url)
	if !p.Enabled() {
		t.Fatal("expected enabled publisher")
	}
	mr.Close()
	// Closed backend should hit publish error branch without panicking.
	p.PublishPnLUpdate(1, 1)
	p.PublishNewBundle("h", "b", 1, 1)
	p.PublishBreakerStatus(true, "test")
	p.PublishSignerHealth(false)
	p.Close()
}

func TestPublisher_PublishOnClosedClient(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	p := NewPublisher("redis://" + mr.Addr())
	p.Close()
	p.PublishPnLUpdate(1, 1)
}

func TestPublisher_NilReceiverPublish(t *testing.T) {
	var p *Publisher
	p.PublishPnLUpdate(1, 1)
}

func TestSubscriber_RunReconnectBackoff(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	url := "redis://" + mr.Addr()
	state := &DashboardState{}
	sub := NewSubscriber(url, state, nil)
	ctx, cancel := context.WithCancel(context.Background())
	sub.Start(ctx)

	// Kill redis to force listen() error and run() reconnect/backoff path.
	mr.Close()
	time.Sleep(200 * time.Millisecond)
	cancel()
	sub.Stop()
}

func TestSubscriber_RouteBadJSON_Table(t *testing.T) {
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

	tests := []struct {
		name    string
		channel string
		payload string
	}{
		{name: "bundle", channel: ChannelBundlesNew, payload: "not-json"},
		{name: "breaker", channel: ChannelBreaker, payload: "{bad"},
		{name: "signer", channel: ChannelSignerHealth, payload: "[]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_ = mr.Publish(tc.channel, tc.payload)
			time.Sleep(50 * time.Millisecond)
		})
	}
	if state.Get().LastBundleHash != "" {
		t.Fatal("bad bundle json should not update state")
	}
}

func TestSubscriber_Enabled(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	sub := NewSubscriber("redis://"+mr.Addr(), &DashboardState{}, nil)
	if !sub.Enabled() {
		t.Fatal("expected enabled subscriber")
	}
	sub.Stop()
}

func TestSubscriber_ListenChannelClose(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	url := "redis://" + mr.Addr()
	state := &DashboardState{}
	sub := NewSubscriber(url, state, nil)
	ctx, cancel := context.WithCancel(context.Background())
	sub.Start(ctx)

	pub := NewPublisher(url)
	pub.PublishPnLUpdate(1, 1)
	time.Sleep(100 * time.Millisecond)

	mr.Close()
	time.Sleep(200 * time.Millisecond)
	cancel()
	sub.Stop()
	pub.Close()
}
