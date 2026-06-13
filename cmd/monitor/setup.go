package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/aether-arb/aether/internal/config"
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

	prodPath := config.ProductionConfigPath()
	prodCfg, prodErr := config.LoadProductionConfig(prodPath)
	if prodErr != nil {
		slog.Warn("production.toml not loaded, using env-only alerting", "path", prodPath, "err", prodErr)
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
		if prodErr == nil && prodCfg.Monitor.Port > 0 {
			dashboardPort = fmt.Sprintf("%d", prodCfg.Monitor.Port)
		} else {
			dashboardPort = "8090"
		}
	}

	if prodErr == nil {
		if err := config.ValidateMonitorAlertingForProduction(prodCfg.Monitor.Alerting); err != nil {
			slog.Error("monitor alerting config invalid", "err", err)
			os.Exit(1)
		}
	} else if config.IsProductionEnv() {
		slog.Error("FATAL: production.toml required when AETHER_ENV=production")
		os.Exit(1)
	}

	metrics := NewMetrics()
	dashboard := NewDashboard(metrics)
	var alerter *Alerter
	if prodErr == nil {
		alerter = NewAlerterFromConfig([]AlertChannel{ChannelPagerDuty, ChannelTelegram, ChannelDiscord}, prodCfg.Monitor.Alerting)
	} else {
		alerter = NewAlerter([]AlertChannel{ChannelPagerDuty, ChannelTelegram, ChannelDiscord})
	}

	return MonitorSetup{
		Metrics:        metrics,
		Dashboard:      dashboard,
		Alerter:        alerter,
		MetricsPort:    metricsPort,
		DashboardPort:  dashboardPort,
		ShutdownTracer: shutdownTracer,
	}
}
