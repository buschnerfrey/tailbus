package daemon

import (
	"context"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
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
