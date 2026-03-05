package session

import (
	"context"
	"testing"
	"time"
)

func TestNewSession(t *testing.T) {
	s := New("marketing", "sales")
	if s.ID == "" {
		t.Error("empty session ID")
	}
	if s.FromHandle != "marketing" || s.ToHandle != "sales" {
		t.Errorf("got from=%q to=%q", s.FromHandle, s.ToHandle)
	}
	if s.State != StateOpen {
		t.Errorf("state = %q, want %q", s.State, StateOpen)
	}
}

func TestSequenceMonotonic(t *testing.T) {
	s := New("a", "b")
	if s.NextSeq != 1 {
		t.Errorf("initial NextSeq = %d, want 1", s.NextSeq)
	}

	seq1 := s.NextSequence()
	seq2 := s.NextSequence()
	seq3 := s.NextSequence()

	if seq1 != 1 || seq2 != 2 || seq3 != 3 {
		t.Errorf("sequences = %d, %d, %d; want 1, 2, 3", seq1, seq2, seq3)
	}
	if s.NextSeq != 4 {
		t.Errorf("NextSeq after 3 calls = %d, want 4", s.NextSeq)
	}
}

func TestNextSequenceUpdatesUpdatedAt(t *testing.T) {
	s := New("a", "b")
	updatedAt := s.UpdatedAt

	time.Sleep(10 * time.Millisecond)
	s.NextSequence()

	if !s.UpdatedAt.After(updatedAt) {
		t.Fatalf("UpdatedAt = %v, want later than %v", s.UpdatedAt, updatedAt)
	}
}

func TestResolve(t *testing.T) {
	s := New("a", "b")
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}
	if s.State != StateResolved {
		t.Errorf("state = %q, want %q", s.State, StateResolved)
	}
	if err := s.Resolve(); err == nil {
		t.Error("expected error resolving already-resolved session")
	}
}

func TestGetReturnsCopy(t *testing.T) {
	store := NewStore()
	s := New("a", "b")
	store.Put(s)

	// Get a copy and mutate it
	got, ok := store.Get(s.ID)
	if !ok {
		t.Fatal("session not found")
	}
	got.State = StateResolved
	got.FromHandle = "mutated"

	// Re-Get — store should be unchanged
	original, ok := store.Get(s.ID)
	if !ok {
		t.Fatal("session not found after mutation")
	}
	if original.State != StateOpen {
		t.Errorf("store was mutated: state = %q, want %q", original.State, StateOpen)
	}
	if original.FromHandle != "a" {
		t.Errorf("store was mutated: from = %q, want %q", original.FromHandle, "a")
	}
}

func TestListAllReturnsCopies(t *testing.T) {
	store := NewStore()
	s := New("a", "b")
	store.Put(s)

	list := store.ListAll()
	list[0].State = StateResolved

	original, _ := store.Get(s.ID)
	if original.State != StateOpen {
		t.Errorf("store was mutated via ListAll: state = %q", original.State)
	}
}

func TestListByHandleReturnsCopies(t *testing.T) {
	store := NewStore()
	s := New("a", "b")
	store.Put(s)

	list := store.ListByHandle("a")
	list[0].State = StateResolved

	original, _ := store.Get(s.ID)
	if original.State != StateOpen {
		t.Errorf("store was mutated via ListByHandle: state = %q", original.State)
	}
}

func TestStore(t *testing.T) {
	store := NewStore()

	s1 := New("marketing", "sales")
	s2 := New("sales", "support")
	store.Put(s1)
	store.Put(s2)

	got, ok := store.Get(s1.ID)
	if !ok || got.ID != s1.ID {
		t.Error("failed to get session")
	}

	_, ok = store.Get("nonexistent")
	if ok {
		t.Error("expected not found")
	}

	list := store.ListByHandle("sales")
	if len(list) != 2 {
		t.Errorf("ListByHandle(sales) = %d sessions, want 2", len(list))
	}

	list = store.ListByHandle("marketing")
	if len(list) != 1 {
		t.Errorf("ListByHandle(marketing) = %d sessions, want 1", len(list))
	}
}

func TestEviction(t *testing.T) {
	store := NewStore()

	s := New("a", "b")
	s.Resolve()
	// Backdate UpdatedAt so it's older than TTL
	s.UpdatedAt = time.Now().Add(-10 * time.Minute)
	store.Put(s)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store.StartEviction(ctx, 1*time.Millisecond, 10*time.Millisecond, nil)

	// Wait for eviction sweep
	time.Sleep(50 * time.Millisecond)

	_, ok := store.Get(s.ID)
	if ok {
		t.Error("resolved session should have been evicted")
	}
}

func TestEvictionKeepsOpenSessions(t *testing.T) {
	store := NewStore()

	// Open session — should NOT be evicted even if old
	s := New("a", "b")
	s.UpdatedAt = time.Now().Add(-10 * time.Minute)
	store.Put(s)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store.StartEviction(ctx, 1*time.Millisecond, 10*time.Millisecond, nil)

	time.Sleep(50 * time.Millisecond)

	_, ok := store.Get(s.ID)
	if !ok {
		t.Error("open session should NOT have been evicted")
	}
}
