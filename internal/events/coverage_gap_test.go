package events

import (
	"testing"
)

func TestPublisher_NilClient_Coverage(t *testing.T) {
	p := &Publisher{}
	p.publish("test-channel", map[string]string{"key": "val"})
	if p.Enabled() {
		t.Error("expected not enabled for nil client")
	}
}

func TestPublisher_Close_Nil_Coverage(t *testing.T) {
	var p *Publisher
	p.Close()
}

func TestPublisher_Close_Empty_Coverage(t *testing.T) {
	p := &Publisher{}
	p.Close()
}

func TestNewPublisher_EmptyURL_Coverage(t *testing.T) {
	p := NewPublisher("")
	if p.Enabled() {
		t.Error("expected not enabled for empty URL")
	}
}

func TestNewPublisher_InvalidURL_Coverage(t *testing.T) {
	p := NewPublisher("not-a-url")
	if p.Enabled() {
		t.Error("expected not enabled for invalid URL")
	}
}

func TestNewPublisherFromEnv_Empty_Coverage(t *testing.T) {
	t.Setenv("REDIS_URL", "")
	p := NewPublisherFromEnv()
	if p.Enabled() {
		t.Error("expected not enabled")
	}
}

func TestPublisher_PublishNewBundle_NilClient_Coverage(t *testing.T) {
	p := &Publisher{}
	p.PublishNewBundle("hash", "builder", 0.1, 0.01)
}

func TestPublisher_PublishPnLUpdate_NilClient_Coverage(t *testing.T) {
	p := &Publisher{}
	p.PublishPnLUpdate(1.0, 60.0)
}

func TestPublisher_PublishBreakerStatus_NilClient_Coverage(t *testing.T) {
	p := &Publisher{}
	p.PublishBreakerStatus(true, "test reason")
}

func TestPublisher_PublishSignerHealth_NilClient_Coverage(t *testing.T) {
	p := &Publisher{}
	p.PublishSignerHealth(false)
}

func TestSubscriber_NilState_Coverage(t *testing.T) {
	s := NewSubscriber("redis://localhost:6379", nil, nil)
	if s != nil {
		t.Error("expected nil for nil state")
	}
}

func TestSubscriber_EmptyURL_Coverage(t *testing.T) {
	s := NewSubscriber("", &DashboardState{}, nil)
	if s != nil {
		t.Error("expected nil for empty URL")
	}
}

func TestSubscriber_InvalidURL_Coverage(t *testing.T) {
	s := NewSubscriber("not-a-valid-url", &DashboardState{}, nil)
	if s != nil {
		t.Error("expected nil for invalid URL")
	}
}

func TestSubscriber_NilStart_Coverage(t *testing.T) {
	var s *Subscriber
	s.Start(t.Context())
}

func TestSubscriber_Stop_Nil_Coverage(t *testing.T) {
	var s *Subscriber
	s.Stop()
}

func TestSubscriber_Stop_NoClient_Coverage(t *testing.T) {
	s := &Subscriber{}
	s.Stop()
}

func TestSubscriber_Enabled_Nil_Coverage(t *testing.T) {
	var s *Subscriber
	if s.Enabled() {
		t.Error("expected not enabled for nil")
	}
}

func TestSubscriber_Enabled_NoClient_Coverage(t *testing.T) {
	s := &Subscriber{}
	if s.Enabled() {
		t.Error("expected not enabled")
	}
}

func TestDashboardState_Get_Coverage(t *testing.T) {
	d := &DashboardState{DashboardData: DashboardData{PnLTotal: 1.5, WinRate: 60.0}}
	state := d.Get()
	if state.PnLTotal != 1.5 || state.WinRate != 60.0 {
		t.Errorf("unexpected state: %+v", state)
	}
}

func TestDashboardState_Update_Coverage(t *testing.T) {
	d := &DashboardState{}
	d.update(func(st *DashboardState) {
		st.PnLTotal = 2.0
	})
	if d.Get().PnLTotal != 2.0 {
		t.Error("expected update")
	}
}

func TestSubscriber_Route_BundlesNew_Coverage(t *testing.T) {
	d := &DashboardState{}
	s := &Subscriber{state: d}
	payload := `{"bundle_hash":"0xabc","builder":"flashbots","profit":0.1,"gas":0.01}`
	s.route(ChannelBundlesNew, payload)
	state := d.Get()
	if state.LastBundleHash != "0xabc" {
		t.Errorf("expected 0xabc, got %s", state.LastBundleHash)
	}
}

func TestSubscriber_Route_PnLUpdate_Coverage(t *testing.T) {
	d := &DashboardState{}
	s := &Subscriber{state: d}
	payload := `{"total_profit":2.5,"winrate":70.0}`
	s.route(ChannelPnLUpdate, payload)
	state := d.Get()
	if state.PnLTotal != 2.5 {
		t.Errorf("expected 2.5, got %f", state.PnLTotal)
	}
}

func TestSubscriber_Route_Breaker_Coverage(t *testing.T) {
	d := &DashboardState{}
	s := &Subscriber{state: d}
	payload := `{"open":true,"reason":"test"}`
	s.route(ChannelBreaker, payload)
	state := d.Get()
	if !state.BreakerOpen || state.BreakerReason != "test" {
		t.Errorf("unexpected: %+v", state)
	}
}

func TestSubscriber_Route_SignerHealth_Coverage(t *testing.T) {
	d := &DashboardState{}
	s := &Subscriber{state: d}
	payload := `{"healthy":true}`
	s.route(ChannelSignerHealth, payload)
	state := d.Get()
	if !state.SignerHealthy {
		t.Error("expected signer healthy")
	}
}

func TestSubscriber_Route_UnknownChannel_Coverage(t *testing.T) {
	d := &DashboardState{}
	s := &Subscriber{state: d}
	s.route("unknown_channel", "{}")
}

func TestSubscriber_Route_InvalidJSON_Coverage(t *testing.T) {
	d := &DashboardState{}
	s := &Subscriber{state: d}
	s.route(ChannelBundlesNew, "not-json")
}
