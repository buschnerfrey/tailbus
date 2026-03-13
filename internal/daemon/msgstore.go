package daemon

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	"github.com/alexanderfrey/tailbus/internal/session"
	bolt "go.etcd.io/bbolt"
	"google.golang.org/protobuf/proto"
)

var (
	bucketPending     = []byte("pending_messages")
	bucketSessions    = []byte("sessions")
	bucketRooms       = []byte("rooms")
	bucketRoomLogs    = []byte("room_events")
	bucketUsageDaily  = []byte("usage_daily")
	bucketUsageHourly = []byte("usage_hourly")
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
		if _, err := tx.CreateBucketIfNotExists(bucketRooms); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(bucketRoomLogs); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(bucketUsageDaily); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(bucketUsageHourly); err != nil {
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

// --- Rooms ---

func roomSeqKey(seq uint64) []byte {
	return []byte(fmt.Sprintf("%020d", seq))
}

// StoreRoom persists room metadata.
func (ms *MessageStore) StoreRoom(room *messagepb.RoomInfo) error {
	raw, err := proto.Marshal(room)
	if err != nil {
		return fmt.Errorf("marshal room: %w", err)
	}
	return ms.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketRooms).Put([]byte(room.RoomId), raw)
	})
}

// LoadRoom returns a persisted room, if any.
func (ms *MessageStore) LoadRoom(roomID string) (*messagepb.RoomInfo, error) {
	var room *messagepb.RoomInfo
	err := ms.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bucketRooms).Get([]byte(roomID))
		if raw == nil {
			return nil
		}
		room = &messagepb.RoomInfo{}
		return proto.Unmarshal(raw, room)
	})
	return room, err
}

// LoadRooms returns all persisted rooms.
func (ms *MessageStore) LoadRooms() ([]*messagepb.RoomInfo, error) {
	var rooms []*messagepb.RoomInfo
	err := ms.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketRooms).ForEach(func(_, v []byte) error {
			room := &messagepb.RoomInfo{}
			if err := proto.Unmarshal(v, room); err != nil {
				return err
			}
			rooms = append(rooms, room)
			return nil
		})
	})
	return rooms, err
}

// StoreRoomEvent appends a room event and enforces a max retained event count.
func (ms *MessageStore) StoreRoomEvent(event *messagepb.RoomEvent, maxEvents int) error {
	raw, err := proto.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal room event: %w", err)
	}

	return ms.db.Update(func(tx *bolt.Tx) error {
		root := tx.Bucket(bucketRoomLogs)
		roomBucket, err := root.CreateBucketIfNotExists([]byte(event.RoomId))
		if err != nil {
			return err
		}
		if err := roomBucket.Put(roomSeqKey(event.RoomSeq), raw); err != nil {
			return err
		}
		for maxEvents > 0 && countKeys(roomBucket) > maxEvents {
			k, _ := roomBucket.Cursor().First()
			if k == nil {
				break
			}
			if err := roomBucket.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

func countKeys(b *bolt.Bucket) int {
	count := 0
	c := b.Cursor()
	for k, _ := c.First(); k != nil; k, _ = c.Next() {
		count++
	}
	return count
}

func usageMetricBucketKey(metric agentpb.UsageMetric) []byte {
	return []byte(fmt.Sprintf("%02d", int32(metric)))
}

func usageTimeBucketKey(unix int64) []byte {
	return []byte(fmt.Sprintf("%020d", unix))
}

func usageBucketStart(ts time.Time, hourly bool) time.Time {
	ts = ts.UTC()
	if hourly {
		return time.Date(ts.Year(), ts.Month(), ts.Day(), ts.Hour(), 0, 0, 0, time.UTC)
	}
	return time.Date(ts.Year(), ts.Month(), ts.Day(), 0, 0, 0, 0, time.UTC)
}

func incrementUsageBucket(root *bolt.Bucket, metric agentpb.UsageMetric, bucketStartUnix, delta int64) error {
	metricBucket, err := root.CreateBucketIfNotExists(usageMetricBucketKey(metric))
	if err != nil {
		return err
	}
	key := usageTimeBucketKey(bucketStartUnix)
	current := int64(0)
	if raw := metricBucket.Get(key); raw != nil {
		current, err = strconv.ParseInt(string(raw), 10, 64)
		if err != nil {
			return fmt.Errorf("parse usage bucket %q: %w", string(key), err)
		}
	}
	return metricBucket.Put(key, []byte(strconv.FormatInt(current+delta, 10)))
}

func (ms *MessageStore) RecordUsage(metric agentpb.UsageMetric, at time.Time, count int64) error {
	if metric == agentpb.UsageMetric_USAGE_METRIC_UNSPECIFIED || count == 0 {
		return nil
	}
	dayStart := usageBucketStart(at, false).Unix()
	hourStart := usageBucketStart(at, true).Unix()
	return ms.db.Update(func(tx *bolt.Tx) error {
		if err := incrementUsageBucket(tx.Bucket(bucketUsageDaily), metric, dayStart, count); err != nil {
			return err
		}
		if err := incrementUsageBucket(tx.Bucket(bucketUsageHourly), metric, hourStart, count); err != nil {
			return err
		}
		return nil
	})
}

func usageMetricsInOrder() []agentpb.UsageMetric {
	return []agentpb.UsageMetric{
		agentpb.UsageMetric_USAGE_METRIC_MESSAGES_ROUTED,
		agentpb.UsageMetric_USAGE_METRIC_SESSIONS_OPENED,
		agentpb.UsageMetric_USAGE_METRIC_ROOM_MESSAGES_POSTED,
		agentpb.UsageMetric_USAGE_METRIC_ROOMS_CREATED,
	}
}

func loadUsageMetricHistory(root *bolt.Bucket, metric agentpb.UsageMetric) (*agentpb.UsageMetricHistory, error) {
	history := &agentpb.UsageMetricHistory{Metric: metric}
	if root == nil {
		return history, nil
	}
	metricBucket := root.Bucket(usageMetricBucketKey(metric))
	if metricBucket == nil {
		return history, nil
	}
	err := metricBucket.ForEach(func(k, v []byte) error {
		startUnix, err := strconv.ParseInt(string(k), 10, 64)
		if err != nil {
			return fmt.Errorf("parse usage key %q: %w", string(k), err)
		}
		count, err := strconv.ParseInt(string(v), 10, 64)
		if err != nil {
			return fmt.Errorf("parse usage value %q: %w", string(v), err)
		}
		history.DailyBuckets = append(history.DailyBuckets, &agentpb.UsageBucket{
			BucketStartUnix: startUnix,
			Count:           count,
		})
		history.Total += count
		return nil
	})
	return history, err
}

func (ms *MessageStore) LoadUsageHistory() (*agentpb.UsageHistory, error) {
	result := &agentpb.UsageHistory{}
	err := ms.db.View(func(tx *bolt.Tx) error {
		root := tx.Bucket(bucketUsageDaily)
		for _, metric := range usageMetricsInOrder() {
			history, err := loadUsageMetricHistory(root, metric)
			if err != nil {
				return err
			}
			result.Metrics = append(result.Metrics, history)
		}
		return nil
	})
	return result, err
}

// ReplayRoomEvents returns retained room events with sequence greater than sinceSeq.
func (ms *MessageStore) ReplayRoomEvents(roomID string, sinceSeq uint64) ([]*messagepb.RoomEvent, error) {
	var events []*messagepb.RoomEvent
	err := ms.db.View(func(tx *bolt.Tx) error {
		root := tx.Bucket(bucketRoomLogs)
		if root == nil {
			return nil
		}
		roomBucket := root.Bucket([]byte(roomID))
		if roomBucket == nil {
			return nil
		}
		c := roomBucket.Cursor()
		for k, v := c.Seek(roomSeqKey(sinceSeq + 1)); k != nil; k, v = c.Next() {
			event := &messagepb.RoomEvent{}
			if err := proto.Unmarshal(v, event); err != nil {
				return err
			}
			events = append(events, event)
		}
		return nil
	})
	return events, err
}
