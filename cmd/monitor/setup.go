package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/aether-arb/aether/internal/tracing"
)

// MonitorSetup holds the monitor service components initialized at boot.
type MonitorSetup struct {
	Metrics        *Metrics
	Dashboard      *Dashboard
	Alerter        *Alerter
	MetricsPort    string
	DashboardPort  string
	ShutdownTracer func(context.Context) error
}

// runMonitorSetup initializes metrics, dashboard, alerter, and tracing for the
// monitor process. Extracted from main for unit tests.
func runMonitorSetup() MonitorSetup {
	shutdownTracer, err := tracing.Init(context.Background(), "aether-monitor")
	if err != nil {
		slog.Warn("otlp tracer init failed, continuing without traces", "err", err)
		shutdownTracer = func(context.Context) error { return nil }
	}

	metricsPort := os.Getenv("METRICS_PORT")
	if metricsPort == "" {
		metricsPort = "9090"
	}
	dashboardPort := os.Getenv("MONITOR_HTTP_PORT")
	if dashboardPort == "" {
		dashboardPort = os.Getenv("DASHBOARD_PORT")
	}
	if dashboardPort == "" {
		dashboardPort = "8090"
	}

	metrics := NewMetrics()
	dashboard := NewDashboard(metrics)
	alerter := NewAlerter([]AlertChannel{ChannelPagerDuty, ChannelTelegram, ChannelDiscord})

	return MonitorSetup{
		Metrics:        metrics,
		Dashboard:      dashboard,
		Alerter:        alerter,
		MetricsPort:    metricsPort,
		DashboardPort:  dashboardPort,
		ShutdownTracer: shutdownTracer,
	}
}
