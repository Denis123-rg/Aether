package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/aether-arb/aether/internal/events"
	"github.com/aether-arb/aether/internal/metrics"
)

type mockBotFailing struct {
	mu       sync.Mutex
	sent     []tgbotapi.Chattable
	updates  chan tgbotapi.Update
	sendErr  error
}

func (m *mockBotFailing) Send(c tgbotapi.Chattable) (tgbotapi.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, c)
	if m.sendErr != nil {
		return tgbotapi.Message{}, m.sendErr
	}
	return tgbotapi.Message{MessageID: len(m.sent)}, nil
}

func (m *mockBotFailing) GetUpdatesChan(tgbotapi.UpdateConfig) tgbotapi.UpdatesChannel {
	if m.updates == nil {
		m.updates = make(chan tgbotapi.Update)
	}
	return m.updates
}

func (m *mockBotFailing) Request(tgbotapi.Chattable) (*tgbotapi.APIResponse, error) {
	return &tgbotapi.APIResponse{Ok: true}, nil
}

func (m *mockBotFailing) lastSentText() string {
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

func (m *mockBotFailing) sentCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

func TestHandleCallbackPools(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleCallback(context.Background(), &tgbotapi.CallbackQuery{
		ID:   "cb-pools",
		Data: "pools",
		Message: &tgbotapi.Message{
			MessageID: 5,
			Chat:      &tgbotapi.Chat{ID: 1},
		},
	})
	if !containsStr(mock.lastSentText(), "0xabc") {
		t.Fatalf("expected pools content, got: %s", mock.lastSentText())
	}
}

func TestHandleCallbackHealth(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleCallback(context.Background(), &tgbotapi.CallbackQuery{
		ID:   "cb-health",
		Data: "health",
		Message: &tgbotapi.Message{
			MessageID: 5,
			Chat:      &tgbotapi.Chat{ID: 1},
		},
	})
	if !containsStr(mock.lastSentText(), "Health") {
		t.Fatalf("expected health content, got: %s", mock.lastSentText())
	}
}

func TestHandleCallbackTrades(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleCallback(context.Background(), &tgbotapi.CallbackQuery{
		ID:   "cb-trades",
		Data: "trades",
		Message: &tgbotapi.Message{
			MessageID: 5,
			Chat:      &tgbotapi.Chat{ID: 1},
		},
	})
	if !containsStr(mock.lastSentText(), "Trades") {
		t.Fatalf("expected trades content, got: %s", mock.lastSentText())
	}
}

func TestHandleCallbackPause(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleCallback(context.Background(), &tgbotapi.CallbackQuery{
		ID:   "cb-pause",
		Data: "pause",
		Message: &tgbotapi.Message{
			MessageID: 5,
			Chat:      &tgbotapi.Chat{ID: 1},
		},
	})
	if !containsStr(mock.lastSentText(), "paused") {
		t.Fatalf("expected paused, got: %s", mock.lastSentText())
	}
}

func TestHandleCallbackResume(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleCallback(context.Background(), &tgbotapi.CallbackQuery{
		ID:   "cb-resume",
		Data: "resume",
		Message: &tgbotapi.Message{
			MessageID: 5,
			Chat:      &tgbotapi.Chat{ID: 1},
		},
	})
	if !containsStr(mock.lastSentText(), "resumed") {
		t.Fatalf("expected resumed, got: %s", mock.lastSentText())
	}
}

func TestHandleCallbackNilMessage(t *testing.T) {
	mock := &mockBot{}
	bot := NewTeleBot(mock, "http://localhost/metrics/json", []int64{1}, time.Second, "")
	bot.handleCallback(context.Background(), &tgbotapi.CallbackQuery{
		ID:      "cb-nil",
		Data:    "refresh",
		Message: nil,
	})
	if len(mock.sent) != 0 {
		t.Fatal("nil message callback should be ignored")
	}
}

func TestHandleCallbackUnknownData(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleCallback(context.Background(), &tgbotapi.CallbackQuery{
		ID:   "cb-unknown",
		Data: "unknown_action",
		Message: &tgbotapi.Message{
			MessageID: 5,
			Chat:      &tgbotapi.Chat{ID: 1},
		},
	})
}

func TestSendHealthExecutorUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.sendHealth(context.Background(), 1)
	if !containsStr(mock.lastSentText(), "unreachable") {
		t.Fatalf("expected unreachable, got: %s", mock.lastSentText())
	}
}

func TestSendTradesExecutorUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.sendTrades(context.Background(), 1)
	if !containsStr(mock.lastSentText(), "unreachable") {
		t.Fatalf("expected unreachable, got: %s", mock.lastSentText())
	}
}

func TestHandlePauseAdminError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/pause" {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte("409 Conflict: already paused"))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handlePause(context.Background(), 1)
	if !containsStr(mock.lastSentText(), "failed") && !containsStr(mock.lastSentText(), "Cannot") {
		t.Fatalf("expected error message, got: %s", mock.lastSentText())
	}
}

func TestHandleResumeAdminError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/resume" {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte("409 Conflict: cannot resume from Halted"))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleResume(context.Background(), 1)
	if !containsStr(mock.lastSentText(), "failed") && !containsStr(mock.lastSentText(), "Cannot") && !containsStr(mock.lastSentText(), "halted") {
		t.Fatalf("expected error message, got: %s", mock.lastSentText())
	}
}

func TestHandleResetConfirmAdminError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/reset" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("internal error"))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.mu.Lock()
	bot.resetPending[1] = true
	bot.mu.Unlock()
	bot.handleResetConfirm(context.Background(), 1)
	if !containsStr(mock.lastSentText(), "failed") {
		t.Fatalf("expected error, got: %s", mock.lastSentText())
	}
}

func TestHandleSetMinProfitAdminError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "set_min_profit") {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("cannot set"))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleSetMinProfit(context.Background(), 1, "0.001")
	if !containsStr(mock.lastSentText(), "Failed") {
		t.Fatalf("expected Failed, got: %s", mock.lastSentText())
	}
}

func TestFormatAdminErrorDefault(t *testing.T) {
	msg := formatAdminError("Pause", errors.New("network timeout"))
	if !strings.Contains(msg, "Pause failed") || !strings.Contains(msg, "network timeout") {
		t.Fatalf("unexpected msg: %s", msg)
	}
}

func TestFormatAdminErrorConflictAlready(t *testing.T) {
	msg := formatAdminError("Pause", errors.New("409 already paused"))
	if !strings.Contains(msg, "transition not allowed") {
		t.Fatalf("expected transition message, got: %s", msg)
	}
}

func TestFormatAdminErrorConflictGeneric(t *testing.T) {
	msg := formatAdminError("Resume", errors.New("409 some conflict"))
	if !strings.Contains(msg, "Cannot resume") || !strings.Contains(msg, "some conflict") {
		t.Fatalf("unexpected msg: %s", msg)
	}
}

func TestReplySendError(t *testing.T) {
	mock := &mockBotFailing{sendErr: errors.New("network error")}
	bot := NewTeleBot(mock, "http://localhost/metrics/json", []int64{1}, time.Second, "")
	bot.reply(1, "test message")
}

func TestSendDashboardSendError(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBotFailing{sendErr: errors.New("send failed")}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.sendDashboard(context.Background(), 1)
}

func TestSendDashboardExecutorUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.sendDashboard(context.Background(), 1)
	text := mock.lastSentText()
	if !containsStr(text, "Dashboard") {
		t.Fatalf("expected dashboard, got: %s", text)
	}
}

func TestUpdateDashboardSendError(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBotFailing{sendErr: errors.New("edit failed")}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.updateDashboard(context.Background(), 1, 42)
}

func TestUpdateDashboardExecutorUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.updateDashboard(context.Background(), 1, 42)
}

func TestHandleUpdateCallbackQueryPath(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleUpdate(context.Background(), tgbotapi.Update{
		Message: nil,
		CallbackQuery: &tgbotapi.CallbackQuery{
			ID:   "cb-inline",
			Data: "refresh",
			Message: &tgbotapi.Message{
				MessageID: 7,
				Chat:      &tgbotapi.Chat{ID: 1},
			},
		},
	})
}

func TestHandleUpdateNonCommandNoCallback(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleUpdate(context.Background(), tgbotapi.Update{
		Message: &tgbotapi.Message{
			Chat: &tgbotapi.Chat{ID: 1},
			Text: "hello world",
		},
	})
}

func TestFetchSnapshotNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	}))
	defer srv.Close()
	client := NewMetricsClient(srv.URL)
	_, err := client.FetchSnapshot(context.Background())
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected 404 in error, got: %v", err)
	}
}

func TestFetchSnapshotInvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json at all {{{"))
	}))
	defer srv.Close()
	client := NewMetricsClient(srv.URL)
	_, err := client.FetchSnapshot(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Fatalf("expected decode error, got: %v", err)
	}
}

func TestFetchSnapshotContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewMetricsClient("http://localhost:1/metrics/json")
	_, err := client.FetchSnapshot(ctx)
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
}

func TestFetchSnapshotConnectionRefused(t *testing.T) {
	client := NewMetricsClient("http://localhost:1/metrics/json")
	_, err := client.FetchSnapshot(context.Background())
	if err == nil {
		t.Fatal("expected error for unreachable executor")
	}
	if !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("expected unreachable, got: %v", err)
	}
}

func TestPostNon200Status(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("service unavailable"))
	}))
	defer srv.Close()
	client := NewAdminClient(srv.URL + "/metrics/json")
	err := client.Pause(context.Background())
	if err == nil {
		t.Fatal("expected error for 503")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Fatalf("expected 503, got: %v", err)
	}
}

func TestPostConnectionRefused(t *testing.T) {
	client := NewAdminClient("http://localhost:1/metrics/json")
	err := client.Pause(context.Background())
	if err == nil {
		t.Fatal("expected error for connection refused")
	}
}

func TestPostContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewAdminClient("http://localhost:1/metrics/json")
	err := client.Pause(ctx)
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
}

func TestNewTeleBotWithRedis(t *testing.T) {
	mock := &mockBot{}
	state := &events.DashboardState{}
	sub := events.NewSubscriber("", state, func() {})
	bot := NewTeleBot(mock, "http://localhost/metrics/json", []int64{1}, time.Second, "")
	if bot == nil {
		t.Fatal("bot should not be nil")
	}
	_ = sub
}

func TestRunWithRefreshCh(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{updates: make(chan tgbotapi.Update)}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Hour, "")
	bot.mu.Lock()
	bot.dashboardMsgID[1] = 99
	bot.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		bot.Run(ctx)
		close(done)
	}()

	bot.refreshCh <- struct{}{}
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit")
	}
}

func TestHandleCallbackNilMessageWithAdminID(t *testing.T) {
	mock := &mockBot{}
	bot := NewTeleBot(mock, "http://localhost/metrics/json", []int64{1}, time.Second, "")
	bot.handleCallback(context.Background(), &tgbotapi.CallbackQuery{
		ID:      "cb-nm2",
		Data:    "pools",
		Message: nil,
	})
	if len(mock.sent) != 0 {
		t.Fatal("nil message should be ignored")
	}
}

func TestRefreshAllDashboards(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.mu.Lock()
	bot.dashboardMsgID[1] = 42
	bot.mu.Unlock()
	bot.refreshAllDashboards(context.Background())
}

func TestRefreshAllDashboardsNoDashboard(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.refreshAllDashboards(context.Background())
	if len(mock.sent) != 0 {
		t.Fatal("no dashboard should mean no updates")
	}
}

func TestSendPoolsExecutorUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.sendPools(context.Background(), 1)
	if !containsStr(mock.lastSentText(), "unreachable") {
		t.Fatalf("expected unreachable, got: %s", mock.lastSentText())
	}
}

func TestAdminClientReset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin/reset":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client := NewAdminClient(srv.URL + "/metrics/json")
	if err := client.Reset(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAdminClientSetMinProfit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/set_min_profit" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	client := NewAdminClient(srv.URL + "/metrics/json")
	if err := client.SetMinProfit(context.Background(), 0.01); err != nil {
		t.Fatal(err)
	}
}

func TestHandleSetMinProfitEmptyArgs(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleSetMinProfit(context.Background(), 1, "")
	if !containsStr(mock.lastSentText(), "Usage") {
		t.Fatalf("expected Usage, got: %s", mock.lastSentText())
	}
}

func TestHandleSetMinProfitZeroValue(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleSetMinProfit(context.Background(), 1, "0")
	if !containsStr(mock.lastSentText(), "Usage") {
		t.Fatalf("expected Usage, got: %s", mock.lastSentText())
	}
}

func TestHandleSetMinProfitNonNumeric(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleSetMinProfit(context.Background(), 1, "abc")
	if !containsStr(mock.lastSentText(), "Usage") {
		t.Fatalf("expected Usage, got: %s", mock.lastSentText())
	}
}

func TestFriendlyConflictAlready(t *testing.T) {
	result := friendlyConflict("already paused")
	if result != "state transition not allowed in current state" {
		t.Fatalf("unexpected: %s", result)
	}
}

func TestFriendlyConflictNoMatch(t *testing.T) {
	result := friendlyConflict("random error message")
	if result != "random error message" {
		t.Fatalf("expected original message, got: %s", result)
	}
}

func TestDashboardKeyboard(t *testing.T) {
	kbd := dashboardKeyboard()
	if len(kbd.InlineKeyboard) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(kbd.InlineKeyboard))
	}
}

func TestIsAdmin(t *testing.T) {
	bot := &TeleBot{
		adminChatIDs: map[int64]struct{}{1: {}, 2: {}},
	}
	if !bot.isAdmin(1) {
		t.Fatal("expected admin")
	}
	if !bot.isAdmin(2) {
		t.Fatal("expected admin")
	}
	if bot.isAdmin(999) {
		t.Fatal("should not be admin")
	}
}

func TestRunContextCancelImmediate(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{updates: make(chan tgbotapi.Update)}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Hour, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		bot.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run should exit immediately with canceled context")
	}
}

func TestSendDashboardRedisActive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.redisActive = true
	bot.redisState = &events.DashboardState{DashboardData: events.DashboardData{PnLTotal: 42.0, WinRate: 99.0}}
	bot.sendDashboard(context.Background(), 1)
	text := mock.lastSentText()
	if !containsStr(text, "Dashboard") {
		t.Fatalf("expected Dashboard, got: %s", text)
	}
}

func TestUpdateDashboardRedisActive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.redisActive = true
	bot.redisState = &events.DashboardState{DashboardData: events.DashboardData{PnLTotal: 42.0}}
	bot.updateDashboard(context.Background(), 1, 10)
}

func TestHandleCallbackWithNilCallbackMessage(t *testing.T) {
	mock := &mockBot{}
	bot := NewTeleBot(mock, "http://localhost/metrics/json", []int64{1}, time.Second, "")
	bot.handleCallback(context.Background(), &tgbotapi.CallbackQuery{
		ID:      "cb-nil-msg",
		Data:    "health",
		Message: nil,
	})
	if len(mock.sent) != 0 {
		t.Fatal("nil message should be ignored")
	}
}

func TestMetricsClientFetchNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, "page not found")
	}))
	defer srv.Close()
	client := NewMetricsClient(srv.URL)
	_, err := client.FetchSnapshot(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestMetricsClientFetchInvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "this is not json")
	}))
	defer srv.Close()
	client := NewMetricsClient(srv.URL)
	_, err := client.FetchSnapshot(context.Background())
	if err == nil {
		t.Fatal("expected decode error")
	}
}

func TestFetchSnapshotValidResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(metrics.Snapshot{
			PnLToday: 0.5, WinRate: 60.0, SystemState: "Running",
			SignerHealthy: true, RPCHealthy: true,
			ExecutorReachable: true,
		})
	}))
	defer srv.Close()
	client := NewMetricsClient(srv.URL)
	snap, err := client.FetchSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snap.ExecutorReachable {
		t.Fatal("expected reachable")
	}
}

func TestPostWithBearerToken(t *testing.T) {
	t.Setenv("AETHER_ADMIN_TOKEN", "test-token-123")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-token-123" {
			t.Fatalf("expected bearer token, got: %s", auth)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	client := NewAdminClient(srv.URL + "/metrics/json")
	if err := client.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPostWithoutBearerToken(t *testing.T) {
	t.Setenv("AETHER_ADMIN_TOKEN", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "" {
			t.Fatalf("expected no auth, got: %s", auth)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	client := NewAdminClient(srv.URL + "/metrics/json")
	if err := client.Pause(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHandleCallbackRefreshWithDashboard(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleCallback(context.Background(), &tgbotapi.CallbackQuery{
		ID:   "cb-refresh2",
		Data: "refresh",
		Message: &tgbotapi.Message{
			MessageID: 99,
			Chat:      &tgbotapi.Chat{ID: 1},
		},
	})
	text := mock.lastSentText()
	if text == "" {
		t.Fatal("expected some response")
	}
}

func TestFormatDashboardWithMultiplePools(t *testing.T) {
	pools := make([]metrics.TopPool, 10)
	for i := range pools {
		pools[i] = metrics.TopPool{
			Address: fmt.Sprintf("0x%040d", i),
			Score:   float64(i) * 0.1,
			Protocol: "uniswap_v2",
		}
	}
	snap := metrics.Snapshot{
		TopPools:     pools,
		SystemState:  "Running",
		SignerHealthy: true,
		RPCHealthy:    true,
	}
	text := FormatDashboard(snap, events.DashboardData{}, false)
	if !strings.Contains(text, "Dashboard") {
		t.Fatal("expected dashboard")
	}
}

func TestFormatDashboardShortPoolAddress(t *testing.T) {
	snap := metrics.Snapshot{
		TopPools: []metrics.TopPool{
			{Address: "0xabc", Score: 0.5, Protocol: "v2"},
		},
		SystemState:  "Running",
		SignerHealthy: true,
	}
	text := FormatDashboard(snap, events.DashboardData{}, false)
	if !strings.Contains(text, "0xabc") {
		t.Fatal("expected short address")
	}
}

func TestFormatDashboardNoPools(t *testing.T) {
	snap := metrics.Snapshot{
		TopPools:     nil,
		SystemState:  "Running",
		SignerHealthy: true,
	}
	text := FormatDashboard(snap, events.DashboardData{}, false)
	if !strings.Contains(text, "No pool data") {
		t.Fatal("expected No pool data")
	}
}

func TestFormatDashboardRedisOverlayWithBreaker(t *testing.T) {
	snap := metrics.Snapshot{BreakerOpen: false}
	redis := events.DashboardData{BreakerOpen: true, BreakerReason: "gas_high"}
	text := FormatDashboard(snap, redis, true)
	if !strings.Contains(text, "🔴") {
		t.Fatal("expected breaker open from redis")
	}
}

func TestFormatDashboardRedisOverlaySignerHealthy(t *testing.T) {
	snap := metrics.Snapshot{SignerHealthy: false}
	redis := events.DashboardData{SignerHealthy: true, PnLTotal: 5.0, LastBuilder: "titan"}
	text := FormatDashboard(snap, redis, true)
	if !strings.Contains(text, "5.000000") {
		t.Fatal("expected redis PnLTotal")
	}
}

func TestFormatDashboardRedisNotActive(t *testing.T) {
	snap := metrics.Snapshot{
		SystemState:  "Running",
		SignerHealthy: true,
		RPCHealthy:    true,
		RedisHealthy:  true,
	}
	text := FormatDashboard(snap, events.DashboardData{}, false)
	if !strings.Contains(text, "polling fallback") {
		t.Fatal("expected polling fallback message")
	}
}

func TestFormatHealthAllHealthy(t *testing.T) {
	snap := metrics.Snapshot{
		SignerHealthy:      true,
		RPCHealthy:         true,
		DiscoveryHealthy:   true,
		TimescaleHealthy:   true,
		RedisHealthy:       true,
		SystemState:        "Running",
		BreakerOpen:        false,
	}
	text := FormatHealth(snap)
	if !strings.Contains(text, "healthy") {
		t.Fatal("expected healthy")
	}
	if strings.Count(text, "unhealthy") > 0 {
		t.Fatal("expected no unhealthy")
	}
}

func TestFormatHealthAllUnhealthy(t *testing.T) {
	snap := metrics.Snapshot{
		SignerHealthy:      false,
		RPCHealthy:         false,
		DiscoveryHealthy:   false,
		TimescaleHealthy:   false,
		RedisHealthy:       false,
		SystemState:        "Halted",
		BreakerOpen:        true,
	}
	text := FormatHealth(snap)
	if strings.Count(text, "unhealthy") < 5 {
		t.Fatal("expected all unhealthy")
	}
	if !strings.Contains(text, "Halted") {
		t.Fatal("expected Halted")
	}
}

func TestFormatTradesMultiple(t *testing.T) {
	now := time.Now()
	trades := []metrics.TradeRecord{
		{Timestamp: now, ProfitETH: 0.01, GasETH: 0.001, Builder: "eden"},
		{Timestamp: now.Add(-time.Minute), ProfitETH: 0.02, GasETH: 0.002, Builder: "flashbots"},
	}
	text := FormatTrades(trades)
	if !strings.Contains(text, "eden") || !strings.Contains(text, "flashbots") {
		t.Fatalf("expected builders, got: %s", text)
	}
}

func TestFormatPoolsEmptyNil(t *testing.T) {
	text := FormatPools([]metrics.TopPool{})
	if !strings.Contains(text, "No pools") {
		t.Fatal("expected No pools")
	}
}

func TestMetricsClientFetchConnectionRefused(t *testing.T) {
	client := NewMetricsClient("http://localhost:1/metrics/json")
	_, err := client.FetchSnapshot(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("expected unreachable, got: %v", err)
	}
}

func TestPostNewRequestError(t *testing.T) {
	client := &AdminClient{
		baseHost:   "://invalid-url",
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
	err := client.post(context.Background(), "/admin/pause")
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

func TestMetricsClientFetchNewRequestError(t *testing.T) {
	client := &MetricsClient{
		baseURL:    "://invalid",
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
	_, err := client.FetchSnapshot(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

func TestStartCommand(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleUpdate(context.Background(), tgbotapi.Update{
		Message: cmdMessage(1, "/start"),
	})
	if !containsStr(mock.lastSentText(), "TeleBot Commands") {
		t.Fatalf("expected help text for /start, got: %s", mock.lastSentText())
	}
}

func TestDashboardCommand(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleUpdate(context.Background(), tgbotapi.Update{
		Message: cmdMessage(1, "/dashboard"),
	})
	if !containsStr(mock.lastSentText(), "Dashboard") {
		t.Fatalf("expected dashboard, got: %s", mock.lastSentText())
	}
}

func TestPost409ConflictHalted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte("409 Conflict: cannot resume from Halted"))
	}))
	defer srv.Close()
	client := NewAdminClient(srv.URL + "/metrics/json")
	err := client.Resume(context.Background())
	if err == nil {
		t.Fatal("expected error for 409")
	}
}

func TestFormatDashboardRedisOverlayWinRateZero(t *testing.T) {
	snap := metrics.Snapshot{WinRate: 75.0}
	redis := events.DashboardData{WinRate: 0}
	text := FormatDashboard(snap, redis, true)
	if !strings.Contains(text, "75.0") {
		t.Fatal("expected snap win rate when redis is 0")
	}
}

func TestFormatDashboardRedisOverlayLastBuilderEmpty(t *testing.T) {
	snap := metrics.Snapshot{LastBuilder: "flashbots", LastBundleProfit: 0.01, LastBundleGas: 0.001}
	redis := events.DashboardData{LastBuilder: ""}
	text := FormatDashboard(snap, redis, true)
	if !strings.Contains(text, "flashbots") {
		t.Fatal("expected snap builder when redis is empty")
	}
}

func TestFormatDashboardRedisOverlayAllFields(t *testing.T) {
	snap := metrics.Snapshot{
		PnLToday: 1.0, PnLTotal: 2.0, WinRate: 50.0,
		LastBundleProfit: 0.1, LastBundleGas: 0.01, LastBuilder: "builder_a",
	}
	redis := events.DashboardData{
		PnLTotal: 99.0, WinRate: 90.0,
		LastBundleProfit: 0.99, LastBundleGas: 0.09, LastBuilder: "builder_b",
		SignerHealthy: true,
		BreakerOpen: true, BreakerReason: "high_gas",
	}
	text := FormatDashboard(snap, redis, true)
	for _, want := range []string{"99.000000", "90.0", "0.990000", "0.090000", "builder_b"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}

func TestPostNewRequestFails(t *testing.T) {
	client := &AdminClient{
		baseHost:   "http://[::1]:namedport", // invalid
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
	err := client.post(context.Background(), "/admin/pause")
	if err == nil {
		t.Fatal("expected error")
	}
}

func mockAPIFactory() func(string) (BotAPI, error) {
	return func(_ string) (BotAPI, error) {
		return &tgbotapi.BotAPI{Self: tgbotapi.User{UserName: "test-bot"}}, nil
	}
}

func mockAPIFactoryErr() func(string) (BotAPI, error) {
	return func(_ string) (BotAPI, error) {
		return nil, fmt.Errorf("telegram init failed")
	}
}

func writeTestConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "production.toml")
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AETHER_PRODUCTION_CONFIG", cfgPath)
	t.Setenv("AETHER_CONFIG_DIR", dir)
	return cfgPath
}

func TestStartValidConfigFromToml(t *testing.T) {
	writeTestConfig(t, `
[telegram]
bot_token = "test-123:ABC"
admin_chat_ids = [100, 200]
dashboard_update_interval_secs = 5
executor_metrics_url = "http://localhost:9090/metrics/json"
[redis]
url = ""
`)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- start(ctx, mockAPIFactory())
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
}

func TestStartTokenFromEnv(t *testing.T) {
	writeTestConfig(t, `
[telegram]
bot_token = ""
admin_chat_ids = [100]
dashboard_update_interval_secs = 3
executor_metrics_url = "http://localhost:8080/metrics/json"
[redis]
url = ""
`)
	t.Setenv("TELEGRAM_BOT_TOKEN", "env-token-456")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- start(ctx, mockAPIFactory())
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
}

func TestStartMissingToken(t *testing.T) {
	writeTestConfig(t, `
[telegram]
bot_token = ""
admin_chat_ids = [100]
dashboard_update_interval_secs = 3
executor_metrics_url = "http://localhost:8080/metrics/json"
[redis]
url = ""
`)
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	err := start(context.Background(), mockAPIFactory())
	if err == nil || !strings.Contains(err.Error(), "production config") {
		t.Fatalf("expected config error, got: %v", err)
	}
}

func TestStartAdminIDsFromEnv(t *testing.T) {
	writeTestConfig(t, `
[telegram]
bot_token = "test-token"
admin_chat_ids = []
dashboard_update_interval_secs = 3
executor_metrics_url = "http://localhost:8080/metrics/json"
[redis]
url = ""
`)
	t.Setenv("TELEGRAM_ADMIN_CHAT_IDS", "300,400")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- start(ctx, mockAPIFactory())
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
}

func TestStartInvalidAdminIDs(t *testing.T) {
	writeTestConfig(t, `
[telegram]
bot_token = "test-token"
admin_chat_ids = [100]
dashboard_update_interval_secs = 3
executor_metrics_url = "http://localhost:8080/metrics/json"
[redis]
url = ""
`)
	t.Setenv("TELEGRAM_ADMIN_CHAT_IDS", "not-a-number")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- start(ctx, mockAPIFactory())
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
}

func TestStartNoAdminIDs(t *testing.T) {
	writeTestConfig(t, `
[telegram]
bot_token = "test-token"
admin_chat_ids = [100]
dashboard_update_interval_secs = 3
executor_metrics_url = "http://localhost:8080/metrics/json"
[redis]
url = ""
`)
	t.Setenv("TELEGRAM_ADMIN_CHAT_IDS", "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- start(ctx, mockAPIFactory())
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
}

func TestStartDefaultMetricsURL(t *testing.T) {
	writeTestConfig(t, `
[telegram]
bot_token = "test-token"
admin_chat_ids = [100]
dashboard_update_interval_secs = 3
executor_metrics_url = ""
[redis]
url = ""
`)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- start(ctx, mockAPIFactory())
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
}

func TestStartRedisURLOverride(t *testing.T) {
	writeTestConfig(t, `
[telegram]
bot_token = "test-token"
admin_chat_ids = [100]
dashboard_update_interval_secs = 3
executor_metrics_url = "http://localhost:8080/metrics/json"
[redis]
url = ""
`)
	t.Setenv("REDIS_URL", "redis://localhost:6379")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- start(ctx, mockAPIFactory())
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
}

func TestStartRedisEmptyNonProduction(t *testing.T) {
	writeTestConfig(t, `
[telegram]
bot_token = "test-token"
admin_chat_ids = [100]
dashboard_update_interval_secs = 3
executor_metrics_url = "http://localhost:8080/metrics/json"
[redis]
url = ""
`)
	t.Setenv("REDIS_URL", "")
	t.Setenv("AETHER_ENV", "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- start(ctx, mockAPIFactory())
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
}

func TestStartBotAPIFactoryError(t *testing.T) {
	writeTestConfig(t, `
[telegram]
bot_token = "test-token"
admin_chat_ids = [100]
dashboard_update_interval_secs = 3
executor_metrics_url = "http://localhost:8080/metrics/json"
[redis]
url = ""
`)
	err := start(context.Background(), mockAPIFactoryErr())
	if err == nil || !strings.Contains(err.Error(), "telegram bot init failed") {
		t.Fatalf("expected bot API error, got: %v", err)
	}
}

func TestStartInvalidConfigPath(t *testing.T) {
	t.Setenv("AETHER_PRODUCTION_CONFIG", "/nonexistent/path/config.toml")
	err := start(context.Background(), mockAPIFactory())
	if err == nil {
		t.Fatal("expected error for invalid config path")
	}
}

func TestStartWithDebugMode(t *testing.T) {
	writeTestConfig(t, `
[telegram]
bot_token = "test-token"
admin_chat_ids = [100]
dashboard_update_interval_secs = 3
executor_metrics_url = "http://localhost:8080/metrics/json"
[redis]
url = ""
`)
	t.Setenv("TELEBOT_DEBUG", "1")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- start(ctx, mockAPIFactory())
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
}

func TestRunWithRedisSubscriber(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{updates: make(chan tgbotapi.Update)}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Hour, "")

	bot.redisSub = nil
	bot.redisActive = false

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		bot.Run(ctx)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit")
	}
}

func TestRunWithUpdateFromChannel(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{updates: make(chan tgbotapi.Update, 1)}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Hour, "")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		bot.Run(ctx)
		close(done)
	}()

	mock.updates <- tgbotapi.Update{
		Message: &tgbotapi.Message{
			Chat: &tgbotapi.Chat{ID: 1},
			Text: "/dashboard",
			Entities: []tgbotapi.MessageEntity{{
				Type: "bot_command", Offset: 0, Length: 10,
			}},
		},
	}
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit")
	}
}

func TestStartWithRedisURLFromConfig(t *testing.T) {
	writeTestConfig(t, `
[telegram]
bot_token = "test-token"
admin_chat_ids = [100]
dashboard_update_interval_secs = 3
executor_metrics_url = "http://localhost:8080/metrics/json"
[redis]
url = "redis://localhost:6379"
`)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- start(ctx, mockAPIFactory())
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
}

func TestStartDebugOff(t *testing.T) {
	writeTestConfig(t, `
[telegram]
bot_token = "test-token"
admin_chat_ids = [100]
dashboard_update_interval_secs = 3
executor_metrics_url = "http://localhost:8080/metrics/json"
[redis]
url = ""
`)
	t.Setenv("TELEBOT_DEBUG", "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- start(ctx, mockAPIFactory())
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
}

func TestStartWithEmptyEnvAdminIDs(t *testing.T) {
	writeTestConfig(t, `
[telegram]
bot_token = "test-token"
admin_chat_ids = [100]
dashboard_update_interval_secs = 3
executor_metrics_url = "http://localhost:8080/metrics/json"
[redis]
url = ""
`)
	t.Setenv("TELEGRAM_ADMIN_CHAT_IDS", "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- start(ctx, mockAPIFactory())
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
}
