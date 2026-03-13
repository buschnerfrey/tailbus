package daemon

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	"github.com/alexanderfrey/tailbus/internal/session"
)

func testStore(t *testing.T) *MessageStore {
	t.Helper()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ms, err := NewMessageStore(filepath.Join(dir, "test.db"), logger)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { ms.Close() })
	return ms
}

func TestMessageStore_PendingRoundTrip(t *testing.T) {
	ms := testStore(t)

	env := &messagepb.Envelope{
		MessageId:  "msg-1",
		SessionId:  "sess-1",
		FromHandle: "alice",
		ToHandle:   "bob",
		Payload:    []byte("hello"),
		Type:       messagepb.EnvelopeType_ENVELOPE_TYPE_MESSAGE,
	}

	if err := ms.StorePending(env, "10.0.0.1:9443"); err != nil {
		t.Fatalf("store pending: %v", err)
	}

	if ms.PendingCount() != 1 {
		t.Fatalf("expected 1 pending, got %d", ms.PendingCount())
	}

	loaded, err := ms.LoadPending()
	if err != nil {
		t.Fatalf("load pending: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 pending, got %d", len(loaded))
	}
	if loaded[0].env.MessageId != "msg-1" {
		t.Fatalf("wrong message ID: %s", loaded[0].env.MessageId)
	}
	if loaded[0].peerAddr != "10.0.0.1:9443" {
		t.Fatalf("wrong peer addr: %s", loaded[0].peerAddr)
	}

	// ACK removes it
	if err := ms.RemovePending("msg-1"); err != nil {
		t.Fatalf("remove pending: %v", err)
	}
	if ms.PendingCount() != 0 {
		t.Fatalf("expected 0 pending after ACK, got %d", ms.PendingCount())
	}
}

func TestMessageStore_UpdatePendingRoundTrip(t *testing.T) {
	ms := testStore(t)

	env := &messagepb.Envelope{
		MessageId: "msg-update",
		Type:      messagepb.EnvelopeType_ENVELOPE_TYPE_MESSAGE,
	}
	if err := ms.StorePending(env, "10.0.0.1:9443"); err != nil {
		t.Fatalf("store pending: %v", err)
	}

	if err := ms.UpdatePending("msg-update", time.Now().Add(time.Second), 2); err != nil {
		t.Fatalf("update pending: %v", err)
	}

	loaded, err := ms.LoadPending()
	if err != nil {
		t.Fatalf("load pending: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 pending, got %d", len(loaded))
	}
	if loaded[0].retries != 2 {
		t.Fatalf("retries = %d, want 2", loaded[0].retries)
	}
}

func TestMessageStore_SessionRoundTrip(t *testing.T) {
	ms := testStore(t)

	sess := &session.Session{
		ID:         "sess-1",
		FromHandle: "alice",
		ToHandle:   "bob",
		State:      session.StateOpen,
		TraceID:    "trace-1",
		NextSeq:    3,
		CreatedAt:  time.Now().Truncate(time.Second),
		UpdatedAt:  time.Now().Truncate(time.Second),
	}

	if err := ms.StoreSession(sess); err != nil {
		t.Fatalf("store session: %v", err)
	}

	loaded, err := ms.LoadSessions()
	if err != nil {
		t.Fatalf("load sessions: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 session, got %d", len(loaded))
	}

	got := loaded[0]
	if got.ID != "sess-1" || got.FromHandle != "alice" || got.ToHandle != "bob" {
		t.Fatalf("wrong session data: %+v", got)
	}
	if got.State != session.StateOpen {
		t.Fatalf("wrong state: %s", got.State)
	}
	if got.NextSeq != 3 {
		t.Fatalf("wrong next_seq: %d", got.NextSeq)
	}

	// Remove
	if err := ms.RemoveSession("sess-1"); err != nil {
		t.Fatalf("remove session: %v", err)
	}
	loaded, _ = ms.LoadSessions()
	if len(loaded) != 0 {
		t.Fatalf("expected 0 sessions after remove, got %d", len(loaded))
	}
}

func TestMessageStore_SurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Write data
	ms, err := NewMessageStore(dbPath, logger)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	env := &messagepb.Envelope{
		MessageId: "msg-survive",
		Payload:   []byte("persistent"),
		Type:      messagepb.EnvelopeType_ENVELOPE_TYPE_MESSAGE,
	}
	ms.StorePending(env, "peer:1234")
	ms.StoreSession(&session.Session{
		ID:        "sess-survive",
		State:     session.StateOpen,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	ms.Close()

	// Reopen and verify
	ms2, err := NewMessageStore(dbPath, logger)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer ms2.Close()

	pending, _ := ms2.LoadPending()
	if len(pending) != 1 || pending[0].env.MessageId != "msg-survive" {
		t.Fatalf("pending not survived: %v", pending)
	}

	sessions, _ := ms2.LoadSessions()
	if len(sessions) != 1 || sessions[0].ID != "sess-survive" {
		t.Fatalf("session not survived: %v", sessions)
	}
}

func TestMessageStore_RoomRoundTrip(t *testing.T) {
	ms := testStore(t)

	room := &messagepb.RoomInfo{
		RoomId:        "room-1",
		Title:         "design-review",
		CreatedBy:     "alice",
		HomeNodeId:    "node-1",
		Members:       []string{"alice", "bob"},
		Status:        "open",
		NextSeq:       2,
		CreatedAtUnix: time.Now().Unix(),
		UpdatedAtUnix: time.Now().Unix(),
	}
	if err := ms.StoreRoom(room); err != nil {
		t.Fatalf("store room: %v", err)
	}

	loaded, err := ms.LoadRoom("room-1")
	if err != nil {
		t.Fatalf("load room: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected room to load")
	}
	if loaded.Title != "design-review" || loaded.HomeNodeId != "node-1" {
		t.Fatalf("wrong room data: %+v", loaded)
	}
	if len(loaded.Members) != 2 {
		t.Fatalf("members = %d, want 2", len(loaded.Members))
	}
}

func TestMessageStore_RoomReplayRetention(t *testing.T) {
	ms := testStore(t)

	for seq := uint64(1); seq <= 3; seq++ {
		event := &messagepb.RoomEvent{
			EventId:      "event-" + string(rune('0'+seq)),
			RoomId:       "room-1",
			RoomSeq:      seq,
			SenderHandle: "alice",
			Type:         messagepb.RoomEventType_ROOM_EVENT_TYPE_MESSAGE_POSTED,
			Payload:      []byte("hello"),
			ContentType:  "text/plain",
			SentAtUnix:   time.Now().Unix(),
			Members:      []string{"alice", "bob"},
		}
		if err := ms.StoreRoomEvent(event, 2); err != nil {
			t.Fatalf("store room event %d: %v", seq, err)
		}
	}

	events, err := ms.ReplayRoomEvents("room-1", 0)
	if err != nil {
		t.Fatalf("replay room: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if events[0].RoomSeq != 2 || events[1].RoomSeq != 3 {
		t.Fatalf("unexpected replay sequences: %d, %d", events[0].RoomSeq, events[1].RoomSeq)
	}
}

func TestMessageStore_UsageHistoryRoundTrip(t *testing.T) {
	ms := testStore(t)

	day1 := time.Date(2026, time.March, 3, 9, 15, 0, 0, time.UTC)
	day2 := time.Date(2026, time.March, 5, 11, 45, 0, 0, time.UTC)

	if err := ms.RecordUsage(agentpb.UsageMetric_USAGE_METRIC_MESSAGES_ROUTED, day1, 3); err != nil {
		t.Fatalf("record usage day1: %v", err)
	}
	if err := ms.RecordUsage(agentpb.UsageMetric_USAGE_METRIC_MESSAGES_ROUTED, day1.Add(2*time.Hour), 2); err != nil {
		t.Fatalf("record usage day1 second bucket: %v", err)
	}
	if err := ms.RecordUsage(agentpb.UsageMetric_USAGE_METRIC_MESSAGES_ROUTED, day2, 4); err != nil {
		t.Fatalf("record usage day2: %v", err)
	}
	if err := ms.RecordUsage(agentpb.UsageMetric_USAGE_METRIC_ROOMS_CREATED, day2, 1); err != nil {
		t.Fatalf("record usage rooms created: %v", err)
	}

	history, err := ms.LoadUsageHistory()
	if err != nil {
		t.Fatalf("load usage history: %v", err)
	}
	if len(history.Metrics) != 4 {
		t.Fatalf("metrics = %d, want 4", len(history.Metrics))
	}
	routed := history.Metrics[0]
	if routed.Metric != agentpb.UsageMetric_USAGE_METRIC_MESSAGES_ROUTED {
		t.Fatalf("metric = %v, want routed messages", routed.Metric)
	}
	if routed.Total != 9 {
		t.Fatalf("routed total = %d, want 9", routed.Total)
	}
	if len(routed.DailyBuckets) != 2 {
		t.Fatalf("routed daily buckets = %d, want 2", len(routed.DailyBuckets))
	}
	if routed.DailyBuckets[0].Count != 5 || routed.DailyBuckets[1].Count != 4 {
		t.Fatalf("unexpected routed bucket counts: %d, %d", routed.DailyBuckets[0].Count, routed.DailyBuckets[1].Count)
	}
	roomsCreated := history.Metrics[3]
	if roomsCreated.Total != 1 {
		t.Fatalf("rooms created total = %d, want 1", roomsCreated.Total)
	}
}
