package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebhookDispatcher_DispatchesJSON(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost {
			t.Errorf("method %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type %s", ct)
		}
		body, _ := io.ReadAll(r.Body)
		var payload webhookPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Title != "test-title" || payload.Severity != "WARNING" {
			t.Fatalf("payload %+v", payload)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := &WebhookDispatcher{url: srv.URL, httpClient: srv.Client()}
	alert := Alert{
		Severity:  SeverityWarning,
		Title:     "test-title",
		Message:   "body",
		Timestamp: time.Now(),
	}
	if err := d.Dispatch(ChannelTelegram, alert); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestWebhookDispatcher_NilNoOp(t *testing.T) {
	var d *WebhookDispatcher
	if err := d.Dispatch(ChannelDiscord, Alert{Title: "x"}); err != nil {
		t.Fatal(err)
	}
}

func TestWebhookDispatcher_EmptyURLNoOp(t *testing.T) {
	d := &WebhookDispatcher{url: "", httpClient: http.DefaultClient}
	if err := d.Dispatch(ChannelPagerDuty, Alert{Title: "x"}); err != nil {
		t.Fatal(err)
	}
}

func TestNewWebhookDispatcherFromEnv_Unset(t *testing.T) {
	t.Setenv("ALERT_WEBHOOK_URL", "")
	if NewWebhookDispatcherFromEnv() != nil {
		t.Fatal("expected nil")
	}
}

func TestAlerter_SendTriggersWebhook(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := NewAlerterWithWebhook([]AlertChannel{ChannelTelegram}, &WebhookDispatcher{
		url:        srv.URL,
		httpClient: srv.Client(),
	})
	a.rateLimit = 0
	a.Send(SeverityCritical, "webhook-alert", "msg")
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestAlerter_SendWithoutWebhook_StillLogs(t *testing.T) {
	a := NewAlerter([]AlertChannel{ChannelDiscord})
	a.rateLimit = 0
	a.webhook = nil
	a.Send(SeverityInfo, "log-only", "ok")
	if len(a.History()) != 1 {
		t.Fatal("history missing")
	}
}

func TestWebhookDispatcher_Non2xxDoesNotError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	d := &WebhookDispatcher{url: srv.URL, httpClient: srv.Client()}
	if err := d.Dispatch(ChannelDiscord, Alert{Title: "x", Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
}
