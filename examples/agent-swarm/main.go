// Package main implements a Go agent swarm demo for tailbus.
//
// Five agents run as goroutines, each on a separate daemon, analyzing the
// tailbus codebase in parallel: LOC counter, import scanner, TODO finder,
// complexity gauge, and an orchestrator that fans out and collects results.
//
// Zero external dependencies — pure Go stdlib analysis, completes in seconds.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// tokenCreds implements grpc.PerRPCCredentials for Bearer token auth.
type tokenCreds struct{ token string }

func (t tokenCreds) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + t.token}, nil
}
func (t tokenCreds) RequireTransportSecurity() bool { return false }

// agentConn bundles a gRPC client and its subscribe stream.
type agentConn struct {
	client agentpb.AgentAPIClient
	stream agentpb.AgentAPI_SubscribeClient
	handle string
}

func connectAgent(ctx context.Context, socketPath, handle string, manifest *messagepb.ServiceManifest) (*agentConn, error) {
	dialOpts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	tokenData, err := os.ReadFile(socketPath + ".token")
	if err == nil {
		dialOpts = append(dialOpts, grpc.WithPerRPCCredentials(tokenCreds{token: strings.TrimSpace(string(tokenData))}))
	}
	conn, err := grpc.NewClient("unix://"+socketPath, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", socketPath, err)
	}
	client := agentpb.NewAgentAPIClient(conn)
	_, err = client.Register(ctx, &agentpb.RegisterRequest{
		Handle:   handle,
		Manifest: manifest,
	})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("register %s: %w", handle, err)
	}
	stream, err := client.Subscribe(ctx, &agentpb.SubscribeRequest{Handle: handle})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("subscribe %s: %w", handle, err)
	}
	return &agentConn{client: client, stream: stream, handle: handle}, nil
}

func postRoom(ctx context.Context, ac *agentConn, roomID string, payload map[string]any) error {
	data, _ := json.Marshal(payload)
	_, err := ac.client.PostRoomMessage(ctx, &agentpb.PostRoomMessageRequest{
		RoomId:      roomID,
		FromHandle:  ac.handle,
		Payload:     data,
		ContentType: "application/json",
		TraceId:     str(payload, "turn_id"),
	})
	return err
}

func str(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// ── Analyzers ──────────────────────────────────────────────────────────

func analyzeLOC(targetDir string) map[string]any {
	totalLines := 0
	totalFiles := 0
	byDir := map[string]int{}
	filepath.Walk(targetDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(targetDir, path)
		dir := filepath.Dir(rel)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		lines := strings.Count(string(data), "\n")
		totalLines += lines
		totalFiles++
		byDir[dir] += lines
		return nil
	})
	// Top 5 directories by LOC
	type dirEntry struct {
		Dir   string `json:"dir"`
		Lines int    `json:"lines"`
	}
	var top []dirEntry
	for d, l := range byDir {
		top = append(top, dirEntry{d, l})
	}
	sort.Slice(top, func(i, j int) bool { return top[i].Lines > top[j].Lines })
	if len(top) > 5 {
		top = top[:5]
	}
	return map[string]any{
		"total_go_files": totalFiles,
		"total_lines":    totalLines,
		"top_dirs":       top,
	}
}

func analyzeImports(targetDir string) map[string]any {
	external := map[string]int{}
	filepath.Walk(targetDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		content := string(data)
		re := regexp.MustCompile(`"([^"]+)"`)
		// Find import blocks
		for _, m := range re.FindAllStringSubmatch(content, -1) {
			pkg := m[1]
			if strings.Contains(pkg, ".") && !strings.HasPrefix(pkg, "github.com/alexanderfrey/tailbus") {
				external[pkg]++
			}
		}
		return nil
	})
	type dep struct {
		Package string `json:"package"`
		Files   int    `json:"files"`
	}
	var deps []dep
	for pkg, count := range external {
		deps = append(deps, dep{pkg, count})
	}
	sort.Slice(deps, func(i, j int) bool { return deps[i].Files > deps[j].Files })
	if len(deps) > 10 {
		deps = deps[:10]
	}
	return map[string]any{
		"total_external_deps": len(external),
		"top_deps":            deps,
	}
}

func analyzeTODOs(targetDir string) map[string]any {
	type todoItem struct {
		File string `json:"file"`
		Line int    `json:"line"`
		Text string `json:"text"`
	}
	re := regexp.MustCompile(`(?i)//\s*(TODO|FIXME|HACK|XXX)\b.*`)
	var items []todoItem
	filepath.Walk(targetDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(targetDir, path)
		for i, line := range strings.Split(string(data), "\n") {
			if m := re.FindString(line); m != "" {
				items = append(items, todoItem{rel, i + 1, strings.TrimSpace(m)})
			}
		}
		return nil
	})
	if len(items) > 20 {
		items = items[:20]
	}
	return map[string]any{
		"total_todos": len(items),
		"items":       items,
	}
}

func analyzeComplexity(targetDir string) map[string]any {
	type longFunc struct {
		File  string `json:"file"`
		Func  string `json:"func"`
		Lines int    `json:"lines"`
	}
	var funcs []longFunc
	fset := token.NewFileSet()
	filepath.Walk(targetDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.Contains(path, "vendor") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(targetDir, path)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			start := fset.Position(fn.Pos()).Line
			end := fset.Position(fn.End()).Line
			lines := end - start + 1
			if lines >= 40 {
				name := fn.Name.Name
				if fn.Recv != nil && len(fn.Recv.List) > 0 {
					name = "(" + recvType(fn.Recv.List[0].Type) + ")." + name
				}
				funcs = append(funcs, longFunc{rel, name, lines})
			}
		}
		return nil
	})
	sort.Slice(funcs, func(i, j int) bool { return funcs[i].Lines > funcs[j].Lines })
	if len(funcs) > 10 {
		funcs = funcs[:10]
	}
	return map[string]any{
		"functions_over_40_lines": len(funcs),
		"longest":                funcs,
	}
}

func recvType(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return "*" + recvType(t.X)
	case *ast.Ident:
		return t.Name
	default:
		return "?"
	}
}

// ── Analyzer agent goroutine ───────────────────────────────────────────

func runAnalyzer(ctx context.Context, wg *sync.WaitGroup, socketPath, handle, capability string, analyzeFn func(string) map[string]any) {
	defer wg.Done()
	ac, err := connectAgent(ctx, socketPath, handle, &messagepb.ServiceManifest{
		Description:  fmt.Sprintf("Analyzes Go source code: %s", capability),
		Tags:         []string{"analysis", "go", strings.TrimPrefix(capability, "swarm.analyze.")},
		Version:      "1.0.0",
		Capabilities: []string{capability},
		Domains:      []string{"engineering"},
		InputTypes:   []string{"application/json"},
		OutputTypes:  []string{"application/json"},
	})
	if err != nil {
		log.Printf("[%s] connect failed: %v", handle, err)
		return
	}
	log.Printf("[%s] ready — capability %s", handle, capability)

	for {
		msg, err := ac.stream.Recv()
		if err != nil {
			log.Printf("[%s] stream closed: %v", handle, err)
			return
		}
		re := msg.GetRoomEvent()
		if re == nil || re.Type != messagepb.RoomEventType_ROOM_EVENT_TYPE_MESSAGE_POSTED {
			continue
		}
		if re.ContentType != "application/json" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(re.Payload, &payload); err != nil {
			continue
		}
		if str(payload, "kind") != "analyze_request" {
			continue
		}
		if t := str(payload, "target_handle"); t != "" && t != handle {
			continue
		}
		if t := str(payload, "target_capability"); t != "" && t != capability {
			continue
		}
		targetDir := str(payload, "target_dir")
		if targetDir == "" {
			continue
		}
		turnID := str(payload, "turn_id")
		log.Printf("[%s] analyzing %s...", handle, targetDir)

		// Add a small delay so the dashboard can show the busy state.
		time.Sleep(300 * time.Millisecond)

		started := time.Now()
		result := analyzeFn(targetDir)
		elapsed := time.Since(started).Seconds()

		reply := map[string]any{
			"kind":        "analyze_reply",
			"turn_id":     turnID,
			"author":      handle,
			"status":      "ok",
			"capability":  capability,
			"result":      result,
			"elapsed_sec": elapsed,
		}
		if err := postRoom(ctx, ac, re.RoomId, reply); err != nil {
			log.Printf("[%s] post failed: %v", handle, err)
		} else {
			log.Printf("[%s] posted result in %.1fs", handle, elapsed)
		}
	}
}

// ── Orchestrator agent goroutine ───────────────────────────────────────

func runOrchestrator(ctx context.Context, wg *sync.WaitGroup, socketPath string) {
	defer wg.Done()
	handle := "swarm-orchestrator"
	ac, err := connectAgent(ctx, socketPath, handle, &messagepb.ServiceManifest{
		Description:  "Coordinates parallel source analysis across the agent swarm",
		Tags:         []string{"workflow", "rooms", "discovery", "analysis"},
		Version:      "1.0.0",
		Capabilities: []string{"swarm.orchestrate"},
		Domains:      []string{"engineering"},
		InputTypes:   []string{"application/json"},
		OutputTypes:  []string{"application/json"},
		Commands: []*messagepb.CommandSpec{
			{
				Name:        "analyze",
				Description: "Analyze Go source code with the agent swarm",
			},
		},
	})
	if err != nil {
		log.Printf("[%s] connect failed: %v", handle, err)
		return
	}
	log.Printf("[%s] ready — command: analyze", handle)

	for {
		msg, err := ac.stream.Recv()
		if err != nil {
			log.Printf("[%s] stream closed: %v", handle, err)
			return
		}
		env := msg.GetEnvelope()
		if env == nil || env.Type != messagepb.EnvelopeType_ENVELOPE_TYPE_SESSION_OPEN {
			// Also handle room events for reply collection (handled inline below).
			continue
		}
		var cmdPayload map[string]any
		if err := json.Unmarshal(env.Payload, &cmdPayload); err != nil {
			continue
		}
		args, _ := cmdPayload["arguments"].(map[string]any)
		if args == nil {
			args = cmdPayload
		}
		targetDir, _ := args["target_dir"].(string)
		if targetDir == "" {
			targetDir = os.Getenv("TARGET_DIR")
		}
		if targetDir == "" {
			targetDir = "."
		}

		log.Printf("[%s] analyzing %s", handle, targetDir)
		result, err := orchestrateAnalysis(ctx, ac, handle, targetDir)
		if err != nil {
			log.Printf("[%s] error: %v", handle, err)
			errPayload, _ := json.Marshal(map[string]string{"error": err.Error(), "status": "failed"})
			ac.client.ResolveSession(ctx, &agentpb.ResolveSessionRequest{
				SessionId:   env.SessionId,
				FromHandle:  handle,
				Payload:     errPayload,
				ContentType: "application/json",
			})
			continue
		}
		resultData, _ := json.Marshal(result)
		ac.client.ResolveSession(ctx, &agentpb.ResolveSessionRequest{
			SessionId:   env.SessionId,
			FromHandle:  handle,
			Payload:     resultData,
			ContentType: "application/json",
		})
		log.Printf("[%s] done — total %.1fs", handle, result["total_time"])
	}
}

func orchestrateAnalysis(ctx context.Context, ac *agentConn, handle, targetDir string) (map[string]any, error) {
	caps := []string{
		"swarm.analyze.loc",
		"swarm.analyze.imports",
		"swarm.analyze.todos",
		"swarm.analyze.complexity",
	}

	// Discover all analyzer agents.
	type discovered struct {
		handle     string
		capability string
	}
	var agents []discovered
	for _, cap := range caps {
		resp, err := ac.client.FindHandles(ctx, &agentpb.FindHandlesRequest{
			Capabilities: []string{cap},
			Limit:        1,
		})
		if err != nil || len(resp.Matches) == 0 {
			return nil, fmt.Errorf("no agent for capability %s", cap)
		}
		m := resp.Matches[0]
		agents = append(agents, discovered{m.Handle, cap})
		log.Printf("[%s] found %s for %s (score %d)", handle, m.Handle, cap, m.Score)
	}

	// Create room with all members.
	members := make([]string, len(agents))
	for i, a := range agents {
		members[i] = a.handle
	}
	roomResp, err := ac.client.CreateRoom(ctx, &agentpb.CreateRoomRequest{
		CreatorHandle:  handle,
		Title:          "swarm: source analysis",
		InitialMembers: members,
	})
	if err != nil {
		return nil, fmt.Errorf("create room: %w", err)
	}
	roomID := roomResp.RoomId
	log.Printf("[%s] room created: %s", handle, roomID[:8])

	// Post task_opened.
	postRoom(ctx, ac, roomID, map[string]any{
		"kind":      "task_opened",
		"title":     "Source analysis",
		"author":    handle,
		"opened_at": time.Now().Unix(),
	})

	// Fan out analyze_request to each agent.
	turnIDs := make(map[string]string) // turn_id -> capability
	started := time.Now()
	for _, a := range agents {
		turnID := uuid.New().String()
		turnIDs[turnID] = a.capability
		postRoom(ctx, ac, roomID, map[string]any{
			"kind":              "analyze_request",
			"turn_id":           turnID,
			"target_handle":     a.handle,
			"target_capability": a.capability,
			"target_dir":        targetDir,
			"requested_by":      handle,
		})
		log.Printf("[%s] → @%s [%s]", handle, a.handle, a.capability)
	}

	// Collect replies from the room subscription stream.
	// We need to read from the same stream in the orchestrator goroutine.
	// Since the orchestrator is handling its own stream, we read inline.
	results := map[string]any{}
	remaining := len(turnIDs)
	deadline := time.After(60 * time.Second)
	for remaining > 0 {
		select {
		case <-deadline:
			return nil, fmt.Errorf("timed out waiting for %d replies", remaining)
		default:
		}
		msg, err := ac.stream.Recv()
		if err != nil {
			return nil, fmt.Errorf("stream error: %w", err)
		}
		re := msg.GetRoomEvent()
		if re == nil || re.RoomId != roomID {
			continue
		}
		if re.Type != messagepb.RoomEventType_ROOM_EVENT_TYPE_MESSAGE_POSTED {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(re.Payload, &payload); err != nil {
			continue
		}
		if str(payload, "kind") != "analyze_reply" {
			continue
		}
		turnID := str(payload, "turn_id")
		cap, ok := turnIDs[turnID]
		if !ok {
			continue
		}
		delete(turnIDs, turnID)
		remaining--
		elapsed, _ := payload["elapsed_sec"].(float64)
		log.Printf("[%s] ← @%s (%.1fs)", handle, str(payload, "author"), elapsed)
		// Use short key like "loc", "imports", etc.
		shortCap := strings.TrimPrefix(cap, "swarm.analyze.")
		results[shortCap] = payload["result"]
	}

	totalTime := time.Since(started).Seconds()

	// Post summary and close room.
	postRoom(ctx, ac, roomID, map[string]any{
		"kind":       "final_summary",
		"author":     handle,
		"results":    results,
		"total_time": totalTime,
	})
	ac.client.CloseRoom(ctx, &agentpb.CloseRoomRequest{
		RoomId: roomID,
		Handle: handle,
	})

	return map[string]any{
		"status":     "complete",
		"room_id":    roomID,
		"target_dir": targetDir,
		"results":    results,
		"total_time": totalTime,
	}, nil
}

func main() {
	// Socket paths from environment, defaulting to the swarm pattern.
	sockets := map[string]string{
		"orchestrator": envOr("SWARM_ORCH_SOCKET", "/tmp/agentswarm-control-node.sock"),
		"loc":          envOr("SWARM_LOC_SOCKET", "/tmp/agentswarm-loc-node.sock"),
		"imports":      envOr("SWARM_IMPORTS_SOCKET", "/tmp/agentswarm-imports-node.sock"),
		"todos":        envOr("SWARM_TODOS_SOCKET", "/tmp/agentswarm-todos-node.sock"),
		"complexity":   envOr("SWARM_COMPLEXITY_SOCKET", "/tmp/agentswarm-complexity-node.sock"),
	}

	ctx := context.Background()
	var wg sync.WaitGroup

	wg.Add(5)
	go runOrchestrator(ctx, &wg, sockets["orchestrator"])
	go runAnalyzer(ctx, &wg, sockets["loc"], "loc-counter", "swarm.analyze.loc", analyzeLOC)
	go runAnalyzer(ctx, &wg, sockets["imports"], "import-scanner", "swarm.analyze.imports", analyzeImports)
	go runAnalyzer(ctx, &wg, sockets["todos"], "todo-finder", "swarm.analyze.todos", analyzeTODOs)
	go runAnalyzer(ctx, &wg, sockets["complexity"], "complexity-gauge", "swarm.analyze.complexity", analyzeComplexity)

	wg.Wait()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
