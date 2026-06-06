package events

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNewPublisherEmptyURL(t *testing.T) {
	p := NewPublisher("")
	if p.Enabled() {
		t.Fatal("empty url should be no-op")
	}
	p.PublishNewBundle("hash", "builder", 1.0, 0.1) // must not panic
}

func TestNewPublisherFromEnvUnset(t *testing.T) {
	p := NewPublisherFromEnv()
	if p == nil {
		t.Fatal("nil publisher")
	}
	p.PublishPnLUpdate(1.0, 50.0)
}

func TestNewPublisherInvalidURL(t *testing.T) {
	p := NewPublisher("not-a-valid-redis-url")
	if p.Enabled() {
		t.Fatal("invalid url should be no-op")
	}
}

func TestBundleEventSerialization(t *testing.T) {
	ev := BundleEvent{
		BundleHash: "0xabc",
		Builder:    "flashbots",
		Profit:     0.05,
		Gas:        0.01,
		Timestamp:  time.Now().UTC(),
	}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var decoded BundleEvent
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.BundleHash != ev.BundleHash || decoded.Profit != ev.Profit {
		t.Fatalf("decoded: %+v", decoded)
	}
}

func TestPnLEventSerialization(t *testing.T) {
	ev := PnLEvent{TotalProfit: 10.5, WinRate: 66.7, Timestamp: time.Now().UTC()}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Fatal("invalid json")
	}
}

func TestBreakerEventSerialization(t *testing.T) {
	ev := BreakerEvent{Open: true, Reason: "admin_pause", Timestamp: time.Now().UTC()}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var d BreakerEvent
	json.Unmarshal(data, &d)
	if !d.Open || d.Reason != "admin_pause" {
		t.Fatalf("decoded: %+v", d)
	}
}

func TestSignerHealthEventSerialization(t *testing.T) {
	ev := SignerHealthEvent{Healthy: false, Timestamp: time.Now().UTC()}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var d SignerHealthEvent
	json.Unmarshal(data, &d)
	if d.Healthy {
		t.Fatal("expected unhealthy")
	}
}

func TestPublisherNoOpMethods(t *testing.T) {
	var p *Publisher
	p.PublishBreakerStatus(true, "test")
	p.PublishSignerHealth(false)
	p.Close()
}

func TestChannelNames(t *testing.T) {
	if ChannelBundlesNew != "aether:bundles:new" {
		t.Fatal("channel name mismatch")
	}
	if ChannelPnLUpdate != "aether:pnl:update" {
		t.Fatal("channel name mismatch")
	}
}

func TestPublisherEnabledNil(t *testing.T) {
	var p *Publisher
	if p.Enabled() {
		t.Fatal("nil should not be enabled")
	}
}

func TestBundleEventZeroValues(t *testing.T) {
	ev := BundleEvent{}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("empty marshal")
	}
}
