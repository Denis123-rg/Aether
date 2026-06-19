package events

import (
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

var errMarshalFailed = errors.New("intentional marshal failure")

type unmarshalable struct{}

func (u unmarshalable) MarshalJSON() ([]byte, error) {
	return nil, errMarshalFailed
}

func TestPublish_MarshalError(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	p := NewPublisher("redis://" + mr.Addr())
	if !p.Enabled() {
		t.Fatal("expected enabled publisher")
	}
	defer p.Close()

	p.publish("test-channel", unmarshalable{})
}

func TestPublish_RedisPublishError(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	p := NewPublisher("redis://" + mr.Addr())
	if !p.Enabled() {
		t.Fatal("expected enabled publisher")
	}

	mr.Close()
	time.Sleep(50 * time.Millisecond)

	p.publish("test-channel", map[string]string{"key": "value"})
}

func TestSubscriber_Run_ReconnectLoop(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}

	state := &DashboardState{}
	sub := NewSubscriber("redis://"+mr.Addr(), state, nil)
	if sub == nil {
		t.Fatal("expected non-nil subscriber")
	}

	ctx := t.Context()
	sub.Start(ctx)

	time.Sleep(200 * time.Millisecond)

	mr.Close()
	time.Sleep(800 * time.Millisecond)

	sub.Stop()
}

func TestPublisher_PublishNewBundle_WithRedis(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	p := NewPublisher("redis://" + mr.Addr())
	if !p.Enabled() {
		t.Fatal("expected enabled")
	}
	defer p.Close()

	p.PublishNewBundle("0xabc", "flashbots", 0.05, 0.01)
	p.PublishPnLUpdate(1.5, 66.7)
	p.PublishBreakerStatus(true, "gas_high")
	p.PublishSignerHealth(true)
}

func TestPublisher_PublishWithNilPayloadFields(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	p := NewPublisher("redis://" + mr.Addr())
	if !p.Enabled() {
		t.Fatal("expected enabled")
	}
	defer p.Close()

	p.PublishNewBundle("", "", 0, 0)
	p.PublishPnLUpdate(0, 0)
	p.PublishBreakerStatus(false, "")
	p.PublishSignerHealth(false)
}
