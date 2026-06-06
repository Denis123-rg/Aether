package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// StateHandler is called when an event updates dashboard state.
type StateHandler func()

// DashboardState is the in-memory state telebot uses for the dashboard.
type DashboardState struct {
	mu              sync.RWMutex
	PnLTotal        float64
	WinRate         float64
	LastBundleProfit float64
	LastBundleGas   float64
	LastBuilder     string
	BreakerOpen     bool
	BreakerReason   string
	SignerHealthy   bool
	LastBundleHash  string
	RedisConnected  bool
}

// Get returns a copy of the current state.
func (d *DashboardState) Get() DashboardState {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return *d
}

func (d *DashboardState) update(fn func(*DashboardState)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	fn(d)
}

// Subscriber listens to Redis channels and updates DashboardState.
type Subscriber struct {
	client  *redis.Client
	state   *DashboardState
	onEvent StateHandler
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// NewSubscriber connects to Redis. Returns nil when url is empty or
// connection fails (caller should fall back to polling).
func NewSubscriber(url string, state *DashboardState, onEvent StateHandler) *Subscriber {
	if url == "" || state == nil {
		return nil
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		slog.Warn("invalid redis url for subscriber", "err", err)
		return nil
	}
	client := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		slog.Warn("redis subscriber ping failed", "err", err)
		_ = client.Close()
		return nil
	}
	return &Subscriber{
		client:  client,
		state:   state,
		onEvent: onEvent,
	}
}

// Start begins listening. Reconnects automatically on connection loss.
func (s *Subscriber) Start(ctx context.Context) {
	if s == nil || s.client == nil {
		return
	}
	ctx, s.cancel = context.WithCancel(ctx)
	s.wg.Add(1)
	go s.run(ctx)
}

func (s *Subscriber) run(ctx context.Context) {
	defer s.wg.Done()
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := s.listen(ctx); err != nil {
			slog.Warn("redis subscriber disconnected, reconnecting", "err", err, "backoff", backoff)
			s.state.update(func(st *DashboardState) { st.RedisConnected = false })
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
	}
}

func (s *Subscriber) listen(ctx context.Context) error {
	pubsub := s.client.Subscribe(ctx,
		ChannelBundlesNew,
		ChannelPnLUpdate,
		ChannelBreaker,
		ChannelSignerHealth,
	)
	defer pubsub.Close()

	if _, err := pubsub.Receive(ctx); err != nil {
		return err
	}
	s.state.update(func(st *DashboardState) { st.RedisConnected = true })
	slog.Info("redis subscriber connected")

	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-ch:
			if !ok {
				return context.Canceled
			}
			s.route(msg.Channel, msg.Payload)
			if s.onEvent != nil {
				s.onEvent()
			}
		}
	}
}

func (s *Subscriber) route(channel, payload string) {
	switch channel {
	case ChannelBundlesNew:
		var ev BundleEvent
		if json.Unmarshal([]byte(payload), &ev) == nil {
			s.state.update(func(st *DashboardState) {
				st.LastBundleHash = ev.BundleHash
				st.LastBuilder = ev.Builder
				st.LastBundleProfit = ev.Profit
				st.LastBundleGas = ev.Gas
			})
		}
	case ChannelPnLUpdate:
		var ev PnLEvent
		if json.Unmarshal([]byte(payload), &ev) == nil {
			s.state.update(func(st *DashboardState) {
				st.PnLTotal = ev.TotalProfit
				st.WinRate = ev.WinRate
			})
		}
	case ChannelBreaker:
		var ev BreakerEvent
		if json.Unmarshal([]byte(payload), &ev) == nil {
			s.state.update(func(st *DashboardState) {
				st.BreakerOpen = ev.Open
				st.BreakerReason = ev.Reason
			})
		}
	case ChannelSignerHealth:
		var ev SignerHealthEvent
		if json.Unmarshal([]byte(payload), &ev) == nil {
			s.state.update(func(st *DashboardState) {
				st.SignerHealthy = ev.Healthy
			})
		}
	}
}

// Stop shuts down the subscriber.
func (s *Subscriber) Stop() {
	if s == nil {
		return
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	if s.client != nil {
		_ = s.client.Close()
	}
}

// Enabled reports whether the subscriber has an active connection.
func (s *Subscriber) Enabled() bool {
	return s != nil && s.client != nil
}
