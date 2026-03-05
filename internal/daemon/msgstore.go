package daemon

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	"github.com/alexanderfrey/tailbus/internal/session"
	bolt "go.etcd.io/bbolt"
	"google.golang.org/protobuf/proto"
)

var (
	bucketPending  = []byte("pending_messages")
	bucketSessions = []byte("sessions")
)

// MessageStore provides durable storage for pending messages and sessions
// using bbolt. Messages are stored until ACKed. Sessions survive daemon restarts.
type MessageStore struct {
	db     *bolt.DB
	logger *slog.Logger
	mu     sync.Mutex // serialize writes for pending tracking
}

// storedSession is the JSON-serializable form of a session.
type storedSession struct {
	ID         string `json:"id"`
	FromHandle string `json:"from_handle"`
	ToHandle   string `json:"to_handle"`
	State      string `json:"state"`
	TraceID    string `json:"trace_id"`
	NextSeq    uint64 `json:"next_seq"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
}

// storedPending wraps an envelope with routing metadata for retry.
type storedPending struct {
	EnvelopeRaw []byte `json:"envelope"`
	PeerAddr    string `json:"peer_addr"`
	StoredAt    int64  `json:"stored_at"`
	Retries     int    `json:"retries"`
}

// NewMessageStore opens (or creates) a bbolt database at the given path.
func NewMessageStore(path string, logger *slog.Logger) (*MessageStore, error) {
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open message store: %w", err)
	}

	// Create buckets
	err = db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(bucketPending); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(bucketSessions); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("create buckets: %w", err)
	}

	return &MessageStore{db: db, logger: logger}, nil
}

// Close closes the underlying database.
func (ms *MessageStore) Close() error {
	return ms.db.Close()
}

// --- Pending messages ---

// StorePending persists a sent envelope that is awaiting ACK.
func (ms *MessageStore) StorePending(env *messagepb.Envelope, peerAddr string) error {
	return ms.storePending(env, peerAddr, time.Now().Unix(), 0)
}

func (ms *MessageStore) storePending(env *messagepb.Envelope, peerAddr string, storedAt int64, retries int) error {
	raw, err := proto.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}

	sp := storedPending{
		EnvelopeRaw: raw,
		PeerAddr:    peerAddr,
		StoredAt:    storedAt,
		Retries:     retries,
	}
	val, err := json.Marshal(sp)
	if err != nil {
		return fmt.Errorf("marshal pending: %w", err)
	}

	return ms.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketPending).Put([]byte(env.MessageId), val)
	})
}

// UpdatePending updates retry metadata for a pending message.
func (ms *MessageStore) UpdatePending(messageID string, sentAt time.Time, retries int) error {
	return ms.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketPending)
		raw := b.Get([]byte(messageID))
		if raw == nil {
			return nil
		}

		var sp storedPending
		if err := json.Unmarshal(raw, &sp); err != nil {
			return fmt.Errorf("unmarshal pending %s: %w", messageID, err)
		}
		sp.StoredAt = sentAt.Unix()
		sp.Retries = retries

		val, err := json.Marshal(sp)
		if err != nil {
			return fmt.Errorf("marshal pending %s: %w", messageID, err)
		}
		return b.Put([]byte(messageID), val)
	})
}

// RemovePending deletes a pending message (called on ACK).
func (ms *MessageStore) RemovePending(messageID string) error {
	return ms.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketPending).Delete([]byte(messageID))
	})
}

// LoadPending returns all pending messages from the store.
func (ms *MessageStore) LoadPending() ([]*pendingMessage, error) {
	var result []*pendingMessage

	err := ms.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketPending)
		return b.ForEach(func(k, v []byte) error {
			var sp storedPending
			if err := json.Unmarshal(v, &sp); err != nil {
				ms.logger.Warn("skipping corrupt pending entry", "key", string(k), "error", err)
				return nil
			}

			env := &messagepb.Envelope{}
			if err := proto.Unmarshal(sp.EnvelopeRaw, env); err != nil {
				ms.logger.Warn("skipping corrupt envelope", "key", string(k), "error", err)
				return nil
			}

			result = append(result, &pendingMessage{
				env:      env,
				peerAddr: sp.PeerAddr,
				sentAt:   time.Unix(sp.StoredAt, 0),
				retries:  sp.Retries,
			})
			return nil
		})
	})

	return result, err
}

// --- Sessions ---

// StoreSession persists a session to disk.
func (ms *MessageStore) StoreSession(sess *session.Session) error {
	ss := storedSession{
		ID:         sess.ID,
		FromHandle: sess.FromHandle,
		ToHandle:   sess.ToHandle,
		State:      string(sess.State),
		TraceID:    sess.TraceID,
		NextSeq:    sess.NextSeq,
		CreatedAt:  sess.CreatedAt.Unix(),
		UpdatedAt:  sess.UpdatedAt.Unix(),
	}
	val, err := json.Marshal(ss)
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}

	return ms.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketSessions).Put([]byte(sess.ID), val)
	})
}

// RemoveSession deletes a session from the store.
func (ms *MessageStore) RemoveSession(sessionID string) error {
	return ms.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketSessions).Delete([]byte(sessionID))
	})
}

// LoadSessions returns all persisted sessions.
func (ms *MessageStore) LoadSessions() ([]*session.Session, error) {
	var result []*session.Session

	err := ms.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSessions)
		return b.ForEach(func(k, v []byte) error {
			var ss storedSession
			if err := json.Unmarshal(v, &ss); err != nil {
				ms.logger.Warn("skipping corrupt session entry", "key", string(k), "error", err)
				return nil
			}

			sess := &session.Session{
				ID:         ss.ID,
				FromHandle: ss.FromHandle,
				ToHandle:   ss.ToHandle,
				State:      session.State(ss.State),
				TraceID:    ss.TraceID,
				NextSeq:    ss.NextSeq,
				CreatedAt:  time.Unix(ss.CreatedAt, 0),
				UpdatedAt:  time.Unix(ss.UpdatedAt, 0),
			}
			result = append(result, sess)
			return nil
		})
	})

	return result, err
}

// PendingCount returns the number of pending messages in the store.
func (ms *MessageStore) PendingCount() int {
	var count int
	ms.db.View(func(tx *bolt.Tx) error {
		count = tx.Bucket(bucketPending).Stats().KeyN
		return nil
	})
	return count
}
