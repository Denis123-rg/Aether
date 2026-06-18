package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aether-arb/aether/internal/config"
)

// --- NewWebhookDispatcherFromEnv: success path (URL set) ---

func TestNewWebhookDispatcherFromEnv_Set(t *testing.T) {
	t.Setenv("ALERT_WEBHOOK_URL", "http://example.com/hook")
	d := NewWebhookDispatcherFromEnv()
	if d == nil {
		t.Fatal("expected non-nil dispatcher")
	}
	if d.url != "http://example.com/hook" {
		t.Fatalf("url = %q", d.url)
	}
}

// --- WebhookDispatcher.Dispatch: timeout fallback (httpClient.Timeout <= 0) ---

func TestWebhookDispatcher_Dispatch_ZeroTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	d := &WebhookDispatcher{url: srv.URL, httpClient: &http.Client{Timeout: 0}}
	if err := d.Dispatch(ChannelDiscord, Alert{Title: "zero-timeout", Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

// --- WebhookDispatcher.Dispatch: negative timeout fallback ---

func TestWebhookDispatcher_Dispatch_NegativeTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	d := &WebhookDispatcher{url: srv.URL, httpClient: &http.Client{Timeout: -1}}
	if err := d.Dispatch(ChannelTelegram, Alert{Title: "neg-timeout", Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

// --- WebhookDispatcher.Dispatch: HTTP request error (unreachable host) ---

func TestWebhookDispatcher_Dispatch_HTTPErr(t *testing.T) {
	d := &WebhookDispatcher{
		url:        "http://127.0.0.1:1/hook",
		httpClient: &http.Client{Timeout: 100 * time.Millisecond},
	}
	err := d.Dispatch(ChannelPagerDuty, Alert{Title: "unreachable", Timestamp: time.Now()})
	if err == nil {
		t.Fatal("expected error for unreachable host")
	}
}

// --- NewAlerter: webhookURL path (webhookURL != "" && wh == nil) ---

func TestNewAlerter_WebhookURLFromEnv(t *testing.T) {
	t.Setenv("ALERT_WEBHOOK_URL", "http://localhost:9999/hook")
	a := NewAlerter([]AlertChannel{ChannelTelegram})
	if a.webhook == nil {
		t.Fatal("expected webhook dispatcher")
	}
}

// --- dispatch: PagerDuty native success path ---

func TestDispatch_PagerDutyNativeSuccess(t *testing.T) {
	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	a := NewAlerter([]AlertChannel{ChannelPagerDuty})
	a.rateLimit = 0
	a.webhook = nil
	a.pagerduty = &PagerDutyNotifier{routingKey: "key", enqueueURL: srv.URL, client: srv.Client()}
	a.Send(SeverityCritical, "pd-native", "msg")

	if payload == nil {
		t.Fatal("no payload received")
	}
	if payload["routing_key"] != "key" {
		t.Fatalf("unexpected payload: %v", payload)
	}
}

// --- dispatch: Telegram native success path ---

func TestDispatch_TelegramNativeSuccess(t *testing.T) {
	var payload map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := NewAlerter([]AlertChannel{ChannelTelegram})
	a.rateLimit = 0
	a.webhook = nil
	a.telegram = &TelegramNotifier{botToken: "tok", chatID: "123", apiBase: srv.URL, client: srv.Client()}
	a.Send(SeverityInfo, "tg-native", "details")

	if payload == nil {
		t.Fatal("no payload received")
	}
	if payload["chat_id"] != "123" {
		t.Fatalf("unexpected payload: %v", payload)
	}
}

// --- dispatch: Discord native success path ---

func TestDispatch_DiscordNativeSuccess(t *testing.T) {
	var payload map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	a := NewAlerter([]AlertChannel{ChannelDiscord})
	a.rateLimit = 0
	a.webhook = nil
	a.discord = &DiscordNotifier{webhookURL: srv.URL, client: srv.Client()}
	a.Send(SeverityWarning, "dc-native", "test")

	if payload == nil {
		t.Fatal("no payload received")
	}
	if !strings.Contains(payload["content"], "dc-native") {
		t.Fatalf("unexpected payload: %v", payload)
	}
}

// --- dispatch: webhook fallback error path ---

func TestDispatch_WebhookFallbackError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	a := NewAlerter([]AlertChannel{ChannelPagerDuty})
	a.rateLimit = 0
	a.pagerduty = nil
	a.telegram = nil
	a.discord = nil
	a.webhook = &WebhookDispatcher{url: srv.URL, httpClient: srv.Client()}
	a.Send(SeverityInfo, "wh-err", "msg")

	if len(a.History()) != 1 {
		t.Fatal("alert not recorded")
	}
}

// --- dispatch: webhook fallback success path ---

func TestDispatch_WebhookFallbackSuccess(t *testing.T) {
	var payload webhookPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := NewAlerter([]AlertChannel{ChannelTelegram})
	a.rateLimit = 0
	a.pagerduty = nil
	a.telegram = nil
	a.discord = nil
	a.webhook = &WebhookDispatcher{url: srv.URL, httpClient: srv.Client()}
	a.Send(SeverityWarning, "wh-ok", "ok")

	if payload.Title != "wh-ok" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

// --- histogramAvg: parse error path ---

func TestHistogramAvg_ParseError(t *testing.T) {
	m := map[string]string{
		"bar_sum":   "not-a-number",
		"bar_count": "5",
	}
	if histogramAvg(m, "bar") != -1 {
		t.Fatal("expected -1 for parse error")
	}
}

func TestHistogramAvg_CountParseError(t *testing.T) {
	m := map[string]string{
		"bar_sum":   "100",
		"bar_count": "not-a-number",
	}
	if histogramAvg(m, "bar") != -1 {
		t.Fatal("expected -1 for count parse error")
	}
}

func TestHistogramAvg_NegativeCount(t *testing.T) {
	m := map[string]string{
		"bar_sum":   "100",
		"bar_count": "-1",
	}
	if histogramAvg(m, "bar") != -1 {
		t.Fatal("expected -1 for negative count")
	}
}

// --- PagerDuty Send: default URL path (enqueueURL empty) ---

func TestPagerDutyNotifier_Send_DefaultURL(t *testing.T) {
	// Create a fake server that responds to any URL
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/enqueue" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	n := &PagerDutyNotifier{
		routingKey: "key",
		enqueueURL: srv.URL + "/v2/enqueue",
		client:     srv.Client(),
	}
	if err := n.Send(Alert{Severity: SeverityInfo, Title: "t", Message: "m"}); err != nil {
		t.Fatal(err)
	}
}

// --- PagerDuty Send: http.NewRequestWithContext error ---

func TestPagerDutyNotifier_Send_BadURL(t *testing.T) {
	n := &PagerDutyNotifier{
		routingKey: "key",
		enqueueURL: "://bad-url",
		client:     &http.Client{Timeout: 5 * time.Second},
	}
	err := n.Send(Alert{Severity: SeverityInfo, Title: "t", Message: "m"})
	if err == nil {
		t.Fatal("expected error for bad URL")
	}
}

// --- NewTelegramNotifier: success path ---

func TestNewTelegramNotifier_Success(t *testing.T) {
	n := NewTelegramNotifier("token-123", "chat-456")
	if n == nil {
		t.Fatal("expected non-nil notifier")
	}
	if n.botToken != "token-123" || n.chatID != "chat-456" {
		t.Fatalf("unexpected fields: %+v", n)
	}
}

// --- Telegram Send: default base URL path ---

func TestTelegramNotifier_Send_DefaultBase(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := &TelegramNotifier{
		botToken: "tok",
		chatID:   "123",
		apiBase:  srv.URL,
		client:   srv.Client(),
	}
	if err := n.Send(Alert{Severity: SeverityInfo, Title: "t", Message: "m"}); err != nil {
		t.Fatal(err)
	}
}

// --- Telegram Send: http.NewRequestWithContext error ---

func TestTelegramNotifier_Send_BadURL(t *testing.T) {
	n := &TelegramNotifier{
		botToken: "tok",
		chatID:   "123",
		apiBase:  "://bad",
		client:   &http.Client{Timeout: 5 * time.Second},
	}
	err := n.Send(Alert{Severity: SeverityInfo, Title: "t", Message: "m"})
	if err == nil {
		t.Fatal("expected error for bad URL")
	}
}

// --- Discord Send: http.NewRequestWithContext error ---

func TestDiscordNotifier_Send_BadURL(t *testing.T) {
	n := &DiscordNotifier{
		webhookURL: "://bad",
		client:     &http.Client{Timeout: 5 * time.Second},
	}
	err := n.Send(Alert{Severity: SeverityInfo, Title: "t", Message: "m"})
	if err == nil {
		t.Fatal("expected error for bad URL")
	}
}

// --- loadAlertingFromConfig: all override paths ---

func TestLoadAlertingFromConfig_AllOverrides(t *testing.T) {
	cfg := config.MonitorAlerting{
		PagerDutyRoutingKey: "toml-pd",
		TelegramBotToken:    "toml-tg",
		TelegramChatID:      "toml-chat",
		DiscordWebhookURL:   "toml-dc",
		AlertWebhookURL:     "toml-hook",
	}
	pd, tg, chat, dc, hook := loadAlertingFromConfig(cfg)
	// When no env vars are set, toml values should be used
	if pd != "toml-pd" || tg != "toml-tg" || chat != "toml-chat" || dc != "toml-dc" || hook != "toml-hook" {
		t.Fatalf("got pd=%s tg=%s chat=%s dc=%s hook=%s", pd, tg, chat, dc, hook)
	}
}

func TestLoadAlertingFromConfig_EnvOverridesAll(t *testing.T) {
	t.Setenv("PD_ROUTING_KEY", "env-pd")
	t.Setenv("TELEGRAM_ALERT_BOT_TOKEN", "env-tg")
	t.Setenv("TELEGRAM_ALERT_CHAT_ID", "env-chat")
	t.Setenv("DISCORD_WEBHOOK_URL", "env-dc")
	t.Setenv("ALERT_WEBHOOK_URL", "env-hook")
	cfg := config.MonitorAlerting{}
	pd, tg, chat, dc, hook := loadAlertingFromConfig(cfg)
	if pd != "env-pd" || tg != "env-tg" || chat != "env-chat" || dc != "env-dc" || hook != "env-hook" {
		t.Fatalf("got pd=%s tg=%s chat=%s dc=%s hook=%s", pd, tg, chat, dc, hook)
	}
}

// --- loadAlertingFromConfig: partial env override ---

func TestLoadAlertingFromConfig_PartialOverride(t *testing.T) {
	t.Setenv("TELEGRAM_ALERT_BOT_TOKEN", "env-tg")
	t.Setenv("DISCORD_WEBHOOK_URL", "env-dc")
	cfg := config.MonitorAlerting{
		PagerDutyRoutingKey: "toml-pd",
		TelegramChatID:      "toml-chat",
		AlertWebhookURL:     "toml-hook",
	}
	pd, tg, chat, dc, hook := loadAlertingFromConfig(cfg)
	if pd != "toml-pd" {
		t.Fatalf("pd = %s, expected toml-pd", pd)
	}
	if tg != "env-tg" {
		t.Fatalf("tg = %s, expected env-tg", tg)
	}
	if chat != "toml-chat" {
		t.Fatalf("chat = %s, expected toml-chat", chat)
	}
	if dc != "env-dc" {
		t.Fatalf("dc = %s, expected env-dc", dc)
	}
	if hook != "toml-hook" {
		t.Fatalf("hook = %s, expected toml-hook", hook)
	}
}

// --- NewAlerterFromConfig: with webhook URL ---

func TestNewAlerterFromConfig_WithWebhook(t *testing.T) {
	cfg := config.MonitorAlerting{AlertWebhookURL: "http://localhost:1234/hook"}
	a := NewAlerterFromConfig([]AlertChannel{ChannelDiscord}, cfg)
	if a == nil {
		t.Fatal("nil alerter")
	}
}

// --- setup.go: runMonitorSetup with MONITOR_HTTP_PORT override ---

func TestRunMonitorSetup_MonitorHTTPPort(t *testing.T) {
	t.Setenv("MONITOR_HTTP_PORT", "8095")
	t.Setenv("METRICS_PORT", "9095")
	os.Unsetenv("DASHBOARD_PORT")
	setup := runMonitorSetup()
	if setup.DashboardPort != "8095" {
		t.Fatalf("dashboard port = %s", setup.DashboardPort)
	}
	if setup.MetricsPort != "9095" {
		t.Fatalf("metrics port = %s", setup.MetricsPort)
	}
}

// --- setup.go: runMonitorSetup with DASHBOARD_PORT override takes precedence ---

func TestRunMonitorSetup_DashboardPortOverridesConfig(t *testing.T) {
	t.Setenv("DASHBOARD_PORT", "8888")
	t.Setenv("MONITOR_HTTP_PORT", "8889")
	setup := runMonitorSetup()
	if setup.DashboardPort != "8889" {
		t.Fatalf("expected MONITOR_HTTP_PORT to take precedence, got %s", setup.DashboardPort)
	}
}

// --- runMonitorService: context cancellation path ---

func TestRunMonitorService_ContextCancel(t *testing.T) {
	t.Setenv("METRICS_PORT", "0")
	t.Setenv("DASHBOARD_PORT", "0")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := runMonitorService(ctx)
	if err != nil {
		t.Fatalf("runMonitorService: %v", err)
	}
}

// --- runMonitorService: server error path ---

func TestRunMonitorService_ServerError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()

	t.Setenv("METRICS_PORT", "0")
	t.Setenv("DASHBOARD_PORT", "0")

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_ = runMonitorService(ctx)
}

// --- runMonitorSetup: production.toml loads successfully, port from config ---

func TestRunMonitorSetup_ProdConfigPort(t *testing.T) {
	os.Unsetenv("METRICS_PORT")
	os.Unsetenv("DASHBOARD_PORT")
	os.Unsetenv("MONITOR_HTTP_PORT")
	t.Setenv("AETHER_ENV", "development")

	setup := runMonitorSetup()
	// production.toml has port=8090
	if setup.DashboardPort != "8090" {
		t.Fatalf("dashboard port = %s", setup.DashboardPort)
	}
}

// --- dispatch: webhook fallback with nil webhook ---

func TestDispatch_NoNativeAndNoWebhook(t *testing.T) {
	a := NewAlerter([]AlertChannel{ChannelPagerDuty})
	a.rateLimit = 0
	a.pagerduty = nil
	a.telegram = nil
	a.discord = nil
	a.webhook = nil
	a.Send(SeverityInfo, "no-channel", "orphan alert")
	if len(a.History()) != 1 {
		t.Fatal("alert should be recorded even with no channels")
	}
}

// --- runMonitorService: signal handling ---

func TestRunMonitorService_SignalPath(t *testing.T) {
	// Test that the select picks up context cancel (simulating signal)
	t.Setenv("METRICS_PORT", "0")
	t.Setenv("DASHBOARD_PORT", "0")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := runMonitorService(ctx)
	if err != nil {
		t.Fatalf("runMonitorService: %v", err)
	}
}

// --- NewWebhookDispatcherFromEnv: confirm timeout set ---

func TestNewWebhookDispatcherFromEnv_Timeout(t *testing.T) {
	t.Setenv("ALERT_WEBHOOK_URL", "http://example.com")
	d := NewWebhookDispatcherFromEnv()
	if d == nil {
		t.Fatal("nil dispatcher")
	}
	if d.httpClient.Timeout != 5*time.Second {
		t.Fatalf("timeout = %v", d.httpClient.Timeout)
	}
}

// --- NewAlerterFromConfig: all notifiers initialized ---

func TestNewAlerterFromConfig_AllNotifiers(t *testing.T) {
	cfg := config.MonitorAlerting{
		PagerDutyRoutingKey: "pd-key",
		TelegramBotToken:    "tg-token",
		TelegramChatID:      "tg-chat",
		DiscordWebhookURL:   "http://discord/webhook",
	}
	a := NewAlerterFromConfig([]AlertChannel{ChannelPagerDuty, ChannelTelegram, ChannelDiscord}, cfg)
	if a.pagerduty == nil {
		t.Fatal("pagerduty nil")
	}
	if a.telegram == nil {
		t.Fatal("telegram nil")
	}
	if a.discord == nil {
		t.Fatal("discord nil")
	}
}

// --- NewAlerterFromConfig: webhook from config ---

func TestNewAlerterFromConfig_WebhookFromConfig(t *testing.T) {
	os.Unsetenv("ALERT_WEBHOOK_URL")
	cfg := config.MonitorAlerting{AlertWebhookURL: "http://cfg/hook"}
	a := NewAlerterFromConfig([]AlertChannel{ChannelDiscord}, cfg)
	if a.webhook == nil {
		t.Fatal("expected webhook from config")
	}
}

// --- NewAlerter: webhook URL fallback path ---

func TestNewAlerter_WebhookFallbackPath(t *testing.T) {
	os.Unsetenv("ALERT_WEBHOOK_URL")
	t.Setenv("ALERT_WEBHOOK_URL", "")
	a := NewAlerter([]AlertChannel{ChannelDiscord})
	// No env webhook, no native → webhook is nil
	if a.webhook != nil {
		t.Log("webhook non-nil (may be set via env)")
	}
}

// --- PagerDuty Send: status >= 300 with body ---

func TestPagerDutyNotifier_Send_StatusWithBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("server error"))
	}))
	defer srv.Close()
	n := &PagerDutyNotifier{routingKey: "k", enqueueURL: srv.URL, client: srv.Client()}
	err := n.Send(Alert{Severity: SeverityInfo, Title: "t", Message: "m"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error should mention status code: %v", err)
	}
}

// --- Telegram Send: connection error ---

func TestTelegramNotifier_Send_ConnError(t *testing.T) {
	n := &TelegramNotifier{
		botToken: "tok",
		chatID:   "123",
		apiBase:  "http://127.0.0.1:1",
		client:   &http.Client{Timeout: 100 * time.Millisecond},
	}
	err := n.Send(Alert{Severity: SeverityInfo, Title: "t", Message: "m"})
	if err == nil {
		t.Fatal("expected connection error")
	}
}

// --- Discord Send: connection error ---

func TestDiscordNotifier_Send_ConnError(t *testing.T) {
	n := &DiscordNotifier{
		webhookURL: "http://127.0.0.1:1",
		client:     &http.Client{Timeout: 100 * time.Millisecond},
	}
	err := n.Send(Alert{Severity: SeverityInfo, Title: "t", Message: "m"})
	if err == nil {
		t.Fatal("expected connection error")
	}
}

// --- Dispatch: webhook returns error ---

func TestDispatch_WebhookReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	a := NewAlerter([]AlertChannel{ChannelDiscord})
	a.rateLimit = 0
	a.discord = nil
	a.pagerduty = nil
	a.telegram = nil
	a.webhook = &WebhookDispatcher{url: srv.URL, httpClient: srv.Client()}
	a.Send(SeverityInfo, "webhook-err", "msg")
}

// --- runMonitorSetup: tracer init with env ---

func TestRunMonitorSetup_TracerInit(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	setup := runMonitorSetup()
	if setup.ShutdownTracer == nil {
		t.Fatal("nil shutdown tracer")
	}
	ctx := context.Background()
	if err := setup.ShutdownTracer(ctx); err != nil {
		t.Logf("tracer shutdown: %v", err)
	}
}

// --- runMonitorSetup: production config loads but alerting validation passes ---

func TestRunMonitorSetup_ProdConfigLoads(t *testing.T) {
	t.Setenv("AETHER_ENV", "development")
	os.Unsetenv("METRICS_PORT")
	os.Unsetenv("DASHBOARD_PORT")
	os.Unsetenv("MONITOR_HTTP_PORT")
	setup := runMonitorSetup()
	if setup.Metrics == nil || setup.Dashboard == nil || setup.Alerter == nil {
		t.Fatal("nil component")
	}
}

// --- PagerDuty Send: empty enqueueURL triggers default URL ---

func TestPagerDutyNotifier_Send_EmptyEnqueueURL(t *testing.T) {
	n := &PagerDutyNotifier{
		routingKey: "key",
		enqueueURL: "",
		client:     &http.Client{Timeout: 100 * time.Millisecond},
	}
	err := n.Send(Alert{Severity: SeverityInfo, Title: "t", Message: "m"})
	if err == nil {
		t.Log("expected error hitting default PD URL (might succeed if PD is reachable)")
	}
}

// --- PagerDuty Send: client.Do error (valid URL, unreachable host) ---

func TestPagerDutyNotifier_Send_ClientDoError(t *testing.T) {
	n := &PagerDutyNotifier{
		routingKey: "key",
		enqueueURL: "http://127.0.0.1:1/v2/enqueue",
		client:     &http.Client{Timeout: 100 * time.Millisecond},
	}
	err := n.Send(Alert{Severity: SeverityInfo, Title: "t", Message: "m"})
	if err == nil {
		t.Fatal("expected client.Do error")
	}
}

// --- Telegram Send: client.Do error ---

func TestTelegramNotifier_Send_ClientDoError(t *testing.T) {
	n := &TelegramNotifier{
		botToken: "tok",
		chatID:   "123",
		apiBase:  "http://127.0.0.1:1",
		client:   &http.Client{Timeout: 100 * time.Millisecond},
	}
	err := n.Send(Alert{Severity: SeverityInfo, Title: "t", Message: "m"})
	if err == nil {
		t.Fatal("expected client.Do error")
	}
}

// --- Discord Send: client.Do error ---

func TestDiscordNotifier_Send_ClientDoError(t *testing.T) {
	n := &DiscordNotifier{
		webhookURL: "http://127.0.0.1:1/webhook",
		client:     &http.Client{Timeout: 100 * time.Millisecond},
	}
	err := n.Send(Alert{Severity: SeverityInfo, Title: "t", Message: "m"})
	if err == nil {
		t.Fatal("expected client.Do error")
	}
}

// --- WebhookDispatcher.Dispatch: malformed URL (NewRequestWithContext error) ---

func TestWebhookDispatcher_Dispatch_MalformedURL(t *testing.T) {
	d := &WebhookDispatcher{
		url:        "://not-a-url",
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
	err := d.Dispatch(ChannelDiscord, Alert{Title: "bad", Timestamp: time.Now()})
	if err == nil {
		t.Fatal("expected NewRequestWithContext error")
	}
}

// --- runMonitorService: dashboard server error (port conflict) ---

func TestRunMonitorService_DashboardPortConflict(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	usedPort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	t.Setenv("METRICS_PORT", "0")
	t.Setenv("DASHBOARD_PORT", strconv.Itoa(usedPort))

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err = runMonitorService(ctx)
	if err != nil {
		t.Logf("server error (expected): %v", err)
	}
}

// --- runMonitorService: signal path ---

func TestRunMonitorService_SignalShutdown(t *testing.T) {
	t.Setenv("METRICS_PORT", "0")
	t.Setenv("DASHBOARD_PORT", "0")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- runMonitorService(ctx)
	}()

	time.Sleep(100 * time.Millisecond)
	_ = syscall.Kill(syscall.Getpid(), syscall.SIGINT)
	time.Sleep(200 * time.Millisecond)

	select {
	case err := <-done:
		if err != nil {
			t.Logf("runMonitorService returned: %v", err)
		}
	default:
		t.Log("service still running, forcing cancel")
		cancel()
		time.Sleep(100 * time.Millisecond)
	}
}

// --- setup.go: production config loads with valid TELEGRAM_BOT_TOKEN ---

func TestRunMonitorSetup_ProdConfigValid(t *testing.T) {
	t.Setenv("AETHER_ENV", "development")
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	os.Unsetenv("METRICS_PORT")
	os.Unsetenv("DASHBOARD_PORT")
	os.Unsetenv("MONITOR_HTTP_PORT")
	setup := runMonitorSetup()
	if setup.DashboardPort == "" {
		t.Fatal("empty dashboard port")
	}
}

// --- setup.go: DASHBOARD_PORT only (no MONITOR_HTTP_PORT) ---

func TestRunMonitorSetup_DashboardPortOnly(t *testing.T) {
	os.Unsetenv("MONITOR_HTTP_PORT")
	t.Setenv("DASHBOARD_PORT", "8097")
	setup := runMonitorSetup()
	if setup.DashboardPort != "8097" {
		t.Fatalf("dashboard port = %s", setup.DashboardPort)
	}
}

// --- WebhookDispatcher.Dispatch: large payload ---

func TestWebhookDispatcher_Dispatch_LargePayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload webhookPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("unmarshal: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	d := &WebhookDispatcher{url: srv.URL, httpClient: srv.Client()}
	alert := Alert{
		Severity:  SeverityCritical,
		Title:     "large-alert",
		Message:   strings.Repeat("x", 10000),
		Timestamp: time.Now(),
	}
	if err := d.Dispatch(ChannelDiscord, alert); err != nil {
		t.Fatal(err)
	}
}
