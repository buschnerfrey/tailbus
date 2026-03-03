package coord

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/alexanderfrey/tailbus/api/coordpb"
	"github.com/alexanderfrey/tailbus/internal/identity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func testStore(t *testing.T) (*Store, func()) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	return store, func() { store.Close() }
}

func testServer(t *testing.T) (pb.CoordinationAPIClient, func()) {
	t.Helper()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	store, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	// Generate coord keypair for mTLS
	coordKP, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(store, logger, coordKP)
	if err != nil {
		t.Fatal(err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	go srv.Serve(lis)

	// Create a client with TLS + TOFU
	clientKP, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	clientCert, err := identity.SelfSignedCert(clientKP)
	if err != nil {
		t.Fatal(err)
	}
	tofuFile := filepath.Join(dir, "coord.fp")
	tofu := identity.NewTOFUVerifier(tofuFile)

	clientTLS := &tls.Config{
		Certificates:          []tls.Certificate{clientCert},
		InsecureSkipVerify:    true,
		VerifyPeerCertificate: tofu.Verify,
	}
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(clientTLS)))
	if err != nil {
		t.Fatal(err)
	}

	client := pb.NewCoordinationAPIClient(conn)
	cleanup := func() {
		conn.Close()
		srv.GracefulStop()
		store.Close()
	}

	return client, cleanup
}

func TestRegisterAndLookup(t *testing.T) {
	client, cleanup := testServer(t)
	defer cleanup()

	ctx := context.Background()

	// Register a node with handles
	resp, err := client.RegisterNode(ctx, &pb.RegisterNodeRequest{
		NodeId:        "node-1",
		PublicKey:     []byte("pubkey1"),
		AdvertiseAddr: "10.0.0.1:9443",
		Handles:       []string{"marketing", "sales"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Ok {
		t.Fatalf("registration failed: %s", resp.Error)
	}

	// Lookup handle
	lr, err := client.LookupHandle(ctx, &pb.LookupHandleRequest{Handle: "marketing"})
	if err != nil {
		t.Fatal(err)
	}
	if !lr.Found {
		t.Fatal("handle not found")
	}
	if lr.Peer.NodeId != "node-1" {
		t.Errorf("node_id = %q, want node-1", lr.Peer.NodeId)
	}
	if lr.Peer.AdvertiseAddr != "10.0.0.1:9443" {
		t.Errorf("addr = %q", lr.Peer.AdvertiseAddr)
	}

	// Lookup non-existent
	lr, err = client.LookupHandle(ctx, &pb.LookupHandleRequest{Handle: "nonexistent"})
	if err != nil {
		t.Fatal(err)
	}
	if lr.Found {
		t.Error("expected not found")
	}
}

func TestWatchPeerMap(t *testing.T) {
	client, cleanup := testServer(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Register first node
	_, err := client.RegisterNode(ctx, &pb.RegisterNodeRequest{
		NodeId:        "node-1",
		PublicKey:     []byte("pk1"),
		AdvertiseAddr: "10.0.0.1:9443",
		Handles:       []string{"marketing"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Start watching
	stream, err := client.WatchPeerMap(ctx, &pb.WatchPeerMapRequest{NodeId: "node-1"})
	if err != nil {
		t.Fatal(err)
	}

	// Should get initial peer map
	update, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if len(update.Peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(update.Peers))
	}
	if update.Peers[0].NodeId != "node-1" {
		t.Errorf("peer node_id = %q", update.Peers[0].NodeId)
	}

	// Register second node — should trigger update
	_, err = client.RegisterNode(ctx, &pb.RegisterNodeRequest{
		NodeId:        "node-2",
		PublicKey:     []byte("pk2"),
		AdvertiseAddr: "10.0.0.2:9443",
		Handles:       []string{"sales"},
	})
	if err != nil {
		t.Fatal(err)
	}

	update, err = stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if len(update.Peers) != 2 {
		t.Fatalf("expected 2 peers after second registration, got %d", len(update.Peers))
	}
}

func TestHeartbeat(t *testing.T) {
	client, cleanup := testServer(t)
	defer cleanup()

	ctx := context.Background()

	// Register
	_, err := client.RegisterNode(ctx, &pb.RegisterNodeRequest{
		NodeId:        "node-1",
		PublicKey:     []byte("pk1"),
		AdvertiseAddr: "10.0.0.1:9443",
		Handles:       []string{"marketing"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Heartbeat with updated handles
	hr, err := client.Heartbeat(ctx, &pb.HeartbeatRequest{
		NodeId:  "node-1",
		Handles: []string{"marketing", "analytics"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hr.Ok {
		t.Error("heartbeat failed")
	}

	// Verify updated handles
	lr, err := client.LookupHandle(ctx, &pb.LookupHandleRequest{Handle: "analytics"})
	if err != nil {
		t.Fatal(err)
	}
	if !lr.Found {
		t.Error("analytics handle not found after heartbeat update")
	}
}

func TestStaleNodeEviction(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	// Register a node
	rec := &NodeRecord{
		NodeID:        "node-stale",
		PublicKey:     []byte("pk"),
		AdvertiseAddr: "10.0.0.1:9443",
		Handles:       []string{"marketing"},
		LastHeartbeat: time.Now(),
	}
	if err := store.UpsertNode(rec); err != nil {
		t.Fatal(err)
	}

	// Backdate the heartbeat to 2 minutes ago
	_, err := store.db.Exec("UPDATE nodes SET last_heartbeat = ? WHERE node_id = ?",
		time.Now().Add(-2*time.Minute).Unix(), "node-stale")
	if err != nil {
		t.Fatal(err)
	}

	// Remove stale nodes older than 90 seconds
	n, err := store.RemoveStaleNodes(time.Now().Add(-90 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("removed %d nodes, want 1", n)
	}

	// Verify node is gone
	nodes, err := store.GetAllNodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 {
		t.Errorf("got %d nodes, want 0", len(nodes))
	}

	// Verify handles are also gone (CASCADE)
	rec2, err := store.LookupHandle("marketing")
	if err != nil {
		t.Fatal(err)
	}
	if rec2 != nil {
		t.Error("handle should have been cascaded")
	}
}

func TestStaleNodeEvictionKeepsFreshNodes(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	// Register a fresh node
	rec := &NodeRecord{
		NodeID:        "node-fresh",
		PublicKey:     []byte("pk"),
		AdvertiseAddr: "10.0.0.1:9443",
		Handles:       []string{"sales"},
		LastHeartbeat: time.Now(),
	}
	if err := store.UpsertNode(rec); err != nil {
		t.Fatal(err)
	}

	// Try to remove stale nodes — fresh node should survive
	n, err := store.RemoveStaleNodes(time.Now().Add(-90 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("removed %d nodes, want 0", n)
	}

	nodes, err := store.GetAllNodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Errorf("got %d nodes, want 1", len(nodes))
	}
}

func TestBroadcastSuppressedWhenUnchanged(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	pm := NewPeerMap(store, logger)
	ch := pm.AddWatcher("watcher-1")
	defer pm.RemoveWatcher("watcher-1")

	// Register a node
	rec := &NodeRecord{
		NodeID:        "node-1",
		PublicKey:     []byte("pk"),
		AdvertiseAddr: "10.0.0.1:9443",
		Handles:       []string{"marketing"},
		LastHeartbeat: time.Now(),
	}
	if err := store.UpsertNode(rec); err != nil {
		t.Fatal(err)
	}

	// First broadcast should send
	if err := pm.Broadcast(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
		// good
	default:
		t.Fatal("expected first broadcast to send")
	}

	// Second broadcast with no changes should be suppressed
	// (heartbeat update doesn't change topology)
	if err := store.UpdateHeartbeat("node-1", []string{"marketing"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := pm.Broadcast(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
		t.Fatal("second broadcast should have been suppressed (no topology change)")
	default:
		// good — suppressed
	}
}

func TestBroadcastTriggeredOnChange(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	pm := NewPeerMap(store, logger)
	ch := pm.AddWatcher("watcher-1")
	defer pm.RemoveWatcher("watcher-1")

	// Register a node
	rec := &NodeRecord{
		NodeID:        "node-1",
		PublicKey:     []byte("pk"),
		AdvertiseAddr: "10.0.0.1:9443",
		Handles:       []string{"marketing"},
		LastHeartbeat: time.Now(),
	}
	if err := store.UpsertNode(rec); err != nil {
		t.Fatal(err)
	}

	// First broadcast
	if err := pm.Broadcast(); err != nil {
		t.Fatal(err)
	}
	<-ch

	// Change handles — should trigger broadcast
	if err := store.UpdateHeartbeat("node-1", []string{"marketing", "sales"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := pm.Broadcast(); err != nil {
		t.Fatal(err)
	}
	select {
	case update := <-ch:
		if len(update.Peers) != 1 {
			t.Errorf("got %d peers, want 1", len(update.Peers))
		}
	default:
		t.Fatal("broadcast should have been triggered after handle change")
	}
}
