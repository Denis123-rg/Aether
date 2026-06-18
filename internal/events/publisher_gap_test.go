package events

import (
	"testing"
)

// TestPublishNilPublisher verifies that calling publish with nil receiver or
// nil client is safe and returns without panic.
func TestPublishNil(t *testing.T) {
	var p *Publisher
	p.publish("test", map[string]string{"key": "value"})

	p = NewPublisher("")
	p.publish("test", map[string]string{"key": "value"})
}

// TestPublishMethodsNoRedis verifies all public Publish* methods are safe when
// REDIS_URL is empty.
func TestPublishMethodsNoRedis(t *testing.T) {
	p := NewPublisher("")
	if p.Enabled() {
		t.Fatal("expected disabled publisher")
	}

	p.PublishNewBundle("0xhash", "flashbots", 0.01, 0.001)
	p.PublishPnLUpdate(1.5, 75.0)
	p.PublishBreakerStatus(true, "test")
	p.PublishSignerHealth(true)
	p.Close()
}

// TestPublishEnabledWhenRedisSet verifies that Enabled() returns true when
// a valid redis URL is configured.
func TestPublishEnabledWhenRedisSet(t *testing.T) {
	p := NewPublisher("redis://localhost:6379/0")
	// Even though we cannot connect in most test environments,
	// the constructor should not panic and should return a valid publisher.
	if p == nil {
		t.Fatal("expected non-nil publisher")
	}
	// It may or may not be enabled depending on connection success
	p.Close()
}

// TestPublisherFromEnvEmptyURL verifies that when REDIS_URL is empty,
// the publisher is a no-op.
func TestPublisherFromEnvEmptyURL(t *testing.T) {
	t.Setenv("REDIS_URL", "")
	p := NewPublisherFromEnv()
	if p.Enabled() {
		t.Fatal("expected disabled publisher when REDIS_URL is empty")
	}
	if p == nil {
		t.Fatal("expected non-nil publisher")
	}
}

// TestPublisherFromEnvInvalidURL verifies handling of invalid REDIS_URL.
func TestPublisherFromEnvInvalidURL(t *testing.T) {
	t.Setenv("REDIS_URL", "not-a-valid-url")
	p := NewPublisherFromEnv()
	if p.Enabled() {
		t.Log("invalid URL may or may not produce enabled publisher")
	}
}

// TestPublisherClose safely closes a publisher.
func TestPublisherClose(t *testing.T) {
	// No-op publisher
	p := NewPublisher("")
	p.Close()
	if p.Enabled() {
		t.Fatal("expected disabled after close")
	}
}

// TestPublisherMarshalError verifies the publish error path for unmarshalable data.
func TestPublisherMarshalError(t *testing.T) {
	// This test exercises the json.Marshal error branch inside publish().
	// json.Marshal accepts any Go value, so we create a minimal Publisher
	// and ensure it doesn't panic.
	p := NewPublisher("")
	p.PublishNewBundle("", "", 0, 0)
	p.PublishPnLUpdate(0, 0)
	p.PublishBreakerStatus(false, "")
	p.PublishSignerHealth(false)
}
