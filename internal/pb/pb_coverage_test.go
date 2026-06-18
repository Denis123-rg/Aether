package pb

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// --- Enum .Enum() and .EnumDescriptor() ---

func TestProtocolTypeEnumMethods(t *testing.T) {
	for _, e := range []ProtocolType{
		ProtocolType_PROTOCOL_UNKNOWN,
		ProtocolType_UNISWAP_V2,
		ProtocolType_UNISWAP_V3,
		ProtocolType_SUSHISWAP,
		ProtocolType_CURVE,
		ProtocolType_BALANCER_V2,
		ProtocolType_BANCOR_V3,
		ProtocolType(99),
	} {
		p := e.Enum()
		if p == nil || *p != e {
			t.Errorf("Enum() returned wrong value for %v", e)
		}
		b, idx := e.EnumDescriptor()
		if len(b) == 0 || len(idx) == 0 {
			t.Errorf("EnumDescriptor() returned empty for %v", e)
		}
	}
}

func TestSystemStateEnumMethods(t *testing.T) {
	for _, e := range []SystemState{
		SystemState_STATE_UNKNOWN,
		SystemState_RUNNING,
		SystemState_DEGRADED,
		SystemState_PAUSED,
		SystemState_HALTED,
		SystemState(99),
	} {
		p := e.Enum()
		if p == nil || *p != e {
			t.Errorf("Enum() returned wrong value for %v", e)
		}
		b, idx := e.EnumDescriptor()
		if len(b) == 0 || len(idx) == 0 {
			t.Errorf("EnumDescriptor() returned empty for %v", e)
		}
	}
}

func TestArbSourceEnumMethods(t *testing.T) {
	for _, e := range []ArbSource{
		ArbSource_SOURCE_UNKNOWN,
		ArbSource_BLOCK_DRIVEN,
		ArbSource_MEMPOOL_BACKRUN,
		ArbSource(99),
	} {
		p := e.Enum()
		if p == nil || *p != e {
			t.Errorf("Enum() returned wrong value for %v", e)
		}
		b, idx := e.EnumDescriptor()
		if len(b) == 0 || len(idx) == 0 {
			t.Errorf("EnumDescriptor() returned empty for %v", e)
		}
	}
}

// --- Getters on nil receivers (covers nil branch) ---

func TestNilSwapStepGetters(t *testing.T) {
	var s *SwapStep
	_ = s.GetProtocol()
	_ = s.GetPoolAddress()
	_ = s.GetTokenIn()
	_ = s.GetTokenOut()
	_ = s.GetAmountIn()
	_ = s.GetMinAmountOut()
	_ = s.GetCalldata()
}

func TestNilArbHopGetters(t *testing.T) {
	var h *ArbHop
	_ = h.GetProtocol()
	_ = h.GetPoolAddress()
	_ = h.GetTokenIn()
	_ = h.GetTokenOut()
	_ = h.GetAmountIn()
	_ = h.GetExpectedOut()
	_ = h.GetEstimatedGas()
}

func TestNilValidatedArbGetters(t *testing.T) {
	var a *ValidatedArb
	_ = a.GetId()
	_ = a.GetHops()
	_ = a.GetTotalProfitWei()
	_ = a.GetTotalGas()
	_ = a.GetGasCostWei()
	_ = a.GetNetProfitWei()
	_ = a.GetBlockNumber()
	_ = a.GetTimestampNs()
	_ = a.GetFlashloanToken()
	_ = a.GetFlashloanAmount()
	_ = a.GetSteps()
	_ = a.GetCalldata()
	_ = a.GetSource()
	_ = a.GetVictimTxHash()
	_ = a.GetTargetBlock()
	_ = a.GetVictimRawTx()
}

func TestNilSubmitArbResponseGetters(t *testing.T) {
	var r *SubmitArbResponse
	_ = r.GetAccepted()
	_ = r.GetBundleHash()
	_ = r.GetError()
}

func TestNilStreamArbsRequestGetters(t *testing.T) {
	var r *StreamArbsRequest
	_ = r.GetMinProfitEth()
}

func TestNilHealthCheckResponseGetters(t *testing.T) {
	var r *HealthCheckResponse
	_ = r.GetHealthy()
	_ = r.GetStatus()
	_ = r.GetUptimeSeconds()
	_ = r.GetLastBlock()
	_ = r.GetActivePools()
}

func TestNilSetStateRequestGetters(t *testing.T) {
	var r *SetStateRequest
	_ = r.GetState()
	_ = r.GetReason()
}

func TestNilSetStateResponseGetters(t *testing.T) {
	var r *SetStateResponse
	_ = r.GetSuccess()
	_ = r.GetPreviousState()
}

func TestNilReloadConfigRequestGetters(t *testing.T) {
	var r *ReloadConfigRequest
	_ = r.GetConfigPath()
}

func TestNilReloadConfigResponseGetters(t *testing.T) {
	var r *ReloadConfigResponse
	_ = r.GetSuccess()
	_ = r.GetPoolsLoaded()
	_ = r.GetError()
}

// --- Populate getters on small messages not covered by TestMessageGettersAndString ---

func TestPopulatedSmallMessageGetters(t *testing.T) {
	sar := &SubmitArbResponse{Accepted: true, BundleHash: "0xabc", Error: "fail"}
	if !sar.GetAccepted() || sar.GetBundleHash() != "0xabc" || sar.GetError() != "fail" {
		t.Error("SubmitArbResponse getters returned wrong values")
	}

	hcr := &HealthCheckResponse{
		Healthy:       true,
		Status:        "RUNNING",
		UptimeSeconds: 3600,
		LastBlock:     20000000,
		ActivePools:   50,
	}
	if !hcr.GetHealthy() || hcr.GetStatus() != "RUNNING" || hcr.GetUptimeSeconds() != 3600 || hcr.GetLastBlock() != 20000000 || hcr.GetActivePools() != 50 {
		t.Error("HealthCheckResponse getters returned wrong values")
	}

	ssr := &SetStateResponse{Success: true, PreviousState: SystemState_RUNNING}
	if !ssr.GetSuccess() || ssr.GetPreviousState() != SystemState_RUNNING {
		t.Error("SetStateResponse getters returned wrong values")
	}

	ssrq := &SetStateRequest{State: SystemState_PAUSED, Reason: "maintenance"}
	if ssrq.GetState() != SystemState_PAUSED || ssrq.GetReason() != "maintenance" {
		t.Error("SetStateRequest getters returned wrong values")
	}

	rr := &ReloadConfigResponse{Success: true, PoolsLoaded: 42, Error: "none"}
	if !rr.GetSuccess() || rr.GetPoolsLoaded() != 42 || rr.GetError() != "none" {
		t.Error("ReloadConfigResponse getters returned wrong values")
	}
}

// --- ProtoReflect on nil receivers ---

func TestNilProtoReflect(t *testing.T) {
	var nilStep *SwapStep
	_ = nilStep.ProtoReflect()

	var nilHop *ArbHop
	_ = nilHop.ProtoReflect()

	var nilArb *ValidatedArb
	_ = nilArb.ProtoReflect()

	var nilResp *SubmitArbResponse
	_ = nilResp.ProtoReflect()

	var nilStreamReq *StreamArbsRequest
	_ = nilStreamReq.ProtoReflect()

	var nilHealthReq *HealthCheckRequest
	_ = nilHealthReq.ProtoReflect()

	var nilHealthResp *HealthCheckResponse
	_ = nilHealthResp.ProtoReflect()

	var nilSetReq *SetStateRequest
	_ = nilSetReq.ProtoReflect()

	var nilSetResp *SetStateResponse
	_ = nilSetResp.ProtoReflect()

	var nilReloadReq *ReloadConfigRequest
	_ = nilReloadReq.ProtoReflect()

	var nilReloadResp *ReloadConfigResponse
	_ = nilReloadResp.ProtoReflect()
}

// --- Unimplemented server methods ---

func TestUnimplementedArbServiceServer(t *testing.T) {
	s := &UnimplementedArbServiceServer{}
	_, err := s.SubmitArb(context.Background(), &ValidatedArb{Id: "test"})
	if err == nil {
		t.Error("expected error from unimplemented SubmitArb")
	}
	if c := status.Code(err); c != codes.Unimplemented {
		t.Errorf("expected Unimplemented, got %v", c)
	}

	err = s.StreamArbs(&StreamArbsRequest{}, nil)
	if err == nil {
		t.Error("expected error from unimplemented StreamArbs")
	}
	if c := status.Code(err); c != codes.Unimplemented {
		t.Errorf("expected Unimplemented, got %v", c)
	}
}

func TestUnimplementedHealthServiceServer(t *testing.T) {
	s := &UnimplementedHealthServiceServer{}
	_, err := s.Check(context.Background(), &HealthCheckRequest{})
	if err == nil {
		t.Error("expected error from unimplemented Check")
	}
	if c := status.Code(err); c != codes.Unimplemented {
		t.Errorf("expected Unimplemented, got %v", c)
	}
}

func TestUnimplementedControlServiceServer(t *testing.T) {
	s := &UnimplementedControlServiceServer{}

	_, err := s.SetState(context.Background(), &SetStateRequest{State: SystemState_RUNNING})
	if err == nil {
		t.Error("expected error from unimplemented SetState")
	}
	if c := status.Code(err); c != codes.Unimplemented {
		t.Errorf("expected Unimplemented, got %v", c)
	}

	_, err = s.ReloadConfig(context.Background(), &ReloadConfigRequest{ConfigPath: "c.toml"})
	if err == nil {
		t.Error("expected error from unimplemented ReloadConfig")
	}
	if c := status.Code(err); c != codes.Unimplemented {
		t.Errorf("expected Unimplemented, got %v", c)
	}
}

// --- gRPC with unary interceptor (covers interceptor branches in handlers) ---

func TestGRPCWithUnaryInterceptor(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	interceptorCalled := 0
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			interceptorCalled++
			return handler(ctx, req)
		}),
	)
	RegisterArbServiceServer(srv, &mockArbServer{})
	RegisterHealthServiceServer(srv, &mockHealthServer{})
	RegisterControlServiceServer(srv, &mockControlServer{})
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	ctx := context.Background()
	conn, err := grpc.DialContext(ctx, "bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	arbClient := NewArbServiceClient(conn)
	_, _ = arbClient.SubmitArb(ctx, &ValidatedArb{Id: "x"})

	healthClient := NewHealthServiceClient(conn)
	_, _ = healthClient.Check(ctx, &HealthCheckRequest{})

	ctrlClient := NewControlServiceClient(conn)
	_, _ = ctrlClient.SetState(ctx, &SetStateRequest{State: SystemState_RUNNING})
	_, _ = ctrlClient.ReloadConfig(ctx, &ReloadConfigRequest{ConfigPath: "c.toml"})

	if interceptorCalled != 4 {
		t.Errorf("interceptor called %d times, expected 4", interceptorCalled)
	}
}

// --- gRPC with stream interceptor ---

func TestGRPCWithStreamInterceptor(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	streamInterceptorCalled := 0
	srv := grpc.NewServer(
		grpc.StreamInterceptor(func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			streamInterceptorCalled++
			return handler(srv, ss)
		}),
	)
	RegisterArbServiceServer(srv, &mockArbServer{})
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	ctx := context.Background()
	conn, err := grpc.DialContext(ctx, "bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	arbClient := NewArbServiceClient(conn)
	stream, err := arbClient.StreamArbs(ctx, &StreamArbsRequest{MinProfitEth: 0.001})
	if err != nil {
		t.Fatalf("StreamArbs: %v", err)
	}
	_, _ = stream.Recv()

	if streamInterceptorCalled != 1 {
		t.Errorf("stream interceptor called %d times, expected 1", streamInterceptorCalled)
	}
}

// --- Service descriptor access ---

func TestServiceDescriptors(t *testing.T) {
	if ArbService_ServiceDesc.ServiceName != "aether.ArbService" {
		t.Errorf("unexpected ArbService name: %s", ArbService_ServiceDesc.ServiceName)
	}
	if HealthService_ServiceDesc.ServiceName != "aether.HealthService" {
		t.Errorf("unexpected HealthService name: %s", HealthService_ServiceDesc.ServiceName)
	}
	if ControlService_ServiceDesc.ServiceName != "aether.ControlService" {
		t.Errorf("unexpected ControlService name: %s", ControlService_ServiceDesc.ServiceName)
	}

	if len(ArbService_ServiceDesc.Methods) != 1 {
		t.Errorf("expected 1 method in ArbService, got %d", len(ArbService_ServiceDesc.Methods))
	}
	if len(ArbService_ServiceDesc.Streams) != 1 {
		t.Errorf("expected 1 stream in ArbService, got %d", len(ArbService_ServiceDesc.Streams))
	}
	if len(HealthService_ServiceDesc.Methods) != 1 {
		t.Errorf("expected 1 method in HealthService, got %d", len(HealthService_ServiceDesc.Methods))
	}
	if len(ControlService_ServiceDesc.Methods) != 2 {
		t.Errorf("expected 2 methods in ControlService, got %d", len(ControlService_ServiceDesc.Methods))
	}

	if ArbService_SubmitArb_FullMethodName != "/aether.ArbService/SubmitArb" {
		t.Errorf("unexpected SubmitArb method name: %s", ArbService_SubmitArb_FullMethodName)
	}
	if ArbService_StreamArbs_FullMethodName != "/aether.ArbService/StreamArbs" {
		t.Errorf("unexpected StreamArbs method name: %s", ArbService_StreamArbs_FullMethodName)
	}
	if HealthService_Check_FullMethodName != "/aether.HealthService/Check" {
		t.Errorf("unexpected Check method name: %s", HealthService_Check_FullMethodName)
	}
	if ControlService_SetState_FullMethodName != "/aether.ControlService/SetState" {
		t.Errorf("unexpected SetState method name: %s", ControlService_SetState_FullMethodName)
	}
	if ControlService_ReloadConfig_FullMethodName != "/aether.ControlService/ReloadConfig" {
		t.Errorf("unexpected ReloadConfig method name: %s", ControlService_ReloadConfig_FullMethodName)
	}
}

// --- Registration functions ---

func TestRegisterServers(t *testing.T) {
	srv := grpc.NewServer()
	RegisterArbServiceServer(srv, &mockArbServer{})
	RegisterHealthServiceServer(srv, &mockHealthServer{})
	RegisterControlServiceServer(srv, &mockControlServiceServer{})
	srv.Stop()
}

type mockControlServiceServer struct {
	UnimplementedControlServiceServer
}

// --- Error paths in gRPC client (invoke with bad context) ---

func TestGRPCClientsErrorPaths(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	conn, err := grpc.DialContext(ctx, "bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return nil, net.ErrClosed
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	arbClient := NewArbServiceClient(conn)
	_, err = arbClient.SubmitArb(ctx, &ValidatedArb{Id: "x"})
	if err == nil {
		t.Error("expected error from SubmitArb with cancelled context")
	}

	stream, err := arbClient.StreamArbs(ctx, &StreamArbsRequest{MinProfitEth: 0.001})
	if err == nil {
		_, _ = stream.Recv()
	}

	healthClient := NewHealthServiceClient(conn)
	_, err = healthClient.Check(ctx, &HealthCheckRequest{})
	if err == nil {
		t.Error("expected error from Check with cancelled context")
	}

	ctrlClient := NewControlServiceClient(conn)
	_, err = ctrlClient.SetState(ctx, &SetStateRequest{State: SystemState_RUNNING})
	if err == nil {
		t.Error("expected error from SetState with cancelled context")
	}
	_, err = ctrlClient.ReloadConfig(ctx, &ReloadConfigRequest{ConfigPath: "c.toml"})
	if err == nil {
		t.Error("expected error from ReloadConfig with cancelled context")
	}
}
