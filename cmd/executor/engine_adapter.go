package main

import (
	"context"

	aethergrpc "github.com/aether-arb/aether/internal/grpc"
)

// grpcEngineAdapter wraps a gRPC client for admin pause/resume.
type grpcEngineAdapter struct {
	client *aethergrpc.Client
}

func newGRPCEngineAdapter(c *aethergrpc.Client) *grpcEngineAdapter {
	if c == nil {
		return nil
	}
	return &grpcEngineAdapter{client: c}
}

func (a *grpcEngineAdapter) SetEngineState(ctx context.Context, paused bool) error {
	if a == nil || a.client == nil {
		return nil
	}
	_, err := a.client.SetEngineState(ctx, paused)
	return err
}
