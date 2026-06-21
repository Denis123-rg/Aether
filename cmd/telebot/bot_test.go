package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/aether-arb/aether/internal/metrics"
)

type mockBot struct {
	mu       sync.Mutex
	sent     []tgbotapi.Chattable
	updates  chan tgbotapi.Update
}

func (m *mockBot) Send(c tgbotapi.Chattable) (tgbotapi.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, c)
	switch v := c.(type) {
	case tgbotapi.MessageConfig:
		return tgbotapi.Message{MessageID: len(m.sent), Chat: &tgbotapi.Chat{ID: v.ChatID}}, nil
	case tgbotapi.EditMessageTextConfig:
		return tgbotapi.Message{MessageID: v.MessageID, Chat: &tgbotapi.Chat{ID: v.ChatID}}, nil
	}
	return tgbotapi.Message{MessageID: 1}, nil
}

func (m *mockBot) GetUpdatesChan(tgbotapi.UpdateConfig) tgbotapi.UpdatesChannel {
	if m.updates == nil {
		m.updates = make(chan tgbotapi.Update)
	}
	return m.updates
}

func (m *mockBot) Request(tgbotapi.Chattable) (*tgbotapi.APIResponse, error) {
	return &tgbotapi.APIResponse{Ok: true}, nil
}

func (m *mockBot) lastSentText() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) == 0 {
		return ""
	}
	switch v := m.sent[len(m.sent)-1].(type) {
	case tgbotapi.MessageConfig:
		return v.Text
	case tgbotapi.EditMessageTextConfig:
		return v.Text
	}
	return ""
}

func startMockExecutor(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/metrics/json":
			_ = json.NewEncoder(w).Encode(metrics.Snapshot{
				PnLToday: 0.1, PnLTotal: 1.0, WinRate: 70.0,
				LastBuilder: "flashbots", SignerHealthy: true, RPCHealthy: true,
				SystemState: "Running", MinProfitETH: 0.001,
				TopPools: []metrics.TopPool{{Address: "0xabc", Score: 0.9}},
				ExecutorReachable: true,
			})
		case "/admin/pause", "/admin/resume":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/admin/set_min_profit":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestTeleBotDashboardCommand(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()

	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{123}, time.Second, "")
	bot.handleUpdate(context.Background(), tgbotapi.Update{
		Message: &tgbotapi.Message{
			Chat: &tgbotapi.Chat{ID: 123},
			Text: "/dashboard",
			Entities: []tgbotapi.MessageEntity{{
				Type: "bot_command", Offset: 0, Length: 10,
			}},
		},
	})
	text := mock.lastSentText()
	if text == "" || !containsStr(text, "Dashboard") {
		t.Fatalf("expected dashboard text, got: %s", text)
	}
}

func TestTeleBotPauseResume(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")

	bot.handleUpdate(context.Background(), tgbotapi.Update{
		Message: cmdMessage(1, "/pause"),
	})
	if !containsStr(mock.lastSentText(), "paused") {
		t.Fatalf("pause: %s", mock.lastSentText())
	}

	bot.handleUpdate(context.Background(), tgbotapi.Update{
		Message: cmdMessage(1, "/resume"),
	})
	if !containsStr(mock.lastSentText(), "resumed") {
		t.Fatalf("resume: %s", mock.lastSentText())
	}
}

func TestTeleBotSetMinProfit(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")

	bot.handleUpdate(context.Background(), tgbotapi.Update{
		Message: cmdMessage(1, "/set_min_profit 0.005"),
	})
	if !containsStr(mock.lastSentText(), "0.005000") {
		t.Fatalf("set min: %s", mock.lastSentText())
	}
}

func TestTeleBotHealthCommand(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleUpdate(context.Background(), tgbotapi.Update{
		Message: cmdMessage(1, "/health"),
	})
	if !containsStr(mock.lastSentText(), "Health") {
		t.Fatalf("health: %s", mock.lastSentText())
	}
}

func TestTeleBotTradesCommand(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleUpdate(context.Background(), tgbotapi.Update{
		Message: cmdMessage(1, "/trades"),
	})
	if !containsStr(mock.lastSentText(), "Trades") {
		t.Fatalf("trades: %s", mock.lastSentText())
	}
}

func TestTeleBotPoolsCommand(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleUpdate(context.Background(), tgbotapi.Update{
		Message: cmdMessage(1, "/pools"),
	})
	if !containsStr(mock.lastSentText(), "0xabc") {
		t.Fatalf("pools: %s", mock.lastSentText())
	}
}

func TestTeleBotNonAdminIgnored(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleUpdate(context.Background(), tgbotapi.Update{
		Message: cmdMessage(999, "/dashboard"),
	})
	if len(mock.sent) != 0 {
		t.Fatal("non-admin should be ignored")
	}
}

func TestTeleBotPollLoopRefresh(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, 50*time.Millisecond, "")
	bot.mu.Lock()
	bot.dashboardMsgID[1] = 42
	bot.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	go bot.pollLoop(ctx)
	time.Sleep(120 * time.Millisecond)
	cancel()
}

func TestMetricsClientFetch(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	client := NewMetricsClient(srv.URL + "/metrics/json")
	snap, err := client.FetchSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.WinRate != 70.0 {
		t.Fatalf("winrate: %f", snap.WinRate)
	}
}

func TestAdminClientPause(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	admin := NewAdminClient(srv.URL + "/metrics/json")
	if err := admin.Pause(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := admin.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func cmdMessage(chatID int64, text string) *tgbotapi.Message {
	cmd := text
	if i := indexStr(text, " "); i >= 0 {
		cmd = text[:i]
	}
	return &tgbotapi.Message{
		Chat: &tgbotapi.Chat{ID: chatID},
		Text: text,
		Entities: []tgbotapi.MessageEntity{{
			Type: "bot_command", Offset: 0, Length: len([]rune(cmd)),
		}},
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexStr(s, sub) >= 0)
}

func indexStr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
