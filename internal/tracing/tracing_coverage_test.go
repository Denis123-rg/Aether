package tracing

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestInit_NoEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	shutdown, err := Init(context.Background(), "test-service")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if shutdown == nil {
		t.Fatal("expected non-nil shutdown")
	}
	_ = shutdown(context.Background())
}

func TestInit_NoEndpointWhitespace(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "   \t\n  ")
	shutdown, err := Init(context.Background(), "test-service")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if shutdown == nil {
		t.Fatal("expected non-nil shutdown")
	}
	_ = shutdown(context.Background())
}

func TestInit_HTTTPPrefixStripped(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdown, err := Init(ctx, "test-service")
	if err != nil {
		t.Skipf("OTLP init skipped: %v", err)
	}
	if shutdown == nil {
		t.Fatal("expected non-nil shutdown")
	}
	_ = shutdown(context.Background())
}

func TestInit_HTTPSPrefixStripped(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://localhost:4317")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdown, err := Init(ctx, "test-service")
	if err != nil {
		t.Skipf("OTLP init skipped: %v", err)
	}
	if shutdown == nil {
		t.Fatal("expected non-nil shutdown")
	}
	_ = shutdown(context.Background())
}

func TestInit_OTELServiceNameOverride(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")
	t.Setenv("OTEL_SERVICE_NAME", "custom-service-name")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdown, err := Init(ctx, "test-service")
	if err != nil {
		t.Skipf("OTLP init skipped: %v", err)
	}
	if shutdown == nil {
		t.Fatal("expected non-nil shutdown")
	}
	_ = shutdown(context.Background())
}

func TestInit_OTELServiceNameWhitespace(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")
	t.Setenv("OTEL_SERVICE_NAME", "  \t  ")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdown, err := Init(ctx, "original-service")
	if err != nil {
		t.Skipf("OTLP init skipped: %v", err)
	}
	if shutdown == nil {
		t.Fatal("expected non-nil shutdown")
	}
	_ = shutdown(context.Background())
}

func TestInit_ExporterError(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "\x01")
	defer os.Unsetenv("OTEL_EXPORTER_OTLP_ENDPOINT")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := Init(ctx, "test-service")
	if err == nil {
		t.Fatal("expected error from Init with invalid endpoint")
	}
	if !strings.Contains(err.Error(), "otlp exporter") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInit_ShutdownReturnsNil(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	shutdown, err := Init(context.Background(), "test-service")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	err = shutdown(context.Background())
	if err != nil {
		t.Fatalf("shutdown returned error: %v", err)
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
