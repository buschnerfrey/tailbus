package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/alexanderfrey/tailbus/internal/config"
	"github.com/alexanderfrey/tailbus/internal/daemon"
)

func main() {
	configPath := flag.String("config", "", "path to config file")
	nodeID := flag.String("node-id", "", "node ID")
	coordAddr := flag.String("coord", "coord.tailbus.co:8443", "coordination server address")
	advAddr := flag.String("advertise", "", "advertise address for P2P")
	listenAddr := flag.String("listen", ":9443", "P2P listen address")
	socketPath := flag.String("socket", "/tmp/tailbusd.sock", "Unix socket path")
	keyFile := flag.String("key", "", "path to node key file")
	metricsAddr := flag.String("metrics", ":9090", "Prometheus metrics listen address (empty to disable)")
	authToken := flag.String("auth-token", "", "auth token for coord admission control")
	mcpAddr := flag.String("mcp", "", "MCP gateway listen address (e.g. :8080, empty to disable)")
	chatAddr := flag.String("chat", "", "chat UI listen address (e.g. :3000, empty to disable)")
	meshToken := flag.String("mesh-token", "", "shared token for personal mesh auth")
	oauthURL := flag.String("oauth-url", "", "OAuth HTTP URL override for coord (default: auto-detect)")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	var cfg config.DaemonConfig
	if *configPath != "" {
		loaded, err := config.LoadDaemonConfig(*configPath)
		if err != nil {
			logger.Error("failed to load config", "error", err)
			os.Exit(1)
		}
		cfg = *loaded
	} else {
		cfg.NodeID = *nodeID
		cfg.CoordAddr = *coordAddr
		cfg.AdvertiseAddr = *advAddr
		cfg.ListenAddr = *listenAddr
		cfg.SocketPath = *socketPath
		cfg.KeyFile = *keyFile
		cfg.MetricsAddr = *metricsAddr
		cfg.AuthToken = *authToken
		cfg.MCPAddr = *mcpAddr
		cfg.ChatAddr = *chatAddr
		cfg.MeshToken = *meshToken
	}

	// Flag overrides config file
	if *authToken != "" {
		cfg.AuthToken = *authToken
	}
	if *mcpAddr != "" {
		cfg.MCPAddr = *mcpAddr
	}
	if *chatAddr != "" {
		cfg.ChatAddr = *chatAddr
	}
	if *meshToken != "" {
		cfg.MeshToken = *meshToken
	}
	if *oauthURL != "" {
		cfg.OAuthURL = *oauthURL
	}

	if cfg.NodeID == "" {
		hostname, _ := os.Hostname()
		cfg.NodeID = hostname
	}
	if cfg.KeyFile == "" {
		cfg.KeyFile = filepath.Join(os.TempDir(), "tailbusd-"+cfg.NodeID+".key")
	}

	d, err := daemon.New(&cfg, logger)
	if err != nil {
		if strings.Contains(err.Error(), "already running") {
			fmt.Fprintln(os.Stderr, err)
		} else {
			logger.Error("failed to create daemon", "error", err)
		}
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	if err := d.Run(ctx); err != nil {
		logger.Error("daemon error", "error", err)
		os.Exit(1)
	}
}
