package daemon

import (
	"context"
	"log/slog"
	"sync"
	"time"

	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
)

const (
	defaultACKTimeout = 5 * time.Second
	defaultMaxRetries = 3
)

type pendingMessage struct {
	env      *messagepb.Envelope
	peerAddr string
	sentAt   time.Time
	retries  int
}

// AckTracker tracks pending messages awaiting ACK and retries on timeout.
type AckTracker struct {
	mu         sync.Mutex
	pending    map[string]*pendingMessage // messageID -> pending
	sendFn     func(ctx context.Context, addr string, env *messagepb.Envelope) error
	logger     *slog.Logger
	timeout    time.Duration
	maxRetries int
	store      *MessageStore // optional durable backing
}

// NewAckTracker creates a new ACK tracker.
func NewAckTracker(sendFn func(ctx context.Context, addr string, env *messagepb.Envelope) error, logger *slog.Logger) *AckTracker {
	return &AckTracker{
		pending:    make(map[string]*pendingMessage),
		sendFn:     sendFn,
		logger:     logger,
		timeout:    defaultACKTimeout,
		maxRetries: defaultMaxRetries,
	}
}

// SetStore attaches a durable message store for persistence.
func (a *AckTracker) SetStore(store *MessageStore) {
	a.store = store
}

// Restore adds a pending message loaded from durable storage (no re-persist).
func (a *AckTracker) Restore(pm *pendingMessage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pending[pm.env.MessageId] = pm
}

// Track registers a sent envelope as pending ACK.
func (a *AckTracker) Track(env *messagepb.Envelope, peerAddr string) {
	a.mu.Lock()
	a.pending[env.MessageId] = &pendingMessage{
		env:      env,
		peerAddr: peerAddr,
		sentAt:   time.Now(),
	}
	store := a.store
	a.mu.Unlock()

	if store != nil {
		if err := store.StorePending(env, peerAddr); err != nil {
			a.logger.Warn("failed to persist pending message", "message_id", env.MessageId, "error", err)
		}
	}
}

// Acknowledge removes a message from pending. Returns true if it was pending.
func (a *AckTracker) Acknowledge(messageID string) bool {
	a.mu.Lock()
	_, ok := a.pending[messageID]
	if ok {
		delete(a.pending, messageID)
	}
	store := a.store
	a.mu.Unlock()

	if ok && store != nil {
		if err := store.RemovePending(messageID); err != nil {
			a.logger.Warn("failed to remove pending message from store", "message_id", messageID, "error", err)
		}
	}
	return ok
}

// PendingCount returns the number of messages awaiting ACK.
func (a *AckTracker) PendingCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.pending)
}

// StartRetryLoop runs a background loop that retries expired pending messages.
// It checks every interval and exits when ctx is cancelled.
func (a *AckTracker) StartRetryLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.sweep()
		}
	}
}

func (a *AckTracker) sweep() {
	a.mu.Lock()
	now := time.Now()
	var retryList []*pendingMessage
	var dropIDs []string

	for id, pm := range a.pending {
		if now.Sub(pm.sentAt) < a.timeout {
			continue
		}
		if pm.retries >= a.maxRetries {
			dropIDs = append(dropIDs, id)
			continue
		}
		retryList = append(retryList, pm)
	}

	for _, id := range dropIDs {
		pm := a.pending[id]
		delete(a.pending, id)
		a.logger.Warn("dropping unacked message after max retries",
			"message_id", id, "peer", pm.peerAddr, "retries", pm.retries)
	}
	store := a.store
	a.mu.Unlock()

	if store != nil {
		for _, id := range dropIDs {
			if err := store.RemovePending(id); err != nil {
				a.logger.Warn("failed to remove dropped pending message from store", "message_id", id, "error", err)
			}
		}
	}

	// Retry outside the lock
	for _, pm := range retryList {
		retryCtx, retryCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := a.sendFn(retryCtx, pm.peerAddr, pm.env); err != nil {
			a.logger.Warn("retry send failed", "message_id", pm.env.MessageId, "error", err)
		}
		retryCancel()
		var retrySentAt time.Time
		var retryCount int
		a.mu.Lock()
		// Update the pending entry if it still exists (might have been ACKed in the meantime)
		if existing, ok := a.pending[pm.env.MessageId]; ok {
			existing.retries++
			existing.sentAt = time.Now()
			retrySentAt = existing.sentAt
			retryCount = existing.retries
		}
		a.mu.Unlock()

		if retryCount > 0 && store != nil {
			if err := store.UpdatePending(pm.env.MessageId, retrySentAt, retryCount); err != nil {
				a.logger.Warn("failed to update pending retry state", "message_id", pm.env.MessageId, "error", err)
			}
		}
	}
}
