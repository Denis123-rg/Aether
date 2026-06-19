package grpc

import (
	"testing"
)

func TestDialWithOptions_GRPCNewClientError(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TCP", "true")
	// Address with a NUL byte: passes validateDialTarget (net.SplitHostPort
	// accepts it) and buildTransportCredentials (returns insecure creds), but
	// causes grpc.NewClient to fail because url.Parse rejects control chars.
	_, err := DialWithOptions("localhost\x00:50051", DialOptions{})
	if err == nil {
		t.Fatal("expected error from grpc.NewClient for address with control character")
	}
}
