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
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	"github.com/alexanderfrey/tailbus/internal/config"
	"github.com/alexanderfrey/tailbus/internal/handle"
	"github.com/alexanderfrey/tailbus/internal/identity"
	"github.com/alexanderfrey/tailbus/internal/mcp"
	"github.com/alexanderfrey/tailbus/internal/session"
	"github.com/alexanderfrey/tailbus/internal/transport"
)

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
	msgStore, err := NewMessageStore(filepath.Join(dataDir, "messages.db"), logger)
	if err != nil {
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

	// Create ACK tracker and wire into agent server + router
	ackTracker := NewAckTracker(tp.Send, logger)
	ackTracker.SetStore(msgStore)
	agentSrv.SetAckTracker(ackTracker)
	router.SetAckTracker(ackTracker)

	d.agentServer = agentSrv
	d.router = router
	d.ackTracker = ackTracker

	// Set up transport callbacks
	tp.OnSend(func(env *messagepb.Envelope) {
		if traceStore != nil && env.TraceId != "" {
			traceStore.RecordSpan(env.TraceId, env.MessageId, cfg.NodeID, agentpb.TraceAction_TRACE_ACTION_SENT_TO_TRANSPORT, nil)
		}
	})
	tp.OnReceive(func(env *messagepb.Envelope) {
		if traceStore != nil && env.TraceId != "" {
			traceStore.RecordSpan(env.TraceId, env.MessageId, cfg.NodeID, agentpb.TraceAction_TRACE_ACTION_RECEIVED_FROM_TRANSPORT, nil)
		}
		agentSrv.DeliverToLocal(env)
		activity.MessagesReceivedRemote.Add(1)
	})

	return d, nil
}

// Run starts the daemon and blocks until the context is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	// Bind transport to daemon context so streams close on shutdown
	d.transport.Start(ctx)

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
		for _, pm := range pending {
			d.ackTracker.Restore(pm)
		}
		d.logger.Info("restored pending messages for retry", "count", len(pending))
	}

	// Start session eviction (5min TTL, 30s sweep)
	d.sessions.StartEviction(ctx, 5*time.Minute, 30*time.Second, d.logger)

	// Start ACK retry loop
	go d.ackTracker.StartRetryLoop(ctx, time.Second)

	// Wire dashboard dependencies
	d.agentServer.SetDashboardDeps(d.cfg.NodeID, d.resolver, d.transport)

	// Connect to coord server with mTLS + TOFU
	coordFPFile := filepath.Join(os.TempDir(), "tailbusd-"+d.cfg.NodeID+".coord-fp")
	cc, err := NewCoordClient(d.cfg.CoordAddr, d.cfg.NodeID, d.keypair.Public, d.cfg.AdvertiseAddr, d.resolver, d.logger, d.keypair, coordFPFile, d.cfg.AuthToken)
	if err != nil {
		return fmt.Errorf("create coord client: %w", err)
	}
	d.coordClient = cc
	defer cc.Close()

	// Proactively connect to relays when relay info updates
	cc.SetOnRelayUpdate(func() { d.transport.ConnectToRelays() })

	// Register with coord (retries with exponential backoff)
	if err := registerWithRetry(ctx, cc, d.logger); err != nil {
		return fmt.Errorf("register with coord: %w", err)
	}

	// Signal readiness after successful coord registration
	d.metrics.SetReadyFunc(func() bool { return true })

	// When local handles change, re-register with coord so peer map updates immediately
	d.agentServer.SetOnHandleChange(func(handles []string, manifests map[string]*messagepb.ServiceManifest) {
		if err := cc.Register(ctx, handles, manifests); err != nil {
			d.logger.Error("failed to re-register handles with coord", "error", err)
		}
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
			if err := cc.WatchPeerMap(ctx); err != nil {
				if ctx.Err() != nil {
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
	go cc.Heartbeat(ctx, d.agentServer.GetHandles, d.agentServer.GetManifests, 30*time.Second, reRegister)

	// Start metrics server if configured
	if d.cfg.MetricsAddr != "" {
		go d.metrics.Serve(ctx, d.cfg.MetricsAddr, d.logger)
	}

	// Start MCP gateway if configured
	if d.cfg.MCPAddr != "" {
		gw := mcp.NewGateway(d.agentServer, d.sessions, d.logger)
		if err := gw.Start(ctx); err != nil {
			d.logger.Error("failed to start MCP gateway", "error", err)
		} else {
			go func() {
				srv := &http.Server{Addr: d.cfg.MCPAddr, Handler: gw.Handler()}
				go func() {
					<-ctx.Done()
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

	<-ctx.Done()
	d.logger.Info("daemon shutting down")
	d.agentServer.GracefulStop()
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
