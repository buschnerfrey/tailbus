package daemon

import (
	"context"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	"github.com/alexanderfrey/tailbus/internal/handle"
	"github.com/alexanderfrey/tailbus/internal/session"
	"github.com/alexanderfrey/tailbus/internal/transport"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func startTestAgentServer(t *testing.T) (*AgentServer, string, func()) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	sessions := session.NewStore()
	activity := NewActivityBus()

	resolver := handle.NewResolver()
	tp := transport.NewGRPCTransport(logger, nil, nil)

	srv := NewAgentServer(sessions, nil, activity, logger)
	srv.SetDashboardDeps("test-node", resolver, tp)
	router := NewMessageRouter(resolver, tp, srv, activity, logger)
	srv.SetRouter(router)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.ServeTCP(lis)

	cleanup := func() {
		srv.GracefulStop()
		tp.Close()
	}
	return srv, lis.Addr().String(), cleanup
}

func dialAgent(t *testing.T, addr string) (agentpb.AgentAPIClient, func()) {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	return agentpb.NewAgentAPIClient(conn), func() { conn.Close() }
}

type fakeDashboardRoomService struct {
	rooms []*messagepb.RoomInfo
}

type fakeUsageHistoryProvider struct {
	history *agentpb.UsageHistory
}

func (f *fakeUsageHistoryProvider) LoadUsageHistory() (*agentpb.UsageHistory, error) {
	return f.history, nil
}

func (f *fakeDashboardRoomService) CreateRoom(context.Context, string, string, []string) (*messagepb.RoomInfo, error) {
	return nil, nil
}
func (f *fakeDashboardRoomService) JoinRoom(context.Context, string, string) (*messagepb.RoomInfo, error) {
	return nil, nil
}
func (f *fakeDashboardRoomService) LeaveRoom(context.Context, string, string) (*messagepb.RoomInfo, error) {
	return nil, nil
}
func (f *fakeDashboardRoomService) PostMessage(context.Context, string, string, []byte, string, string) (*messagepb.RoomEvent, error) {
	return nil, nil
}
func (f *fakeDashboardRoomService) ListRooms(context.Context, string) ([]*messagepb.RoomInfo, error) {
	return nil, nil
}
func (f *fakeDashboardRoomService) ListMembers(context.Context, string, string) ([]string, error) {
	return nil, nil
}
func (f *fakeDashboardRoomService) Replay(context.Context, string, string, uint64) ([]*messagepb.RoomEvent, error) {
	return nil, nil
}
func (f *fakeDashboardRoomService) CloseRoom(context.Context, string, string) (*messagepb.RoomInfo, error) {
	return nil, nil
}
func (f *fakeDashboardRoomService) DashboardRooms(handles []string) ([]*messagepb.RoomInfo, error) {
	_ = handles
	return f.rooms, nil
}

func TestHandleBindingToConnection(t *testing.T) {
	_, addr, cleanup := startTestAgentServer(t)
	defer cleanup()

	ctx := context.Background()

	// Connection A registers "marketing"
	clientA, closeA := dialAgent(t, addr)
	defer closeA()
	resp, err := clientA.Register(ctx, &agentpb.RegisterRequest{Handle: "marketing"})
	if err != nil || !resp.Ok {
		t.Fatalf("register failed: %v %+v", err, resp)
	}

	// Connection B tries to send as "marketing" — should fail
	clientB, closeB := dialAgent(t, addr)
	defer closeB()

	// First register a handle on B so B has something, then try to send as marketing
	respB, err := clientB.Register(ctx, &agentpb.RegisterRequest{Handle: "sales"})
	if err != nil || !respB.Ok {
		t.Fatalf("register sales failed: %v %+v", err, respB)
	}

	// B tries to open session as "marketing" — should fail with PermissionDenied
	_, err = clientB.OpenSession(ctx, &agentpb.OpenSessionRequest{
		FromHandle:  "marketing",
		ToHandle:    "sales",
		Payload:     []byte("test"),
		ContentType: "text/plain",
	})
	if err == nil {
		t.Fatal("expected error when non-owner sends as marketing")
	}
	if st, ok := status.FromError(err); !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestSubscribeRequiresOwnership(t *testing.T) {
	_, addr, cleanup := startTestAgentServer(t)
	defer cleanup()

	ctx := context.Background()

	// Connection A registers "marketing"
	clientA, closeA := dialAgent(t, addr)
	defer closeA()
	resp, err := clientA.Register(ctx, &agentpb.RegisterRequest{Handle: "marketing"})
	if err != nil || !resp.Ok {
		t.Fatalf("register failed: %v %+v", err, resp)
	}

	// Connection B tries to subscribe to "marketing" — should fail
	clientB, closeB := dialAgent(t, addr)
	defer closeB()

	stream, err := clientB.Subscribe(ctx, &agentpb.SubscribeRequest{Handle: "marketing"})
	if err != nil {
		// Some gRPC versions return error on Subscribe call
		if st, ok := status.FromError(err); ok && st.Code() == codes.PermissionDenied {
			return // expected
		}
		t.Fatalf("unexpected error: %v", err)
	}
	// Error may come on first Recv instead
	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected error when non-owner subscribes")
	}
	if st, ok := status.FromError(err); !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestDisconnectCleansHandles(t *testing.T) {
	srv, addr, cleanup := startTestAgentServer(t)
	defer cleanup()

	ctx := context.Background()

	// Connection A registers "marketing"
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	clientA := agentpb.NewAgentAPIClient(conn)
	resp, err := clientA.Register(ctx, &agentpb.RegisterRequest{Handle: "marketing"})
	if err != nil || !resp.Ok {
		t.Fatalf("register failed: %v %+v", err, resp)
	}

	// Verify handle exists
	if !srv.HasHandle("marketing") {
		t.Fatal("marketing should be registered")
	}

	// Disconnect by closing the gRPC connection
	conn.Close()

	// Wait for disconnect cleanup
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for handle cleanup")
		default:
			if !srv.HasHandle("marketing") {
				return // success
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func TestMultipleConnectionsIndependent(t *testing.T) {
	srv, addr, cleanup := startTestAgentServer(t)
	defer cleanup()

	ctx := context.Background()

	// Connection A registers "marketing"
	connA, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	clientA := agentpb.NewAgentAPIClient(connA)
	resp, err := clientA.Register(ctx, &agentpb.RegisterRequest{Handle: "marketing"})
	if err != nil || !resp.Ok {
		t.Fatalf("register marketing failed: %v", err)
	}

	// Connection B registers "sales"
	clientB, closeB := dialAgent(t, addr)
	defer closeB()
	resp, err = clientB.Register(ctx, &agentpb.RegisterRequest{Handle: "sales"})
	if err != nil || !resp.Ok {
		t.Fatalf("register sales failed: %v", err)
	}

	// Disconnect A
	connA.Close()

	// Wait for cleanup
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for marketing cleanup")
		default:
			if !srv.HasHandle("marketing") {
				// marketing gone, sales should still be there
				if !srv.HasHandle("sales") {
					t.Fatal("sales should still be registered")
				}
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func TestGetNodeStatusIncludesRooms(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := NewAgentServer(session.NewStore(), nil, NewActivityBus(), logger)
	srv.mu.Lock()
	srv.handles["orchestrator"] = true
	srv.subscribers["orchestrator"] = []chan *agentpb.IncomingMessage{make(chan *agentpb.IncomingMessage, 1)}
	srv.hstats["orchestrator"] = &handleStats{}
	srv.mu.Unlock()

	srv.SetRoomManager(&fakeDashboardRoomService{
		rooms: []*messagepb.RoomInfo{
			{
				RoomId:     "room-1",
				Title:      "pair-solver",
				CreatedBy:  "orchestrator",
				HomeNodeId: "test-node",
				Members:    []string{"orchestrator", "codex-solver"},
				Status:     "open",
				NextSeq:    4,
			},
		},
	})
	srv.SetUsageHistoryProvider(&fakeUsageHistoryProvider{
		history: &agentpb.UsageHistory{
			Metrics: []*agentpb.UsageMetricHistory{
				{
					Metric: agentpb.UsageMetric_USAGE_METRIC_MESSAGES_ROUTED,
					Total:  7,
					DailyBuckets: []*agentpb.UsageBucket{
						{BucketStartUnix: time.Date(2026, time.March, 3, 0, 0, 0, 0, time.UTC).Unix(), Count: 7},
					},
				},
			},
		},
	})

	statusResp, err := srv.GetNodeStatus(context.Background(), &agentpb.GetNodeStatusRequest{})
	if err != nil {
		t.Fatalf("get node status: %v", err)
	}
	if len(statusResp.Rooms) != 1 {
		t.Fatalf("rooms = %d, want 1", len(statusResp.Rooms))
	}
	if statusResp.Rooms[0].RoomId != "room-1" {
		t.Fatalf("room id = %q, want room-1", statusResp.Rooms[0].RoomId)
	}
	if statusResp.Usage == nil || len(statusResp.Usage.Metrics) != 1 {
		t.Fatalf("usage metrics = %v, want 1 metric", statusResp.Usage)
	}
	if statusResp.Usage.Metrics[0].Total != 7 {
		t.Fatalf("usage total = %d, want 7", statusResp.Usage.Metrics[0].Total)
	}
}

func TestFindHandlesMergesLocalAndRemote(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := NewAgentServer(session.NewStore(), nil, NewActivityBus(), logger)
	resolver := handle.NewResolver()
	resolver.UpdatePeerMap(map[string]handle.PeerInfo{
		"remote-solver": {
			Manifest: handle.ServiceManifest{
				Description:  "Remote solver",
				Capabilities: []string{"code.solve"},
				Domains:      []string{"engineering"},
				Tags:         []string{"deep"},
				Commands:     []handle.CommandSpec{{Name: "solve"}},
			},
		},
	})
	srv.SetDashboardDeps("test-node", resolver, nil)

	srv.mu.Lock()
	srv.handles["local-solver"] = true
	srv.manifests["local-solver"] = &messagepb.ServiceManifest{
		Description:  "Local solver",
		Capabilities: []string{"code.solve"},
		Domains:      []string{"engineering"},
		Tags:         []string{"fast"},
		Commands: []*messagepb.CommandSpec{
			{Name: "solve"},
		},
	}
	srv.mu.Unlock()

	resp, err := srv.FindHandles(context.Background(), &agentpb.FindHandlesRequest{
		Capabilities: []string{"code.solve"},
		Domains:      []string{"engineering"},
		CommandName:  "solve",
	})
	if err != nil {
		t.Fatalf("FindHandles returned error: %v", err)
	}
	if len(resp.Matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(resp.Matches))
	}
	if resp.Matches[0].Handle != "local-solver" {
		t.Fatalf("expected local-solver first, got %s", resp.Matches[0].Handle)
	}
	if resp.Matches[1].Handle != "remote-solver" {
		t.Fatalf("expected remote-solver second, got %s", resp.Matches[1].Handle)
	}
}
