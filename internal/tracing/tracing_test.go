package tracing

import (
	"context"
	"os"
	"testing"
)

func TestInit_NoEndpoint_ReturnsNoOpShutdown(t *testing.T) {
	os.Unsetenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	shutdown, err := Init(context.Background(), "test-service")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if shutdown == nil {
		t.Fatal("expected non-nil shutdown function")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("no-op shutdown should not error: %v", err)
	}
}

func TestInit_WithInvalidEndpoint_ReturnsError(t *testing.T) {
	// Set an endpoint that is not a valid gRPC target so the exporter fails.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "not-a-valid-host::99999")
	_, err := Init(context.Background(), "test-service")
	if err == nil {
		t.Fatal("expected error for invalid OTLP endpoint")
	}
}

func TestInit_ServiceNameOverride(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "overridden-service")
	// Keep endpoint empty so we only exercise the override path without dialing.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	shutdown, err := Init(context.Background(), "test-service")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = shutdown(context.Background())
}
