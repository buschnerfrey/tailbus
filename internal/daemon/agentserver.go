package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	"github.com/alexanderfrey/tailbus/internal/handle"
	"github.com/alexanderfrey/tailbus/internal/session"
	"github.com/alexanderfrey/tailbus/internal/transport"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// connIDKey is the context key for connection IDs.
type connIDKey struct{}

// connTracker implements grpc stats.Handler to track connection lifecycles.
type connTracker struct {
	nextID atomic.Uint64
	server *AgentServer
}

func (ct *connTracker) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	id := ct.nextID.Add(1)
	return context.WithValue(ctx, connIDKey{}, id)
}

func (ct *connTracker) HandleConn(ctx context.Context, s stats.ConnStats) {
	if _, ok := s.(*stats.ConnEnd); ok {
		if id, ok := ctx.Value(connIDKey{}).(uint64); ok {
			ct.server.disconnectConn(id)
		}
	}
}

func (ct *connTracker) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context { return ctx }
func (ct *connTracker) HandleRPC(context.Context, stats.RPCStats)                       {}

// handleStats tracks per-handle message counters.
type handleStats struct {
	msgsIn  atomic.Int64
	msgsOut atomic.Int64
	drops   atomic.Int64
}

// Router is the interface the agent server uses to send outbound messages.
type Router interface {
	Route(ctx context.Context, env *messagepb.Envelope) error
}

type RoomService interface {
	CreateRoom(ctx context.Context, creator, title string, initialMembers []string) (*messagepb.RoomInfo, error)
	JoinRoom(ctx context.Context, roomID, handle string) (*messagepb.RoomInfo, error)
	LeaveRoom(ctx context.Context, roomID, handle string) (*messagepb.RoomInfo, error)
	PostMessage(ctx context.Context, roomID, from string, payload []byte, contentType, traceID string) (*messagepb.RoomEvent, error)
	ListRooms(ctx context.Context, handle string) ([]*messagepb.RoomInfo, error)
	ListMembers(ctx context.Context, roomID, handle string) ([]string, error)
	Replay(ctx context.Context, roomID, handle string, sinceSeq uint64) ([]*messagepb.RoomEvent, error)
	CloseRoom(ctx context.Context, roomID, handle string) (*messagepb.RoomInfo, error)
	DashboardRooms(handles []string) ([]*messagepb.RoomInfo, error)
}

type UsageHistoryProvider interface {
	LoadUsageHistory() (*agentpb.UsageHistory, error)
}

// AgentServer is the local gRPC server that agent programs connect to via Unix socket.
type AgentServer struct {
	agentpb.UnimplementedAgentAPIServer

	logger     *slog.Logger
	sessions   *session.Store
	router     Router
	grpc       *grpc.Server
	activity   *ActivityBus
	traceStore *TraceStore
	metrics    *Metrics

	// Auth token for Unix socket authentication (empty = no auth / test mode)
	authToken string
	tokenPath string // path to token file (for cleanup)

	mu             sync.RWMutex
	handles        map[string]bool                                                         // registered handles on this node
	manifests      map[string]*messagepb.ServiceManifest                                   // handle -> manifest
	subscribers    map[string][]chan *agentpb.IncomingMessage                              // handle -> subscriber channels
	onHandleChange func(handles []string, manifests map[string]*messagepb.ServiceManifest) // called when handles change

	// Per-handle health stats
	hstats map[string]*handleStats

	// Connection-based handle ownership
	connHandles map[uint64]map[string]bool // connID -> set of handles
	handleOwner map[string]uint64          // handle -> owning connID

	// ACK tracking
	ackTracker *AckTracker

	roomManager RoomService

	// Shutdown callback (set via SetShutdownFunc)
	shutdownFn func()

	// Dashboard dependencies (set via SetDashboardDeps)
	dashResolver  *handle.Resolver
	dashTransport *transport.GRPCTransport
	dashUsage     UsageHistoryProvider
	nodeID        string
	startedAt     time.Time
}

// NewAgentServer creates a new agent server.
func NewAgentServer(sessions *session.Store, router Router, activity *ActivityBus, logger *slog.Logger) *AgentServer {
	s := &AgentServer{
		logger:      logger,
		sessions:    sessions,
		router:      router,
		activity:    activity,
		handles:     make(map[string]bool),
		manifests:   make(map[string]*messagepb.ServiceManifest),
		subscribers: make(map[string][]chan *agentpb.IncomingMessage),
		hstats:      make(map[string]*handleStats),
		connHandles: make(map[uint64]map[string]bool),
		handleOwner: make(map[string]uint64),
		startedAt:   time.Now(),
	}

	tracker := &connTracker{server: s}
	gs := grpc.NewServer(
		grpc.StatsHandler(tracker),
		grpc.ChainUnaryInterceptor(s.authUnaryInterceptor),
		grpc.ChainStreamInterceptor(s.authStreamInterceptor),
	)
	agentpb.RegisterAgentAPIServer(gs, s)
	s.grpc = gs
	return s
}

// SetAuthToken sets the auth token for testing (bypasses file-based token generation).
func (s *AgentServer) SetAuthToken(token string) {
	s.authToken = token
}

// checkAuth verifies the authorization token from gRPC metadata.
// No-op if authToken is empty (test mode / no auth configured).
func (s *AgentServer) checkAuth(ctx context.Context) error {
	if s.authToken == "" {
		return nil
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing metadata")
	}
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return status.Error(codes.Unauthenticated, "missing authorization token")
	}
	token := strings.TrimPrefix(vals[0], "Bearer ")
	if token != s.authToken {
		return status.Error(codes.Unauthenticated, "invalid authorization token")
	}
	return nil
}

func (s *AgentServer) authUnaryInterceptor(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if err := s.checkAuth(ctx); err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

func (s *AgentServer) authStreamInterceptor(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if err := s.checkAuth(ss.Context()); err != nil {
		return err
	}
	return handler(srv, ss)
}

// SetShutdownFunc sets the function called when a Shutdown RPC is received.
func (s *AgentServer) SetShutdownFunc(fn func()) {
	s.shutdownFn = fn
}

// Shutdown gracefully shuts down the daemon.
func (s *AgentServer) Shutdown(_ context.Context, _ *agentpb.ShutdownRequest) (*agentpb.ShutdownResponse, error) {
	s.logger.Info("shutdown requested via RPC")
	if s.shutdownFn != nil {
		go s.shutdownFn()
	}
	return &agentpb.ShutdownResponse{}, nil
}

// SetDashboardDeps sets dependencies needed for dashboard RPCs.
func (s *AgentServer) SetDashboardDeps(nodeID string, resolver *handle.Resolver, tp *transport.GRPCTransport) {
	s.nodeID = nodeID
	s.dashResolver = resolver
	s.dashTransport = tp
}

func (s *AgentServer) SetUsageHistoryProvider(provider UsageHistoryProvider) {
	s.dashUsage = provider
}

// SetTracing sets the trace store and metrics for tracing support.
func (s *AgentServer) SetTracing(ts *TraceStore, m *Metrics) {
	s.traceStore = ts
	s.metrics = m
}

// SetAckTracker sets the ACK tracker for delivery acknowledgement.
func (s *AgentServer) SetAckTracker(at *AckTracker) {
	s.ackTracker = at
}

// SetRoomManager sets the room manager used by room RPCs.
func (s *AgentServer) SetRoomManager(rm RoomService) {
	s.roomManager = rm
}

// ServeUnix starts the gRPC server on a Unix socket.
// It generates a random auth token and writes it to <socketPath>.token (mode 0600).
func (s *AgentServer) ServeUnix(socketPath string) error {
	os.Remove(socketPath)

	// Generate auth token
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return fmt.Errorf("generate auth token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)
	tokenPath := socketPath + ".token"
	if err := os.WriteFile(tokenPath, []byte(token), 0600); err != nil {
		return fmt.Errorf("write auth token: %w", err)
	}
	s.authToken = token
	s.tokenPath = tokenPath
	s.logger.Info("auth token written", "path", tokenPath)

	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen unix: %w", err)
	}
	s.logger.Info("agent server listening", "socket", socketPath)
	return s.grpc.Serve(lis)
}

// ServeTCP starts the gRPC server on a TCP address (for testing).
func (s *AgentServer) ServeTCP(lis net.Listener) error {
	return s.grpc.Serve(lis)
}

// SetRouter sets the router (used for breaking circular dependency during setup).
func (s *AgentServer) SetRouter(r Router) {
	s.router = r
}

// SetOnHandleChange sets a callback invoked when local handles change.
func (s *AgentServer) SetOnHandleChange(fn func(handles []string, manifests map[string]*messagepb.ServiceManifest)) {
	s.onHandleChange = fn
}

// connIDFromCtx extracts the connection ID from context.
func connIDFromCtx(ctx context.Context) (uint64, bool) {
	id, ok := ctx.Value(connIDKey{}).(uint64)
	return id, ok
}

// verifyOwnership checks that the connection in ctx owns the given handle.
func (s *AgentServer) verifyOwnership(ctx context.Context, handle string) error {
	connID, ok := connIDFromCtx(ctx)
	if !ok {
		return nil // no conn tracking (e.g. tests without stats handler)
	}
	s.mu.RLock()
	owner, exists := s.handleOwner[handle]
	s.mu.RUnlock()
	if !exists {
		return status.Errorf(codes.NotFound, "handle %q is not registered", handle)
	}
	if owner != connID {
		return status.Errorf(codes.PermissionDenied, "handle %q is owned by another connection", handle)
	}
	return nil
}

// disconnectConn removes all handles and closes all subscriber channels for a disconnected connection.
func (s *AgentServer) disconnectConn(id uint64) {
	s.mu.Lock()
	handles := s.connHandles[id]
	delete(s.connHandles, id)

	var removedHandles []string
	for h := range handles {
		delete(s.handles, h)
		delete(s.manifests, h)
		delete(s.hstats, h)
		delete(s.handleOwner, h)
		removedHandles = append(removedHandles, h)

		// Close and remove subscriber channels for this handle
		for _, ch := range s.subscribers[h] {
			close(ch)
		}
		delete(s.subscribers, h)
	}

	// Snapshot remaining handles/manifests for callback
	var currentHandles []string
	currentManifests := make(map[string]*messagepb.ServiceManifest)
	for h := range s.handles {
		currentHandles = append(currentHandles, h)
	}
	for h, m := range s.manifests {
		currentManifests[h] = m
	}
	cb := s.onHandleChange
	s.mu.Unlock()

	if len(removedHandles) > 0 {
		s.logger.Info("connection disconnected, removed handles", "conn_id", id, "handles", removedHandles)
		if cb != nil {
			go cb(currentHandles, currentManifests)
		}
	}
}

// GracefulStop stops the server gracefully and removes the auth token file.
func (s *AgentServer) GracefulStop() {
	s.grpc.GracefulStop()
	if s.tokenPath != "" {
		os.Remove(s.tokenPath)
	}
}

// ForceStop immediately stops the gRPC server without waiting for streams.
func (s *AgentServer) ForceStop() {
	s.grpc.Stop()
	if s.tokenPath != "" {
		os.Remove(s.tokenPath)
	}
}

// GetHandles returns the list of currently registered handles.
func (s *AgentServer) GetHandles() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []string
	for h := range s.handles {
		result = append(result, h)
	}
	return result
}

// DeliverToLocal delivers an envelope to local subscribers. Returns true if delivered.
func (s *AgentServer) DeliverToLocal(env *messagepb.Envelope) bool {
	// Handle incoming ACK: acknowledge and never forward to subscribers
	if env.Type == messagepb.EnvelopeType_ENVELOPE_TYPE_ACK {
		if s.ackTracker != nil && env.AckId != "" {
			s.ackTracker.Acknowledge(env.AckId)
		}
		return true
	}

	// Create a local session for incoming session_open from a remote node
	// so the local agent can resolve/send on it.
	if env.Type == messagepb.EnvelopeType_ENVELOPE_TYPE_SESSION_OPEN && env.SessionId != "" {
		if _, exists := s.sessions.Get(env.SessionId); !exists {
			sess := &session.Session{
				ID:         env.SessionId,
				FromHandle: env.FromHandle,
				ToHandle:   env.ToHandle,
				State:      session.StateOpen,
				TraceID:    env.TraceId,
				NextSeq:    1,
				CreatedAt:  time.Now(),
				UpdatedAt:  time.Now(),
			}
			s.sessions.Put(sess)
			s.logger.Info("created local session for remote session_open",
				"session_id", sess.ID, "from", sess.FromHandle, "to", sess.ToHandle)
		} else {
			s.logger.Debug("session already exists for remote session_open",
				"session_id", env.SessionId)
		}
	}

	s.mu.RLock()
	subs := s.subscribers[env.ToHandle]
	hs := s.hstats[env.ToHandle]
	s.mu.RUnlock()

	if len(subs) == 0 {
		return false
	}

	msg := &agentpb.IncomingMessage{Envelope: env}
	for _, ch := range subs {
		select {
		case ch <- msg:
			if hs != nil {
				hs.msgsIn.Add(1)
			}
		default:
			s.logger.Warn("subscriber channel full, dropping message", "handle", env.ToHandle)
			if hs != nil {
				hs.drops.Add(1)
			}
		}
	}

	if s.traceStore != nil && env.TraceId != "" {
		s.traceStore.RecordSpan(env.TraceId, env.MessageId, s.nodeID, agentpb.TraceAction_TRACE_ACTION_DELIVERED_TO_SUBSCRIBER, map[string]string{
			"to": env.ToHandle,
		})
	}

	// Send ACK back to sender (best-effort, async)
	if s.router != nil && env.FromHandle != "" {
		ack := &messagepb.Envelope{
			MessageId:  uuid.New().String(),
			SessionId:  env.SessionId,
			FromHandle: env.ToHandle,
			ToHandle:   env.FromHandle,
			Type:       messagepb.EnvelopeType_ENVELOPE_TYPE_ACK,
			AckId:      env.MessageId,
			SentAtUnix: time.Now().Unix(),
		}
		go func() {
			if err := s.router.Route(context.Background(), ack); err != nil {
				s.logger.Debug("ACK route failed", "ack_for", env.MessageId, "error", err)
			}
		}()
	}

	return true
}

// DeliverRoomEventLocal delivers a room event to all local room members except the sender.
func (s *AgentServer) DeliverRoomEventLocal(event *messagepb.RoomEvent) bool {
	delivered := false
	for _, member := range event.Members {
		if member == event.SenderHandle || !s.HasHandle(member) {
			continue
		}
		s.mu.RLock()
		subs := s.subscribers[member]
		hs := s.hstats[member]
		s.mu.RUnlock()
		if len(subs) == 0 {
			continue
		}
		msg := &agentpb.IncomingMessage{RoomEvent: event}
		for _, ch := range subs {
			select {
			case ch <- msg:
				delivered = true
				if hs != nil {
					hs.msgsIn.Add(1)
				}
			default:
				s.logger.Warn("subscriber channel full, dropping room event", "handle", member, "room_id", event.RoomId)
				if hs != nil {
					hs.drops.Add(1)
				}
			}
		}
	}
	return delivered
}

// HasHandle returns true if the given handle is registered locally.
func (s *AgentServer) HasHandle(h string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.handles[h]
}

func validateSessionParticipant(sess *session.Session, fromHandle string) error {
	if fromHandle != sess.FromHandle && fromHandle != sess.ToHandle {
		return status.Errorf(codes.PermissionDenied,
			"handle %q is not a participant in session %q", fromHandle, sess.ID)
	}
	return nil
}

func validateSessionOpen(sess *session.Session) error {
	if sess.State != session.StateOpen {
		return status.Errorf(codes.FailedPrecondition,
			"session %q is %s", sess.ID, sess.State)
	}
	return nil
}

// Register registers an agent handle on this node.
func (s *AgentServer) Register(ctx context.Context, req *agentpb.RegisterRequest) (*agentpb.RegisterResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.handles[req.Handle] {
		return &agentpb.RegisterResponse{Ok: false, Error: "handle already registered"}, nil
	}

	s.handles[req.Handle] = true
	s.hstats[req.Handle] = &handleStats{}

	// Bind handle to connection
	if connID, ok := connIDFromCtx(ctx); ok {
		if s.connHandles[connID] == nil {
			s.connHandles[connID] = make(map[string]bool)
		}
		s.connHandles[connID][req.Handle] = true
		s.handleOwner[req.Handle] = connID
	}

	// Build manifest from request: prefer manifest field, fall back to deprecated description
	if req.Manifest != nil {
		s.manifests[req.Handle] = req.Manifest
	} else if req.Description != "" {
		s.manifests[req.Handle] = &messagepb.ServiceManifest{Description: req.Description}
	}

	s.logger.Info("agent registered", "handle", req.Handle)

	if s.activity != nil {
		s.activity.EmitHandleRegistered(req.Handle)
	}

	// Notify coord about handle change (must copy handles+manifests while holding lock)
	if s.onHandleChange != nil {
		handles := make([]string, 0, len(s.handles))
		for h := range s.handles {
			handles = append(handles, h)
		}
		mans := make(map[string]*messagepb.ServiceManifest, len(s.manifests))
		for h, m := range s.manifests {
			mans[h] = m
		}
		go s.onHandleChange(handles, mans)
	}

	return &agentpb.RegisterResponse{Ok: true}, nil
}

// OpenSession opens a new session and sends the opening message.
func (s *AgentServer) OpenSession(ctx context.Context, req *agentpb.OpenSessionRequest) (*agentpb.OpenSessionResponse, error) {
	if err := s.verifyOwnership(ctx, req.FromHandle); err != nil {
		return nil, err
	}

	sess := session.New(req.FromHandle, req.ToHandle)

	// Generate or use agent-provided trace ID
	traceID := req.TraceId
	if traceID == "" {
		traceID = uuid.New().String()
	}
	sess.TraceID = traceID
	seq := sess.NextSequence()
	s.sessions.Put(sess)

	msgID := uuid.New().String()
	env := &messagepb.Envelope{
		MessageId:   msgID,
		SessionId:   sess.ID,
		FromHandle:  req.FromHandle,
		ToHandle:    req.ToHandle,
		Payload:     req.Payload,
		ContentType: req.ContentType,
		SentAtUnix:  sess.CreatedAt.Unix(),
		Type:        messagepb.EnvelopeType_ENVELOPE_TYPE_SESSION_OPEN,
		TraceId:     traceID,
		Sequence:    seq,
	}

	if s.traceStore != nil {
		s.traceStore.RecordSpan(traceID, msgID, s.nodeID, agentpb.TraceAction_TRACE_ACTION_MESSAGE_CREATED, map[string]string{
			"from": req.FromHandle,
			"to":   req.ToHandle,
			"type": "session_open",
		})
	}

	if err := s.router.Route(ctx, env); err != nil {
		return nil, fmt.Errorf("route session open: %w", err)
	}

	s.mu.RLock()
	if hs := s.hstats[req.FromHandle]; hs != nil {
		hs.msgsOut.Add(1)
	}
	s.mu.RUnlock()

	if s.activity != nil {
		s.activity.EmitSessionOpened(sess.ID, req.FromHandle, req.ToHandle)
	}

	// Check for @-mentions in the opening payload
	s.maybeRouteMentions(ctx, req.FromHandle, req.ToHandle, req.ContentType, req.Payload)

	s.logger.Info("session opened", "session_id", sess.ID, "from", req.FromHandle, "to", req.ToHandle, "trace_id", traceID)
	return &agentpb.OpenSessionResponse{SessionId: sess.ID, MessageId: msgID, TraceId: traceID}, nil
}

// SendMessage sends a message within an existing session.
func (s *AgentServer) SendMessage(ctx context.Context, req *agentpb.SendMessageRequest) (*agentpb.SendMessageResponse, error) {
	if err := s.verifyOwnership(ctx, req.FromHandle); err != nil {
		return nil, err
	}

	sess, ok := s.sessions.Get(req.SessionId)
	if !ok {
		s.logger.Error("session not found for send_message",
			"session_id", req.SessionId,
			"from_handle", req.FromHandle,
			"store_size", len(s.sessions.ListAll()))
		return nil, fmt.Errorf("session %q not found", req.SessionId)
	}
	if err := validateSessionParticipant(sess, req.FromHandle); err != nil {
		return nil, err
	}
	if err := validateSessionOpen(sess); err != nil {
		return nil, err
	}

	// Determine recipient
	toHandle := sess.ToHandle
	if req.FromHandle == sess.ToHandle {
		toHandle = sess.FromHandle
	}

	seq := sess.NextSequence()
	s.sessions.Put(sess)

	msgID := uuid.New().String()
	env := &messagepb.Envelope{
		MessageId:   msgID,
		SessionId:   req.SessionId,
		FromHandle:  req.FromHandle,
		ToHandle:    toHandle,
		Payload:     req.Payload,
		ContentType: req.ContentType,
		SentAtUnix:  sess.UpdatedAt.Unix(),
		Type:        messagepb.EnvelopeType_ENVELOPE_TYPE_MESSAGE,
		TraceId:     sess.TraceID,
		Sequence:    seq,
	}

	if s.traceStore != nil && sess.TraceID != "" {
		s.traceStore.RecordSpan(sess.TraceID, msgID, s.nodeID, agentpb.TraceAction_TRACE_ACTION_MESSAGE_CREATED, map[string]string{
			"from": req.FromHandle,
			"to":   toHandle,
			"type": "message",
		})
	}

	if err := s.router.Route(ctx, env); err != nil {
		return nil, fmt.Errorf("route message: %w", err)
	}

	s.mu.RLock()
	if hs := s.hstats[req.FromHandle]; hs != nil {
		hs.msgsOut.Add(1)
	}
	s.mu.RUnlock()

	// Check for @-mentions
	s.maybeRouteMentions(ctx, req.FromHandle, toHandle, req.ContentType, req.Payload)

	return &agentpb.SendMessageResponse{MessageId: msgID}, nil
}

// Subscribe opens a stream of incoming messages for a handle.
func (s *AgentServer) Subscribe(req *agentpb.SubscribeRequest, stream agentpb.AgentAPI_SubscribeServer) error {
	if err := s.verifyOwnership(stream.Context(), req.Handle); err != nil {
		return err
	}

	ch := make(chan *agentpb.IncomingMessage, 64)

	s.mu.Lock()
	s.subscribers[req.Handle] = append(s.subscribers[req.Handle], ch)
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		subs := s.subscribers[req.Handle]
		for i, sub := range subs {
			if sub == ch {
				s.subscribers[req.Handle] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
	}()

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return nil // channel closed (disconnect)
			}
			if err := stream.Send(msg); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

// ResolveSession resolves (closes) a session with a final message.
func (s *AgentServer) ResolveSession(ctx context.Context, req *agentpb.ResolveSessionRequest) (*agentpb.ResolveSessionResponse, error) {
	if err := s.verifyOwnership(ctx, req.FromHandle); err != nil {
		return nil, err
	}

	sess, ok := s.sessions.Get(req.SessionId)
	if !ok {
		s.logger.Error("session not found for resolve",
			"session_id", req.SessionId,
			"from_handle", req.FromHandle,
			"store_size", len(s.sessions.ListAll()))
		return nil, fmt.Errorf("session %q not found", req.SessionId)
	}
	if err := validateSessionParticipant(sess, req.FromHandle); err != nil {
		return nil, err
	}
	if err := validateSessionOpen(sess); err != nil {
		return nil, err
	}

	toHandle := sess.ToHandle
	if req.FromHandle == sess.ToHandle {
		toHandle = sess.FromHandle
	}

	seq := sess.NextSequence()

	msgID := uuid.New().String()
	env := &messagepb.Envelope{
		MessageId:   msgID,
		SessionId:   req.SessionId,
		FromHandle:  req.FromHandle,
		ToHandle:    toHandle,
		Payload:     req.Payload,
		ContentType: req.ContentType,
		SentAtUnix:  sess.UpdatedAt.Unix(),
		Type:        messagepb.EnvelopeType_ENVELOPE_TYPE_SESSION_RESOLVE,
		TraceId:     sess.TraceID,
		Sequence:    seq,
	}

	if s.traceStore != nil && sess.TraceID != "" {
		s.traceStore.RecordSpan(sess.TraceID, msgID, s.nodeID, agentpb.TraceAction_TRACE_ACTION_MESSAGE_CREATED, map[string]string{
			"from": req.FromHandle,
			"to":   toHandle,
			"type": "session_resolve",
		})
	}

	if err := s.router.Route(ctx, env); err != nil {
		return nil, fmt.Errorf("route resolve: %w", err)
	}

	s.mu.RLock()
	if hs := s.hstats[req.FromHandle]; hs != nil {
		hs.msgsOut.Add(1)
	}
	s.mu.RUnlock()

	// Check for @-mentions in the resolve payload
	s.maybeRouteMentions(ctx, req.FromHandle, toHandle, req.ContentType, req.Payload)

	createdAt := sess.CreatedAt
	if err := sess.Resolve(); err != nil {
		return nil, err
	}
	s.sessions.Put(sess) // persist resolved clone back to store

	// Observe session lifetime
	if s.metrics != nil {
		lifetime := sess.UpdatedAt.Sub(createdAt).Seconds()
		(*s.metrics.SessionLifetime).(prometheus.Histogram).Observe(lifetime)
	}

	if s.activity != nil {
		s.activity.EmitSessionResolved(req.SessionId, req.FromHandle)
	}

	s.logger.Info("session resolved", "session_id", req.SessionId)
	return &agentpb.ResolveSessionResponse{MessageId: msgID}, nil
}

func (s *AgentServer) CreateRoom(ctx context.Context, req *agentpb.CreateRoomRequest) (*agentpb.CreateRoomResponse, error) {
	if s.roomManager == nil {
		return nil, status.Error(codes.Unimplemented, "rooms are not configured")
	}
	if err := s.verifyOwnership(ctx, req.CreatorHandle); err != nil {
		return nil, err
	}
	room, err := s.roomManager.CreateRoom(ctx, req.CreatorHandle, req.Title, req.InitialMembers)
	if err != nil {
		return nil, err
	}
	return &agentpb.CreateRoomResponse{RoomId: room.RoomId}, nil
}

func (s *AgentServer) JoinRoom(ctx context.Context, req *agentpb.JoinRoomRequest) (*agentpb.JoinRoomResponse, error) {
	if s.roomManager == nil {
		return nil, status.Error(codes.Unimplemented, "rooms are not configured")
	}
	if err := s.verifyOwnership(ctx, req.Handle); err != nil {
		return nil, err
	}
	if _, err := s.roomManager.JoinRoom(ctx, req.RoomId, req.Handle); err != nil {
		return nil, err
	}
	return &agentpb.JoinRoomResponse{Ok: true}, nil
}

func (s *AgentServer) LeaveRoom(ctx context.Context, req *agentpb.LeaveRoomRequest) (*agentpb.LeaveRoomResponse, error) {
	if s.roomManager == nil {
		return nil, status.Error(codes.Unimplemented, "rooms are not configured")
	}
	if err := s.verifyOwnership(ctx, req.Handle); err != nil {
		return nil, err
	}
	if _, err := s.roomManager.LeaveRoom(ctx, req.RoomId, req.Handle); err != nil {
		return nil, err
	}
	return &agentpb.LeaveRoomResponse{Ok: true}, nil
}

func (s *AgentServer) PostRoomMessage(ctx context.Context, req *agentpb.PostRoomMessageRequest) (*agentpb.PostRoomMessageResponse, error) {
	if s.roomManager == nil {
		return nil, status.Error(codes.Unimplemented, "rooms are not configured")
	}
	if err := s.verifyOwnership(ctx, req.FromHandle); err != nil {
		return nil, err
	}
	event, err := s.roomManager.PostMessage(ctx, req.RoomId, req.FromHandle, req.Payload, req.ContentType, req.TraceId)
	if err != nil {
		return nil, err
	}
	return &agentpb.PostRoomMessageResponse{EventId: event.EventId, RoomSeq: event.RoomSeq}, nil
}

func (s *AgentServer) ListRooms(ctx context.Context, req *agentpb.ListRoomsRequest) (*agentpb.ListRoomsResponse, error) {
	if s.roomManager == nil {
		return nil, status.Error(codes.Unimplemented, "rooms are not configured")
	}
	if err := s.verifyOwnership(ctx, req.Handle); err != nil {
		return nil, err
	}
	rooms, err := s.roomManager.ListRooms(ctx, req.Handle)
	if err != nil {
		return nil, err
	}
	return &agentpb.ListRoomsResponse{Rooms: rooms}, nil
}

func (s *AgentServer) ListRoomMembers(ctx context.Context, req *agentpb.ListRoomMembersRequest) (*agentpb.ListRoomMembersResponse, error) {
	if s.roomManager == nil {
		return nil, status.Error(codes.Unimplemented, "rooms are not configured")
	}
	if err := s.verifyOwnership(ctx, req.Handle); err != nil {
		return nil, err
	}
	members, err := s.roomManager.ListMembers(ctx, req.RoomId, req.Handle)
	if err != nil {
		return nil, err
	}
	return &agentpb.ListRoomMembersResponse{Members: members}, nil
}

func (s *AgentServer) ReplayRoom(ctx context.Context, req *agentpb.ReplayRoomRequest) (*agentpb.ReplayRoomResponse, error) {
	if s.roomManager == nil {
		return nil, status.Error(codes.Unimplemented, "rooms are not configured")
	}
	if err := s.verifyOwnership(ctx, req.Handle); err != nil {
		return nil, err
	}
	events, err := s.roomManager.Replay(ctx, req.RoomId, req.Handle, req.SinceSeq)
	if err != nil {
		return nil, err
	}
	return &agentpb.ReplayRoomResponse{Events: events}, nil
}

func (s *AgentServer) CloseRoom(ctx context.Context, req *agentpb.CloseRoomRequest) (*agentpb.CloseRoomResponse, error) {
	if s.roomManager == nil {
		return nil, status.Error(codes.Unimplemented, "rooms are not configured")
	}
	if err := s.verifyOwnership(ctx, req.Handle); err != nil {
		return nil, err
	}
	if _, err := s.roomManager.CloseRoom(ctx, req.RoomId, req.Handle); err != nil {
		return nil, err
	}
	return &agentpb.CloseRoomResponse{Ok: true}, nil
}

// ListSessions lists sessions involving a handle.
func (s *AgentServer) ListSessions(_ context.Context, req *agentpb.ListSessionsRequest) (*agentpb.ListSessionsResponse, error) {
	sessions := s.sessions.ListByHandle(req.Handle)
	var infos []*agentpb.SessionInfo
	for _, sess := range sessions {
		infos = append(infos, &agentpb.SessionInfo{
			SessionId:     sess.ID,
			FromHandle:    sess.FromHandle,
			ToHandle:      sess.ToHandle,
			State:         string(sess.State),
			CreatedAtUnix: sess.CreatedAt.Unix(),
			UpdatedAtUnix: sess.UpdatedAt.Unix(),
		})
	}
	return &agentpb.ListSessionsResponse{Sessions: infos}, nil
}

// queueDepth returns the total buffered messages across all subscriber channels for a handle.
// Must be called with s.mu held (at least RLock).
func (s *AgentServer) queueDepth(handle string) int {
	depth := 0
	for _, ch := range s.subscribers[handle] {
		depth += len(ch)
	}
	return depth
}

// GetNodeStatus returns a snapshot of the node's current state for the dashboard.
func (s *AgentServer) GetNodeStatus(_ context.Context, _ *agentpb.GetNodeStatusRequest) (*agentpb.GetNodeStatusResponse, error) {
	s.mu.RLock()
	// Build handle infos with subscriber counts and manifests
	var handles []*agentpb.HandleInfo
	for h := range s.handles {
		hi := &agentpb.HandleInfo{
			Name:            h,
			SubscriberCount: int32(len(s.subscribers[h])),
			QueueDepth:      int32(s.queueDepth(h)),
		}
		if hs := s.hstats[h]; hs != nil {
			hi.MessagesIn = hs.msgsIn.Load()
			hi.MessagesOut = hs.msgsOut.Load()
			hi.Drops = hs.drops.Load()
		}
		if m := s.manifests[h]; m != nil {
			hi.Manifest = m
			hi.Description = m.Description // deprecated field for backward compat
		}
		handles = append(handles, hi)
	}
	s.mu.RUnlock()

	// Build session infos
	allSessions := s.sessions.ListAll()
	var sessInfos []*agentpb.SessionInfo
	for _, sess := range allSessions {
		sessInfos = append(sessInfos, &agentpb.SessionInfo{
			SessionId:     sess.ID,
			FromHandle:    sess.FromHandle,
			ToHandle:      sess.ToHandle,
			State:         string(sess.State),
			CreatedAtUnix: sess.CreatedAt.Unix(),
			UpdatedAtUnix: sess.UpdatedAt.Unix(),
		})
	}

	// Build peer statuses from resolver + transport
	var peers []*agentpb.PeerStatus
	if s.dashResolver != nil {
		// Build node list from all known nodes (including those with no handles)
		nodeHandles := make(map[string][]string)
		nodeInfo := make(map[string]*agentpb.PeerStatus)

		for _, n := range s.dashResolver.GetNodes() {
			if n.NodeID == s.nodeID {
				continue
			}
			nodeInfo[n.NodeID] = &agentpb.PeerStatus{
				NodeId:        n.NodeID,
				AdvertiseAddr: n.AdvertiseAddr,
			}
		}

		// Add handles from peer map
		peerMap := s.dashResolver.GetPeerMap()
		for h, info := range peerMap {
			if info.NodeID == s.nodeID {
				continue
			}
			nodeHandles[info.NodeID] = append(nodeHandles[info.NodeID], h)
		}

		// Check which addrs are connected
		connectedAddrs := make(map[string]bool)
		if s.dashTransport != nil {
			for _, addr := range s.dashTransport.ConnectedAddrs() {
				connectedAddrs[addr] = true
			}
		}

		// Any relay connected means non-direct peers are relay-reachable
		anyRelayConnected := false
		if s.dashTransport != nil {
			anyRelayConnected = len(s.dashTransport.ConnectedRelayAddrs()) > 0
		}

		for nodeID, status := range nodeInfo {
			status.Handles = nodeHandles[nodeID]
			if connectedAddrs[status.AdvertiseAddr] {
				status.Connected = true
				status.Connectivity = "direct"
			} else if anyRelayConnected {
				status.Connected = true
				status.Connectivity = "relay"
			} else {
				status.Connected = false
				status.Connectivity = "offline"
			}
			peers = append(peers, status)
		}
	}

	// Build relay statuses
	var relays []*agentpb.RelayStatus
	if s.dashResolver != nil {
		connectedRelays := make(map[string]bool)
		if s.dashTransport != nil {
			for _, addr := range s.dashTransport.ConnectedRelayAddrs() {
				connectedRelays[addr] = true
			}
		}
		for _, ri := range s.dashResolver.GetRelays() {
			relays = append(relays, &agentpb.RelayStatus{
				NodeId:    ri.NodeID,
				Addr:      ri.Addr,
				Connected: connectedRelays[ri.Addr],
			})
		}
	}

	var rooms []*messagepb.RoomInfo
	if s.roomManager != nil {
		localHandles := make([]string, 0, len(handles))
		for _, h := range handles {
			localHandles = append(localHandles, h.Name)
		}
		var err error
		rooms, err = s.roomManager.DashboardRooms(localHandles)
		if err != nil {
			return nil, err
		}
	}

	// Get counters
	var counters *agentpb.Counters
	if s.activity != nil {
		counters = s.activity.Counters()
	}
	var usage *agentpb.UsageHistory
	if s.dashUsage != nil {
		var err error
		usage, err = s.dashUsage.LoadUsageHistory()
		if err != nil {
			s.logger.Warn("failed to load usage history", "error", err)
		}
	}

	return &agentpb.GetNodeStatusResponse{
		NodeId:    s.nodeID,
		StartedAt: timestamppb.New(s.startedAt),
		Handles:   handles,
		Peers:     peers,
		Sessions:  sessInfos,
		Counters:  counters,
		Relays:    relays,
		Rooms:     rooms,
		Usage:     usage,
	}, nil
}

// WatchActivity streams activity events to the dashboard.
func (s *AgentServer) WatchActivity(_ *agentpb.WatchActivityRequest, stream agentpb.AgentAPI_WatchActivityServer) error {
	if s.activity == nil {
		return fmt.Errorf("activity bus not configured")
	}

	ch := s.activity.Subscribe()
	defer s.activity.Unsubscribe(ch)

	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(event); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

// GetTrace returns trace spans for a given trace ID from the local node.
func (s *AgentServer) GetTrace(_ context.Context, req *agentpb.GetTraceRequest) (*agentpb.GetTraceResponse, error) {
	if s.traceStore == nil {
		return &agentpb.GetTraceResponse{}, nil
	}
	spans := s.traceStore.GetTrace(req.TraceId)
	return &agentpb.GetTraceResponse{Spans: spans}, nil
}

// GetManifests returns a snapshot of all handle manifests.
func (s *AgentServer) GetManifests() map[string]*messagepb.ServiceManifest {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]*messagepb.ServiceManifest, len(s.manifests))
	for h, m := range s.manifests {
		result[h] = m
	}
	return result
}

// IntrospectHandle returns the manifest for a handle.
func (s *AgentServer) IntrospectHandle(_ context.Context, req *agentpb.IntrospectHandleRequest) (*agentpb.IntrospectHandleResponse, error) {
	// Check local manifests first
	s.mu.RLock()
	m, ok := s.manifests[req.Handle]
	s.mu.RUnlock()
	if ok {
		resp := &agentpb.IntrospectHandleResponse{Handle: req.Handle, Found: true, Manifest: m}
		if m != nil {
			resp.Description = m.Description
		}
		return resp, nil
	}

	// Fall back to resolver (covers remote handles)
	if s.dashResolver != nil {
		if manifest, found := s.dashResolver.GetManifest(req.Handle); found {
			pm := handleManifestToProto(manifest)
			return &agentpb.IntrospectHandleResponse{
				Handle:      req.Handle,
				Found:       true,
				Manifest:    pm,
				Description: manifest.Description,
			}, nil
		}
	}

	return &agentpb.IntrospectHandleResponse{Handle: req.Handle, Found: false}, nil
}

// ListHandles returns all known handles, optionally filtered by tags.
func (s *AgentServer) ListHandles(_ context.Context, req *agentpb.ListHandlesRequest) (*agentpb.ListHandlesResponse, error) {
	seen := make(map[string]bool)
	var entries []*agentpb.HandleEntry

	// Local handles
	s.mu.RLock()
	for h := range s.handles {
		m := s.manifests[h]
		if matchesTagsProto(m, req.Tags) {
			entries = append(entries, &agentpb.HandleEntry{Handle: h, Manifest: m})
			seen[h] = true
		}
	}
	s.mu.RUnlock()

	// Remote handles from resolver
	if s.dashResolver != nil {
		remote := s.dashResolver.ListHandlesByTags(req.Tags)
		for h, manifest := range remote {
			if seen[h] {
				continue
			}
			entries = append(entries, &agentpb.HandleEntry{Handle: h, Manifest: handleManifestToProto(manifest)})
		}
	}

	return &agentpb.ListHandlesResponse{Entries: entries}, nil
}

// FindHandles returns ranked handles matching structured discovery constraints.
func (s *AgentServer) FindHandles(_ context.Context, req *agentpb.FindHandlesRequest) (*agentpb.FindHandlesResponse, error) {
	entries := make(map[string]handle.ServiceManifest)

	s.mu.RLock()
	for h := range s.handles {
		entries[h] = protoToHandleManifest(s.manifests[h])
	}
	s.mu.RUnlock()

	if s.dashResolver != nil {
		for _, match := range s.dashResolver.FindHandles(handle.FindQuery{
			Capabilities: req.Capabilities,
			Domains:      req.Domains,
			Tags:         req.Tags,
			CommandName:  req.CommandName,
			Version:      req.Version,
		}) {
			if _, exists := entries[match.Handle]; exists {
				continue
			}
			entries[match.Handle] = match.Manifest
		}
	}

	matches := handle.FindMatches(entries, handle.FindQuery{
		Capabilities: req.Capabilities,
		Domains:      req.Domains,
		Tags:         req.Tags,
		CommandName:  req.CommandName,
		Version:      req.Version,
		Limit:        int(req.Limit),
	})
	resp := make([]*agentpb.HandleMatch, 0, len(matches))
	for _, match := range matches {
		resp = append(resp, &agentpb.HandleMatch{
			Handle:       match.Handle,
			Manifest:     handleManifestToProto(match.Manifest),
			Score:        int32(match.Score),
			MatchReasons: match.MatchReasons,
		})
	}
	return &agentpb.FindHandlesResponse{Matches: resp}, nil
}

// matchesTagsProto checks if a protobuf manifest's tags match the required tags.
func matchesTagsProto(m *messagepb.ServiceManifest, required []string) bool {
	if len(required) == 0 {
		return true
	}
	if m == nil {
		return false
	}
	tagSet := make(map[string]bool, len(m.Tags))
	for _, t := range m.Tags {
		tagSet[t] = true
	}
	for _, r := range required {
		if !tagSet[r] {
			return false
		}
	}
	return true
}

// --- @-mention auto-routing ---

var mentionRe = regexp.MustCompile(`@([a-z][a-z0-9_-]*)`)

// extractMentions returns unique handle names from @handle patterns, excluding the skip set.
func extractMentions(text string, skip map[string]bool) []string {
	matches := mentionRe.FindAllStringSubmatch(text, -1)
	seen := make(map[string]bool, len(matches))
	var result []string
	for _, m := range matches {
		h := m[1]
		if skip[h] || seen[h] {
			continue
		}
		seen[h] = true
		result = append(result, h)
	}
	return result
}

// openMentionSession creates a new session from fromHandle to the mentioned handle
// and routes the opening message. Best-effort: logs warnings on failure.
func (s *AgentServer) openMentionSession(ctx context.Context, fromHandle, mentionedHandle string, payload []byte, contentType string) {
	sess := session.New(fromHandle, mentionedHandle)
	sess.TraceID = uuid.New().String()
	s.sessions.Put(sess)

	msgID := uuid.New().String()
	env := &messagepb.Envelope{
		MessageId:   msgID,
		SessionId:   sess.ID,
		FromHandle:  fromHandle,
		ToHandle:    mentionedHandle,
		Payload:     payload,
		ContentType: contentType,
		SentAtUnix:  sess.CreatedAt.Unix(),
		Type:        messagepb.EnvelopeType_ENVELOPE_TYPE_SESSION_OPEN,
		TraceId:     sess.TraceID,
	}

	if err := s.router.Route(ctx, env); err != nil {
		s.logger.Warn("@-mention session route failed", "from", fromHandle, "to", mentionedHandle, "error", err)
		return
	}

	if s.activity != nil {
		s.activity.EmitSessionOpened(sess.ID, fromHandle, mentionedHandle)
	}

	s.logger.Info("@-mention session opened", "session_id", sess.ID, "from", fromHandle, "to", mentionedHandle)
}

// maybeRouteMentions scans a message payload for @-mentions and opens sessions to each.
func (s *AgentServer) maybeRouteMentions(ctx context.Context, fromHandle, toHandle, contentType string, payload []byte) {
	if !strings.HasPrefix(contentType, "text/") {
		return
	}

	skip := map[string]bool{fromHandle: true, toHandle: true}
	mentions := extractMentions(string(payload), skip)
	for _, m := range mentions {
		mentioned := m // capture
		go s.openMentionSession(ctx, fromHandle, mentioned, payload, contentType)
	}
}
