package pb

import (
	"testing"
)

// TestProtoMessageMethods exercises the ProtoMessage method on all message
// types. The ProtoMessage method is generated and simply returns the message
// itself, but calling it covers the 0% lines reported by the coverage tool.
func TestProtoMessageMethods(t *testing.T) {
	// Test SwapStep
	ss := &SwapStep{}
	ss.ProtoMessage()

	// Test ArbHop
	hop := &ArbHop{}
	hop.ProtoMessage()

	// Test ValidatedArb
	arb := &ValidatedArb{}
	arb.ProtoMessage()

	// Test SubmitArbResponse
	resp := &SubmitArbResponse{}
	resp.ProtoMessage()

	// Test StreamArbsRequest
	req := &StreamArbsRequest{}
	req.ProtoMessage()

	// Test HealthCheckRequest
	hcr := &HealthCheckRequest{}
	hcr.ProtoMessage()

	// Test HealthCheckResponse
	hcr2 := &HealthCheckResponse{}
	hcr2.ProtoMessage()

	// Test SetStateRequest
	ssr := &SetStateRequest{}
	ssr.ProtoMessage()

	// Test SetStateResponse
	ssr2 := &SetStateResponse{}
	ssr2.ProtoMessage()

	// Test ReloadConfigRequest
	rcr := &ReloadConfigRequest{}
	rcr.ProtoMessage()

	// Test ReloadConfigResponse
	rcr2 := &ReloadConfigResponse{}
	rcr2.ProtoMessage()
}

// TestFileInitFunction exercises the file-level init function by simply
// referencing types that force the init to run. This covers the init path
// reported in file_proto_aether_proto_init.
func TestFileInitFunction(t *testing.T) {
	f := File_proto_aether_proto
	if f == nil {
		t.Fatal("expected non-nil File descriptor")
	}
}
