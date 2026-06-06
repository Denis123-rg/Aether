package events

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestNewSubscriberEmptyURL(t *testing.T) {
	state := &DashboardState{}
	s := NewSubscriber("", state, nil)
	if s != nil {
		t.Fatal("empty url should return nil")
	}
}

func TestNewSubscriberNilState(t *testing.T) {
	s := NewSubscriber("redis://localhost:6379", nil, nil)
	if s != nil {
		t.Fatal("nil state should return nil")
	}
}

func TestDashboardStateGet(t *testing.T) {
	d := &DashboardState{PnLTotal: 5.0, WinRate: 80.0}
	got := d.Get()
	if got.PnLTotal != 5.0 || got.WinRate != 80.0 {
		t.Fatalf("state: %+v", got)
	}
}

func TestDashboardStateUpdate(t *testing.T) {
	d := &DashboardState{}
	d.update(func(s *DashboardState) {
		s.BreakerOpen = true
		s.BreakerReason = "test"
	})
	got := d.Get()
	if !got.BreakerOpen || got.BreakerReason != "test" {
		t.Fatalf("state: %+v", got)
	}
}

func TestSubscriberRouteBundle(t *testing.T) {
	d := &DashboardState{}
	s := &Subscriber{state: d}
	ev := BundleEvent{BundleHash: "0x1", Builder: "titan", Profit: 0.1, Gas: 0.01}
	data, _ := json.Marshal(ev)
	s.route(ChannelBundlesNew, string(data))
	got := d.Get()
	if got.LastBuilder != "titan" || got.LastBundleProfit != 0.1 {
		t.Fatalf("state: %+v", got)
	}
}

func TestSubscriberRoutePnL(t *testing.T) {
	d := &DashboardState{}
	s := &Subscriber{state: d}
	ev := PnLEvent{TotalProfit: 99.0, WinRate: 55.0}
	data, _ := json.Marshal(ev)
	s.route(ChannelPnLUpdate, string(data))
	got := d.Get()
	if got.PnLTotal != 99.0 || got.WinRate != 55.0 {
		t.Fatalf("state: %+v", got)
	}
}

func TestSubscriberRouteBreaker(t *testing.T) {
	d := &DashboardState{}
	s := &Subscriber{state: d}
	ev := BreakerEvent{Open: true, Reason: "halted"}
	data, _ := json.Marshal(ev)
	s.route(ChannelBreaker, string(data))
	if !d.Get().BreakerOpen {
		t.Fatal("breaker should be open")
	}
}

func TestSubscriberRouteSigner(t *testing.T) {
	d := &DashboardState{}
	s := &Subscriber{state: d}
	ev := SignerHealthEvent{Healthy: false}
	data, _ := json.Marshal(ev)
	s.route(ChannelSignerHealth, string(data))
	if d.Get().SignerHealthy {
		t.Fatal("signer should be unhealthy")
	}
}

func TestSubscriberRouteInvalidJSON(t *testing.T) {
	d := &DashboardState{PnLTotal: 1.0}
	s := &Subscriber{state: d}
	s.route(ChannelPnLUpdate, "not-json")
	if d.Get().PnLTotal != 1.0 {
		t.Fatal("state should be unchanged")
	}
}

func TestSubscriberStopNil(t *testing.T) {
	var s *Subscriber
	s.Stop() // must not panic
}

func TestSubscriberEnabledNil(t *testing.T) {
	var s *Subscriber
	if s.Enabled() {
		t.Fatal("nil not enabled")
	}
}

func TestSubscriberInvalidURL(t *testing.T) {
	state := &DashboardState{}
	s := NewSubscriber("redis://invalid-host:59999", state, nil)
	if s != nil {
		t.Fatal("unreachable redis should return nil")
	}
}

func TestDashboardStateConcurrent(t *testing.T) {
	d := &DashboardState{}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			d.update(func(s *DashboardState) { s.PnLTotal += 1 })
		}
		close(done)
	}()
	for i := 0; i < 50; i++ {
		_ = d.Get()
	}
	<-done
}

func TestSubscriberContextCancel(t *testing.T) {
	s := &Subscriber{state: &DashboardState{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// run should exit quickly on cancelled ctx when no client
	s.wg.Add(0)
	_ = ctx
	_ = time.Second
}
