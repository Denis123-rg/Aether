package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aether-arb/aether/internal/config"
)

func TestPagerDutyNotifier_SendSuccess(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	n := &PagerDutyNotifier{
		routingKey: "routing-key-abc",
		enqueueURL: srv.URL,
		client:     srv.Client(),
	}
	err := n.Send(Alert{Severity: SeverityWarning, Title: "gas spike", Message: "300 gwei"})
	if err != nil {
		t.Fatal(err)
	}
	if got["routing_key"] != "routing-key-abc" {
		t.Fatalf("payload %v", got)
	}
}

func TestPagerDutyNotifier_CriticalSeverity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	n := &PagerDutyNotifier{routingKey: "k", enqueueURL: srv.URL, client: srv.Client()}
	if err := n.Send(Alert{Severity: SeverityCritical, Title: "halt", Message: "daily loss"}); err != nil {
		t.Fatal(err)
	}
}

func TestPagerDutyNotifier_StatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("fail"))
	}))
	defer srv.Close()
	n := &PagerDutyNotifier{routingKey: "k", enqueueURL: srv.URL, client: srv.Client()}
	if err := n.Send(Alert{Severity: SeverityInfo, Title: "t", Message: "m"}); err == nil {
		t.Fatal("expected status error")
	}
}

func TestPagerDutyNotifier_NilReceiver(t *testing.T) {
	var n *PagerDutyNotifier
	if err := n.Send(Alert{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestNewPagerDutyNotifier_EmptyKey(t *testing.T) {
	if NewPagerDutyNotifier("") != nil {
		t.Fatal("expected nil")
	}
}

func TestTelegramNotifier_SendSuccess(t *testing.T) {
	var chatID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]string
		_ = json.Unmarshal(body, &m)
		chatID = m["chat_id"]
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := &TelegramNotifier{
		botToken: "bot-token",
		chatID:   "-100123",
		apiBase:  srv.URL,
		client:   srv.Client(),
	}
	if err := n.Send(Alert{Severity: SeverityWarning, Title: "warn", Message: "details"}); err != nil {
		t.Fatal(err)
	}
	if chatID != "-100123" {
		t.Fatalf("chat %s", chatID)
	}
}

func TestTelegramNotifier_StatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	n := &TelegramNotifier{botToken: "t", chatID: "1", apiBase: srv.URL, client: srv.Client()}
	if err := n.Send(Alert{Severity: SeverityInfo, Title: "x", Message: "y"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestTelegramNotifier_NilReceiver(t *testing.T) {
	var n *TelegramNotifier
	if err := n.Send(Alert{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestNewTelegramNotifier_EmptyFields(t *testing.T) {
	if NewTelegramNotifier("", "1") != nil || NewTelegramNotifier("t", "") != nil {
		t.Fatal("expected nil")
	}
}

func TestDiscordNotifier_StatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	n := &DiscordNotifier{webhookURL: srv.URL, client: srv.Client()}
	if err := n.Send(Alert{Severity: SeverityCritical, Title: "t", Message: "m"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadAlertingFromEnv(t *testing.T) {
	t.Setenv("PD_ROUTING_KEY", "pd")
	t.Setenv("TELEGRAM_ALERT_BOT_TOKEN", "tg")
	t.Setenv("TELEGRAM_ALERT_CHAT_ID", "chat")
	t.Setenv("DISCORD_WEBHOOK_URL", "discord")
	t.Setenv("ALERT_WEBHOOK_URL", "hook")
	pd, tg, chat, discord, hook := loadAlertingFromEnv()
	if pd != "pd" || tg != "tg" || chat != "chat" || discord != "discord" || hook != "hook" {
		t.Fatalf("got %s %s %s %s %s", pd, tg, chat, discord, hook)
	}
}

func TestLoadAlertingFromConfig_EnvOverridesToml(t *testing.T) {
	t.Setenv("PD_ROUTING_KEY", "env-pd")
	cfg := config.MonitorAlerting{
		PagerDutyRoutingKey: "toml-pd",
		AlertWebhookURL:     "http://toml-hook",
	}
	pd, _, _, _, hook := loadAlertingFromConfig(cfg)
	if pd != "env-pd" || hook != "http://toml-hook" {
		t.Fatalf("pd=%s hook=%s", pd, hook)
	}
}

func TestNewAlerterFromConfig_WithPagerDuty(t *testing.T) {
	cfg := config.MonitorAlerting{PagerDutyRoutingKey: "pd-key"}
	a := NewAlerterFromConfig([]AlertChannel{ChannelPagerDuty}, cfg)
	if a == nil || a.pagerduty == nil {
		t.Fatal("expected pagerduty alerter")
	}
}
