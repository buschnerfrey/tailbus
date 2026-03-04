package coord

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	pb "github.com/alexanderfrey/tailbus/api/coordpb"
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	"github.com/alexanderfrey/tailbus/internal/identity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Server is the coordination gRPC server.
type Server struct {
	pb.UnimplementedCoordinationAPIServer

	store      *Store
	registry   *Registry
	peerMap    *PeerMap
	admission  *Admission
	jwtIssuer  *JWTIssuer
	oauth      *OAuthServer
	rest       *RESTHandler
	corsOrigin string
	logger     *slog.Logger
	grpc       *grpc.Server
}

// NewServer creates a new coordination server.
// If kp is non-nil, mTLS is enabled: the server presents its cert and
// requires valid client certs with an Ed25519 pubkey in Organization[0].
func NewServer(store *Store, logger *slog.Logger, kp *identity.Keypair) (*Server, error) {
	registry := NewRegistry(store, logger)
	peerMap := NewPeerMap(store, logger)

	admission := NewAdmission(store, logger)

	s := &Server{
		store:     store,
		registry:  registry,
		peerMap:   peerMap,
		admission: admission,
		logger:    logger,
	}

	var serverOpts []grpc.ServerOption
	if kp != nil {
		cert, err := identity.SelfSignedCert(kp)
		if err != nil {
			return nil, fmt.Errorf("generate coord TLS cert: %w", err)
		}
		tlsCfg := &tls.Config{
			Certificates: []tls.Certificate{cert},
			ClientAuth:   tls.RequireAnyClientCert,
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				if len(rawCerts) == 0 {
					return nil
				}
				// Verify the client cert has a valid pubkey in Organization[0]
				_, err := identity.PubKeyFromCert(rawCerts[0])
				return err
			},
		}
		serverOpts = append(serverOpts, grpc.Creds(credentials.NewTLS(tlsCfg)))
	}

	gs := grpc.NewServer(serverOpts...)
	pb.RegisterCoordinationAPIServer(gs, s)
	s.grpc = gs
	return s, nil
}

// Admission returns the server's admission controller for token seeding.
func (s *Server) Admission() *Admission {
	return s.admission
}

// SetJWT configures JWT issuing/validation on the server and admission controller.
func (s *Server) SetJWT(issuer *JWTIssuer) {
	s.jwtIssuer = issuer
	s.admission.SetJWT(issuer)
}

// SetOAuth configures the OAuth device flow server.
func (s *Server) SetOAuth(oauth *OAuthServer) {
	s.oauth = oauth
}

// SetREST configures the REST API handler.
func (s *Server) SetREST(rest *RESTHandler) {
	s.rest = rest
}

// SetCORSOrigin configures the CORS allowed origin.
func (s *Server) SetCORSOrigin(origin string) {
	s.corsOrigin = origin
}

// HTTPHandler returns an http.Handler for the OAuth and REST routes, or nil if neither is configured.
func (s *Server) HTTPHandler() http.Handler {
	if s.oauth == nil && s.rest == nil {
		return nil
	}

	mux := http.NewServeMux()
	if s.oauth != nil {
		s.oauth.RegisterRoutes(mux)
	}
	if s.rest != nil {
		s.rest.RegisterRoutes(mux)
	}

	if s.corsOrigin != "" {
		return CORSMiddleware(s.corsOrigin, mux)
	}
	return mux
}

// Serve starts the gRPC server on the given listener.
func (s *Server) Serve(lis net.Listener) error {
	s.logger.Info("coordination server listening", "addr", lis.Addr())
	return s.grpc.Serve(lis)
}

// StartReaper starts a background goroutine that removes nodes whose last
// heartbeat is older than ttl. It sweeps every interval and broadcasts the
// peer map when stale nodes are removed. The goroutine exits when ctx is cancelled.
func (s *Server) StartReaper(ctx context.Context, ttl, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cutoff := time.Now().Add(-ttl)
				n, err := s.store.RemoveStaleNodes(cutoff)
				if err != nil {
					s.logger.Error("reaper: failed to remove stale nodes", "error", err)
					continue
				}
				if n > 0 {
					s.logger.Info("reaper: evicted stale nodes", "count", n)
					if err := s.peerMap.ForceBroadcast(); err != nil {
						s.logger.Error("reaper: failed to broadcast peer map", "error", err)
					}
				}
			}
		}
	}()
}

// GracefulStop stops the server gracefully.
func (s *Server) GracefulStop() {
	s.grpc.GracefulStop()
}

// synthesizeManifests creates HandleManifests from deprecated HandleDescriptions
// for backward compatibility with old clients.
func synthesizeManifests(descriptions map[string]string) map[string]*messagepb.ServiceManifest {
	if len(descriptions) == 0 {
		return nil
	}
	manifests := make(map[string]*messagepb.ServiceManifest, len(descriptions))
	for h, d := range descriptions {
		if d != "" {
			manifests[h] = &messagepb.ServiceManifest{Description: d}
		}
	}
	return manifests
}

// RegisterNode handles node registration.
func (s *Server) RegisterNode(_ context.Context, req *pb.RegisterNodeRequest) (*pb.RegisterNodeResponse, error) {
	// Admission control: check auth token before allowing registration
	result, err := s.admission.ValidateRegistration(req.AuthToken, req.NodeId)
	if err != nil {
		return &pb.RegisterNodeResponse{Ok: false, Error: err.Error()}, nil
	}

	// Resolve team: explicit request > JWT claim > personal mode
	teamID := req.TeamId
	if teamID == "" && result != nil {
		teamID = result.TeamID
	}

	// If team specified, verify user is a member
	if teamID != "" && result != nil && result.Email != "" {
		role, err := s.store.GetUserTeamRole(teamID, result.Email)
		if err != nil {
			return &pb.RegisterNodeResponse{Ok: false, Error: fmt.Sprintf("team lookup failed: %v", err)}, nil
		}
		if role == "" {
			return &pb.RegisterNodeResponse{Ok: false, Error: fmt.Sprintf("not a member of team %q", teamID)}, nil
		}
	}

	manifests := req.HandleManifests
	if len(manifests) == 0 {
		manifests = synthesizeManifests(req.HandleDescriptions)
	}

	if err := s.registry.RegisterNode(req.NodeId, req.PublicKey, req.AdvertiseAddr, req.Handles, manifests, req.IsRelay, teamID); err != nil {
		return &pb.RegisterNodeResponse{Ok: false, Error: err.Error()}, nil
	}

	// Set node team in DB
	if teamID != "" {
		if err := s.store.SetNodeTeam(req.NodeId, teamID); err != nil {
			s.logger.Error("failed to set node team", "error", err)
		}
	}

	// Broadcast updated peer map to all watchers
	if err := s.peerMap.Broadcast(); err != nil {
		s.logger.Error("failed to broadcast peer map", "error", err)
	}

	resp := &pb.RegisterNodeResponse{Ok: true, TeamId: teamID}
	if result != nil && result.Email != "" {
		resp.UserEmail = result.Email
	}

	// Resolve team name for response
	if teamID != "" {
		if _, name, _ := s.resolveTeamName(teamID); name != "" {
			resp.TeamName = name
		}
	}

	return resp, nil
}

// resolveTeamName looks up team name by ID. Returns teamID, name, error.
func (s *Server) resolveTeamName(teamID string) (string, string, error) {
	var name string
	err := s.store.db.QueryRow("SELECT name FROM teams WHERE team_id = ?", teamID).Scan(&name)
	if err != nil {
		return teamID, "", err
	}
	return teamID, name, nil
}

// WatchPeerMap streams peer map updates to a node.
func (s *Server) WatchPeerMap(req *pb.WatchPeerMapRequest, stream pb.CoordinationAPI_WatchPeerMapServer) error {
	// Look up node's team from DB (belt-and-suspenders: don't trust client claim)
	teamID := req.TeamId
	if teamID == "" {
		// Fall back to stored team for the node
		var storedTeam string
		_ = s.store.db.QueryRow("SELECT team_id FROM nodes WHERE node_id = ?", req.NodeId).Scan(&storedTeam)
		if storedTeam != "" {
			teamID = storedTeam
		}
	}

	// Send current peer map immediately (team-scoped)
	current, err := s.peerMap.BuildForTeam(teamID)
	if err != nil {
		return fmt.Errorf("build peer map: %w", err)
	}
	if err := stream.Send(current); err != nil {
		return err
	}

	// Watch for updates (team-scoped)
	ch := s.peerMap.AddWatcher(req.NodeId, teamID)
	defer s.peerMap.RemoveWatcher(req.NodeId)

	for {
		select {
		case update, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(update); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

// LookupHandle looks up which node serves a handle, scoped by team.
func (s *Server) LookupHandle(_ context.Context, req *pb.LookupHandleRequest) (*pb.LookupHandleResponse, error) {
	rec, err := s.store.LookupHandleInTeam(req.Handle, req.TeamId)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return &pb.LookupHandleResponse{Found: false}, nil
	}

	// Build deprecated descriptions from manifests
	descs := make(map[string]string, len(rec.HandleManifests))
	for h, m := range rec.HandleManifests {
		if m != nil && m.Description != "" {
			descs[h] = m.Description
		}
	}

	return &pb.LookupHandleResponse{
		Found: true,
		Peer: &pb.PeerInfo{
			NodeId:             rec.NodeID,
			PublicKey:          rec.PublicKey,
			AdvertiseAddr:      rec.AdvertiseAddr,
			Handles:            rec.Handles,
			LastHeartbeatUnix:  rec.LastHeartbeat.Unix(),
			HandleDescriptions: descs,
			HandleManifests:    rec.HandleManifests,
		},
	}, nil
}

// Heartbeat handles node heartbeats.
func (s *Server) Heartbeat(_ context.Context, req *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	manifests := req.HandleManifests
	if len(manifests) == 0 {
		manifests = synthesizeManifests(req.HandleDescriptions)
	}

	if err := s.registry.Heartbeat(req.NodeId, req.Handles, manifests); err != nil {
		return &pb.HeartbeatResponse{Ok: false}, nil
	}

	// Broadcast in case handles changed
	if err := s.peerMap.Broadcast(); err != nil {
		s.logger.Error("failed to broadcast peer map", "error", err)
	}

	return &pb.HeartbeatResponse{Ok: true}, nil
}

// validateJWT validates a JWT auth token and returns the claims.
func (s *Server) validateJWT(authToken string) (*Claims, error) {
	if s.jwtIssuer == nil {
		return nil, fmt.Errorf("JWT not configured on this server")
	}
	claims, err := s.jwtIssuer.Validate(authToken)
	if err != nil {
		return nil, fmt.Errorf("invalid auth token: %w", err)
	}
	if claims.TokenType != "access" {
		return nil, fmt.Errorf("expected access token, got %s", claims.TokenType)
	}
	return claims, nil
}

// CreateTeam creates a new team and makes the caller the owner.
func (s *Server) CreateTeam(_ context.Context, req *pb.CreateTeamRequest) (*pb.CreateTeamResponse, error) {
	claims, err := s.validateJWT(req.AuthToken)
	if err != nil {
		return &pb.CreateTeamResponse{Error: err.Error()}, nil
	}

	teamID := generateID(8)
	if err := s.store.CreateTeam(teamID, req.Name, claims.Email); err != nil {
		return &pb.CreateTeamResponse{Error: fmt.Sprintf("create team: %v", err)}, nil
	}

	s.logger.Info("team created", "team_id", teamID, "name", req.Name, "owner", claims.Email)
	return &pb.CreateTeamResponse{TeamId: teamID, Name: req.Name}, nil
}

// ListTeams returns teams the caller is a member of.
func (s *Server) ListTeams(_ context.Context, req *pb.ListTeamsRequest) (*pb.ListTeamsResponse, error) {
	claims, err := s.validateJWT(req.AuthToken)
	if err != nil {
		return &pb.ListTeamsResponse{Error: err.Error()}, nil
	}

	teams, err := s.store.ListUserTeams(claims.Email)
	if err != nil {
		return &pb.ListTeamsResponse{Error: fmt.Sprintf("list teams: %v", err)}, nil
	}

	var infos []*pb.TeamInfo
	for _, t := range teams {
		infos = append(infos, &pb.TeamInfo{
			TeamId: t.TeamID,
			Name:   t.Name,
			Role:   t.Role,
		})
	}
	return &pb.ListTeamsResponse{Teams: infos}, nil
}

// GetTeamMembers returns the members of a team.
func (s *Server) GetTeamMembers(_ context.Context, req *pb.GetTeamMembersRequest) (*pb.GetTeamMembersResponse, error) {
	claims, err := s.validateJWT(req.AuthToken)
	if err != nil {
		return &pb.GetTeamMembersResponse{Error: err.Error()}, nil
	}

	// Verify caller is a member
	role, err := s.store.GetUserTeamRole(req.TeamId, claims.Email)
	if err != nil {
		return &pb.GetTeamMembersResponse{Error: fmt.Sprintf("lookup role: %v", err)}, nil
	}
	if role == "" {
		return &pb.GetTeamMembersResponse{Error: "not a member of this team"}, nil
	}

	members, err := s.store.GetTeamMembers(req.TeamId)
	if err != nil {
		return &pb.GetTeamMembersResponse{Error: fmt.Sprintf("get members: %v", err)}, nil
	}

	var result []*pb.TeamMember
	for _, m := range members {
		result = append(result, &pb.TeamMember{
			Email: m.Email,
			Role:  m.Role,
		})
	}
	return &pb.GetTeamMembersResponse{Members: result}, nil
}

// CreateTeamInvite generates an invite code for a team.
func (s *Server) CreateTeamInvite(_ context.Context, req *pb.CreateTeamInviteRequest) (*pb.CreateTeamInviteResponse, error) {
	claims, err := s.validateJWT(req.AuthToken)
	if err != nil {
		return &pb.CreateTeamInviteResponse{Error: err.Error()}, nil
	}

	// Verify caller is an owner
	role, err := s.store.GetUserTeamRole(req.TeamId, claims.Email)
	if err != nil {
		return &pb.CreateTeamInviteResponse{Error: fmt.Sprintf("lookup role: %v", err)}, nil
	}
	if role != "owner" {
		return &pb.CreateTeamInviteResponse{Error: "only team owners can create invites"}, nil
	}

	maxUses := int(req.MaxUses)
	if maxUses <= 0 {
		maxUses = 1
	}
	ttl := time.Duration(req.TtlSeconds) * time.Second
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour // default 7 days
	}
	expiresAt := time.Now().Add(ttl)

	code := generateID(4) + "-" + generateID(4)
	if err := s.store.CreateTeamInvite(code, req.TeamId, claims.Email, expiresAt, maxUses); err != nil {
		return &pb.CreateTeamInviteResponse{Error: fmt.Sprintf("create invite: %v", err)}, nil
	}

	s.logger.Info("team invite created", "team_id", req.TeamId, "code", code, "max_uses", maxUses)
	return &pb.CreateTeamInviteResponse{Code: code, ExpiresAt: expiresAt.Unix()}, nil
}

// AcceptTeamInvite consumes an invite code and adds the caller as a team member.
func (s *Server) AcceptTeamInvite(_ context.Context, req *pb.AcceptTeamInviteRequest) (*pb.AcceptTeamInviteResponse, error) {
	claims, err := s.validateJWT(req.AuthToken)
	if err != nil {
		return &pb.AcceptTeamInviteResponse{Error: err.Error()}, nil
	}

	teamID, err := s.store.ConsumeTeamInvite(req.Code)
	if err != nil {
		return &pb.AcceptTeamInviteResponse{Error: err.Error()}, nil
	}

	if err := s.store.AddTeamMember(teamID, claims.Email, "member"); err != nil {
		return &pb.AcceptTeamInviteResponse{Error: fmt.Sprintf("add member: %v", err)}, nil
	}

	_, teamName, _ := s.resolveTeamName(teamID)
	s.logger.Info("team invite accepted", "team_id", teamID, "email", claims.Email)
	return &pb.AcceptTeamInviteResponse{TeamId: teamID, TeamName: teamName}, nil
}

// RemoveTeamMember removes a user from a team. Only owners can remove members.
// Owners cannot remove themselves (to prevent orphaned teams).
func (s *Server) RemoveTeamMember(_ context.Context, req *pb.RemoveTeamMemberRequest) (*pb.RemoveTeamMemberResponse, error) {
	claims, err := s.validateJWT(req.AuthToken)
	if err != nil {
		return &pb.RemoveTeamMemberResponse{Error: err.Error()}, nil
	}

	// Verify caller is an owner
	role, err := s.store.GetUserTeamRole(req.TeamId, claims.Email)
	if err != nil {
		return &pb.RemoveTeamMemberResponse{Error: fmt.Sprintf("lookup role: %v", err)}, nil
	}
	if role != "owner" {
		return &pb.RemoveTeamMemberResponse{Error: "only team owners can remove members"}, nil
	}

	// Owners cannot remove themselves
	if req.Email == claims.Email {
		return &pb.RemoveTeamMemberResponse{Error: "cannot remove yourself; transfer ownership first or delete the team"}, nil
	}

	if err := s.store.RemoveTeamMember(req.TeamId, req.Email); err != nil {
		return &pb.RemoveTeamMemberResponse{Error: err.Error()}, nil
	}

	s.logger.Info("team member removed", "team_id", req.TeamId, "email", req.Email, "by", claims.Email)
	return &pb.RemoveTeamMemberResponse{}, nil
}

// UpdateTeamMemberRole changes a member's role. Only owners can change roles.
// At least one owner must remain.
func (s *Server) UpdateTeamMemberRole(_ context.Context, req *pb.UpdateTeamMemberRoleRequest) (*pb.UpdateTeamMemberRoleResponse, error) {
	claims, err := s.validateJWT(req.AuthToken)
	if err != nil {
		return &pb.UpdateTeamMemberRoleResponse{Error: err.Error()}, nil
	}

	if req.Role != "owner" && req.Role != "member" {
		return &pb.UpdateTeamMemberRoleResponse{Error: "role must be 'owner' or 'member'"}, nil
	}

	// Verify caller is an owner
	callerRole, err := s.store.GetUserTeamRole(req.TeamId, claims.Email)
	if err != nil {
		return &pb.UpdateTeamMemberRoleResponse{Error: fmt.Sprintf("lookup role: %v", err)}, nil
	}
	if callerRole != "owner" {
		return &pb.UpdateTeamMemberRoleResponse{Error: "only team owners can change roles"}, nil
	}

	// Prevent demoting the last owner
	if req.Email == claims.Email && req.Role != "owner" {
		members, err := s.store.GetTeamMembers(req.TeamId)
		if err != nil {
			return &pb.UpdateTeamMemberRoleResponse{Error: fmt.Sprintf("get members: %v", err)}, nil
		}
		ownerCount := 0
		for _, m := range members {
			if m.Role == "owner" {
				ownerCount++
			}
		}
		if ownerCount <= 1 {
			return &pb.UpdateTeamMemberRoleResponse{Error: "cannot demote the last owner; promote another member first"}, nil
		}
	}

	if err := s.store.UpdateTeamMemberRole(req.TeamId, req.Email, req.Role); err != nil {
		return &pb.UpdateTeamMemberRoleResponse{Error: err.Error()}, nil
	}

	s.logger.Info("team member role updated", "team_id", req.TeamId, "email", req.Email, "role", req.Role, "by", claims.Email)
	return &pb.UpdateTeamMemberRoleResponse{}, nil
}

// DeleteTeam deletes a team and all associated data. Only owners can delete.
func (s *Server) DeleteTeam(_ context.Context, req *pb.DeleteTeamRequest) (*pb.DeleteTeamResponse, error) {
	claims, err := s.validateJWT(req.AuthToken)
	if err != nil {
		return &pb.DeleteTeamResponse{Error: err.Error()}, nil
	}

	role, err := s.store.GetUserTeamRole(req.TeamId, claims.Email)
	if err != nil {
		return &pb.DeleteTeamResponse{Error: fmt.Sprintf("lookup role: %v", err)}, nil
	}
	if role != "owner" {
		return &pb.DeleteTeamResponse{Error: "only team owners can delete teams"}, nil
	}

	if err := s.store.DeleteTeam(req.TeamId); err != nil {
		return &pb.DeleteTeamResponse{Error: err.Error()}, nil
	}

	// Broadcast updated peer maps since team nodes are now unscoped
	if err := s.peerMap.ForceBroadcast(); err != nil {
		s.logger.Error("failed to broadcast after team deletion", "error", err)
	}

	s.logger.Info("team deleted", "team_id", req.TeamId, "by", claims.Email)
	return &pb.DeleteTeamResponse{}, nil
}

// generateID generates a random hex string of the given byte length.
func generateID(byteLen int) string {
	b := make([]byte, byteLen)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}
