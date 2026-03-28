package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const meshTokenFile = "mesh-token"

func runUp(args []string, logger *slog.Logger) {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	coordAddr := fs.String("coord", "coord.tailbus.co:8443", "coordination server address")
	chatAddr := fs.String("chat", ":3000", "chat UI listen address (empty to disable)")
	mcpAddr := fs.String("mcp", ":1423", "MCP gateway listen address (empty to disable)")
	token := fs.String("token", "", "mesh token (join existing mesh)")
	foreground := fs.Bool("fg", false, "run in foreground (don't daemonize)")
	socketPath := fs.String("socket", "/tmp/tailbusd.sock", "daemon Unix socket path")
	fs.Parse(args)

	tailbusDir := tailbusConfigDir()

	// Check if daemon is already running
	if isDaemonRunning(*socketPath) {
		fmt.Println("Tailbus is already running.")
		printStatus(*socketPath, tailbusDir, *chatAddr)
		return
	}

	// Resolve mesh token
	meshToken := *token
	tokenPath := filepath.Join(tailbusDir, meshTokenFile)

	if meshToken == "" {
		// Try to load existing token
		if data, err := os.ReadFile(tokenPath); err == nil && len(data) > 0 {
			meshToken = string(data)
		}
	}

	if meshToken == "" {
		// Generate a new token
		tokenBytes := make([]byte, 16)
		if _, err := rand.Read(tokenBytes); err != nil {
			fmt.Fprintf(os.Stderr, "Error generating token: %v\n", err)
			os.Exit(1)
		}
		meshToken = hex.EncodeToString(tokenBytes)
	}

	// Save token
	os.MkdirAll(tailbusDir, 0700)
	if err := os.WriteFile(tokenPath, []byte(meshToken), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not save mesh token: %v\n", err)
	}

	// Build tailbusd args
	daemonArgs := []string{
		"-coord", *coordAddr,
		"-socket", *socketPath,
	}
	if *chatAddr != "" {
		daemonArgs = append(daemonArgs, "-chat", *chatAddr)
	}
	if *mcpAddr != "" {
		daemonArgs = append(daemonArgs, "-mcp", *mcpAddr)
	}
	if meshToken != "" {
		daemonArgs = append(daemonArgs, "-mesh-token", meshToken)
	}

	if *foreground {
		// Run in foreground — exec replaces this process
		daemonBin := findDaemonBinary()
		cmd := exec.Command(daemonBin, daemonArgs...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Daemon error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Start daemon in background
	daemonBin := findDaemonBinary()
	cmd := exec.Command(daemonBin, daemonArgs...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start daemon: %v\n", err)
		os.Exit(1)
	}

	// Wait for daemon to become ready
	fmt.Print("Starting tailbus...")
	ready := false
	for i := 0; i < 30; i++ {
		time.Sleep(200 * time.Millisecond)
		if isDaemonRunning(*socketPath) {
			ready = true
			break
		}
	}

	if !ready {
		fmt.Println(" failed")
		fmt.Fprintln(os.Stderr, "Daemon did not start in time. Check logs or run with --fg for details.")
		os.Exit(1)
	}

	fmt.Println(" done")
	printStatus(*socketPath, tailbusDir, *chatAddr)
}

// runDown is handled by the existing "stop" case in main.go's daemon switch,
// which already has the gRPC client connected. "down" is added as an alias.

func printStatus(socketPath, tailbusDir, chatAddr string) {
	fmt.Println()

	// Show chat UI address
	if chatAddr != "" {
		host := "localhost"
		_, port, _ := net.SplitHostPort(chatAddr)
		if port != "" {
			fmt.Printf("  Chat UI: http://%s:%s\n", host, port)
		}
	}

	// Show mesh token
	tokenPath := filepath.Join(tailbusDir, meshTokenFile)
	if data, err := os.ReadFile(tokenPath); err == nil && len(data) > 0 {
		fmt.Printf("  Mesh token: %s\n", string(data))
		fmt.Printf("\n  To connect other machines: tailbus up --token %s\n", string(data))
	}
	fmt.Println()
}

func isDaemonRunning(socketPath string) bool {
	conn, err := net.DialTimeout("unix", socketPath, 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func findDaemonBinary() string {
	// Look for tailbusd next to the current binary
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(exe)
		candidate := filepath.Join(dir, "tailbusd")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	// Fall back to PATH
	path, err := exec.LookPath("tailbusd")
	if err == nil {
		return path
	}

	fmt.Fprintln(os.Stderr, "Could not find tailbusd binary. Make sure it's in your PATH or next to the tailbus binary.")
	os.Exit(1)
	return ""
}

func tailbusConfigDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tailbus")
}
