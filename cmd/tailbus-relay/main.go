package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	"github.com/alexanderfrey/tailbus/internal/config"
	"github.com/alexanderfrey/tailbus/internal/daemon"
	"github.com/alexanderfrey/tailbus/internal/handle"
	"github.com/alexanderfrey/tailbus/internal/identity"
	"github.com/alexanderfrey/tailbus/internal/relay"
	"github.com/alexanderfrey/tailbus/internal/transport"
)

func main() {
	configPath := flag.String("config", "", "path to config file")
	relayID := flag.String("relay-id", "", "relay ID")
	coordAddr := flag.String("coord", "127.0.0.1:8443", "coordination server address")
	listenAddr := flag.String("listen", ":7443", "relay listen address")
	keyFile := flag.String("key", "", "path to relay key file")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	var cfg config.RelayConfig
	if *configPath != "" {
		loaded, err := config.LoadRelayConfig(*configPath)
		if err != nil {
			logger.Error("failed to load config", "error", err)
			os.Exit(1)
		}
		cfg = *loaded
	} else {
		cfg.RelayID = *relayID
		cfg.CoordAddr = *coordAddr
		cfg.ListenAddr = *listenAddr
		cfg.KeyFile = *keyFile
	}

	if cfg.RelayID == "" {
		hostname, _ := os.Hostname()
		cfg.RelayID = "relay-" + hostname
	}
	if cfg.KeyFile == "" {
		cfg.KeyFile = filepath.Join(os.TempDir(), "tailbus-relay-"+cfg.RelayID+".key")
	}

	// Load or generate identity
	kp, err := identity.LoadOrGenerate(cfg.KeyFile)
	if err != nil {
		logger.Error("failed to load identity", "error", err)
		os.Exit(1)
	}

	// Generate mTLS cert
	cert, err := identity.SelfSignedCert(kp)
	if err != nil {
		logger.Error("failed to generate TLS cert", "error", err)
		os.Exit(1)
	}

	// Create resolver and verifier for peer cert verification
	resolver := handle.NewResolver()
	verifier := transport.NewResolverVerifier(resolver)

	// Create relay server with mTLS
	srv := relay.NewServer(logger.With("component", "relay"), &cert, verifier)

	// Connect to coord server
	coordFPFile := filepath.Join(os.TempDir(), "tailbus-relay-"+cfg.RelayID+".coord-fp")
	cc, err := daemon.NewCoordClient(cfg.CoordAddr, cfg.RelayID, kp.Public, cfg.ListenAddr, resolver, logger.With("component", "coord-client"), kp, coordFPFile)
	if err != nil {
		logger.Error("failed to create coord client", "error", err)
		os.Exit(1)
	}
	defer cc.Close()

	// Mark as relay
	cc.SetIsRelay(true)

	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	// Register with coord (is_relay=true, empty handles)
	if err := cc.Register(ctx, nil, nil); err != nil {
		logger.Error("failed to register with coord", "error", err)
		os.Exit(1)
	}

	// Watch peer map in background (updates resolver for verifier)
	go func() {
		for {
			if err := cc.WatchPeerMap(ctx); err != nil {
				if ctx.Err() != nil {
					return
				}
				logger.Error("peer map watch error, retrying", "error", err)
				time.Sleep(5 * time.Second)
			}
		}
	}()

	// Heartbeat loop (30s, empty handles)
	noHandles := func() []string { return nil }
	noManifests := func() map[string]*messagepb.ServiceManifest { return nil }
	go cc.Heartbeat(ctx, noHandles, noManifests, 30*time.Second, nil)

	// Listen and serve
	lis, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		logger.Error("failed to listen", "addr", cfg.ListenAddr, "error", err)
		os.Exit(1)
	}

	logger.Info("relay server starting",
		"relay_id", cfg.RelayID,
		"listen", cfg.ListenAddr,
		"coord", cfg.CoordAddr,
	)

	go func() {
		if err := srv.Serve(lis); err != nil {
			logger.Error("relay server error", "error", err)
		}
	}()

	<-ctx.Done()
	logger.Info("relay shutting down")
	srv.GracefulStop()
}
