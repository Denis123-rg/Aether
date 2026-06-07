package events

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func TestNewPublisher_DisabledWhenEmptyURL(t *testing.T) {
	p := NewPublisher("")
	if p.Enabled() {
		t.Fatal("empty url should disable")
	}
}

func TestNewPublisher_EnabledWithURL(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	p := NewPublisher("redis://" + mr.Addr())
	if !p.Enabled() {
		t.Fatal("expected enabled")
	}
	p.Close()
}

func TestPublisherPublishWhenDisabled(t *testing.T) {
	p := NewPublisher("")
	p.PublishPnLUpdate(1.0, 50)
	p.PublishBreakerStatus(false, "")
}

func TestSubscriberRestartReconnect(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	url := "redis://" + mr.Addr()
	state := &DashboardState{}
	sub := NewSubscriber(url, state, nil)
	if sub == nil {
		t.Fatal("nil subscriber")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub.Start(ctx)
	defer sub.Stop()

	pub := NewPublisher(url)
	defer pub.Close()
	pub.PublishPnLUpdate(42.0, 100)
	time.Sleep(100 * time.Millisecond)

	mr.Restart()
	time.Sleep(150 * time.Millisecond)
	pub.PublishPnLUpdate(43.0, 100)
	time.Sleep(200 * time.Millisecond)
}

func TestNewSubscriber_EmptyURL(t *testing.T) {
	if sub := NewSubscriber("", &DashboardState{}, nil); sub != nil {
		t.Fatal("empty url should return nil")
	}
}

func TestPublisherCloseIdempotent(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	p := NewPublisher("redis://" + mr.Addr())
	p.Close()
	p.Close()
}

func TestDashboardStateGetSet(t *testing.T) {
	st := &DashboardState{}
	st.update(func(s *DashboardState) {
		s.PnLTotal = 1.5
		s.WinRate = 60
	})
	got := st.Get()
	if got.PnLTotal != 1.5 || got.WinRate != 60 {
		t.Fatalf("state = %+v", got)
	}
}
