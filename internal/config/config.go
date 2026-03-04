package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// OIDCProvider holds the configuration for an OpenID Connect provider.
type OIDCProvider struct {
	Name         string `toml:"name"`
	Issuer       string `toml:"issuer"`
	ClientID     string `toml:"client_id"`
	ClientSecret string `toml:"client_secret"`
}

// CoordConfig is the configuration for the coordination server.
type CoordConfig struct {
	ListenAddr     string         `toml:"listen_addr"`
	DataDir        string         `toml:"data_dir"`
	KeyFile        string         `toml:"key_file"`
	AuthTokens     []string       `toml:"auth_tokens"`
	OAuthProviders []OIDCProvider `toml:"oauth_providers"`
	JWTSecret      string         `toml:"jwt_secret"`
	OAuthHTTPAddr  string         `toml:"oauth_http_addr"`
	ExternalURL       string         `toml:"external_url"`
	InsecureGRPC      bool           `toml:"insecure_grpc"`
	RelayAddr         string         `toml:"relay_addr"`          // listen address for embedded relay (e.g. ":7443", empty = disabled)
	RelayAdvertiseAddr string        `toml:"relay_advertise_addr"` // address daemons connect to (e.g. "coord.tailbus.co:7443")
}

// DaemonConfig is the configuration for a node daemon.
type DaemonConfig struct {
	NodeID         string `toml:"node_id"`
	CoordAddr      string `toml:"coord_addr"`
	AdvertiseAddr  string `toml:"advertise_addr"`
	ListenAddr     string `toml:"listen_addr"`
	SocketPath     string `toml:"socket_path"`
	KeyFile        string `toml:"key_file"`
	DataDir        string `toml:"data_dir"`
	MetricsAddr    string `toml:"metrics_addr"`
	AuthToken      string `toml:"auth_token"`
	MCPAddr        string `toml:"mcp_addr"`
	CredentialFile string `toml:"credential_file"`
	OAuthURL       string `toml:"oauth_url"`
}

// RelayConfig is the configuration for a relay server.
type RelayConfig struct {
	RelayID    string `toml:"relay_id"`
	CoordAddr  string `toml:"coord_addr"`
	ListenAddr string `toml:"listen_addr"`
	KeyFile    string `toml:"key_file"`
	AuthToken  string `toml:"auth_token"`
}

// LoadRelayConfig loads a relay server config from a TOML file.
func LoadRelayConfig(path string) (*RelayConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg RelayConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":7443"
	}
	if cfg.CoordAddr == "" {
		cfg.CoordAddr = "127.0.0.1:8443"
	}
	if cfg.KeyFile == "" {
		cfg.KeyFile = "/var/lib/tailbus-relay/relay.key"
	}
	return &cfg, nil
}

// LoadCoordConfig loads a coordination server config from a TOML file.
func LoadCoordConfig(path string) (*CoordConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg CoordConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8443"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "/var/lib/tailbus-coord"
	}
	if cfg.KeyFile == "" {
		cfg.KeyFile = filepath.Join(cfg.DataDir, "coord.key")
	}
	return &cfg, nil
}

// LoadDaemonConfig loads a daemon config from a TOML file.
func LoadDaemonConfig(path string) (*DaemonConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg DaemonConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.SocketPath == "" {
		cfg.SocketPath = "/var/run/tailbus/tailbusd.sock"
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":9443"
	}
	if cfg.KeyFile == "" {
		cfg.KeyFile = "/var/lib/tailbusd/node.key"
	}
	if cfg.MetricsAddr == "" {
		cfg.MetricsAddr = ":9090"
	}
	return &cfg, nil
}
