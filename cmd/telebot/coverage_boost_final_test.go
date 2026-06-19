package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestHandleCallbackResetBoost(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleCallback(context.Background(), &tgbotapi.CallbackQuery{
		ID:   "cb-reset",
		Data: "reset",
		Message: &tgbotapi.Message{
			MessageID: 5,
			Chat:      &tgbotapi.Chat{ID: 1},
		},
	})
}

func TestHandleCallbackSetMinProfitBoost(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleCallback(context.Background(), &tgbotapi.CallbackQuery{
		ID:   "cb-setmin",
		Data: "setminprofit",
		Message: &tgbotapi.Message{
			MessageID: 5,
			Chat:      &tgbotapi.Chat{ID: 1},
		},
	})
}

func TestHandleCommand_RefreshBoost(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleUpdate(context.Background(), tgbotapi.Update{
		Message: cmdMessage(1, "/refresh"),
	})
}

func TestHandleCommand_UnknownBoost(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleUpdate(context.Background(), tgbotapi.Update{
		Message: cmdMessage(1, "/unknown"),
	})
}

func TestHandleCommand_NotAdminBoost(t *testing.T) {
	srv := startMockExecutor(t)
	defer srv.Close()
	mock := &mockBot{}
	bot := NewTeleBot(mock, srv.URL+"/metrics/json", []int64{1}, time.Second, "")
	bot.handleUpdate(context.Background(), tgbotapi.Update{
		Message: cmdMessage(999, "/pause"),
	})
}

func TestMockBuilderMuxSetupBoost(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"bundleHash":"0xe2e"}`))
	})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("health: expected 200, got %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("default: expected 200, got %d", w.Code)
	}
}
