package session

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

// State represents the lifecycle state of a session.
type State string

const (
	StateOpen     State = "open"
	StateResolved State = "resolved"
)

// Session represents a conversation session between two agents.
type Session struct {
	ID         string
	FromHandle string
	ToHandle   string
	State      State
	TraceID    string
	NextSeq    uint64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// New creates a new open session.
func New(from, to string) *Session {
	now := time.Now()
	return &Session{
		ID:         uuid.New().String(),
		FromHandle: from,
		ToHandle:   to,
		State:      StateOpen,
		NextSeq:    1,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

// Clone returns a shallow copy of the session.
// All fields are value types so this is safe for concurrent use.
func (s *Session) Clone() *Session {
	cp := *s
	return &cp
}

// NextSequence returns the current sequence number and increments the counter.
func (s *Session) NextSequence() uint64 {
	seq := s.NextSeq
	s.NextSeq++
	return seq
}

// Resolve transitions the session to resolved state.
func (s *Session) Resolve() error {
	if s.State != StateOpen {
		return fmt.Errorf("session %s is %s, not open", s.ID, s.State)
	}
	s.State = StateResolved
	s.UpdatedAt = time.Now()
	return nil
}

// PersistFunc is called when a session is stored or removed.
type PersistFunc func(sess *Session) error

// RemoveFunc is called when a session is evicted.
type RemoveFunc func(sessionID string) error

// Store is an in-memory session store with optional persistence callbacks.
type Store struct {
	mu        sync.RWMutex
	sessions  map[string]*Session
	onPersist PersistFunc
	onRemove  RemoveFunc
}

// NewStore creates a new session store.
func NewStore() *Store {
	return &Store{
		sessions: make(map[string]*Session),
	}
}

// SetPersistence sets callbacks for durable persistence.
func (s *Store) SetPersistence(persist PersistFunc, remove RemoveFunc) {
	s.onPersist = persist
	s.onRemove = remove
}

// StartEviction runs a background goroutine that removes resolved sessions
// older than ttl. The goroutine exits when ctx is cancelled.
func (s *Store) StartEviction(ctx context.Context, ttl, interval time.Duration, logger *slog.Logger) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				n := s.evict(ttl)
				if n > 0 && logger != nil {
					logger.Info("evicted resolved sessions", "count", n)
				}
			}
		}
	}()
}

// evict removes resolved sessions whose UpdatedAt is older than ttl. Returns count removed.
func (s *Store) evict(ttl time.Duration) int {
	cutoff := time.Now().Add(-ttl)
	s.mu.Lock()
	var evicted []string
	for id, sess := range s.sessions {
		if sess.State == StateResolved && sess.UpdatedAt.Before(cutoff) {
			delete(s.sessions, id)
			evicted = append(evicted, id)
		}
	}
	remove := s.onRemove
	s.mu.Unlock()

	if remove != nil {
		for _, id := range evicted {
			remove(id)
		}
	}
	return len(evicted)
}

// Put stores a session and optionally persists it to disk.
func (s *Store) Put(sess *Session) {
	s.mu.Lock()
	s.sessions[sess.ID] = sess
	persist := s.onPersist
	s.mu.Unlock()

	if persist != nil {
		persist(sess)
	}
}

// Get retrieves a session by ID. Returns a clone safe for concurrent mutation.
func (s *Store) Get(id string) (*Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	if !ok {
		return nil, false
	}
	return sess.Clone(), true
}

// ListAll returns all sessions. Returns clones safe for concurrent mutation.
func (s *Store) ListAll() []*Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		result = append(result, sess.Clone())
	}
	return result
}

// ListByHandle returns all sessions involving a handle (as from or to).
// Returns clones safe for concurrent mutation.
func (s *Store) ListByHandle(handle string) []*Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []*Session
	for _, sess := range s.sessions {
		if sess.FromHandle == handle || sess.ToHandle == handle {
			result = append(result, sess.Clone())
		}
	}
	return result
}
