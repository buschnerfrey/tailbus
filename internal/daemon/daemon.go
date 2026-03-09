package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	transportpb "github.com/alexanderfrey/tailbus/api/transportpb"
	"github.com/alexanderfrey/tailbus/internal/auth"
	"github.com/alexanderfrey/tailbus/internal/config"
	"github.com/alexanderfrey/tailbus/internal/handle"
	"github.com/alexanderfrey/tailbus/internal/identity"
	"github.com/alexanderfrey/tailbus/internal/mcp"
	"github.com/alexanderfrey/tailbus/internal/session"
	"github.com/alexanderfrey/tailbus/internal/transport"
)

// isLockTimeout checks whether err is a bbolt database lock timeout,
// which means another tailbusd process is already running.
func isLockTimeout(err error) bool {
	return err != nil && strings.Contains(err.Error(), "timeout")
}

// isDaemonAlive checks if a daemon is actually listening on the Unix socket.
func isDaemonAlive(socketPath string) bool {
	conn, err := net.DialTimeout("unix", socketPath, 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// Daemon is the main node daemon that ties together all components.
type Daemon struct {
	cfg         *config.DaemonConfig
	logger      *slog.Logger
	keypair     *identity.Keypair
	resolver    *handle.Resolver
	sessions    *session.Store
	coordClient *CoordClient
	agentServer *AgentServer
	transport   *transport.GRPCTransport
	router      *MessageRouter
	activity    *ActivityBus
	traceStore  *TraceStore
	metrics     *Metrics
	ackTracker  *AckTracker
	msgStore    *MessageStore
	roomManager *RoomManager
}

// New creates a new daemon from config.
func New(cfg *config.DaemonConfig, logger *slog.Logger) (*Daemon, error) {
	kp, err := identity.LoadOrGenerate(cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load identity: %w", err)
	}

	resolver := handle.NewResolver()
	sessions := session.NewStore()

	// Generate mTLS certificate from keypair
	cert, err := identity.SelfSignedCert(kp)
	if err != nil {
		return nil, fmt.Errorf("generate TLS cert: %w", err)
	}
	verifier := transport.NewResolverVerifier(resolver)

	// Create transport with mTLS
	tp := transport.NewGRPCTransport(logger, &cert, verifier)
	tp.SetResolver(resolver)
	tp.SetLocalPubKey(kp.Public)

	activity := NewActivityBus()
	traceStore := NewTraceStore(10000)
	metrics := NewMetrics(activity)

	// Open durable message store
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = filepath.Join(os.TempDir(), "tailbusd-"+cfg.NodeID)
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	dbPath := filepath.Join(dataDir, "messages.db")
	msgStore, err := NewMessageStore(dbPath, logger)
	if err != nil && isLockTimeout(err) {
		// DB is locked — check if the daemon is actually alive by probing the socket.
		if !isDaemonAlive(cfg.SocketPath) {
			// Stale lock from a crashed process. Remove the DB and retry.
			logger.Warn("found stale database lock, recovering", "path", dbPath)
			if rmErr := os.Remove(dbPath); rmErr != nil {
				return nil, fmt.Errorf("cannot remove stale database %s: %w", dbPath, rmErr)
			}
			msgStore, err = NewMessageStore(dbPath, logger)
		}
	}
	if err != nil {
		if isLockTimeout(err) {
			return nil, fmt.Errorf("tailbusd is already running (database %s is locked)\nUse \"tailbus stop\" to stop it first", dbPath)
		}
		return nil, fmt.Errorf("open message store: %w", err)
	}

	// Wire session persistence to durable store
	sessions.SetPersistence(
		func(sess *session.Session) error { return msgStore.StoreSession(sess) },
		func(id string) error { return msgStore.RemoveSession(id) },
	)

	d := &Daemon{
		cfg:        cfg,
		logger:     logger,
		keypair:    kp,
		resolver:   resolver,
		sessions:   sessions,
		transport:  tp,
		activity:   activity,
		traceStore: traceStore,
		metrics:    metrics,
		msgStore:   msgStore,
	}

	// Agent server needs router, but router needs agent server (for local delivery).
	// Create agent server first with nil router, then set router.
	agentSrv := NewAgentServer(sessions, nil, activity, logger)
	agentSrv.SetTracing(traceStore, metrics)
	router := NewMessageRouter(resolver, tp, agentSrv, activity, logger)
	router.SetTracing(traceStore, metrics, cfg.NodeID)
	agentSrv.router = router
	roomManager := NewRoomManager(cfg.NodeID, resolver, tp, agentSrv, msgStore, activity, logger)
	agentSrv.SetRoomManager(roomManager)

	// Create ACK tracker and wire into agent server + router
	ackTracker := NewAckTracker(func(ctx context.Context, addr string, env *messagepb.Envelope) error {
		return tp.Send(ctx, addr, &transportpb.TransportMessage{
			Body: &transportpb.TransportMessage_Envelope{Envelope: env},
		})
	}, logger)
	ackTracker.SetStore(msgStore)
	agentSrv.SetAckTracker(ackTracker)
	router.SetAckTracker(ackTracker)

	d.agentServer = agentSrv
	d.router = router
	d.ackTracker = ackTracker
	d.roomManager = roomManager

	// Set up transport callbacks
	tp.OnSend(func(msg *transportpb.TransportMessage) {
		env, ok := msg.Body.(*transportpb.TransportMessage_Envelope)
		if !ok || env.Envelope == nil {
			return
		}
		if traceStore != nil && env.Envelope.TraceId != "" {
			traceStore.RecordSpan(env.Envelope.TraceId, env.Envelope.MessageId, cfg.NodeID, agentpb.TraceAction_TRACE_ACTION_SENT_TO_TRANSPORT, nil)
		}
	})
	tp.OnReceive(func(msg *transportpb.TransportMessage) {
		switch body := msg.Body.(type) {
		case *transportpb.TransportMessage_Envelope:
			if body.Envelope == nil {
				return
			}
			if traceStore != nil && body.Envelope.TraceId != "" {
				traceStore.RecordSpan(body.Envelope.TraceId, body.Envelope.MessageId, cfg.NodeID, agentpb.TraceAction_TRACE_ACTION_RECEIVED_FROM_TRANSPORT, nil)
			}
			agentSrv.DeliverToLocal(body.Envelope)
			activity.MessagesReceivedRemote.Add(1)
			activity.EmitMessageRouted(body.Envelope.SessionId, body.Envelope.FromHandle, body.Envelope.ToHandle, false, body.Envelope.TraceId, body.Envelope.MessageId)
		default:
			roomManager.HandleTransportMessage(context.Background(), msg)
		}
	})

	return d, nil
}

// resolveAuth determines the auth token to use for coord registration.
// Priority: 1) explicit AuthToken in config, 2) saved credentials (refresh if needed),
// 3) device authorization flow (interactive).
func (d *Daemon) resolveAuth(ctx context.Context) (string, error) {
	// 1. Explicit auth token — backward compat, skip OAuth entirely
	if d.cfg.AuthToken != "" {
		d.logger.Info("using pre-shared auth token")
		return d.cfg.AuthToken, nil
	}

	credsPath := d.cfg.CredentialFile
	if credsPath == "" {
		credsPath = auth.DefaultCredentialFile()
	}

	coordURL := coordHTTPURL(d.cfg.CoordAddr, d.cfg.OAuthURL)

	// 2. Saved credentials — try to load and refresh
	creds, err := auth.LoadCredentials(credsPath)
	if err == nil {
		if !creds.NeedsRefresh() {
			d.logger.Info("using saved credentials", "email", creds.Email)
			return creds.AccessToken, nil
		}
		// Try to refresh
		refreshed, rerr := auth.RefreshIfNeeded(ctx, credsPath, coordURL)
		if rerr == nil {
			d.logger.Info("refreshed credentials", "email", refreshed.Email)
			return refreshed.AccessToken, nil
		}
		d.logger.Warn("credential refresh failed, starting device flow", "error", rerr)
	}

	// 3. Device authorization flow (interactive)
	d.logger.Info("no credentials found, starting login flow")

	devResp, err := auth.RequestDeviceCode(ctx, coordURL)
	if err != nil {
		// If the coord doesn't have OAuth configured, the request will fail.
		// In this case, try to register without a token (open mode).
		d.logger.Info("device code request failed (coord may not have OAuth), proceeding without auth", "error", err)
		return "", nil
	}

	// Print the verification URL and user code to stderr
	fmt.Fprintf(os.Stderr, "\nTo authenticate, visit:\n  %s\n\nand enter code: %s\n\nWaiting for login...\n\n",
		devResp.VerificationURI, devResp.UserCode)

	tokenResp, err := auth.PollForToken(ctx, coordURL, devResp.DeviceCode, devResp.Interval)
	if err != nil {
		return "", fmt.Errorf("device flow failed: %w", err)
	}

	// Save credentials
	newCreds := &auth.Credentials{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		Email:        tokenResp.Email,
		CoordAddr:    d.cfg.CoordAddr,
		ExpiresAt:    time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Unix(),
	}
	if err := auth.SaveCredentials(credsPath, newCreds); err != nil {
		d.logger.Warn("failed to save credentials", "error", err)
	}

	fmt.Fprintf(os.Stderr, "Authenticated as %s\n\n", tokenResp.Email)
	return tokenResp.AccessToken, nil
}

// coordHTTPURL derives the OAuth HTTP URL for the coord server.
// If override is set, it is returned directly.
// For localhost/127.0.0.1, defaults to http://{host}:8080 (local dev).
// For remote hosts, defaults to https://{host} (assumes edge TLS on port 443).
func coordHTTPURL(grpcAddr, override string) string {
	if override != "" {
		return override
	}
	host := grpcAddr
	// Strip port if present
	if i := len(host) - 1; i > 0 {
		for i > 0 && host[i] != ':' {
			i--
		}
		if i > 0 {
			host = host[:i]
		}
	}
	// Local dev: use HTTP on port 8080
	if host == "localhost" || host == "127.0.0.1" {
		return "http://" + host + ":8080"
	}
	// Remote: assume HTTPS on port 443 (edge TLS)
	return "https://" + host
}

// Run starts the daemon and blocks until the context is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	// Derive an inner context so the Shutdown RPC can cancel it
	innerCtx, innerCancel := context.WithCancel(ctx)
	defer innerCancel()
	d.agentServer.SetShutdownFunc(innerCancel)

	// Bind transport to daemon context so streams close on shutdown
	d.transport.Start(innerCtx)

	// Restore persisted sessions
	if restored, err := d.msgStore.LoadSessions(); err != nil {
		d.logger.Warn("failed to load persisted sessions", "error", err)
	} else if len(restored) > 0 {
		for _, sess := range restored {
			d.sessions.Put(sess)
		}
		d.logger.Info("restored persisted sessions", "count", len(restored))
	}

	// Restore pending messages into ACK tracker
	if pending, err := d.msgStore.LoadPending(); err != nil {
		d.logger.Warn("failed to load pending messages", "error", err)
	} else if len(pending) > 0 {
		restored := 0
		for _, pm := range pending {
			if pm.peerAddr == "" {
				// Discard stale pending message with no peer address
				_ = d.msgStore.RemovePending(pm.env.MessageId)
				continue
			}
			d.ackTracker.Restore(pm)
			restored++
		}
		d.logger.Info("restored pending messages for retry", "count", restored)
	}

	// Start session eviction (5min TTL, 30s sweep)
	d.sessions.StartEviction(innerCtx, 5*time.Minute, 30*time.Second, d.logger)

	// Start ACK retry loop
	go d.ackTracker.StartRetryLoop(innerCtx, time.Second)

	// Wire dashboard dependencies
	d.agentServer.SetDashboardDeps(d.cfg.NodeID, d.resolver, d.transport)

	// Resolve authentication before connecting to coord
	authToken, err := d.resolveAuth(innerCtx)
	if err != nil {
		return fmt.Errorf("resolve auth: %w", err)
	}

	// Resolve team ID from config or credentials
	teamID := d.resolveTeamID()

	// Connect to coord server with mTLS + TOFU
	coordFPFile := filepath.Join(os.TempDir(), "tailbusd-"+d.cfg.NodeID+".coord-fp")
	cc, err := NewCoordClient(d.cfg.CoordAddr, d.cfg.NodeID, d.keypair.Public, d.cfg.AdvertiseAddr, d.resolver, d.logger, d.keypair, coordFPFile, authToken)
	if err != nil {
		return fmt.Errorf("create coord client: %w", err)
	}
	if teamID != "" {
		cc.SetTeamID(teamID)
		d.logger.Info("team mode enabled", "team_id", teamID)
	}
	d.coordClient = cc
	d.roomManager.SetCoordClient(cc)
	defer cc.Close()

	// Set up token refresh callback for JWT-authenticated connections
	if authToken != "" && d.cfg.AuthToken == "" {
		credsPath := d.cfg.CredentialFile
		if credsPath == "" {
			credsPath = auth.DefaultCredentialFile()
		}
		coordURL := coordHTTPURL(d.cfg.CoordAddr, d.cfg.OAuthURL)
		cc.SetTokenRefresh(func(ctx context.Context) (string, error) {
			creds, err := auth.RefreshIfNeeded(ctx, credsPath, coordURL)
			if err != nil {
				return "", err
			}
			return creds.AccessToken, nil
		})
	}

	// Proactively connect to relays when relay info updates
	cc.SetOnRelayUpdate(func() { d.transport.ConnectToRelays() })

	// Register with coord (retries with exponential backoff)
	if err := registerWithRetry(innerCtx, cc, d.logger); err != nil {
		return fmt.Errorf("register with coord: %w", err)
	}

	// Signal readiness after successful coord registration
	d.metrics.SetReadyFunc(func() bool { return true })

	// When local handles change, re-register with coord so peer map updates immediately
	d.agentServer.SetOnHandleChange(func(handles []string, manifests map[string]*messagepb.ServiceManifest) {
		handleSnapshot := append([]string(nil), handles...)
		manifestSnapshot := make(map[string]*messagepb.ServiceManifest, len(manifests))
		for handle, manifest := range manifests {
			manifestSnapshot[handle] = manifest
		}
		go func() {
			if err := registerHandlesWithRetry(innerCtx, cc, d.logger, handleSnapshot, manifestSnapshot); err != nil && innerCtx.Err() == nil {
				d.logger.Error("failed to re-register handles with coord after retries", "error", err)
			}
		}()
	})

	// Start P2P transport listener
	p2pLis, err := net.Listen("tcp", d.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen P2P: %w", err)
	}
	go d.transport.Serve(p2pLis)

	// Start agent Unix socket server
	go func() {
		if err := d.agentServer.ServeUnix(d.cfg.SocketPath); err != nil {
			d.logger.Error("agent server error", "error", err)
		}
	}()

	// Watch peer map in background
	go func() {
		for {
			if err := cc.WatchPeerMap(innerCtx); err != nil {
				if innerCtx.Err() != nil {
					return
				}
				d.logger.Error("peer map watch error, retrying", "error", err)
				time.Sleep(5 * time.Second)
			}
		}
	}()

	// Heartbeat in background, with re-registration on "node not found"
	reRegister := func(ctx context.Context) error {
		handles := d.agentServer.GetHandles()
		manifests := d.agentServer.GetManifests()
		return retryWithBackoff(ctx, time.Second, 30*time.Second, d.logger, func() error {
			return cc.Register(ctx, handles, manifests)
		})
	}
	go cc.Heartbeat(innerCtx, d.agentServer.GetHandles, d.agentServer.GetManifests, 30*time.Second, reRegister)

	// Start metrics server if configured
	if d.cfg.MetricsAddr != "" {
		go d.metrics.Serve(innerCtx, d.cfg.MetricsAddr, d.logger)
	}

	// Start MCP gateway if configured
	if d.cfg.MCPAddr != "" {
		gw := mcp.NewGateway(d.agentServer, d.sessions, d.logger)
		if err := gw.Start(innerCtx); err != nil {
			d.logger.Error("failed to start MCP gateway", "error", err)
		} else {
			go func() {
				srv := &http.Server{Addr: d.cfg.MCPAddr, Handler: gw.Handler()}
				go func() {
					<-innerCtx.Done()
					srv.Close()
				}()
				d.logger.Info("MCP gateway listening", "addr", d.cfg.MCPAddr)
				if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					d.logger.Error("MCP gateway error", "error", err)
				}
			}()
		}
	}

	d.logger.Info("daemon running",
		"node_id", d.cfg.NodeID,
		"p2p_addr", d.cfg.ListenAddr,
		"socket", d.cfg.SocketPath,
		"metrics", d.cfg.MetricsAddr,
		"mcp", d.cfg.MCPAddr,
	)

	<-innerCtx.Done()
	d.logger.Info("daemon shutting down")

	// GracefulStop can hang if streaming RPCs (Subscribe, WatchActivity) are open.
	// Give it a short deadline, then force stop.
	stopped := make(chan struct{})
	go func() {
		d.agentServer.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		d.logger.Warn("graceful stop timed out, forcing")
		d.agentServer.ForceStop()
	}

	d.transport.Close()
	d.msgStore.Close()
	return nil
}

// registerWithRetry attempts to register with the coord server using
// exponential backoff: 1s, 2s, 4s, 8s, capped at 30s, with 20% jitter.
func registerWithRetry(ctx context.Context, cc *CoordClient, logger *slog.Logger) error {
	return retryWithBackoff(ctx, time.Second, 30*time.Second, logger, func() error {
		return cc.Register(ctx, nil, nil)
	})
}

func registerHandlesWithRetry(ctx context.Context, cc *CoordClient, logger *slog.Logger, handles []string, manifests map[string]*messagepb.ServiceManifest) error {
	return retryWithBackoff(ctx, 100*time.Millisecond, 2*time.Second, logger, func() error {
		return cc.Register(ctx, handles, manifests)
	})
}

// retryWithBackoff retries fn with exponential backoff and 20% jitter until
// it succeeds or the context is cancelled.
func retryWithBackoff(ctx context.Context, initial, max time.Duration, logger *slog.Logger, fn func() error) error {
	backoff := initial
	for {
		err := fn()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if logger != nil {
			logger.Warn("operation failed, retrying", "error", err, "backoff", backoff)
		}

		// Add 20% jitter
		jitter := time.Duration(float64(backoff) * 0.2 * rand.Float64())
		sleep := backoff + jitter
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleep):
		}
		backoff *= 2
		if backoff > max {
			backoff = max
		}
	}
}

// AgentServer returns the agent server (used for testing).
func (d *Daemon) AgentServer() *AgentServer {
	return d.agentServer
}

// TraceStore returns the trace store (used for testing).
func (d *Daemon) TraceStore() *TraceStore {
	return d.traceStore
}

// Resolver returns the handle resolver.
func (d *Daemon) Resolver() *handle.Resolver {
	return d.resolver
}

// Sessions returns the session store.
func (d *Daemon) Sessions() *session.Store {
	return d.sessions
}

// resolveTeamID returns the team ID from config or saved credentials.
// Priority: 1) explicit TeamID in config, 2) TeamID in saved credentials.
func (d *Daemon) resolveTeamID() string {
	if d.cfg.TeamID != "" {
		return d.cfg.TeamID
	}

	credsPath := d.cfg.CredentialFile
	if credsPath == "" {
		credsPath = auth.DefaultCredentialFile()
	}
	creds, err := auth.LoadCredentials(credsPath)
	if err == nil && creds.TeamID != "" {
		return creds.TeamID
	}
	return ""
}
