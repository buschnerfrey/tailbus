package daemon

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	"github.com/alexanderfrey/tailbus/internal/handle"
	"github.com/alexanderfrey/tailbus/internal/session"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type recordingRouter struct {
	calls int
}

func (r *recordingRouter) Route(_ context.Context, _ *messagepb.Envelope) error {
	r.calls++
	return nil
}

func newUnitAgentServer(router Router, activity *ActivityBus) *AgentServer {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewAgentServer(session.NewStore(), router, activity, logger)
}

func putTestSession(store *session.Store, state session.State) *session.Session {
	now := time.Now()
	sess := &session.Session{
		ID:         "sess-1",
		FromHandle: "marketing",
		ToHandle:   "sales",
		State:      state,
		TraceID:    "trace-1",
		NextSeq:    1,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	store.Put(sess)
	return sess
}

func TestSendMessageRejectsNonParticipant(t *testing.T) {
	router := &recordingRouter{}
	srv := newUnitAgentServer(router, nil)
	sess := putTestSession(srv.sessions, session.StateOpen)

	_, err := srv.SendMessage(context.Background(), &agentpb.SendMessageRequest{
		SessionId:   sess.ID,
		FromHandle:  "finance",
		Payload:     []byte("nope"),
		ContentType: "text/plain",
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
	if router.calls != 0 {
		t.Fatalf("router called %d times, want 0", router.calls)
	}
}

func TestSendMessageRejectsResolvedSession(t *testing.T) {
	router := &recordingRouter{}
	srv := newUnitAgentServer(router, nil)
	sess := putTestSession(srv.sessions, session.StateResolved)

	_, err := srv.SendMessage(context.Background(), &agentpb.SendMessageRequest{
		SessionId:   sess.ID,
		FromHandle:  "marketing",
		Payload:     []byte("late message"),
		ContentType: "text/plain",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}
	if router.calls != 0 {
		t.Fatalf("router called %d times, want 0", router.calls)
	}
}

func TestResolveSessionRejectsResolvedSessionBeforeRouting(t *testing.T) {
	router := &recordingRouter{}
	srv := newUnitAgentServer(router, nil)
	sess := putTestSession(srv.sessions, session.StateResolved)

	_, err := srv.ResolveSession(context.Background(), &agentpb.ResolveSessionRequest{
		SessionId:   sess.ID,
		FromHandle:  "marketing",
		ContentType: "text/plain",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}
	if router.calls != 0 {
		t.Fatalf("router called %d times, want 0", router.calls)
	}
}

func TestLocalRouteCountsDeliveryOnce(t *testing.T) {
	activity := NewActivityBus()
	srv := newUnitAgentServer(nil, activity)
	srv.handles["sales"] = true
	srv.subscribers["sales"] = []chan *agentpb.IncomingMessage{make(chan *agentpb.IncomingMessage, 1)}

	router := NewMessageRouter(handle.NewResolver(), nil, srv, activity, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	err := router.Route(context.Background(), &messagepb.Envelope{
		MessageId:  "msg-1",
		SessionId:  "sess-1",
		FromHandle: "marketing",
		ToHandle:   "sales",
		Type:       messagepb.EnvelopeType_ENVELOPE_TYPE_MESSAGE,
	})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if got := activity.MessagesDeliveredLocal.Load(); got != 1 {
		t.Fatalf("MessagesDeliveredLocal = %d, want 1", got)
	}
}
