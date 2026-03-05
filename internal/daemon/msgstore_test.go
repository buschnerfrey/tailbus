package daemon

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

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
