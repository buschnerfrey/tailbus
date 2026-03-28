package chat

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
)

// handleMe returns the current user info.
func (g *Gateway) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"handle": chatUserHandle})
}

// handleListAgents returns all agents on the mesh.
func (g *Gateway) handleListAgents(w http.ResponseWriter, r *http.Request) {
	resp, err := g.agent.ListHandles(r.Context(), &agentpb.ListHandlesRequest{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type commandInfo struct {
		Name             string `json:"name"`
		Description      string `json:"description"`
		ParametersSchema string `json:"parametersSchema,omitempty"`
	}
	type agentInfo struct {
		Handle      string        `json:"handle"`
		Description string        `json:"description"`
		Tags        []string      `json:"tags"`
		Version     string        `json:"version"`
		Commands    []commandInfo `json:"commands,omitempty"`
	}

	var agents []agentInfo
	for _, entry := range resp.Entries {
		// Hide internal handles
		if entry.Handle == chatUserHandle || entry.Handle == "_mcp_gateway" {
			continue
		}
		a := agentInfo{Handle: entry.Handle}
		if entry.Manifest != nil {
			a.Description = entry.Manifest.Description
			a.Tags = entry.Manifest.Tags
			a.Version = entry.Manifest.Version
			for _, cmd := range entry.Manifest.Commands {
				a.Commands = append(a.Commands, commandInfo{
					Name:             cmd.Name,
					Description:      cmd.Description,
					ParametersSchema: cmd.ParametersSchema,
				})
			}
		}
		agents = append(agents, a)
	}

	if agents == nil {
		agents = []agentInfo{}
	}
	writeJSON(w, agents)
}

// handleIntrospectAgent returns detailed info about a single agent.
func (g *Gateway) handleIntrospectAgent(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	resp, err := g.agent.IntrospectHandle(r.Context(), &agentpb.IntrospectHandleRequest{Handle: handle})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !resp.Found {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	result := map[string]any{
		"handle": handle,
		"found":  true,
	}
	if resp.Manifest != nil {
		result["description"] = resp.Manifest.Description
		result["tags"] = resp.Manifest.Tags
		result["version"] = resp.Manifest.Version
		result["capabilities"] = resp.Manifest.Capabilities
		result["domains"] = resp.Manifest.Domains

		var commands []map[string]string
		for _, cmd := range resp.Manifest.Commands {
			commands = append(commands, map[string]string{
				"name":             cmd.Name,
				"description":      cmd.Description,
				"parametersSchema": cmd.ParametersSchema,
			})
		}
		result["commands"] = commands
	}
	writeJSON(w, result)
}

// handleSendToAgent opens a session, sends a message, waits for response, and resolves.
func (g *Gateway) handleSendToAgent(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")

	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// Open session
	openResp, err := g.agent.OpenSession(r.Context(), &agentpb.OpenSessionRequest{
		FromHandle:  chatUserHandle,
		ToHandle:    handle,
		Payload:     []byte(body.Message),
		ContentType: "text/plain",
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	sessionID := openResp.SessionId

	// Wait for response with timeout
	ctx := r.Context()
	deadline := time.After(120 * time.Second)

	// Poll the user manager for a response on this session
	responseCh := g.users.WaitForSession(sessionID)
	defer g.users.StopWaiting(sessionID)

	select {
	case resp := <-responseCh:
		// Resolve the session
		g.agent.ResolveSession(ctx, &agentpb.ResolveSessionRequest{
			SessionId:  sessionID,
			FromHandle: chatUserHandle,
		})

		writeJSON(w, map[string]any{
			"session_id": sessionID,
			"from":       resp.from,
			"message":    resp.payload,
		})
	case <-deadline:
		http.Error(w, "timeout waiting for agent response", http.StatusGatewayTimeout)
	case <-ctx.Done():
		return
	}
}

// handleCreateRoom creates a new room.
func (g *Gateway) handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title   string   `json:"title"`
		Members []string `json:"members"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	resp, err := g.agent.CreateRoom(r.Context(), &agentpb.CreateRoomRequest{
		CreatorHandle:  chatUserHandle,
		Title:          body.Title,
		InitialMembers: body.Members,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]string{"room_id": resp.RoomId})
}

// handleListRooms lists rooms the user is a member of.
func (g *Gateway) handleListRooms(w http.ResponseWriter, r *http.Request) {
	resp, err := g.agent.ListRooms(r.Context(), &agentpb.ListRoomsRequest{
		Handle: chatUserHandle,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type roomInfo struct {
		RoomID    string   `json:"room_id"`
		Title     string   `json:"title"`
		Members   []string `json:"members"`
		Status    string   `json:"status"`
		CreatedBy string   `json:"created_by"`
	}

	rooms := make([]roomInfo, 0, len(resp.Rooms))
	for _, rm := range resp.Rooms {
		rooms = append(rooms, roomInfo{
			RoomID:    rm.RoomId,
			Title:     rm.Title,
			Members:   rm.Members,
			Status:    rm.Status,
			CreatedBy: rm.CreatedBy,
		})
	}

	writeJSON(w, rooms)
}

// handleReplayRoom returns the room event transcript.
func (g *Gateway) handleReplayRoom(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("id")
	sinceSeq := uint64(0)
	if s := r.URL.Query().Get("since"); s != "" {
		if v, err := strconv.ParseUint(s, 10, 64); err == nil {
			sinceSeq = v
		}
	}

	resp, err := g.agent.ReplayRoom(r.Context(), &agentpb.ReplayRoomRequest{
		RoomId:   roomID,
		Handle:   chatUserHandle,
		SinceSeq: sinceSeq,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	events := make([]map[string]any, 0, len(resp.Events))
	for _, ev := range resp.Events {
		events = append(events, roomEventToJSON(ev))
	}

	writeJSON(w, events)
}

// handlePostRoomMessage posts a message to a room.
func (g *Gateway) handlePostRoomMessage(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("id")

	var body struct {
		Message     string `json:"message"`
		ContentType string `json:"content_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	ct := body.ContentType
	if ct == "" {
		ct = "text/plain"
	}

	resp, err := g.agent.PostRoomMessage(r.Context(), &agentpb.PostRoomMessageRequest{
		RoomId:      roomID,
		FromHandle:  chatUserHandle,
		Payload:     []byte(body.Message),
		ContentType: ct,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"event_id": resp.EventId,
		"room_seq": resp.RoomSeq,
	})
}

// handleJoinRoom joins the user to a room.
func (g *Gateway) handleJoinRoom(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("id")

	if _, err := g.agent.JoinRoom(r.Context(), &agentpb.JoinRoomRequest{
		RoomId: roomID,
		Handle: chatUserHandle,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "joined"})
}

// handleNodeStatus returns mesh node status.
func (g *Gateway) handleNodeStatus(w http.ResponseWriter, r *http.Request) {
	resp, err := g.agent.GetNodeStatus(r.Context(), &agentpb.GetNodeStatusRequest{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type handleInfo struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Subscribers int    `json:"subscribers"`
	}

	handles := make([]handleInfo, 0, len(resp.Handles))
	for _, h := range resp.Handles {
		desc := ""
		if h.Manifest != nil {
			desc = h.Manifest.Description
		}
		handles = append(handles, handleInfo{
			Name:        h.Name,
			Description: desc,
			Subscribers: int(h.SubscriberCount),
		})
	}

	writeJSON(w, map[string]any{
		"node_id": resp.NodeId,
		"handles": handles,
		"counters": map[string]int64{
			"messages_routed":  resp.Counters.MessagesRouted,
			"sessions_opened":  resp.Counters.SessionsOpened,
			"rooms_created":    resp.Counters.RoomsCreated,
			"room_messages":    resp.Counters.RoomMessagesPosted,
		},
	})
}

// --- Session response waiting ---

type sessionResponse struct {
	from    string
	payload string
}

var (
	sessionWaiters   = make(map[string]chan sessionResponse)
	sessionWaitersMu sync.Mutex
)

// WaitForSession registers a waiter for a response on the given session.
func (um *UserManager) WaitForSession(sessionID string) <-chan sessionResponse {
	ch := make(chan sessionResponse, 1)
	sessionWaitersMu.Lock()
	sessionWaiters[sessionID] = ch
	sessionWaitersMu.Unlock()
	return ch
}

// StopWaiting removes the session waiter.
func (um *UserManager) StopWaiting(sessionID string) {
	sessionWaitersMu.Lock()
	delete(sessionWaiters, sessionID)
	sessionWaitersMu.Unlock()
}

// --- Helpers ---

func roomEventToJSON(ev *messagepb.RoomEvent) map[string]any {
	return map[string]any{
		"event_id":   ev.EventId,
		"room_id":    ev.RoomId,
		"room_seq":   ev.RoomSeq,
		"sender":     ev.SenderHandle,
		"type":       ev.Type.String(),
		"payload":    string(ev.Payload),
		"content_type": ev.ContentType,
		"sent_at":    ev.SentAtUnix,
		"members":    ev.Members,
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
