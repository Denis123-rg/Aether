package main

import (
	"os"
	"testing"
)

func TestMonitorDefaultPort_8090(t *testing.T) {
	os.Unsetenv("MONITOR_HTTP_PORT")
	os.Unsetenv("DASHBOARD_PORT")
	setup := runMonitorSetup()
	if setup.DashboardPort != "8090" {
		t.Fatalf("port %s", setup.DashboardPort)
	}
}

func TestMonitorPort_EnvOverride(t *testing.T) {
	t.Setenv("MONITOR_HTTP_PORT", "8091")
	setup := runMonitorSetup()
	if setup.DashboardPort != "8091" {
		t.Fatalf("port %s", setup.DashboardPort)
	}
}

func TestMonitorMetricsPort_Default9090(t *testing.T) {
	os.Unsetenv("METRICS_PORT")
	setup := runMonitorSetup()
	if setup.MetricsPort != "9090" {
		t.Fatalf("metrics port %s", setup.MetricsPort)
	}
}

func TestExecutorAndMonitorPortsDistinct(t *testing.T) {
	setup := runMonitorSetup()
	if setup.DashboardPort == "8080" {
		t.Fatal("monitor should not use 8080 by default")
	}
}
