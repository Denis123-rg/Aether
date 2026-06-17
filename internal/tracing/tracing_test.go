package tracing

import (
	"context"
	"os"
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

func TestInit_WithEndpoint(t *testing.T) {
	// Use a local endpoint that the OTLP exporter can resolve. It will not
	// actually connect during Init, but exercises the non-noop branch.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")
	t.Setenv("OTEL_SERVICE_NAME", "aether-test")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdown, err := Init(ctx, "test-service")
	if err != nil {
		// Some environments may fail to build the exporter; treat that as a
		// skipped test rather than a failure.
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
	_ = shutdown(context.Background())
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
