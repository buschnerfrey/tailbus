package daemon

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	transportpb "github.com/alexanderfrey/tailbus/api/transportpb"
	"github.com/alexanderfrey/tailbus/internal/handle"
)

type fakeRoomTransport struct {
	sent []*transportpb.TransportMessage
}

func (f *fakeRoomTransport) Send(_ context.Context, _ string, msg *transportpb.TransportMessage) error {
	f.sent = append(f.sent, msg)
	return nil
}

func (f *fakeRoomTransport) OnReceive(func(*transportpb.TransportMessage)) {}
func (f *fakeRoomTransport) Close() error                                   { return nil }

type fakeRoomDeliverer struct {
	events []*messagepb.RoomEvent
}

func (f *fakeRoomDeliverer) DeliverRoomEventLocal(event *messagepb.RoomEvent) bool {
	f.events = append(f.events, event)
	return true
}

func TestRoomManagerCreatePostReplayAndClose(t *testing.T) {
	store := testStore(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	resolver := handle.NewResolver()
	resolver.UpdatePeerMap(map[string]handle.PeerInfo{
		"bob": {NodeID: "node-2", AdvertiseAddr: "10.0.0.2:9443"},
	})
	tp := &fakeRoomTransport{}
	deliverer := &fakeRoomDeliverer{}
	rm := NewRoomManager("node-1", resolver, tp, deliverer, store, NewActivityBus(), logger)

	room, err := rm.CreateRoom(context.Background(), "alice", "design-review", []string{"bob"})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	if room.HomeNodeId != "node-1" {
		t.Fatalf("home node = %q, want node-1", room.HomeNodeId)
	}

	event, err := rm.PostMessage(context.Background(), room.RoomId, "alice", []byte("hello"), "text/plain", "trace-1")
	if err != nil {
		t.Fatalf("post room message: %v", err)
	}
	if event.RoomSeq != 1 {
		t.Fatalf("room seq = %d, want 1", event.RoomSeq)
	}
	if len(deliverer.events) != 1 {
		t.Fatalf("local deliveries = %d, want 1", len(deliverer.events))
	}

	replayed, err := rm.Replay(context.Background(), room.RoomId, "alice", 0)
	if err != nil {
		t.Fatalf("replay room: %v", err)
	}
	if len(replayed) != 1 || replayed[0].EventId != event.EventId {
		t.Fatalf("unexpected replay: %+v", replayed)
	}

	if _, err := rm.CloseRoom(context.Background(), room.RoomId, "alice"); err != nil {
		t.Fatalf("close room: %v", err)
	}
	if _, err := rm.PostMessage(context.Background(), room.RoomId, "alice", []byte("after close"), "text/plain", ""); err == nil {
		t.Fatal("expected post after close to fail")
	}
}

func TestRoomManagerCachesRemoteRoomEventMetadata(t *testing.T) {
	store := testStore(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	resolver := handle.NewResolver()
	resolver.UpdatePeerMap(map[string]handle.PeerInfo{
		"orchestrator": {NodeID: "node-1", AdvertiseAddr: "10.0.0.1:9443"},
	})
	tp := &fakeRoomTransport{}
	deliverer := &fakeRoomDeliverer{}
	rm := NewRoomManager("node-2", resolver, tp, deliverer, store, NewActivityBus(), logger)

	event := &messagepb.RoomEvent{
		EventId:      "evt-1",
		RoomId:       "room-remote",
		RoomSeq:      3,
		SenderHandle: "orchestrator",
		Members:      []string{"orchestrator", "codex-solver"},
		SentAtUnix:   time.Now().Unix(),
		Type:         messagepb.RoomEventType_ROOM_EVENT_TYPE_MESSAGE_POSTED,
	}
	rm.HandleTransportMessage(context.Background(), &transportpb.TransportMessage{
		Body: &transportpb.TransportMessage_RoomEvent{RoomEvent: event},
	})

	room, err := store.LoadRoom("room-remote")
	if err != nil {
		t.Fatalf("load room: %v", err)
	}
	if room == nil {
		t.Fatal("expected cached room metadata")
	}
	if room.HomeNodeId != "node-1" {
		t.Fatalf("home node = %q, want node-1", room.HomeNodeId)
	}
	if room.NextSeq != 4 {
		t.Fatalf("next seq = %d, want 4", room.NextSeq)
	}
	if len(deliverer.events) != 1 {
		t.Fatalf("local deliveries = %d, want 1", len(deliverer.events))
	}
}

type slowRoomTransport struct {
	delay time.Duration
}

func (s *slowRoomTransport) Send(ctx context.Context, _ string, _ *transportpb.TransportMessage) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(s.delay):
		return nil
	}
}

func (s *slowRoomTransport) OnReceive(func(*transportpb.TransportMessage)) {}
func (s *slowRoomTransport) Close() error                                   { return nil }

func TestWithRoomControlTimesOutOnSlowTransport(t *testing.T) {
	store := testStore(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	resolver := handle.NewResolver()
	resolver.UpdatePeerMap(map[string]handle.PeerInfo{
		"alice": {NodeID: "node-1", AdvertiseAddr: "10.0.0.1:9443"},
	})

	// Simulate a room on a remote node with a transport that blocks forever.
	tp := &slowRoomTransport{delay: time.Minute}
	deliverer := &fakeRoomDeliverer{}
	rm := NewRoomManager("node-2", resolver, tp, deliverer, store, NewActivityBus(), logger)

	// Seed a remote room in the store so resolveRoomInfo finds it.
	remoteRoom := &messagepb.RoomInfo{
		RoomId:     "room-remote-slow",
		HomeNodeId: "node-1",
		Members:    []string{"alice", "bob"},
		Status:     "open",
		NextSeq:    1,
	}
	if err := store.StoreRoom(remoteRoom); err != nil {
		t.Fatalf("seed room: %v", err)
	}

	start := time.Now()
	_, err := rm.Replay(context.Background(), "room-remote-slow", "alice", 0)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error from Replay on slow transport")
	}
	// Should complete within ~roomCommandTimeout (10s), not the transport's 1min delay.
	if elapsed > 15*time.Second {
		t.Fatalf("Replay took %v, expected it to time out around %v", elapsed, roomCommandTimeout)
	}
}

func TestRoomMessageActivityMeta(t *testing.T) {
	event := &messagepb.RoomEvent{
		ContentType: "application/json",
		Payload: []byte(`{
			"kind":"turn_request",
			"target_handle":"codex-solver",
			"turn_id":"turn-123",
			"status":"working",
			"round":2
		}`),
	}
	kind, target, turnID, status, round := roomMessageActivityMeta(event)
	if kind != "turn_request" {
		t.Fatalf("kind = %q, want turn_request", kind)
	}
	if target != "codex-solver" {
		t.Fatalf("target = %q, want codex-solver", target)
	}
	if turnID != "turn-123" {
		t.Fatalf("turn id = %q, want turn-123", turnID)
	}
	if status != "working" {
		t.Fatalf("status = %q, want working", status)
	}
	if round != 2 {
		t.Fatalf("round = %d, want 2", round)
	}
}
