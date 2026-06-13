package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// PagerDutyNotifier sends events to the PagerDuty Events API v2.
type PagerDutyNotifier struct {
	routingKey string
	client     *http.Client
}

func NewPagerDutyNotifier(routingKey string) *PagerDutyNotifier {
	if routingKey == "" {
		return nil
	}
	return &PagerDutyNotifier{
		routingKey: routingKey,
		client:     &http.Client{Timeout: 5 * time.Second},
	}
}

func (n *PagerDutyNotifier) Send(alert Alert) error {
	if n == nil {
		return fmt.Errorf("pagerduty notifier not configured")
	}
	severity := "info"
	switch alert.Severity {
	case SeverityCritical:
		severity = "critical"
	case SeverityWarning:
		severity = "warning"
	}
	body, _ := json.Marshal(map[string]any{
		"routing_key":  n.routingKey,
		"event_action": "trigger",
		"payload": map[string]any{
			"summary":  alert.Title,
			"source":   "aether-monitor",
			"severity": severity,
			"custom_details": map[string]string{
				"message": alert.Message,
			},
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://events.pagerduty.com/v2/enqueue", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("pagerduty status %d: %s", resp.StatusCode, b)
	}
	return nil
}

// TelegramNotifier posts alerts to a Telegram chat.
type TelegramNotifier struct {
	botToken string
	chatID   string
	client   *http.Client
}

func NewTelegramNotifier(botToken, chatID string) *TelegramNotifier {
	if botToken == "" || chatID == "" {
		return nil
	}
	return &TelegramNotifier{
		botToken: botToken,
		chatID:   chatID,
		client:   &http.Client{Timeout: 5 * time.Second},
	}
}

func (n *TelegramNotifier) Send(alert Alert) error {
	if n == nil {
		return fmt.Errorf("telegram notifier not configured")
	}
	text := fmt.Sprintf("[%s] %s\n%s", alert.Severity, alert.Title, alert.Message)
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", n.botToken)
	body, _ := json.Marshal(map[string]string{
		"chat_id": n.chatID,
		"text":    text,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("telegram status %d", resp.StatusCode)
	}
	return nil
}

// DiscordNotifier posts alerts to a Discord webhook.
type DiscordNotifier struct {
	webhookURL string
	client     *http.Client
}

func NewDiscordNotifier(webhookURL string) *DiscordNotifier {
	if webhookURL == "" {
		return nil
	}
	return &DiscordNotifier{
		webhookURL: webhookURL,
		client:     &http.Client{Timeout: 5 * time.Second},
	}
}

func (n *DiscordNotifier) Send(alert Alert) error {
	if n == nil {
		return fmt.Errorf("discord notifier not configured")
	}
	body, _ := json.Marshal(map[string]string{
		"content": fmt.Sprintf("[%s] **%s**\n%s", alert.Severity, alert.Title, alert.Message),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.webhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("discord status %d", resp.StatusCode)
	}
	return nil
}

// loadAlertingFromEnv reads monitor alerting config from env (production.toml
// values are injected at deploy time).
func loadAlertingFromEnv() (pdKey, tgToken, tgChat, discordURL, webhookURL string) {
	pdKey = os.Getenv("PD_ROUTING_KEY")
	tgToken = os.Getenv("TELEGRAM_ALERT_BOT_TOKEN")
	tgChat = os.Getenv("TELEGRAM_ALERT_CHAT_ID")
	discordURL = os.Getenv("DISCORD_WEBHOOK_URL")
	webhookURL = os.Getenv("ALERT_WEBHOOK_URL")
	return
}
