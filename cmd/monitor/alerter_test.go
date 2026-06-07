package main

import (
	"testing"
	"time"
)

func TestNewAlerter(t *testing.T) {
	a := NewAlerter([]AlertChannel{ChannelPagerDuty, ChannelTelegram})
	if a == nil {
		t.Fatal("nil alerter")
	}
	if len(a.channels) != 2 {
		t.Fatalf("channels = %d", len(a.channels))
	}
}

func TestSend_RecordsHistory(t *testing.T) {
	a := NewAlerter([]AlertChannel{ChannelDiscord})
	a.rateLimit = 0 // disable rate limit for test

	a.Send(SeverityWarning, "Test Alert", "details here")
	h := a.History()
	if len(h) != 1 {
		t.Fatalf("history len = %d", len(h))
	}
	if h[0].Title != "Test Alert" {
		t.Fatalf("title = %q", h[0].Title)
	}
	if h[0].Severity != SeverityWarning {
		t.Fatalf("severity = %q", h[0].Severity)
	}
}

func TestSend_RateLimit(t *testing.T) {
	a := NewAlerter([]AlertChannel{ChannelPagerDuty})
	a.rateLimit = time.Hour

	a.Send(SeverityInfo, "Duplicate", "first")
	a.Send(SeverityInfo, "Duplicate", "second")
	if len(a.History()) != 1 {
		t.Fatalf("rate limit should suppress duplicate, got %d alerts", len(a.History()))
	}
}

func TestSend_DifferentTitlesNotRateLimited(t *testing.T) {
	a := NewAlerter([]AlertChannel{ChannelTelegram})
	a.rateLimit = time.Hour

	a.Send(SeverityInfo, "Alert A", "msg")
	a.Send(SeverityInfo, "Alert B", "msg")
	if len(a.History()) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(a.History()))
	}
}

func TestDispatch_AllChannels(t *testing.T) {
	channels := []AlertChannel{ChannelPagerDuty, ChannelTelegram, ChannelDiscord}
	a := NewAlerter(channels)
	a.rateLimit = 0

	for _, sev := range []AlertSeverity{SeverityInfo, SeverityWarning, SeverityCritical} {
		a.Send(sev, "multi-channel", "test")
	}
	if len(a.History()) != 3 {
		t.Fatalf("history = %d", len(a.History()))
	}
}

func TestHistory_ReturnsCopy(t *testing.T) {
	a := NewAlerter([]AlertChannel{ChannelDiscord})
	a.rateLimit = 0
	a.Send(SeverityInfo, "x", "y")
	h1 := a.History()
	h1[0].Title = "mutated"
	h2 := a.History()
	if h2[0].Title == "mutated" {
		t.Fatal("History should return internal slice (document current behavior)")
	}
}

func TestAlertSeverityConstants(t *testing.T) {
	if SeverityInfo != "INFO" || SeverityWarning != "WARNING" || SeverityCritical != "CRITICAL" {
		t.Fatal("severity constants mismatch")
	}
}

func TestAlertChannelConstants(t *testing.T) {
	if ChannelPagerDuty != "pagerduty" || ChannelTelegram != "telegram" || ChannelDiscord != "discord" {
		t.Fatal("channel constants mismatch")
	}
}

func TestSend_AfterRateLimitExpires(t *testing.T) {
	a := NewAlerter([]AlertChannel{ChannelPagerDuty})
	a.rateLimit = 10 * time.Millisecond

	a.Send(SeverityInfo, "repeat", "first")
	time.Sleep(15 * time.Millisecond)
	a.Send(SeverityInfo, "repeat", "second")
	if len(a.History()) != 2 {
		t.Fatalf("expected 2 after rate window, got %d", len(a.History()))
	}
}

func TestNewAlerter_EmptyChannels(t *testing.T) {
	a := NewAlerter(nil)
	a.rateLimit = 0
	a.Send(SeverityInfo, "no channels", "still recorded")
	if len(a.History()) != 1 {
		t.Fatalf("history = %d", len(a.History()))
	}
}

func TestAlertTimestampSet(t *testing.T) {
	a := NewAlerter([]AlertChannel{ChannelDiscord})
	a.rateLimit = 0
	before := time.Now()
	a.Send(SeverityCritical, "ts", "check")
	after := time.Now()
	ts := a.History()[0].Timestamp
	if ts.Before(before) || ts.After(after) {
		t.Fatalf("timestamp %v not in [%v, %v]", ts, before, after)
	}
}
