package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"
)

// webhookPayload is the JSON body sent to ALERT_WEBHOOK_URL.
type webhookPayload struct {
	Severity  string `json:"severity"`
	Title     string `json:"title"`
	Message   string `json:"message"`
	Channel   string `json:"channel"`
	Timestamp string `json:"timestamp"`
}

// WebhookDispatcher posts alerts to a configurable HTTP endpoint.
type WebhookDispatcher struct {
	url        string
	httpClient *http.Client
}

// NewWebhookDispatcherFromEnv reads ALERT_WEBHOOK_URL. Returns nil when unset.
func NewWebhookDispatcherFromEnv() *WebhookDispatcher {
	url := os.Getenv("ALERT_WEBHOOK_URL")
	if url == "" {
		return nil
	}
	return &WebhookDispatcher{
		url: url,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// Dispatch sends the alert JSON to the webhook URL.
func (d *WebhookDispatcher) Dispatch(channel AlertChannel, alert Alert) error {
	if d == nil || d.url == "" {
		return nil
	}
	body, err := json.Marshal(webhookPayload{
		Severity:  string(alert.Severity),
		Title:     alert.Title,
		Message:   alert.Message,
		Channel:   string(channel),
		Timestamp: alert.Timestamp.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	timeout := d.httpClient.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		slog.Warn("alert webhook non-2xx", "status", resp.StatusCode, "url", d.url)
	}
	return nil
}
