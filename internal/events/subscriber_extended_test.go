package events

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func TestNewSubscriber_NilStateReturnsNil(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	if sub := NewSubscriber("redis://"+mr.Addr(), nil, nil); sub != nil {
		t.Fatal("nil state must yield nil subscriber")
	}
}

func TestConcurrentSubscribersReceiveDistinctEvents(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()

	url := "redis://" + mr.Addr()
	pub := NewPublisher(url)
	defer pub.Close()

	const n = 4
	var wg sync.WaitGroup
	states := make([]*DashboardState, n)
	for i := 0; i < n; i++ {
		states[i] = &DashboardState{}
		sub := NewSubscriber(url, states[i], nil)
		if sub == nil {
			t.Fatalf("subscriber %d nil", i)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		sub.Start(ctx)
		wg.Add(1)
		go func(idx int, s *Subscriber) {
			defer wg.Done()
			defer s.Stop()
			time.Sleep(50 * time.Millisecond)
			pub.PublishPnLUpdate(float64(idx+1)*10, 50)
			time.Sleep(200 * time.Millisecond)
		}(i, sub)
	}
	wg.Wait()

	for i, st := range states {
		got := st.Get()
		if got.PnLTotal == 0 {
			t.Fatalf("subscriber %d did not receive PnL update: %+v", i, got)
		}
	}
}

func TestPublisherFromEnvEmptyRedisURL(t *testing.T) {
	t.Setenv("REDIS_URL", "")
	p := NewPublisherFromEnv()
	if p.Enabled() {
		t.Fatal("empty REDIS_URL must disable publisher")
	}
}
