package daemon

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"time"

	pb "github.com/alexanderfrey/tailbus/api/coordpb"
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	"github.com/alexanderfrey/tailbus/internal/handle"
	"github.com/alexanderfrey/tailbus/internal/identity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// CoordClient connects to the coordination server for registration and peer map updates.
type CoordClient struct {
	conn     *grpc.ClientConn
	client   pb.CoordinationAPIClient
	nodeID   string
	pubKey   []byte
	addr     string
	logger   *slog.Logger
	resolver *handle.Resolver
}

// NewCoordClient creates a new coordination client.
// If kp is non-nil, mTLS is enabled with TOFU verification of the coord cert
// using the fingerprint file at coordFPFile.
func NewCoordClient(coordAddr, nodeID string, pubKey []byte, advertiseAddr string, resolver *handle.Resolver, logger *slog.Logger, kp *identity.Keypair, coordFPFile string) (*CoordClient, error) {
	var dialOpt grpc.DialOption
	if kp != nil {
		cert, err := identity.SelfSignedCert(kp)
		if err != nil {
			return nil, fmt.Errorf("generate client TLS cert: %w", err)
		}
		tofu := identity.NewTOFUVerifier(coordFPFile)
		clientTLS := &tls.Config{
			Certificates:          []tls.Certificate{cert},
			InsecureSkipVerify:    true,
			VerifyPeerCertificate: tofu.Verify,
		}
		dialOpt = grpc.WithTransportCredentials(credentials.NewTLS(clientTLS))
	} else {
		dialOpt = grpc.WithTransportCredentials(insecure.NewCredentials())
	}

	conn, err := grpc.NewClient(coordAddr, dialOpt)
	if err != nil {
		return nil, fmt.Errorf("connect to coord server: %w", err)
	}

	return &CoordClient{
		conn:     conn,
		client:   pb.NewCoordinationAPIClient(conn),
		nodeID:   nodeID,
		pubKey:   pubKey,
		addr:     advertiseAddr,
		logger:   logger,
		resolver: resolver,
	}, nil
}

// Register registers this node with the coordination server.
func (c *CoordClient) Register(ctx context.Context, handles []string, manifests map[string]*messagepb.ServiceManifest) error {
	// Build deprecated descriptions from manifests for backward compat with old coord servers
	descs := make(map[string]string, len(manifests))
	for h, m := range manifests {
		if m != nil && m.Description != "" {
			descs[h] = m.Description
		}
	}

	resp, err := c.client.RegisterNode(ctx, &pb.RegisterNodeRequest{
		NodeId:             c.nodeID,
		PublicKey:          c.pubKey,
		AdvertiseAddr:      c.addr,
		Handles:            handles,
		HandleDescriptions: descs,
		HandleManifests:    manifests,
	})
	if err != nil {
		return fmt.Errorf("register node: %w", err)
	}
	if !resp.Ok {
		return fmt.Errorf("registration rejected: %s", resp.Error)
	}
	c.logger.Info("registered with coordination server", "node_id", c.nodeID)
	return nil
}

// WatchPeerMap starts watching for peer map updates and updating the resolver.
// Blocks until the context is cancelled.
func (c *CoordClient) WatchPeerMap(ctx context.Context) error {
	stream, err := c.client.WatchPeerMap(ctx, &pb.WatchPeerMapRequest{NodeId: c.nodeID})
	if err != nil {
		return fmt.Errorf("watch peer map: %w", err)
	}

	for {
		update, err := stream.Recv()
		if err != nil {
			return fmt.Errorf("recv peer map: %w", err)
		}

		entries := make(map[string]handle.PeerInfo)
		for _, p := range update.Peers {
			for _, h := range p.Handles {
				info := handle.PeerInfo{
					NodeID:        p.NodeId,
					PublicKey:     p.PublicKey,
					AdvertiseAddr: p.AdvertiseAddr,
				}
				// Prefer HandleManifests, fall back to HandleDescriptions
				if m, ok := p.HandleManifests[h]; ok && m != nil {
					info.Manifest = protoToHandleManifest(m)
				} else if p.HandleDescriptions != nil {
					if desc := p.HandleDescriptions[h]; desc != "" {
						info.Manifest = handle.ServiceManifest{Description: desc}
					}
				}
				entries[h] = info
			}
		}
		c.resolver.UpdatePeerMap(entries)
		c.logger.Info("peer map updated", "version", update.Version, "peers", len(update.Peers))
	}
}

// Heartbeat sends periodic heartbeats to the coordination server.
// If the coord responds with Ok: false (e.g. "node not found" after a coord
// restart), it calls reRegister to re-register the node. Blocks until the
// context is cancelled.
func (c *CoordClient) Heartbeat(ctx context.Context, getHandles func() []string, getManifests func() map[string]*messagepb.ServiceManifest, interval time.Duration, reRegister func(ctx context.Context) error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			handles := getHandles()
			manifests := getManifests()
			// Build deprecated descriptions for backward compat
			descs := make(map[string]string, len(manifests))
			for h, m := range manifests {
				if m != nil && m.Description != "" {
					descs[h] = m.Description
				}
			}
			resp, err := c.client.Heartbeat(ctx, &pb.HeartbeatRequest{
				NodeId:             c.nodeID,
				Handles:            handles,
				HandleDescriptions: descs,
				HandleManifests:    manifests,
			})
			if err != nil {
				c.logger.Error("heartbeat failed", "error", err)
				if reRegister != nil {
					c.logger.Warn("heartbeat error, attempting re-registration")
					if rerr := reRegister(ctx); rerr != nil {
						c.logger.Error("re-registration failed", "error", rerr)
					}
				}
				continue
			}
			if !resp.Ok && reRegister != nil {
				c.logger.Warn("heartbeat rejected (node not found), re-registering")
				if rerr := reRegister(ctx); rerr != nil {
					c.logger.Error("re-registration failed", "error", rerr)
				}
			}
		}
	}
}

// Close closes the connection.
func (c *CoordClient) Close() error {
	return c.conn.Close()
}

// protoToHandleManifest converts a protobuf ServiceManifest to the Go-native type.
func protoToHandleManifest(m *messagepb.ServiceManifest) handle.ServiceManifest {
	if m == nil {
		return handle.ServiceManifest{}
	}
	result := handle.ServiceManifest{
		Description: m.Description,
		Tags:        m.Tags,
		Version:     m.Version,
	}
	for _, c := range m.Commands {
		result.Commands = append(result.Commands, handle.CommandSpec{
			Name:             c.Name,
			Description:      c.Description,
			ParametersSchema: c.ParametersSchema,
		})
	}
	return result
}

// handleManifestToProto converts a Go-native ServiceManifest to the protobuf type.
func handleManifestToProto(m handle.ServiceManifest) *messagepb.ServiceManifest {
	result := &messagepb.ServiceManifest{
		Description: m.Description,
		Tags:        m.Tags,
		Version:     m.Version,
	}
	for _, c := range m.Commands {
		result.Commands = append(result.Commands, &messagepb.CommandSpec{
			Name:             c.Name,
			Description:      c.Description,
			ParametersSchema: c.ParametersSchema,
		})
	}
	return result
}
