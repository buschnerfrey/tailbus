package relay

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"sync"

	transportpb "github.com/alexanderfrey/tailbus/api/transportpb"
	"github.com/alexanderfrey/tailbus/internal/identity"
	"github.com/alexanderfrey/tailbus/internal/transport"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

// Server is a DERP-style relay that forwards envelopes between daemons
// that cannot reach each other directly.
type Server struct {
	transportpb.UnimplementedNodeTransportServer
	logger  *slog.Logger
	grpcSrv *grpc.Server
	mu      sync.RWMutex
	streams map[string]*relayStream // pubkey hex → stream
}

type relayStream struct {
	nodeID string
	pubKey []byte
	stream transportpb.NodeTransport_ExchangeServer
	mu     sync.Mutex // protects Send()
}

// NewServer creates a new relay server with mTLS.
func NewServer(logger *slog.Logger, cert *tls.Certificate, verifier transport.PeerVerifier) *Server {
	s := &Server{
		logger:  logger,
		streams: make(map[string]*relayStream),
	}

	var serverOpts []grpc.ServerOption
	if cert != nil {
		serverTLS := &tls.Config{
			Certificates: []tls.Certificate{*cert},
			ClientAuth:   tls.RequireAnyClientCert,
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				if verifier == nil || len(rawCerts) == 0 {
					return nil
				}
				pubKey, err := identity.PubKeyFromCert(rawCerts[0])
				if err != nil {
					return fmt.Errorf("extract peer key: %w", err)
				}
				return verifier.VerifyPeerKey("", pubKey)
			},
		}
		serverOpts = append(serverOpts, grpc.Creds(credentials.NewTLS(serverTLS)))
	}

	gs := grpc.NewServer(serverOpts...)
	transportpb.RegisterNodeTransportServer(gs, s)
	s.grpcSrv = gs
	return s
}

// Exchange handles bidirectional streams from daemons.
// It extracts the peer's pubkey from gRPC metadata (x-tailbus-pubkey header)
// or falls back to the mTLS cert. Metadata-based identification enables
// insecure mode (e.g. when edge TLS terminates before the relay).
func (s *Server) Exchange(stream transportpb.NodeTransport_ExchangeServer) error {
	pubKey, err := s.extractPubKey(stream)
	if err != nil {
		return fmt.Errorf("identify peer: %w", err)
	}

	keyHex := hex.EncodeToString(pubKey)

	// Register this stream
	rs := &relayStream{
		pubKey: pubKey,
		stream: stream,
	}

	s.mu.Lock()
	s.streams[keyHex] = rs
	s.mu.Unlock()

	s.logger.Info("relay peer connected", "pubkey", keyHex[:16]+"...")

	defer func() {
		s.mu.Lock()
		if current, ok := s.streams[keyHex]; ok && current == rs {
			delete(s.streams, keyHex)
		}
		s.mu.Unlock()
		s.logger.Info("relay peer disconnected", "pubkey", keyHex[:16]+"...")
	}()

	// Recv loop: forward envelopes to target peers
	for {
		env, err := stream.Recv()
		if err != nil {
			return err
		}

		targetKey := env.RelayTargetKey
		if len(targetKey) == 0 {
			s.logger.Warn("relay received envelope without relay_target_key",
				"from", keyHex[:16]+"...", "msg_id", env.MessageId)
			continue
		}

		targetHex := hex.EncodeToString(targetKey)

		s.mu.RLock()
		target, found := s.streams[targetHex]
		s.mu.RUnlock()

		if !found {
			s.logger.Warn("relay target not connected",
				"target", targetHex[:16]+"...", "from", keyHex[:16]+"...", "msg_id", env.MessageId)
			continue
		}

		target.mu.Lock()
		err = target.stream.Send(env)
		target.mu.Unlock()

		if err != nil {
			s.logger.Warn("relay forward failed",
				"target", targetHex[:16]+"...", "error", err)
		} else {
			s.logger.Debug("relay forwarded envelope",
				"from", keyHex[:16]+"...", "target", targetHex[:16]+"...", "msg_id", env.MessageId)
		}
	}
}

// extractPubKey extracts the connecting peer's public key.
// It first checks gRPC metadata for the x-tailbus-pubkey header (hex-encoded),
// then falls back to extracting from the mTLS certificate.
func (s *Server) extractPubKey(stream transportpb.NodeTransport_ExchangeServer) ([]byte, error) {
	// Try gRPC metadata first (works in both insecure and mTLS modes)
	if md, ok := metadata.FromIncomingContext(stream.Context()); ok {
		if vals := md.Get("x-tailbus-pubkey"); len(vals) > 0 {
			pubKey, err := hex.DecodeString(vals[0])
			if err != nil {
				return nil, fmt.Errorf("decode x-tailbus-pubkey metadata: %w", err)
			}
			return pubKey, nil
		}
	}

	// Fall back to TLS certificate
	p, ok := peer.FromContext(stream.Context())
	if !ok {
		return nil, fmt.Errorf("no peer info and no x-tailbus-pubkey metadata")
	}

	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return nil, fmt.Errorf("no TLS info and no x-tailbus-pubkey metadata")
	}

	if len(tlsInfo.State.PeerCertificates) == 0 {
		return nil, fmt.Errorf("no peer certificates and no x-tailbus-pubkey metadata")
	}

	return identity.PubKeyFromCert(tlsInfo.State.PeerCertificates[0].Raw)
}

// Serve starts the relay gRPC server on the given listener.
func (s *Server) Serve(lis net.Listener) error {
	s.logger.Info("relay server listening", "addr", lis.Addr())
	return s.grpcSrv.Serve(lis)
}

// ConnectedPeers returns the number of currently connected peers.
func (s *Server) ConnectedPeers() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.streams)
}

// GracefulStop stops the relay server gracefully.
func (s *Server) GracefulStop() {
	s.grpcSrv.GracefulStop()
}

// Stop immediately stops the relay server.
func (s *Server) Stop() {
	s.grpcSrv.Stop()
}
