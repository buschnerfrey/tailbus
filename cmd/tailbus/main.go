package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	"github.com/alexanderfrey/tailbus/internal/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// tokenCreds implements grpc.PerRPCCredentials for Bearer token auth.
type tokenCreds struct{ token string }

func (t tokenCreds) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + t.token}, nil
}

func (t tokenCreds) RequireTransportSecurity() bool { return false }

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func stripPort(addr string) string {
	for i := len(addr) - 1; i > 0; i-- {
		if addr[i] == ':' {
			return addr[:i]
		}
	}
	return addr
}

func main() {
	socketPath := flag.String("socket", "/tmp/tailbusd.sock", "daemon Unix socket path")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	args := flag.Args()
	if len(args) == 0 {
		fmt.Println("Usage: tailbus [command] [args...]")
		fmt.Println("\nAuth commands:")
		fmt.Println("  login     Authenticate with the coordination server")
		fmt.Println("  logout    Remove saved credentials")
		fmt.Println("  status    Show login and connection status")
		fmt.Println("\nMesh commands:")
		fmt.Println("  register, introspect, list, open, send, subscribe, resolve, sessions, dashboard, trace, agent")
		os.Exit(1)
	}

	// Handle auth commands before connecting to daemon
	switch args[0] {
	case "login":
		loginFlags := flag.NewFlagSet("login", flag.ExitOnError)
		coordAddr := loginFlags.String("coord", "coord.tailbus.co:8443", "coordination server address")
		credsFile := loginFlags.String("credentials", "", "path to credentials file")
		loginFlags.Parse(args[1:])

		credsPath := *credsFile
		if credsPath == "" {
			credsPath = auth.DefaultCredentialFile()
		}

		coordURL := "http://" + stripPort(*coordAddr) + ":8080"
		ctx := context.Background()

		// Check if already logged in
		if creds, err := auth.LoadCredentials(credsPath); err == nil && !creds.IsExpired() {
			fmt.Printf("Already logged in as %s\n", creds.Email)
			fmt.Printf("Credentials: %s\n", credsPath)
			os.Exit(0)
		}

		devResp, err := auth.RequestDeviceCode(ctx, coordURL)
		if err != nil {
			logger.Error("failed to start device flow", "error", err)
			fmt.Fprintf(os.Stderr, "Could not connect to coord OAuth server at %s\n", coordURL)
			os.Exit(1)
		}

		fmt.Printf("\nTo authenticate, visit:\n  %s\n\nand enter code: %s\n\nWaiting for login...\n",
			devResp.VerificationURI, devResp.UserCode)

		tokenResp, err := auth.PollForToken(ctx, coordURL, devResp.DeviceCode, devResp.Interval)
		if err != nil {
			logger.Error("login failed", "error", err)
			os.Exit(1)
		}

		newCreds := &auth.Credentials{
			AccessToken:  tokenResp.AccessToken,
			RefreshToken: tokenResp.RefreshToken,
			Email:        tokenResp.Email,
			CoordAddr:    *coordAddr,
			ExpiresAt:    time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Unix(),
		}
		if err := auth.SaveCredentials(credsPath, newCreds); err != nil {
			logger.Error("failed to save credentials", "error", err)
			os.Exit(1)
		}

		fmt.Printf("\nLogged in as %s\n", tokenResp.Email)
		fmt.Printf("Credentials saved to %s\n", credsPath)
		os.Exit(0)

	case "logout":
		logoutFlags := flag.NewFlagSet("logout", flag.ExitOnError)
		credsFile := logoutFlags.String("credentials", "", "path to credentials file")
		logoutFlags.Parse(args[1:])

		credsPath := *credsFile
		if credsPath == "" {
			credsPath = auth.DefaultCredentialFile()
		}

		if err := auth.RemoveCredentials(credsPath); err != nil {
			logger.Error("failed to remove credentials", "error", err)
			os.Exit(1)
		}
		fmt.Println("Logged out. Credentials removed.")
		os.Exit(0)

	case "status":
		statusFlags := flag.NewFlagSet("status", flag.ExitOnError)
		credsFile := statusFlags.String("credentials", "", "path to credentials file")
		statusFlags.Parse(args[1:])

		credsPath := *credsFile
		if credsPath == "" {
			credsPath = auth.DefaultCredentialFile()
		}

		creds, err := auth.LoadCredentials(credsPath)
		if err != nil {
			fmt.Println("Not logged in.")
			fmt.Printf("  Credential file: %s (not found)\n", credsPath)
			// Still try to check daemon
		} else {
			fmt.Printf("Logged in as: %s\n", creds.Email)
			fmt.Printf("  Coord: %s\n", creds.CoordAddr)
			expiry := time.Unix(creds.ExpiresAt, 0)
			if creds.IsExpired() {
				fmt.Printf("  Token: expired at %s (will auto-refresh)\n", expiry.Format(time.RFC3339))
			} else {
				fmt.Printf("  Token: valid until %s\n", expiry.Format(time.RFC3339))
			}
			fmt.Printf("  Credentials: %s\n", credsPath)
		}

		// Check daemon connection
		dialOpts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
		if tokenData, err := os.ReadFile(*socketPath + ".token"); err == nil {
			dialOpts = append(dialOpts, grpc.WithPerRPCCredentials(tokenCreds{token: string(tokenData)}))
		}
		conn, cerr := grpc.NewClient("unix://"+*socketPath, dialOpts...)
		if cerr != nil {
			fmt.Println("  Daemon: not running")
		} else {
			defer conn.Close()
			client := agentpb.NewAgentAPIClient(conn)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			resp, err := client.ListHandles(ctx, &agentpb.ListHandlesRequest{})
			if err != nil {
				fmt.Println("  Daemon: not reachable")
			} else {
				fmt.Printf("  Daemon: running (%d handles)\n", len(resp.Entries))
			}
		}
		os.Exit(0)
	}

	// All other commands require a daemon connection
	dialOpts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	if tokenData, err := os.ReadFile(*socketPath + ".token"); err == nil {
		dialOpts = append(dialOpts, grpc.WithPerRPCCredentials(tokenCreds{token: string(tokenData)}))
	}
	conn, err := grpc.NewClient("unix://"+*socketPath, dialOpts...)
	if err != nil {
		logger.Error("failed to connect to daemon", "error", err)
		os.Exit(1)
	}
	defer conn.Close()

	client := agentpb.NewAgentAPIClient(conn)
	ctx := context.Background()

	switch args[0] {
	case "register":
		regFlags := flag.NewFlagSet("register", flag.ExitOnError)
		regDesc := regFlags.String("description", "", "service description")
		regTags := regFlags.String("tags", "", "comma-separated tags")
		regVersion := regFlags.String("version", "", "service version")
		regFlags.Parse(args[1:])
		if regFlags.NArg() < 1 {
			fmt.Println("Usage: tailbus register <handle> [-description \"...\"] [-tags \"a,b\"] [-version \"1.0\"]")
			os.Exit(1)
		}
		handle := regFlags.Arg(0)
		req := &agentpb.RegisterRequest{Handle: handle}
		if *regDesc != "" || *regTags != "" || *regVersion != "" {
			m := &messagepb.ServiceManifest{
				Description: *regDesc,
				Version:     *regVersion,
			}
			if *regTags != "" {
				m.Tags = strings.Split(*regTags, ",")
			}
			req.Manifest = m
		}
		resp, err := client.Register(ctx, req)
		if err != nil {
			logger.Error("register failed", "error", err)
			os.Exit(1)
		}
		if !resp.Ok {
			fmt.Printf("Registration failed: %s\n", resp.Error)
			os.Exit(1)
		}
		fmt.Printf("Registered as %q\n", handle)

	case "introspect":
		if len(args) < 2 {
			fmt.Println("Usage: tailbus introspect <handle>")
			os.Exit(1)
		}
		resp, err := client.IntrospectHandle(ctx, &agentpb.IntrospectHandleRequest{Handle: args[1]})
		if err != nil {
			logger.Error("introspect failed", "error", err)
			os.Exit(1)
		}
		if !resp.Found {
			fmt.Printf("Handle %q not found\n", args[1])
			os.Exit(1)
		}
		fmt.Printf("Handle: %s\n", resp.Handle)
		if resp.Manifest != nil {
			m := resp.Manifest
			if m.Description != "" {
				fmt.Printf("Description: %s\n", m.Description)
			}
			if m.Version != "" {
				fmt.Printf("Version: %s\n", m.Version)
			}
			if len(m.Tags) > 0 {
				fmt.Printf("Tags: %s\n", strings.Join(m.Tags, ", "))
			}
			if len(m.Commands) > 0 {
				fmt.Println("Commands:")
				for _, c := range m.Commands {
					if c.Description != "" {
						fmt.Printf("  %s - %s\n", c.Name, c.Description)
					} else {
						fmt.Printf("  %s\n", c.Name)
					}
				}
			}
		} else if resp.Description != "" {
			fmt.Printf("Description: %s\n", resp.Description)
		} else {
			fmt.Println("(no manifest)")
		}

	case "list":
		req := &agentpb.ListHandlesRequest{}
		if len(args) >= 2 {
			req.Tags = strings.Split(args[1], ",")
		}
		resp, err := client.ListHandles(ctx, req)
		if err != nil {
			logger.Error("list handles failed", "error", err)
			os.Exit(1)
		}
		if len(resp.Entries) == 0 {
			fmt.Println("No handles found")
		} else {
			for _, e := range resp.Entries {
				desc := ""
				if e.Manifest != nil && e.Manifest.Description != "" {
					desc = " - " + e.Manifest.Description
				}
				tags := ""
				if e.Manifest != nil && len(e.Manifest.Tags) > 0 {
					tags = " [" + strings.Join(e.Manifest.Tags, ", ") + "]"
				}
				fmt.Printf("  %s%s%s\n", e.Handle, desc, tags)
			}
		}

	case "open":
		if len(args) < 4 {
			fmt.Println("Usage: tailbus open <from> <to> <message>")
			os.Exit(1)
		}
		resp, err := client.OpenSession(ctx, &agentpb.OpenSessionRequest{
			FromHandle:  args[1],
			ToHandle:    args[2],
			Payload:     []byte(args[3]),
			ContentType: "text/plain",
		})
		if err != nil {
			logger.Error("open session failed", "error", err)
			os.Exit(1)
		}
		fmt.Printf("Session: %s\nMessage: %s\n", resp.SessionId, resp.MessageId)

	case "send":
		if len(args) < 4 {
			fmt.Println("Usage: tailbus send <session-id> <from> <message>")
			os.Exit(1)
		}
		resp, err := client.SendMessage(ctx, &agentpb.SendMessageRequest{
			SessionId:   args[1],
			FromHandle:  args[2],
			Payload:     []byte(args[3]),
			ContentType: "text/plain",
		})
		if err != nil {
			logger.Error("send failed", "error", err)
			os.Exit(1)
		}
		fmt.Printf("Message: %s\n", resp.MessageId)

	case "subscribe":
		if len(args) < 2 {
			fmt.Println("Usage: tailbus subscribe <handle>")
			os.Exit(1)
		}
		stream, err := client.Subscribe(ctx, &agentpb.SubscribeRequest{Handle: args[1]})
		if err != nil {
			logger.Error("subscribe failed", "error", err)
			os.Exit(1)
		}
		fmt.Printf("Subscribed as %q, waiting for messages...\n", args[1])
		for {
			msg, err := stream.Recv()
			if err != nil {
				logger.Error("stream error", "error", err)
				os.Exit(1)
			}
			env := msg.Envelope
			fmt.Printf("[%s] %s -> %s: %s\n", env.SessionId[:8], env.FromHandle, env.ToHandle, string(env.Payload))
		}

	case "resolve":
		if len(args) < 3 {
			fmt.Println("Usage: tailbus resolve <session-id> <from> [message]")
			os.Exit(1)
		}
		var payload []byte
		if len(args) >= 4 {
			payload = []byte(args[3])
		}
		resp, err := client.ResolveSession(ctx, &agentpb.ResolveSessionRequest{
			SessionId:   args[1],
			FromHandle:  args[2],
			Payload:     payload,
			ContentType: "text/plain",
		})
		if err != nil {
			logger.Error("resolve failed", "error", err)
			os.Exit(1)
		}
		fmt.Printf("Resolved. Message: %s\n", resp.MessageId)

	case "sessions":
		if len(args) < 2 {
			fmt.Println("Usage: tailbus sessions <handle>")
			os.Exit(1)
		}
		resp, err := client.ListSessions(ctx, &agentpb.ListSessionsRequest{Handle: args[1]})
		if err != nil {
			logger.Error("list sessions failed", "error", err)
			os.Exit(1)
		}
		for _, s := range resp.Sessions {
			fmt.Printf("  %s  %s -> %s  [%s]\n", s.SessionId[:8], s.FromHandle, s.ToHandle, s.State)
		}

	case "agent":
		if err := runAgent(client, logger); err != nil {
			logger.Error("agent bridge error", "error", err)
			os.Exit(1)
		}

	case "dashboard":
		if err := runDashboard(client); err != nil {
			logger.Error("dashboard error", "error", err)
			os.Exit(1)
		}

	case "trace":
		if len(args) < 2 {
			fmt.Println("Usage: tailbus trace <trace-id>")
			os.Exit(1)
		}
		resp, err := client.GetTrace(ctx, &agentpb.GetTraceRequest{TraceId: args[1]})
		if err != nil {
			logger.Error("get trace failed", "error", err)
			os.Exit(1)
		}
		if len(resp.Spans) == 0 {
			fmt.Println("No spans found for trace", args[1])
			os.Exit(0)
		}
		fmt.Printf("Trace %s (%d spans):\n\n", args[1], len(resp.Spans))
		for _, span := range resp.Spans {
			ts := span.Timestamp.AsTime().Format("15:04:05.000")
			action := span.Action.String()
			meta := ""
			if len(span.Metadata) > 0 {
				var parts []string
				for k, v := range span.Metadata {
					parts = append(parts, k+"="+v)
				}
				meta = " " + fmt.Sprintf("%v", parts)
			}
			fmt.Printf("  %s  %-40s  msg:%s  node:%s%s\n", ts, action, short(span.MessageId), span.NodeId, meta)
		}

	default:
		fmt.Printf("Unknown command: %s\n", args[0])
		os.Exit(1)
	}
}
