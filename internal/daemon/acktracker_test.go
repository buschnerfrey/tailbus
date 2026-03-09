package daemon

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
)

func TestTrackAndAcknowledge(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	at := NewAckTracker(func(context.Context, string, *messagepb.Envelope) error { return nil }, logger)

	env := &messagepb.Envelope{MessageId: "msg-1"}
	at.Track(env, "10.0.0.1:9443")

	if at.PendingCount() != 1 {
		t.Fatalf("pending = %d, want 1", at.PendingCount())
	}

	// Acknowledge
	ok := at.Acknowledge("msg-1")
	if !ok {
		t.Fatal("expected Acknowledge to return true")
	}
	if at.PendingCount() != 0 {
		t.Fatalf("pending = %d after ack, want 0", at.PendingCount())
	}

	// Acknowledge again — should return false
	ok = at.Acknowledge("msg-1")
	if ok {
		t.Fatal("expected Acknowledge to return false for already-acked message")
	}
}

func TestRetryOnTimeout(t *testing.T) {
	var retryCount atomic.Int32
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	at := NewAckTracker(func(_ context.Context, addr string, env *messagepb.Envelope) error {
		retryCount.Add(1)
		return nil
	}, logger)
	at.timeout = 50 * time.Millisecond

	env := &messagepb.Envelope{MessageId: "msg-retry"}
	at.Track(env, "10.0.0.1:9443")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go at.StartRetryLoop(ctx, 20*time.Millisecond)

	// Wait for at least one retry
	deadline := time.After(1 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for retry")
		default:
			if retryCount.Load() > 0 {
				return // success
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestDropAfterMaxRetries(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	at := NewAckTracker(func(context.Context, string, *messagepb.Envelope) error { return nil }, logger)
	at.timeout = 10 * time.Millisecond
	at.maxRetries = 2

	env := &messagepb.Envelope{MessageId: "msg-drop"}
	at.Track(env, "10.0.0.1:9443")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go at.StartRetryLoop(ctx, 15*time.Millisecond)

	// Wait for the message to be dropped
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out; pending=%d, want 0", at.PendingCount())
		default:
			if at.PendingCount() == 0 {
				return // success — dropped after max retries
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestRetryPersistsUpdatedRetryCount(t *testing.T) {
	ms := testStore(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	at := NewAckTracker(func(context.Context, string, *messagepb.Envelope) error { return nil }, logger)
	at.SetStore(ms)
	at.timeout = 0

	env := &messagepb.Envelope{MessageId: "msg-persist"}
	at.Track(env, "10.0.0.1:9443")
	at.sweep()

	loaded, err := ms.LoadPending()
	if err != nil {
		t.Fatalf("load pending: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 pending message, got %d", len(loaded))
	}
	if loaded[0].retries != 1 {
		t.Fatalf("retries = %d, want 1", loaded[0].retries)
	}
}

func TestDropRemovesPendingFromStore(t *testing.T) {
	ms := testStore(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	at := NewAckTracker(func(context.Context, string, *messagepb.Envelope) error { return nil }, logger)
	at.SetStore(ms)
	at.timeout = 0
	at.maxRetries = 0

	env := &messagepb.Envelope{MessageId: "msg-drop-store"}
	at.Track(env, "10.0.0.1:9443")
	at.sweep()

	if got := ms.PendingCount(); got != 0 {
		t.Fatalf("pending in store = %d, want 0", got)
	}
}
