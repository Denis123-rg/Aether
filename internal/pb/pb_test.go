package pb

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type resetStringer interface {
	String() string
	Reset()
	ProtoMessage()
}

type descriptor interface {
	Descriptor() ([]byte, []int)
}

type protoReflecter interface {
	ProtoReflect() protoreflect.Message
}

// TestMessageGettersAndString exercises the generated proto message helpers.
func TestMessageGettersAndString(t *testing.T) {
	msgs := []resetStringer{
		&SwapStep{
			Protocol:     ProtocolType_UNISWAP_V2,
			PoolAddress:  []byte{0x01},
			TokenIn:      []byte{0xAA},
			TokenOut:     []byte{0xBB},
			AmountIn:     []byte{0x01, 0x00},
			MinAmountOut: []byte{0x02, 0x00},
			Calldata:     []byte{0xab},
		},
		&ArbHop{
			Protocol:     ProtocolType_SUSHISWAP,
			PoolAddress:  []byte{0x02},
			TokenIn:      []byte{0xBB},
			TokenOut:     []byte{0xCC},
			AmountIn:     []byte{0x03, 0x00},
			ExpectedOut:  []byte{0x04, 0x00},
			EstimatedGas: 21000,
		},
		&ValidatedArb{
			Id:              "arb-001",
			Hops:            []*ArbHop{{Protocol: ProtocolType_CURVE}},
			TotalProfitWei:  []byte{0x05},
			TotalGas:        300000,
			GasCostWei:      []byte{0x06},
			NetProfitWei:    []byte{0x07},
			BlockNumber:     18000000,
			TimestampNs:     1234567890,
			FlashloanToken:  []byte{0xDD},
			FlashloanAmount: []byte{0x08},
			Steps:           []*SwapStep{{}},
			Calldata:        []byte{0xef},
			Source:          ArbSource_BLOCK_DRIVEN,
			VictimTxHash:    []byte{0x11},
			TargetBlock:     18000001,
			VictimRawTx:     []byte{0x22},
		},
		&SubmitArbResponse{Accepted: true, BundleHash: "0xhash", Error: ""},
		&StreamArbsRequest{MinProfitEth: 0.001},
		&HealthCheckRequest{},
		&HealthCheckResponse{Healthy: true, Status: "RUNNING", UptimeSeconds: 1, LastBlock: 100, ActivePools: 5},
		&SetStateRequest{State: SystemState_PAUSED, Reason: "test"},
		&SetStateResponse{Success: true, PreviousState: SystemState_RUNNING},
		&ReloadConfigRequest{ConfigPath: "/tmp/config.toml"},
		&ReloadConfigResponse{Success: true, PoolsLoaded: 10, Error: ""},
	}

	for _, m := range msgs {
		_ = m.String()
		if pr, ok := m.(protoReflecter); ok {
			_ = pr.ProtoReflect()
		}
		if d, ok := m.(descriptor); ok {
			_, _ = d.Descriptor()
		}
		m.ProtoMessage()
		m.Reset()
	}

	// Exercise the generated getters while the messages are still populated.
	arb := &ValidatedArb{
		Id:              "arb-001",
		Hops:            []*ArbHop{{Protocol: ProtocolType_CURVE}},
		TotalProfitWei:  []byte{0x05},
		TotalGas:        300000,
		GasCostWei:      []byte{0x06},
		NetProfitWei:    []byte{0x07},
		BlockNumber:     18000000,
		TimestampNs:     1234567890,
		FlashloanToken:  []byte{0xDD},
		FlashloanAmount: []byte{0x08},
		Steps:           []*SwapStep{{}},
		Calldata:        []byte{0xef},
		Source:          ArbSource_BLOCK_DRIVEN,
		VictimTxHash:    []byte{0x11},
		TargetBlock:     18000001,
		VictimRawTx:     []byte{0x22},
	}
	_ = arb.GetId()
	_ = arb.GetHops()
	_ = arb.GetTotalProfitWei()
	_ = arb.GetTotalGas()
	_ = arb.GetGasCostWei()
	_ = arb.GetNetProfitWei()
	_ = arb.GetBlockNumber()
	_ = arb.GetTimestampNs()
	_ = arb.GetFlashloanToken()
	_ = arb.GetFlashloanAmount()
	_ = arb.GetSteps()
	_ = arb.GetCalldata()
	_ = arb.GetSource()
	_ = arb.GetVictimTxHash()
	_ = arb.GetTargetBlock()
	_ = arb.GetVictimRawTx()

	hop := &ArbHop{
		Protocol:     ProtocolType_SUSHISWAP,
		PoolAddress:  []byte{0x02},
		TokenIn:      []byte{0xBB},
		TokenOut:     []byte{0xCC},
		AmountIn:     []byte{0x03, 0x00},
		ExpectedOut:  []byte{0x04, 0x00},
		EstimatedGas: 21000,
	}
	_ = hop.GetProtocol()
	_ = hop.GetPoolAddress()
	_ = hop.GetTokenIn()
	_ = hop.GetTokenOut()
	_ = hop.GetAmountIn()
	_ = hop.GetExpectedOut()
	_ = hop.GetEstimatedGas()

	step := &SwapStep{
		Protocol:     ProtocolType_UNISWAP_V2,
		PoolAddress:  []byte{0x01},
		TokenIn:      []byte{0xAA},
		TokenOut:     []byte{0xBB},
		AmountIn:     []byte{0x01, 0x00},
		MinAmountOut: []byte{0x02, 0x00},
		Calldata:     []byte{0xab},
	}
	_ = step.GetProtocol()
	_ = step.GetPoolAddress()
	_ = step.GetTokenIn()
	_ = step.GetTokenOut()
	_ = step.GetAmountIn()
	_ = step.GetMinAmountOut()
	_ = step.GetCalldata()
}

// TestEnumMethods exercises generated enum helpers.
func TestEnumMethods(t *testing.T) {
	for _, e := range []ProtocolType{ProtocolType_PROTOCOL_UNKNOWN, ProtocolType_UNISWAP_V2, ProtocolType_UNISWAP_V3, ProtocolType_SUSHISWAP, ProtocolType_CURVE, ProtocolType_BALANCER_V2, ProtocolType_BANCOR_V3, ProtocolType(99)} {
		_ = e.String()
		_ = e.Descriptor()
		_ = e.Number()
		_ = e.Type()
	}
	for _, e := range []ArbSource{ArbSource_SOURCE_UNKNOWN, ArbSource_BLOCK_DRIVEN, ArbSource_MEMPOOL_BACKRUN, ArbSource(99)} {
		_ = e.String()
		_ = e.Descriptor()
		_ = e.Number()
		_ = e.Type()
	}
	for _, e := range []SystemState{SystemState_STATE_UNKNOWN, SystemState_RUNNING, SystemState_DEGRADED, SystemState_PAUSED, SystemState_HALTED, SystemState(99)} {
		_ = e.String()
		_ = e.Descriptor()
		_ = e.Number()
		_ = e.Type()
	}
}

type mockArbServer struct{ UnimplementedArbServiceServer }
type mockHealthServer struct{ UnimplementedHealthServiceServer }
type mockControlServer struct{ UnimplementedControlServiceServer }

func (s *mockArbServer) SubmitArb(_ context.Context, arb *ValidatedArb) (*SubmitArbResponse, error) {
	return &SubmitArbResponse{Accepted: true, BundleHash: arb.Id}, nil
}

func (s *mockArbServer) StreamArbs(req *StreamArbsRequest, stream ArbService_StreamArbsServer) error {
	_ = req.GetMinProfitEth()
	return nil
}

func (s *mockHealthServer) Check(_ context.Context, _ *HealthCheckRequest) (*HealthCheckResponse, error) {
	return &HealthCheckResponse{Healthy: true}, nil
}

func (s *mockControlServer) SetState(_ context.Context, req *SetStateRequest) (*SetStateResponse, error) {
	_ = req.GetState()
	return &SetStateResponse{Success: true}, nil
}

func (s *mockControlServer) ReloadConfig(_ context.Context, req *ReloadConfigRequest) (*ReloadConfigResponse, error) {
	_ = req.GetConfigPath()
	return &ReloadConfigResponse{Success: true}, nil
}

// TestGRPCClientsAndHandlers exercises the generated gRPC client/server glue.
func TestGRPCClientsAndHandlers(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
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
	stream, _ := arbClient.StreamArbs(ctx, &StreamArbsRequest{MinProfitEth: 0.001})
	if stream != nil {
		_, _ = stream.Recv()
	}

	healthClient := NewHealthServiceClient(conn)
	_, _ = healthClient.Check(ctx, &HealthCheckRequest{})

	ctrlClient := NewControlServiceClient(conn)
	_, _ = ctrlClient.SetState(ctx, &SetStateRequest{State: SystemState_RUNNING})
	_, _ = ctrlClient.ReloadConfig(ctx, &ReloadConfigRequest{ConfigPath: "c.toml"})
}
