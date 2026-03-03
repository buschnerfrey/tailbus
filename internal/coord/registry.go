package coord

import (
	"fmt"
	"log/slog"
	"time"

	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
)

// Registry manages node and handle registration.
type Registry struct {
	store  *Store
	logger *slog.Logger
}

// NewRegistry creates a new registry.
func NewRegistry(store *Store, logger *slog.Logger) *Registry {
	return &Registry{store: store, logger: logger}
}

// RegisterNode registers a node and its handles. Returns an error if any handle is
// already claimed by a different node. If isRelay is true the node is a relay server.
func (r *Registry) RegisterNode(nodeID string, pubKey []byte, addr string, handles []string, manifests map[string]*messagepb.ServiceManifest, isRelay bool) error {
	// Check for handle conflicts
	for _, h := range handles {
		rec, err := r.store.LookupHandle(h)
		if err != nil {
			return fmt.Errorf("lookup handle %q: %w", h, err)
		}
		if rec != nil && rec.NodeID != nodeID {
			return fmt.Errorf("handle %q already claimed by node %q", h, rec.NodeID)
		}
	}

	rec := &NodeRecord{
		NodeID:          nodeID,
		PublicKey:       pubKey,
		AdvertiseAddr:   addr,
		Handles:         handles,
		HandleManifests: manifests,
		LastHeartbeat:   time.Now(),
		IsRelay:         isRelay,
	}
	if err := r.store.UpsertNode(rec); err != nil {
		return fmt.Errorf("upsert node: %w", err)
	}

	r.logger.Info("node registered", "node_id", nodeID, "addr", addr, "handles", handles)
	return nil
}

// Heartbeat updates a node's heartbeat timestamp and handles.
func (r *Registry) Heartbeat(nodeID string, handles []string, manifests map[string]*messagepb.ServiceManifest) error {
	return r.store.UpdateHeartbeat(nodeID, handles, manifests)
}

// LookupHandle looks up which node serves a handle.
func (r *Registry) LookupHandle(handle string) (*NodeRecord, error) {
	return r.store.LookupHandle(handle)
}
