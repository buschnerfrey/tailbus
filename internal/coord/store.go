package coord

import (
	"database/sql"
	"fmt"
	"time"

	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	_ "modernc.org/sqlite"
	"google.golang.org/protobuf/encoding/protojson"
)

// NodeRecord represents a registered node in the database.
type NodeRecord struct {
	NodeID          string
	PublicKey       []byte
	AdvertiseAddr   string
	Handles         []string
	HandleManifests map[string]*messagepb.ServiceManifest
	LastHeartbeat   time.Time
	IsRelay         bool
}

// Store provides SQLite persistence for the coordination server.
type Store struct {
	db *sql.DB
}

// NewStore opens or creates a SQLite database at the given path.
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS nodes (
			node_id TEXT PRIMARY KEY,
			public_key BLOB NOT NULL,
			advertise_addr TEXT NOT NULL,
			last_heartbeat INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS handles (
			handle TEXT PRIMARY KEY,
			node_id TEXT NOT NULL REFERENCES nodes(node_id) ON DELETE CASCADE,
			description TEXT NOT NULL DEFAULT '',
			manifest TEXT NOT NULL DEFAULT '{}'
		);
	`)
	if err != nil {
		return err
	}
	// Additive migrations for existing databases.
	s.db.Exec("ALTER TABLE handles ADD COLUMN description TEXT NOT NULL DEFAULT ''")
	s.db.Exec("ALTER TABLE handles ADD COLUMN manifest TEXT NOT NULL DEFAULT '{}'")
	s.db.Exec("ALTER TABLE nodes ADD COLUMN is_relay INTEGER NOT NULL DEFAULT 0")
	return nil
}

func marshalManifest(m *messagepb.ServiceManifest) string {
	if m == nil {
		return "{}"
	}
	b, err := protojson.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func unmarshalManifest(data string) *messagepb.ServiceManifest {
	if data == "" || data == "{}" {
		return nil
	}
	m := &messagepb.ServiceManifest{}
	if err := protojson.Unmarshal([]byte(data), m); err != nil {
		return nil
	}
	return m
}

// UpsertNode inserts or updates a node record and its handles.
func (s *Store) UpsertNode(rec *NodeRecord) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	isRelay := 0
	if rec.IsRelay {
		isRelay = 1
	}
	_, err = tx.Exec(`
		INSERT INTO nodes (node_id, public_key, advertise_addr, last_heartbeat, is_relay)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET
			public_key = excluded.public_key,
			advertise_addr = excluded.advertise_addr,
			last_heartbeat = excluded.last_heartbeat,
			is_relay = excluded.is_relay
	`, rec.NodeID, rec.PublicKey, rec.AdvertiseAddr, rec.LastHeartbeat.Unix(), isRelay)
	if err != nil {
		return err
	}

	// Remove old handles for this node
	_, err = tx.Exec("DELETE FROM handles WHERE node_id = ?", rec.NodeID)
	if err != nil {
		return err
	}

	// Insert new handles with manifests
	for _, h := range rec.Handles {
		var m *messagepb.ServiceManifest
		if rec.HandleManifests != nil {
			m = rec.HandleManifests[h]
		}
		desc := ""
		if m != nil {
			desc = m.Description
		}
		_, err = tx.Exec("INSERT INTO handles (handle, node_id, description, manifest) VALUES (?, ?, ?, ?)",
			h, rec.NodeID, desc, marshalManifest(m))
		if err != nil {
			return fmt.Errorf("register handle %q: %w", h, err)
		}
	}

	return tx.Commit()
}

// UpdateHeartbeat updates the heartbeat timestamp and handles for a node.
func (s *Store) UpdateHeartbeat(nodeID string, handles []string, manifests map[string]*messagepb.ServiceManifest) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec("UPDATE nodes SET last_heartbeat = ? WHERE node_id = ?", time.Now().Unix(), nodeID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("node %q not found", nodeID)
	}

	// Update handles
	_, err = tx.Exec("DELETE FROM handles WHERE node_id = ?", nodeID)
	if err != nil {
		return err
	}
	for _, h := range handles {
		var m *messagepb.ServiceManifest
		if manifests != nil {
			m = manifests[h]
		}
		desc := ""
		if m != nil {
			desc = m.Description
		}
		_, err = tx.Exec("INSERT OR REPLACE INTO handles (handle, node_id, description, manifest) VALUES (?, ?, ?, ?)",
			h, nodeID, desc, marshalManifest(m))
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetAllNodes returns all registered nodes with their handles.
func (s *Store) GetAllNodes() ([]*NodeRecord, error) {
	rows, err := s.db.Query("SELECT node_id, public_key, advertise_addr, last_heartbeat, is_relay FROM nodes")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	nodeMap := make(map[string]*NodeRecord)
	var nodes []*NodeRecord
	for rows.Next() {
		var rec NodeRecord
		var ts int64
		var isRelay int
		if err := rows.Scan(&rec.NodeID, &rec.PublicKey, &rec.AdvertiseAddr, &ts, &isRelay); err != nil {
			return nil, err
		}
		rec.LastHeartbeat = time.Unix(ts, 0)
		rec.IsRelay = isRelay != 0
		nodeMap[rec.NodeID] = &rec
		nodes = append(nodes, &rec)
	}

	// Load handles with manifests
	hrows, err := s.db.Query("SELECT handle, node_id, description, manifest FROM handles")
	if err != nil {
		return nil, err
	}
	defer hrows.Close()
	for hrows.Next() {
		var h, nodeID, desc, manifestJSON string
		if err := hrows.Scan(&h, &nodeID, &desc, &manifestJSON); err != nil {
			return nil, err
		}
		if rec, ok := nodeMap[nodeID]; ok {
			rec.Handles = append(rec.Handles, h)
			m := unmarshalManifest(manifestJSON)
			// Fall back to description for old rows that have no manifest
			if m == nil && desc != "" {
				m = &messagepb.ServiceManifest{Description: desc}
			}
			if m != nil {
				if rec.HandleManifests == nil {
					rec.HandleManifests = make(map[string]*messagepb.ServiceManifest)
				}
				rec.HandleManifests[h] = m
			}
		}
	}

	return nodes, nil
}

// LookupHandle finds which node serves a given handle.
func (s *Store) LookupHandle(handle string) (*NodeRecord, error) {
	var nodeID string
	err := s.db.QueryRow("SELECT node_id FROM handles WHERE handle = ?", handle).Scan(&nodeID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var rec NodeRecord
	var ts int64
	err = s.db.QueryRow("SELECT node_id, public_key, advertise_addr, last_heartbeat FROM nodes WHERE node_id = ?", nodeID).
		Scan(&rec.NodeID, &rec.PublicKey, &rec.AdvertiseAddr, &ts)
	if err != nil {
		return nil, err
	}
	rec.LastHeartbeat = time.Unix(ts, 0)

	hrows, err := s.db.Query("SELECT handle, manifest FROM handles WHERE node_id = ?", nodeID)
	if err != nil {
		return nil, err
	}
	defer hrows.Close()
	for hrows.Next() {
		var h, manifestJSON string
		if err := hrows.Scan(&h, &manifestJSON); err != nil {
			return nil, err
		}
		rec.Handles = append(rec.Handles, h)
		m := unmarshalManifest(manifestJSON)
		if m != nil {
			if rec.HandleManifests == nil {
				rec.HandleManifests = make(map[string]*messagepb.ServiceManifest)
			}
			rec.HandleManifests[h] = m
		}
	}

	return &rec, nil
}

// RemoveNode removes a node and its handles.
func (s *Store) RemoveNode(nodeID string) error {
	_, err := s.db.Exec("DELETE FROM nodes WHERE node_id = ?", nodeID)
	return err
}

// RemoveStaleNodes removes nodes whose last heartbeat is older than the given time.
// Returns the number of nodes removed.
func (s *Store) RemoveStaleNodes(olderThan time.Time) (int, error) {
	res, err := s.db.Exec("DELETE FROM nodes WHERE last_heartbeat < ?", olderThan.Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}
