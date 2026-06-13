package main

import (
	"log/slog"
	"time"

	"net/http"
)

// AlertSeverity represents alert importance
type AlertSeverity string

const (
	SeverityInfo     AlertSeverity = "INFO"
	SeverityWarning  AlertSeverity = "WARNING"
	SeverityCritical AlertSeverity = "CRITICAL"
)

// Alert represents a system alert
type Alert struct {
	Severity  AlertSeverity
	Title     string
	Message   string
	Timestamp time.Time
}

// AlertChannel represents an alert destination
type AlertChannel string

const (
	ChannelPagerDuty AlertChannel = "pagerduty"
	ChannelTelegram  AlertChannel = "telegram"
	ChannelDiscord   AlertChannel = "discord"
)

// Alerter dispatches alerts to configured channels
type Alerter struct {
	channels  []AlertChannel
	history   []Alert
	rateLimit time.Duration
	lastAlert map[string]time.Time
	webhook   *WebhookDispatcher
	pagerduty *PagerDutyNotifier
	telegram  *TelegramNotifier
	discord   *DiscordNotifier
	maxPerMin int
	sentMin   int
	minute    time.Time
}

// NewAlerter creates a new alerter
func NewAlerter(channels []AlertChannel) *Alerter {
	pdKey, tgToken, tgChat, discordURL, webhookURL := loadAlertingFromEnv()
	wh := NewWebhookDispatcherFromEnv()
	if webhookURL != "" && wh == nil {
		wh = &WebhookDispatcher{url: webhookURL, httpClient: &http.Client{Timeout: 5 * time.Second}}
	}
	return &Alerter{
		channels:  channels,
		history:   make([]Alert, 0),
		rateLimit: 5 * time.Minute,
		lastAlert: make(map[string]time.Time),
		webhook:   wh,
		pagerduty: NewPagerDutyNotifier(pdKey),
		telegram:  NewTelegramNotifier(tgToken, tgChat),
		discord:   NewDiscordNotifier(discordURL),
		maxPerMin: 30,
	}
}

// NewAlerterWithWebhook creates an alerter with an explicit webhook dispatcher (tests).
func NewAlerterWithWebhook(channels []AlertChannel, webhook *WebhookDispatcher) *Alerter {
	a := NewAlerter(channels)
	a.webhook = webhook
	return a
}

// Send dispatches an alert to all configured channels
func (a *Alerter) Send(severity AlertSeverity, title, message string) {
	now := time.Now()
	if now.Sub(a.minute) >= time.Minute {
		a.minute = now
		a.sentMin = 0
	}
	if a.sentMin >= a.maxPerMin {
		slog.Error("alert rate limit exceeded, dropping alert", "title", title)
		return
	}

	// Rate limiting: don't send same title within rateLimit window
	if last, ok := a.lastAlert[title]; ok {
		if time.Since(last) < a.rateLimit {
			return
		}
	}

	alert := Alert{
		Severity:  severity,
		Title:     title,
		Message:   message,
		Timestamp: now,
	}

	a.history = append(a.history, alert)
	a.lastAlert[title] = now
	a.sentMin++

	for _, ch := range a.channels {
		a.dispatch(ch, alert)
	}
}

func (a *Alerter) dispatch(channel AlertChannel, alert Alert) {
	var nativeErr error
	switch channel {
	case ChannelPagerDuty:
		if a.pagerduty != nil {
			nativeErr = a.pagerduty.Send(alert)
		}
	case ChannelTelegram:
		if a.telegram != nil {
			nativeErr = a.telegram.Send(alert)
		}
	case ChannelDiscord:
		if a.discord != nil {
			nativeErr = a.discord.Send(alert)
		}
	}
	if nativeErr == nil && (channel == ChannelPagerDuty && a.pagerduty != nil ||
		channel == ChannelTelegram && a.telegram != nil ||
		channel == ChannelDiscord && a.discord != nil) {
		slog.Info("alert dispatched", "channel", channel, "severity", alert.Severity, "title", alert.Title)
		return
	}
	if a.webhook != nil {
		if err := a.webhook.Dispatch(channel, alert); err != nil {
			slog.Warn("alert webhook dispatch failed",
				"channel", channel,
				"title", alert.Title,
				"err", err,
			)
			return
		}
		slog.Info("alert dispatched via webhook", "channel", channel, "title", alert.Title)
		return
	}
	slog.Error("alert dispatch failed — no native or webhook channel configured",
		"channel", channel,
		"severity", alert.Severity,
		"title", alert.Title,
		"message", alert.Message,
		"native_err", nativeErr,
	)
}

// History returns recent alerts
func (a *Alerter) History() []Alert {
	out := make([]Alert, len(a.history))
	copy(out, a.history)
	return out
}
