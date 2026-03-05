package coord

import (
	"database/sql"
	"fmt"
	"strings"
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
	TeamID          string
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
			handle TEXT NOT NULL,
			node_id TEXT NOT NULL REFERENCES nodes(node_id) ON DELETE CASCADE,
			description TEXT NOT NULL DEFAULT '',
			manifest TEXT NOT NULL DEFAULT '{}',
			PRIMARY KEY (handle, node_id)
		);
	`)
	if err != nil {
		return err
	}
	// Additive migrations for existing databases.
	s.db.Exec("ALTER TABLE handles ADD COLUMN description TEXT NOT NULL DEFAULT ''")
	s.db.Exec("ALTER TABLE handles ADD COLUMN manifest TEXT NOT NULL DEFAULT '{}'")
	s.db.Exec("ALTER TABLE nodes ADD COLUMN is_relay INTEGER NOT NULL DEFAULT 0")

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS auth_tokens (
			name TEXT PRIMARY KEY,
			token_hash TEXT NOT NULL UNIQUE,
			single_use INTEGER NOT NULL DEFAULT 0,
			used_by TEXT,
			created_at INTEGER NOT NULL,
			expires_at INTEGER
		);
	`)
	if err != nil {
		return fmt.Errorf("create auth_tokens table: %w", err)
	}

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			email TEXT PRIMARY KEY,
			created_at INTEGER NOT NULL,
			last_login INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS node_users (
			node_id TEXT NOT NULL,
			email TEXT NOT NULL,
			bound_at INTEGER NOT NULL,
			PRIMARY KEY (node_id, email)
		);
	`)
	if err != nil {
		return fmt.Errorf("create users tables: %w", err)
	}

	// Team tables
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS teams (
			team_id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			created_by TEXT NOT NULL,
			created_at INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS team_members (
			team_id TEXT NOT NULL REFERENCES teams(team_id) ON DELETE CASCADE,
			email TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'member',
			joined_at INTEGER NOT NULL,
			PRIMARY KEY (team_id, email)
		);
		CREATE TABLE IF NOT EXISTS team_invites (
			code TEXT PRIMARY KEY,
			team_id TEXT NOT NULL REFERENCES teams(team_id) ON DELETE CASCADE,
			created_by TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			max_uses INTEGER NOT NULL DEFAULT 1,
			use_count INTEGER NOT NULL DEFAULT 0
		);
	`)
	if err != nil {
		return fmt.Errorf("create team tables: %w", err)
	}
	s.db.Exec("ALTER TABLE nodes ADD COLUMN team_id TEXT NOT NULL DEFAULT ''")

	// Migrate handles table to support team-scoped handles (same handle name in
	// different teams). Old schema: handle TEXT PRIMARY KEY (globally unique).
	// New schema: PRIMARY KEY (handle, node_id) allows duplicate handle names
	// across different nodes/teams.
	var handlePKCols int
	row := s.db.QueryRow(`
		SELECT COUNT(*) FROM pragma_table_info('handles')
		WHERE pk > 0
	`)
	_ = row.Scan(&handlePKCols)
	if handlePKCols == 1 {
		// Single-column PK (old schema) — migrate to composite PK
		s.db.Exec(`
			CREATE TABLE IF NOT EXISTS handles_new (
				handle TEXT NOT NULL,
				node_id TEXT NOT NULL REFERENCES nodes(node_id) ON DELETE CASCADE,
				description TEXT NOT NULL DEFAULT '',
				manifest TEXT NOT NULL DEFAULT '{}',
				PRIMARY KEY (handle, node_id)
			);
			INSERT OR IGNORE INTO handles_new SELECT handle, node_id, description, manifest FROM handles;
			DROP TABLE handles;
			ALTER TABLE handles_new RENAME TO handles;
		`)
	}

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
		INSERT INTO nodes (node_id, public_key, advertise_addr, last_heartbeat, is_relay, team_id)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET
			public_key = excluded.public_key,
			advertise_addr = excluded.advertise_addr,
			last_heartbeat = excluded.last_heartbeat,
			is_relay = excluded.is_relay,
			team_id = excluded.team_id
	`, rec.NodeID, rec.PublicKey, rec.AdvertiseAddr, rec.LastHeartbeat.Unix(), isRelay, rec.TeamID)
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
	rows, err := s.db.Query("SELECT node_id, public_key, advertise_addr, last_heartbeat, is_relay, team_id FROM nodes")
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
		if err := rows.Scan(&rec.NodeID, &rec.PublicKey, &rec.AdvertiseAddr, &ts, &isRelay, &rec.TeamID); err != nil {
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

// InsertAuthToken inserts a new auth token record.
func (s *Store) InsertAuthToken(name, tokenHash string, singleUse bool, expiresAt *time.Time) error {
	su := 0
	if singleUse {
		su = 1
	}
	var exp *int64
	if expiresAt != nil {
		v := expiresAt.Unix()
		exp = &v
	}
	_, err := s.db.Exec(
		"INSERT INTO auth_tokens (name, token_hash, single_use, created_at, expires_at) VALUES (?, ?, ?, ?, ?)",
		name, tokenHash, su, time.Now().Unix(), exp,
	)
	return err
}

// ValidateAndConsumeToken checks that a token hash exists, is not expired,
// and has not been consumed (if single-use). If the token is single-use,
// it marks it as consumed by the given nodeID.
func (s *Store) ValidateAndConsumeToken(tokenHash, nodeID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var name string
	var singleUse int
	var usedBy sql.NullString
	var expiresAt sql.NullInt64
	err = tx.QueryRow(
		"SELECT name, single_use, used_by, expires_at FROM auth_tokens WHERE token_hash = ?",
		tokenHash,
	).Scan(&name, &singleUse, &usedBy, &expiresAt)
	if err == sql.ErrNoRows {
		return fmt.Errorf("invalid auth token")
	}
	if err != nil {
		return err
	}

	if expiresAt.Valid && time.Now().Unix() > expiresAt.Int64 {
		return fmt.Errorf("auth token %q has expired", name)
	}

	if singleUse != 0 && usedBy.Valid {
		return fmt.Errorf("auth token %q has already been used", name)
	}

	if singleUse != 0 {
		_, err = tx.Exec("UPDATE auth_tokens SET used_by = ? WHERE token_hash = ?", nodeID, tokenHash)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// HasAuthTokens returns true if any auth tokens are configured.
func (s *Store) HasAuthTokens() (bool, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM auth_tokens").Scan(&count)
	return count > 0, err
}

// RevokeAuthToken deletes an auth token by name.
func (s *Store) RevokeAuthToken(name string) error {
	_, err := s.db.Exec("DELETE FROM auth_tokens WHERE name = ?", name)
	return err
}

// UpsertUser creates or updates a user record, updating last_login.
func (s *Store) UpsertUser(email string) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(`
		INSERT INTO users (email, created_at, last_login) VALUES (?, ?, ?)
		ON CONFLICT(email) DO UPDATE SET last_login = excluded.last_login
	`, email, now, now)
	return err
}

// BindNodeToUser associates a node with a user.
func (s *Store) BindNodeToUser(nodeID, email string) error {
	_, err := s.db.Exec(`
		INSERT INTO node_users (node_id, email, bound_at) VALUES (?, ?, ?)
		ON CONFLICT(node_id, email) DO UPDATE SET bound_at = excluded.bound_at
	`, nodeID, email, time.Now().Unix())
	return err
}

// GetNodeUser returns the email of the user bound to a node, or "" if none.
func (s *Store) GetNodeUser(nodeID string) (string, error) {
	var email string
	err := s.db.QueryRow("SELECT email FROM node_users WHERE node_id = ? LIMIT 1", nodeID).Scan(&email)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return email, err
}

// ListUserNodes returns all node IDs bound to a user.
func (s *Store) ListUserNodes(email string) ([]string, error) {
	rows, err := s.db.Query("SELECT node_id FROM node_users WHERE email = ?", email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []string
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			return nil, err
		}
		nodes = append(nodes, nodeID)
	}
	return nodes, nil
}

// CreateTeam creates a new team with the given creator as owner.
func (s *Store) CreateTeam(teamID, name, email string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	_, err = tx.Exec("INSERT INTO teams (team_id, name, created_by, created_at) VALUES (?, ?, ?, ?)",
		teamID, name, email, now)
	if err != nil {
		return fmt.Errorf("create team: %w", err)
	}
	_, err = tx.Exec("INSERT INTO team_members (team_id, email, role, joined_at) VALUES (?, ?, 'owner', ?)",
		teamID, email, now)
	if err != nil {
		return fmt.Errorf("add team owner: %w", err)
	}
	return tx.Commit()
}

// GetTeamByName returns a team's ID and creator by name.
func (s *Store) GetTeamByName(name string) (teamID, createdBy string, err error) {
	err = s.db.QueryRow("SELECT team_id, created_by FROM teams WHERE name = ?", name).Scan(&teamID, &createdBy)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	return
}

// ListUserTeams returns all teams a user is a member of.
func (s *Store) ListUserTeams(email string) ([]struct {
	TeamID string
	Name   string
	Role   string
}, error) {
	rows, err := s.db.Query(`
		SELECT t.team_id, t.name, tm.role
		FROM teams t JOIN team_members tm ON t.team_id = tm.team_id
		WHERE tm.email = ?
		ORDER BY t.name
	`, email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var teams []struct {
		TeamID string
		Name   string
		Role   string
	}
	for rows.Next() {
		var t struct {
			TeamID string
			Name   string
			Role   string
		}
		if err := rows.Scan(&t.TeamID, &t.Name, &t.Role); err != nil {
			return nil, err
		}
		teams = append(teams, t)
	}
	return teams, nil
}

// AddTeamMember adds a user as a member of a team.
func (s *Store) AddTeamMember(teamID, email, role string) error {
	_, err := s.db.Exec(`
		INSERT INTO team_members (team_id, email, role, joined_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(team_id, email) DO UPDATE SET role = excluded.role
	`, teamID, email, role, time.Now().Unix())
	return err
}

// RemoveTeamMember removes a user from a team.
func (s *Store) RemoveTeamMember(teamID, email string) error {
	res, err := s.db.Exec("DELETE FROM team_members WHERE team_id = ? AND email = ?", teamID, email)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user %q is not a member of this team", email)
	}
	return nil
}

// UpdateTeamMemberRole changes a member's role within a team.
func (s *Store) UpdateTeamMemberRole(teamID, email, role string) error {
	res, err := s.db.Exec("UPDATE team_members SET role = ? WHERE team_id = ? AND email = ?", role, teamID, email)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user %q is not a member of this team", email)
	}
	return nil
}

// DeleteTeam removes a team and all its members/invites (via CASCADE).
func (s *Store) DeleteTeam(teamID string) error {
	res, err := s.db.Exec("DELETE FROM teams WHERE team_id = ?", teamID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("team not found")
	}
	// Clear team_id from nodes that belonged to this team
	_, err = s.db.Exec("UPDATE nodes SET team_id = '' WHERE team_id = ?", teamID)
	return err
}

// GetTeamMembers returns all members of a team.
func (s *Store) GetTeamMembers(teamID string) ([]struct {
	Email string
	Role  string
}, error) {
	rows, err := s.db.Query("SELECT email, role FROM team_members WHERE team_id = ? ORDER BY email", teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []struct {
		Email string
		Role  string
	}
	for rows.Next() {
		var m struct {
			Email string
			Role  string
		}
		if err := rows.Scan(&m.Email, &m.Role); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, nil
}

// GetUserTeamRole returns the role of a user in a team, or "" if not a member.
func (s *Store) GetUserTeamRole(teamID, email string) (string, error) {
	var role string
	err := s.db.QueryRow("SELECT role FROM team_members WHERE team_id = ? AND email = ?", teamID, email).Scan(&role)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return role, err
}

// CreateTeamInvite creates an invite code for a team.
func (s *Store) CreateTeamInvite(code, teamID, email string, expiresAt time.Time, maxUses int) error {
	_, err := s.db.Exec(`
		INSERT INTO team_invites (code, team_id, created_by, created_at, expires_at, max_uses)
		VALUES (?, ?, ?, ?, ?, ?)
	`, code, teamID, email, time.Now().Unix(), expiresAt.Unix(), maxUses)
	return err
}

// ConsumeTeamInvite validates and consumes an invite code. Returns the team_id if valid.
func (s *Store) ConsumeTeamInvite(code string) (string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var teamID string
	var expiresAt int64
	var maxUses, useCount int
	err = tx.QueryRow(
		"SELECT team_id, expires_at, max_uses, use_count FROM team_invites WHERE code = ?", code,
	).Scan(&teamID, &expiresAt, &maxUses, &useCount)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("invalid invite code")
	}
	if err != nil {
		return "", err
	}

	if time.Now().Unix() > expiresAt {
		return "", fmt.Errorf("invite code has expired")
	}
	if useCount >= maxUses {
		return "", fmt.Errorf("invite code has reached max uses")
	}

	_, err = tx.Exec("UPDATE team_invites SET use_count = use_count + 1 WHERE code = ?", code)
	if err != nil {
		return "", err
	}

	return teamID, tx.Commit()
}

// EnsurePersonalTeam creates a personal team for the user if they have none.
// Returns the team ID and name. If the user already has teams, returns empty strings.
func (s *Store) EnsurePersonalTeam(email string) (string, string, error) {
	teams, err := s.ListUserTeams(email)
	if err != nil {
		return "", "", fmt.Errorf("list user teams: %w", err)
	}
	if len(teams) > 0 {
		return "", "", nil // already has a team
	}

	// Derive team name from email prefix
	name := email
	if idx := strings.Index(email, "@"); idx > 0 {
		name = email[:idx]
	}

	teamID := generateID(8)
	if err := s.CreateTeam(teamID, name, email); err != nil {
		// Name collision — append a short random suffix
		teamID = generateID(8)
		name = name + "-" + generateID(2)
		if err := s.CreateTeam(teamID, name, email); err != nil {
			return "", "", fmt.Errorf("create personal team: %w", err)
		}
	}
	return teamID, name, nil
}

// GetNodesByTeam returns nodes belonging to a team plus all relay nodes.
func (s *Store) GetNodesByTeam(teamID string) ([]*NodeRecord, error) {
	rows, err := s.db.Query(
		"SELECT node_id, public_key, advertise_addr, last_heartbeat, is_relay, team_id FROM nodes WHERE team_id = ? OR is_relay = 1",
		teamID,
	)
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
		if err := rows.Scan(&rec.NodeID, &rec.PublicKey, &rec.AdvertiseAddr, &ts, &isRelay, &rec.TeamID); err != nil {
			return nil, err
		}
		rec.LastHeartbeat = time.Unix(ts, 0)
		rec.IsRelay = isRelay != 0
		nodeMap[rec.NodeID] = &rec
		nodes = append(nodes, &rec)
	}

	// Load handles for these nodes
	if len(nodeMap) == 0 {
		return nodes, nil
	}
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

// SetNodeTeam updates the team_id for a node.
func (s *Store) SetNodeTeam(nodeID, teamID string) error {
	_, err := s.db.Exec("UPDATE nodes SET team_id = ? WHERE node_id = ?", teamID, nodeID)
	return err
}

// LookupHandleInTeam finds which node serves a handle within a team scope.
// If teamID is empty, searches all nodes (personal mode).
func (s *Store) LookupHandleInTeam(handle, teamID string) (*NodeRecord, error) {
	if teamID == "" {
		return s.LookupHandle(handle)
	}

	var nodeID string
	err := s.db.QueryRow(`
		SELECT h.node_id FROM handles h
		JOIN nodes n ON h.node_id = n.node_id
		WHERE h.handle = ? AND n.team_id = ?
	`, handle, teamID).Scan(&nodeID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var rec NodeRecord
	var ts int64
	var isRelay int
	err = s.db.QueryRow(
		"SELECT node_id, public_key, advertise_addr, last_heartbeat, is_relay, team_id FROM nodes WHERE node_id = ?",
		nodeID,
	).Scan(&rec.NodeID, &rec.PublicKey, &rec.AdvertiseAddr, &ts, &isRelay, &rec.TeamID)
	if err != nil {
		return nil, err
	}
	rec.LastHeartbeat = time.Unix(ts, 0)
	rec.IsRelay = isRelay != 0

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

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}
