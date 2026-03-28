package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	"nhooyr.io/websocket"
)

// handleWebSocket upgrades the connection to a WebSocket and streams
// real-time mesh events (room messages, session messages, agent status).
func (g *Gateway) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if !g.checkAuth(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		g.logger.Error("websocket accept error", "error", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := conn.CloseRead(r.Context())

	// Create a client channel and register for message fan-out
	client := &wsClient{ch: make(chan []byte, 64)}
	g.users.AddClient(client)
	defer g.users.RemoveClient(client)

	// Also subscribe to activity events
	activityCh := g.activity.Subscribe()
	defer g.activity.Unsubscribe(activityCh)

	// Write loop: send messages to the WebSocket
	for {
		select {
		case <-ctx.Done():
			return

		case data := <-client.ch:
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Write(writeCtx, websocket.MessageText, data)
			cancel()
			if err != nil {
				return
			}

		case ev := <-activityCh:
			data := encodeActivityEvent(ev)
			if data == nil {
				continue
			}
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Write(writeCtx, websocket.MessageText, data)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

// encodeIncomingMessage converts an IncomingMessage to JSON for WebSocket delivery.
func encodeIncomingMessage(msg *agentpb.IncomingMessage) []byte {
	var m map[string]any

	if env := msg.Envelope; env != nil {
		// Check if there's a session waiter
		sessionWaitersMu.Lock()
		if ch, ok := sessionWaiters[env.SessionId]; ok {
			select {
			case ch <- sessionResponse{from: env.FromHandle, payload: string(env.Payload)}:
			default:
			}
		}
		sessionWaitersMu.Unlock()

		m = map[string]any{
			"type":         "session_message",
			"session_id":   env.SessionId,
			"message_id":   env.MessageId,
			"from":         env.FromHandle,
			"to":           env.ToHandle,
			"payload":      string(env.Payload),
			"content_type": env.ContentType,
			"sent_at":      env.SentAtUnix,
			"message_type": env.Type.String(),
		}
	} else if ev := msg.RoomEvent; ev != nil {
		m = map[string]any{
			"type":         "room_event",
			"event_id":     ev.EventId,
			"room_id":      ev.RoomId,
			"room_seq":     ev.RoomSeq,
			"sender":       ev.SenderHandle,
			"event_type":   ev.Type.String(),
			"payload":      string(ev.Payload),
			"content_type": ev.ContentType,
			"sent_at":      ev.SentAtUnix,
			"members":      ev.Members,
		}
	} else {
		return nil
	}

	data, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return data
}

// encodeActivityEvent converts an ActivityEvent to JSON for WebSocket delivery.
func encodeActivityEvent(ev *agentpb.ActivityEvent) []byte {
	var m map[string]any

	switch e := ev.Event.(type) {
	case *agentpb.ActivityEvent_HandleRegistered:
		m = map[string]any{
			"type":   "agent_online",
			"handle": e.HandleRegistered.Handle,
		}
	case *agentpb.ActivityEvent_RoomMessagePosted:
		// Already delivered via room subscription, skip to avoid duplicates
		return nil
	case *agentpb.ActivityEvent_RoomCreated:
		m = map[string]any{
			"type":    "room_created",
			"room_id": e.RoomCreated.RoomId,
			"title":   e.RoomCreated.Title,
		}
	default:
		// Skip other activity events for now
		return nil
	}

	data, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return data
}
