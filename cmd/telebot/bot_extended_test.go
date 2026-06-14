package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestTeleBot_ResetFlow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin/reset":
			w.WriteHeader(http.StatusOK)
		case "/metrics/json":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"pnl_today":0.1,"executor_reachable":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{42}, time.Second, "")
	bot.handleUpdate(context.Background(), tgbotapi.Update{Message: cmdMessage(42, "/reset")})
	if !containsStr(mock.lastSentText(), "reset_confirm") {
		t.Fatalf("reset prompt: %s", mock.lastSentText())
	}
	bot.handleUpdate(context.Background(), tgbotapi.Update{Message: cmdMessage(42, "/reset_confirm")})
	if !containsStr(mock.lastSentText(), "reset") {
		t.Fatalf("reset confirm: %s", mock.lastSentText())
	}
}

func TestTeleBot_ResetConfirmWithoutPending(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleUpdate(context.Background(), tgbotapi.Update{Message: cmdMessage(1, "/reset_confirm")})
	if !containsStr(mock.lastSentText(), "No pending reset") {
		t.Fatalf("got %s", mock.lastSentText())
	}
}

func TestTeleBot_UnknownCommand(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleUpdate(context.Background(), tgbotapi.Update{Message: cmdMessage(1, "/foobar")})
	if !containsStr(mock.lastSentText(), "Unknown command") {
		t.Fatalf("got %s", mock.lastSentText())
	}
}

func TestTeleBot_HelpCommand(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleUpdate(context.Background(), tgbotapi.Update{Message: cmdMessage(1, "/help")})
	if !containsStr(mock.lastSentText(), "TeleBot Commands") {
		t.Fatalf("got %s", mock.lastSentText())
	}
}

func TestTeleBot_CallbackRefresh(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleCallback(context.Background(), &tgbotapi.CallbackQuery{
		ID:   "cb1",
		Data: "refresh",
		Message: &tgbotapi.Message{
			MessageID: 7,
			Chat:      &tgbotapi.Chat{ID: 1},
		},
	})
}

func TestTeleBot_CallbackNonAdminIgnored(t *testing.T) {
	mock := &mockBot{}
	bot := NewTeleBot(mock, "http://localhost/metrics/json", []int64{1}, time.Second, "")
	bot.handleCallback(context.Background(), &tgbotapi.CallbackQuery{
		ID:   "cb2",
		Data: "pause",
		Message: &tgbotapi.Message{
			Chat: &tgbotapi.Chat{ID: 999},
		},
	})
	if len(mock.sent) != 0 {
		t.Fatal("non-admin callback ignored")
	}
}

func TestTeleBot_SetMinProfitInvalid(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleUpdate(context.Background(), tgbotapi.Update{Message: cmdMessage(1, "/set_min_profit -1")})
	if !containsStr(mock.lastSentText(), "Usage") {
		t.Fatalf("got %s", mock.lastSentText())
	}
}

func TestTeleBot_ExecutorUnreachablePools(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleUpdate(context.Background(), tgbotapi.Update{Message: cmdMessage(1, "/pools")})
	if !containsStr(mock.lastSentText(), "unreachable") {
		t.Fatalf("got %s", mock.lastSentText())
	}
}

func TestTeleBot_RunContextCancel(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{updates: make(chan tgbotapi.Update)}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, 20*time.Millisecond, "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		bot.Run(ctx)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit")
	}
}

func TestTeleBot_TriggerRefreshCoalesces(t *testing.T) {
	bot := &TeleBot{refreshCh: make(chan struct{}, 1)}
	bot.triggerRefresh()
	bot.triggerRefresh()
	if len(bot.refreshCh) != 1 {
		t.Fatal("refresh should coalesce")
	}
}

func TestNewTeleBot_DefaultPollInterval(t *testing.T) {
	mock := &mockBot{}
	bot := NewTeleBot(mock, "http://localhost/m", []int64{1}, 0, "")
	if bot.pollInterval != 3*time.Second {
		t.Fatalf("interval %v", bot.pollInterval)
	}
}
