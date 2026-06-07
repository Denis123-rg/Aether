package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// runMonitorService starts metrics/dashboard servers and blocks until ctx is
// cancelled. Extracted from main() for unit tests.
func runMonitorService(ctx context.Context) error {
	setup := runMonitorSetup()
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := setup.ShutdownTracer(flushCtx); err != nil {
			slog.Warn("tracer shutdown error", "err", err)
		}
	}()

	errCh := make(chan error, 2)
	go func() {
		if err := setup.Metrics.ServeMetrics(":" + setup.MetricsPort); err != nil {
			errCh <- fmt.Errorf("metrics server: %w", err)
		}
	}()
	go func() {
		if err := setup.Dashboard.ServeDashboard(":" + setup.DashboardPort); err != nil {
			errCh <- fmt.Errorf("dashboard server: %w", err)
		}
	}()

	slog.Info("monitor service started")
	slog.Info("metrics endpoint", "url", fmt.Sprintf("http://localhost:%s/metrics", setup.MetricsPort))
	slog.Info("dashboard endpoint", "url", fmt.Sprintf("http://localhost:%s/", setup.DashboardPort))
	setup.Alerter.Send(SeverityInfo, "System Started", "Aether monitor service started")

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}
