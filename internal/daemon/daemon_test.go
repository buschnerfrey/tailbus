package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/alexanderfrey/tailbus/api/coordpb"
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	"github.com/alexanderfrey/tailbus/internal/coord"
	"github.com/alexanderfrey/tailbus/internal/handle"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestRegisterRetryBackoff(t *testing.T) {
	var calls atomic.Int32
	failCount := 3

	fn := func() error {
		n := int(calls.Add(1))
		if n <= failCount {
			return fmt.Errorf("coord unavailable (attempt %d)", n)
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := retryWithBackoff(ctx, 10*time.Millisecond, 50*time.Millisecond, nil, fn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := int(calls.Load())
	if got != failCount+1 {
		t.Errorf("expected %d attempts (3 failures + 1 success), got %d", failCount+1, got)
	}
}

func TestRegisterRetryBackoffContextCancelled(t *testing.T) {
	fn := func() error {
		return fmt.Errorf("always fails")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := retryWithBackoff(ctx, 10*time.Millisecond, 50*time.Millisecond, nil, fn)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if ctx.Err() == nil {
		t.Fatal("expected context to be cancelled")
	}
}

func startTestCoordServer(t *testing.T) (addr string, store *coord.Store, srv *coord.Server, cleanup func()) {
	t.Helper()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	store, err := coord.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	// Coord server now needs a keypair (nil for insecure in tests that don't need mTLS)
	srv, err = coord.NewServer(store, logger, nil)
	if err != nil {
		t.Fatal(err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	go srv.Serve(lis)

	cleanup = func() {
		srv.GracefulStop()
		store.Close()
	}
	return lis.Addr().String(), store, srv, cleanup
}

func TestHeartbeatReRegistersOnNodeNotFound(t *testing.T) {
	addr, store, _, cleanup := startTestCoordServer(t)
	defer cleanup()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	resolver := handle.NewResolver()

	// Use insecure mode (nil keypair) for daemon tests that don't test mTLS
	cc, err := NewCoordClient(addr, "node-1", []byte("pk"), "10.0.0.1:9443", resolver, logger, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Register initially
	if err := cc.Register(ctx, []string{"marketing"}, nil); err != nil {
		t.Fatal(err)
	}

	// Verify node exists
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := pb.NewCoordinationAPIClient(conn)

	lr, err := client.LookupHandle(ctx, &pb.LookupHandleRequest{Handle: "marketing"})
	if err != nil {
		t.Fatal(err)
	}
	if !lr.Found {
		t.Fatal("handle should exist after registration")
	}

	// Simulate coord restart by removing the node
	if err := store.RemoveNode("node-1"); err != nil {
		t.Fatal(err)
	}

	// Verify node is gone
	lr, err = client.LookupHandle(ctx, &pb.LookupHandleRequest{Handle: "marketing"})
	if err != nil {
		t.Fatal(err)
	}
	if lr.Found {
		t.Fatal("handle should be gone after node removal")
	}

	// Track re-registration
	var reRegistered atomic.Bool
	reRegister := func(ctx context.Context) error {
		err := cc.Register(ctx, []string{"marketing"}, nil)
		if err == nil {
			reRegistered.Store(true)
		}
		return err
	}

	// Start heartbeat with short interval
	go cc.Heartbeat(ctx, func() []string { return []string{"marketing"} }, func() map[string]*messagepb.ServiceManifest { return nil }, 50*time.Millisecond, reRegister)

	// Wait for re-registration
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for re-registration")
		default:
			if reRegistered.Load() {
				// Verify node is back
				lr, err = client.LookupHandle(ctx, &pb.LookupHandleRequest{Handle: "marketing"})
				if err != nil {
					t.Fatal(err)
				}
				if !lr.Found {
					t.Fatal("handle should exist after re-registration")
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}
