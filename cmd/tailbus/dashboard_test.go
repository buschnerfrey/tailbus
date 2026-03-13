package main

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeDashboardClient struct {
	getStatusResp *agentpb.GetNodeStatusResponse
	getStatusErr  error
	watchStreams  []grpc.ServerStreamingClient[agentpb.ActivityEvent]
	watchErrs     []error
	watchCalls    int
}

func (f *fakeDashboardClient) GetNodeStatus(context.Context, *agentpb.GetNodeStatusRequest, ...grpc.CallOption) (*agentpb.GetNodeStatusResponse, error) {
	return f.getStatusResp, f.getStatusErr
}

func (f *fakeDashboardClient) WatchActivity(context.Context, *agentpb.WatchActivityRequest, ...grpc.CallOption) (grpc.ServerStreamingClient[agentpb.ActivityEvent], error) {
	idx := f.watchCalls
	f.watchCalls++
	if idx < len(f.watchErrs) && f.watchErrs[idx] != nil {
		return nil, f.watchErrs[idx]
	}
	if idx < len(f.watchStreams) {
		return f.watchStreams[idx], nil
	}
	return &fakeActivityStream{recvErr: io.EOF}, nil
}

type fakeActivityStream struct {
	ctx     context.Context
	events  []*agentpb.ActivityEvent
	recvErr error
	index   int
	closed  int
}

func (s *fakeActivityStream) Header() (metadata.MD, error) { return metadata.MD{}, nil }
func (s *fakeActivityStream) Trailer() metadata.MD         { return metadata.MD{} }
func (s *fakeActivityStream) CloseSend() error {
	s.closed++
	return nil
}
func (s *fakeActivityStream) Context() context.Context {
	if s.ctx != nil {
		return s.ctx
	}
	return context.Background()
}
func (s *fakeActivityStream) SendMsg(any) error { return nil }
func (s *fakeActivityStream) RecvMsg(any) error { return nil }
func (s *fakeActivityStream) Recv() (*agentpb.ActivityEvent, error) {
	if s.index < len(s.events) {
		event := s.events[s.index]
		s.index++
		return event, nil
	}
	if s.recvErr != nil {
		err := s.recvErr
		s.recvErr = nil
		return nil, err
	}
	return nil, io.EOF
}

type fakeDashboardCloser struct {
	closed int
}

func (c *fakeDashboardCloser) Close() error {
	c.closed++
	return nil
}

func TestDashboardClearsStatusOnDisconnect(t *testing.T) {
	model := newDashboardModel(&fakeDashboardClient{})
	model.status = &agentpb.GetNodeStatusResponse{NodeId: "node-1"}

	next, _ := model.Update(statusErrMsg{status.Error(codes.Unavailable, "daemon down")})
	updated := next.(dashboardModel)

	if updated.status != nil {
		t.Fatal("expected status to be cleared after disconnect")
	}
	if updated.err == nil {
		t.Fatal("expected dashboard error to be recorded")
	}
}

func TestDashboardResubscribesAfterActivityDisconnect(t *testing.T) {
	client := &fakeDashboardClient{
		watchStreams: []grpc.ServerStreamingClient[agentpb.ActivityEvent]{
			&fakeActivityStream{
				events: []*agentpb.ActivityEvent{
					{
						Event: &agentpb.ActivityEvent_HandleRegistered{
							HandleRegistered: &agentpb.HandleRegisteredEvent{Handle: "solver"},
						},
					},
				},
			},
		},
	}

	model := newDashboardModel(client)

	next, retryCmd := model.Update(activityErrMsg{status.Error(codes.Unavailable, "stream closed")})
	if retryCmd == nil {
		t.Fatal("expected reconnect retry command after activity stream error")
	}

	updated := next.(dashboardModel)
	_, watchCmd := updated.Update(watchRetryMsg{})
	if watchCmd == nil {
		t.Fatal("expected watch command after retry message")
	}

	msg := watchCmd()
	if _, ok := msg.(activityMsg); !ok {
		t.Fatalf("expected activityMsg after resubscribe, got %T", msg)
	}
	if client.watchCalls != 1 {
		t.Fatalf("expected one watch attempt, got %d", client.watchCalls)
	}
}

func TestDashboardWatchActivityClosesStreamAfterRecv(t *testing.T) {
	stream := &fakeActivityStream{
		events: []*agentpb.ActivityEvent{
			{Event: &agentpb.ActivityEvent_HandleRegistered{HandleRegistered: &agentpb.HandleRegisteredEvent{Handle: "solver"}}},
		},
	}
	model := newDashboardModel(&fakeDashboardClient{
		watchStreams: []grpc.ServerStreamingClient[agentpb.ActivityEvent]{stream},
	})

	msg := model.watchActivity()
	if _, ok := msg.(activityMsg); !ok {
		t.Fatalf("expected activityMsg, got %T", msg)
	}
	if stream.closed != 1 {
		t.Fatalf("expected stream to be closed after recv, got %d", stream.closed)
	}
}

func TestDashboardIgnoresNilRoomMessagePosted(t *testing.T) {
	model := newDashboardModel(&fakeDashboardClient{})

	next, cmd := model.Update(activityMsg(&agentpb.ActivityEvent{
		Event: &agentpb.ActivityEvent_RoomMessagePosted{},
	}))
	if cmd == nil {
		t.Fatal("expected watchNext command")
	}
	updated := next.(dashboardModel)
	if len(updated.activity) != 0 {
		t.Fatalf("expected no activity for nil room message, got %d entries", len(updated.activity))
	}
	if len(updated.busyTurns) != 0 {
		t.Fatalf("expected no busy turns for nil room message, got %d", len(updated.busyTurns))
	}
}

func TestReconnectingDashboardClientRedialsStatusRequests(t *testing.T) {
	firstCloser := &fakeDashboardCloser{}
	secondCloser := &fakeDashboardCloser{}
	dialCalls := 0
	client := &reconnectingDashboardClient{
		dial: func() (dashboardClient, dashboardCloser, error) {
			dialCalls++
			switch dialCalls {
			case 1:
				return &fakeDashboardClient{
					getStatusResp: &agentpb.GetNodeStatusResponse{NodeId: "node-1"},
				}, firstCloser, nil
			case 2:
				return &fakeDashboardClient{
					getStatusResp: &agentpb.GetNodeStatusResponse{NodeId: "node-2"},
				}, secondCloser, nil
			default:
				t.Fatalf("unexpected extra dial %d", dialCalls)
				return nil, nil, nil
			}
		},
	}

	resp1, err := client.GetNodeStatus(context.Background(), &agentpb.GetNodeStatusRequest{})
	if err != nil {
		t.Fatalf("first GetNodeStatus: %v", err)
	}
	resp2, err := client.GetNodeStatus(context.Background(), &agentpb.GetNodeStatusRequest{})
	if err != nil {
		t.Fatalf("second GetNodeStatus: %v", err)
	}

	if resp1.NodeId != "node-1" || resp2.NodeId != "node-2" {
		t.Fatalf("unexpected status responses: %q then %q", resp1.NodeId, resp2.NodeId)
	}
	if dialCalls != 2 {
		t.Fatalf("dial calls = %d, want 2", dialCalls)
	}
	if firstCloser.closed != 1 || secondCloser.closed != 1 {
		t.Fatalf("expected each status connection to be closed once, got %d and %d", firstCloser.closed, secondCloser.closed)
	}
}

func TestReconnectingDashboardClientClosesWatchConnectionOnStreamEnd(t *testing.T) {
	closer := &fakeDashboardCloser{}
	stream := &fakeActivityStream{
		events: []*agentpb.ActivityEvent{
			{Event: &agentpb.ActivityEvent_HandleRegistered{HandleRegistered: &agentpb.HandleRegisteredEvent{Handle: "solver"}}},
		},
		recvErr: io.EOF,
	}
	client := &reconnectingDashboardClient{
		dial: func() (dashboardClient, dashboardCloser, error) {
			return &fakeDashboardClient{
				watchStreams: []grpc.ServerStreamingClient[agentpb.ActivityEvent]{stream},
			}, closer, nil
		},
	}

	watch, err := client.WatchActivity(context.Background(), &agentpb.WatchActivityRequest{})
	if err != nil {
		t.Fatalf("WatchActivity: %v", err)
	}
	if _, err := watch.Recv(); err != nil {
		t.Fatalf("first Recv: %v", err)
	}
	if closer.closed != 0 {
		t.Fatalf("connection closed too early: %d", closer.closed)
	}
	if _, err := watch.Recv(); err != io.EOF {
		t.Fatalf("second Recv error = %v, want EOF", err)
	}
	if closer.closed != 1 {
		t.Fatalf("expected watch connection to close on stream end, got %d", closer.closed)
	}
}

func TestBuildTopoNodesSortsPeersAndRelaysStably(t *testing.T) {
	model := newDashboardModel(&fakeDashboardClient{})
	model.status = &agentpb.GetNodeStatusResponse{
		NodeId: "support-node",
		Handles: []*agentpb.HandleInfo{
			{Name: "support-triage"},
			{Name: "incident-orchestrator"},
		},
		Peers: []*agentpb.PeerStatus{
			{NodeId: "ops-node", Handles: []string{"release-agent", "logs-agent"}},
			{NodeId: "finance-node", Handles: []string{"billing-agent"}},
		},
		Relays: []*agentpb.RelayStatus{
			{NodeId: "z-relay"},
			{NodeId: "a-relay"},
		},
	}

	nodes := model.buildTopoNodes()
	gotIDs := make([]string, len(nodes))
	for i, node := range nodes {
		gotIDs[i] = node.id
	}

	wantIDs := []string{"support-node", "finance-node", "ops-node", "a-relay", "z-relay"}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("unexpected node order: got %v want %v", gotIDs, wantIDs)
	}

	if !reflect.DeepEqual(nodes[0].handles, []string{"incident-orchestrator", "support-triage"}) {
		t.Fatalf("expected local handles to be sorted, got %v", nodes[0].handles)
	}
	if !reflect.DeepEqual(nodes[2].handles, []string{"logs-agent", "release-agent"}) {
		t.Fatalf("expected peer handles to be sorted, got %v", nodes[2].handles)
	}
}

func TestRenderUsageShowsHeatmapAndStats(t *testing.T) {
	model := newDashboardModel(&fakeDashboardClient{})
	model.rightPanel = rightPanelUsage
	model.usageMetric = agentpb.UsageMetric_USAGE_METRIC_MESSAGES_ROUTED
	model.usageRange = usageRange30d
	model.status = &agentpb.GetNodeStatusResponse{
		Sessions: []*agentpb.SessionInfo{
			{SessionId: "sess-1", State: "open"},
		},
		Counters: &agentpb.Counters{
			MessagesRouted: 12,
			RoomsCreated:   3,
		},
		Usage: &agentpb.UsageHistory{
			Metrics: []*agentpb.UsageMetricHistory{
				{
					Metric: agentpb.UsageMetric_USAGE_METRIC_MESSAGES_ROUTED,
					Total:  12,
					DailyBuckets: []*agentpb.UsageBucket{
						{BucketStartUnix: time.Now().UTC().AddDate(0, 0, -2).Truncate(24 * time.Hour).Unix(), Count: 4},
						{BucketStartUnix: time.Now().UTC().AddDate(0, 0, -1).Truncate(24 * time.Hour).Unix(), Count: 8},
					},
				},
			},
		},
	}

	view := model.renderRightPanel(60, 16)
	for _, want := range []string{"USAGE", "Total:", "Active days:", "Messages routed: 12"} {
		if !strings.Contains(view, want) {
			t.Fatalf("renderRightPanel missing %q in:\n%s", want, view)
		}
	}
}

func TestDashboardBusyTurnTracksProgress(t *testing.T) {
	model := newDashboardModel(&fakeDashboardClient{})
	now := time.Now()

	next, _ := model.Update(activityMsg(&agentpb.ActivityEvent{
		Event: &agentpb.ActivityEvent_RoomMessagePosted{
			RoomMessagePosted: &agentpb.RoomMessagePostedEvent{
				RoomId:       "room-1",
				FromHandle:   "task-orchestrator",
				TargetHandle: "implementer",
				TurnId:       "turn-1",
				ContentKind:  "turn_request",
				Round:        1,
			},
		},
	}))
	updated := next.(dashboardModel)
	bt, ok := updated.busyTurns["turn-1"]
	if !ok {
		t.Fatal("expected busy turn after turn_request")
	}
	createdAt := bt.since
	activityCount := len(updated.activity)

	next, _ = updated.Update(activityMsg(&agentpb.ActivityEvent{
		Event: &agentpb.ActivityEvent_RoomMessagePosted{
			RoomMessagePosted: &agentpb.RoomMessagePostedEvent{
				RoomId:       "room-1",
				FromHandle:   "implementer",
				TargetHandle: "implementer",
				TurnId:       "turn-1",
				ContentKind:  "turn_progress",
				Status:       "working",
				Round:        1,
			},
		},
		Timestamp: timestamppb.New(now),
	}))
	updated = next.(dashboardModel)
	bt, ok = updated.busyTurns["turn-1"]
	if !ok {
		t.Fatal("expected busy turn to remain after progress")
	}
	if bt.toHandle != "implementer" {
		t.Fatalf("toHandle = %q, want implementer", bt.toHandle)
	}
	if bt.since != createdAt {
		t.Fatal("expected original busy-turn start time to be preserved")
	}
	if len(updated.activity) != activityCount {
		t.Fatal("expected turn_progress to stay out of the activity feed")
	}

	next, _ = updated.Update(activityMsg(&agentpb.ActivityEvent{
		Event: &agentpb.ActivityEvent_RoomMessagePosted{
			RoomMessagePosted: &agentpb.RoomMessagePostedEvent{
				RoomId:      "room-1",
				FromHandle:  "implementer",
				TurnId:      "turn-1",
				ContentKind: "solver_reply",
				Status:      "ok",
				Round:       1,
			},
		},
	}))
	updated = next.(dashboardModel)
	if _, ok := updated.busyTurns["turn-1"]; ok {
		t.Fatal("expected busy turn to be cleared after reply")
	}
}

func TestDashboardBusyTurnClearsOnImplementReply(t *testing.T) {
	model := newDashboardModel(&fakeDashboardClient{})

	next, _ := model.Update(activityMsg(&agentpb.ActivityEvent{
		Event: &agentpb.ActivityEvent_RoomMessagePosted{
			RoomMessagePosted: &agentpb.RoomMessagePostedEvent{
				RoomId:       "room-1",
				FromHandle:   "task-orchestrator",
				TargetHandle: "implementer",
				TurnId:       "turn-1",
				ContentKind:  "turn_request",
				Round:        1,
			},
		},
	}))
	updated := next.(dashboardModel)
	if _, ok := updated.busyTurns["turn-1"]; !ok {
		t.Fatal("expected busy turn after turn_request")
	}

	next, _ = updated.Update(activityMsg(&agentpb.ActivityEvent{
		Event: &agentpb.ActivityEvent_RoomMessagePosted{
			RoomMessagePosted: &agentpb.RoomMessagePostedEvent{
				RoomId:      "room-1",
				FromHandle:  "implementer",
				TurnId:      "turn-1",
				ContentKind: "implement_reply",
				Status:      "ok",
				Round:       1,
			},
		},
	}))
	updated = next.(dashboardModel)
	if _, ok := updated.busyTurns["turn-1"]; ok {
		t.Fatal("expected busy turn to be cleared after implement_reply")
	}
}

func TestActivityEntryFormatsImplementReplyAsReply(t *testing.T) {
	entry := formatActivity(&agentpb.ActivityEvent{
		Timestamp: timestamppb.New(time.Unix(0, 0)),
		Event: &agentpb.ActivityEvent_RoomMessagePosted{
			RoomMessagePosted: &agentpb.RoomMessagePostedEvent{
				RoomId:      "room-1",
				FromHandle:  "implementer",
				ContentKind: "implement_reply",
				Status:      "ok",
				Round:       2,
			},
		},
	})
	if !strings.Contains(entry.label, "REPLY room-1 r2 implementer [ok]") {
		t.Fatalf("unexpected label %q", entry.label)
	}
}

func TestActivityEntryShortensShortSessionIDsSafely(t *testing.T) {
	entry := formatActivity(&agentpb.ActivityEvent{
		Timestamp: timestamppb.New(time.Unix(0, 0)),
		Event: &agentpb.ActivityEvent_SessionOpened{
			SessionOpened: &agentpb.SessionOpenedEvent{
				FromHandle: "a",
				ToHandle:   "b",
				SessionId:  "xyz",
			},
		},
	})
	if !strings.Contains(entry.label, "(xyz)") {
		t.Fatalf("unexpected label %q", entry.label)
	}
}

func TestDashboardMarksStaleBusyTurnsAsStalled(t *testing.T) {
	model := newDashboardModel(&fakeDashboardClient{})
	now := time.Now()
	model.busyTurns["stale"] = busyTurn{
		turnID:     "stale",
		fromHandle: "task-orchestrator",
		toHandle:   "implementer",
		since:      now.Add(-busyTurnStaleAfter - time.Second),
		status:     "working",
		lastUpdate: now.Add(-busyTurnStaleAfter - time.Second),
	}
	model.busyTurns["fresh"] = busyTurn{
		turnID:     "fresh",
		fromHandle: "task-orchestrator",
		toHandle:   "critic",
		since:      now,
		status:     "working",
		lastUpdate: now,
	}

	next, _ := model.Update(animTickMsg(now))
	updated := next.(dashboardModel)
	stale, ok := updated.busyTurns["stale"]
	if !ok {
		t.Fatal("expected stale busy turn to remain visible")
	}
	if stale.status != "stalled" {
		t.Fatalf("expected stale busy turn to be marked stalled, got %q", stale.status)
	}
	if _, ok := updated.busyTurns["fresh"]; !ok {
		t.Fatal("expected fresh busy turn to remain")
	}
}

func TestStalledBusyTurnIsNotRenderedAsActive(t *testing.T) {
	model := newDashboardModel(&fakeDashboardClient{})
	model.busyTurns["stalled"] = busyTurn{
		turnID:     "stalled",
		fromHandle: "task-orchestrator",
		toHandle:   "critic",
		status:     "stalled",
	}

	local := topoNode{handles: []string{"task-orchestrator"}}
	remote := topoNode{handles: []string{"critic"}}
	if dir := model.flashDirBetween(local, remote); dir != flashNone {
		t.Fatalf("expected stalled turn to produce no active flow, got %v", dir)
	}
	if active := model.activeHandlesFor(remote); len(active) != 0 {
		t.Fatalf("expected stalled turn not to mark active handles, got %v", active)
	}
}

func TestWorkingBusyTurnDoesNotRenderNodeAsActive(t *testing.T) {
	model := newDashboardModel(&fakeDashboardClient{})
	model.busyTurns["working"] = busyTurn{
		turnID:     "working",
		fromHandle: "task-orchestrator",
		toHandle:   "critic",
		status:     "working",
	}

	local := topoNode{handles: []string{"task-orchestrator"}}
	remote := topoNode{handles: []string{"critic"}}
	if dir := model.flashDirBetween(local, remote); dir != flashNone {
		t.Fatalf("expected working busy turn to produce no edge flash, got %v", dir)
	}
	if model.isNodeActive(remote) {
		t.Fatal("expected working busy turn not to mark node active")
	}
	if active := model.activeHandlesFor(remote); len(active) != 0 {
		t.Fatalf("expected working busy turn not to mark active handles, got %v", active)
	}
}

func TestTurnLifecycleRoomMessageDoesNotBroadcastFlashes(t *testing.T) {
	model := newDashboardModel(&fakeDashboardClient{})

	next, _ := model.Update(activityMsg(&agentpb.ActivityEvent{
		Event: &agentpb.ActivityEvent_RoomMessagePosted{
			RoomMessagePosted: &agentpb.RoomMessagePostedEvent{
				RoomId:        "room-1",
				FromHandle:    "nova",
				MemberHandles: []string{"you", "atlas", "nova"},
				TargetHandle:  "nova",
				TurnId:        "turn-1",
				ContentKind:   "turn_request",
				Round:         1,
			},
		},
	}))
	updated := next.(dashboardModel)

	novaNode := topoNode{handles: []string{"nova"}}
	atlasNode := topoNode{handles: []string{"atlas"}}
	controlNode := topoNode{handles: []string{"you"}}

	if dir := updated.flashDirBetween(controlNode, novaNode); dir != flashNone {
		t.Fatalf("expected turn lifecycle message not to create edge flash, got %v", dir)
	}
	if updated.isNodeActive(atlasNode) {
		t.Fatal("expected unrelated room member not to be marked active by turn lifecycle message")
	}
	if active := updated.activeHandlesFor(atlasNode); len(active) != 0 {
		t.Fatalf("expected no active atlas handles, got %v", active)
	}
	if state := updated.nodeState(novaNode); state != handleTurnWorking {
		t.Fatalf("nova nodeState = %q, want %q", state, handleTurnWorking)
	}
}

func TestChatRoomMessageStillBroadcastsFlashes(t *testing.T) {
	model := newDashboardModel(&fakeDashboardClient{})

	next, _ := model.Update(activityMsg(&agentpb.ActivityEvent{
		Event: &agentpb.ActivityEvent_RoomMessagePosted{
			RoomMessagePosted: &agentpb.RoomMessagePostedEvent{
				RoomId:        "room-1",
				FromHandle:    "nova",
				MemberHandles: []string{"you", "atlas", "nova"},
				ContentKind:   "chat",
			},
		},
	}))
	updated := next.(dashboardModel)

	atlasNode := topoNode{handles: []string{"atlas"}}
	controlNode := topoNode{handles: []string{"you"}}
	novaNode := topoNode{handles: []string{"nova"}}

	if !updated.isNodeActive(atlasNode) {
		t.Fatal("expected chat message to mark atlas node active")
	}
	if dir := updated.flashDirBetween(controlNode, novaNode); dir != flashBtoA {
		t.Fatalf("expected chat message from nova to you to flash remote->local, got %v", dir)
	}
}

func TestNodeStateUsesBusyTurnAndReplyStatus(t *testing.T) {
	model := newDashboardModel(&fakeDashboardClient{})
	node := topoNode{handles: []string{"implementer"}}

	if state := model.nodeState(node); state != handleTurnIdle {
		t.Fatalf("idle nodeState = %q, want %q", state, handleTurnIdle)
	}

	model.handleLastStatus["implementer"] = handleTurnError
	if state := model.nodeState(node); state != handleTurnError {
		t.Fatalf("error nodeState = %q, want %q", state, handleTurnError)
	}

	model.busyTurns["turn-1"] = busyTurn{
		turnID:   "turn-1",
		toHandle: "implementer",
		status:   "stalled",
	}
	if state := model.nodeState(node); state != handleTurnStalled {
		t.Fatalf("stalled nodeState = %q, want %q", state, handleTurnStalled)
	}

	model.busyTurns["turn-1"] = busyTurn{
		turnID:   "turn-1",
		toHandle: "implementer",
		status:   "working",
	}
	if state := model.nodeState(node); state != handleTurnWorking {
		t.Fatalf("working nodeState = %q, want %q", state, handleTurnWorking)
	}
}

func TestUpdateHandleTurnStateTracksFailuresAndClearsOnSuccess(t *testing.T) {
	model := newDashboardModel(&fakeDashboardClient{})

	model.updateHandleTurnState(&agentpb.RoomMessagePostedEvent{
		TargetHandle: "implementer",
		ContentKind:  "turn_timeout",
	})
	if got := model.handleLastStatus["implementer"]; got != handleTurnError {
		t.Fatalf("timeout state = %q, want %q", got, handleTurnError)
	}

	model.updateHandleTurnState(&agentpb.RoomMessagePostedEvent{
		FromHandle:   "implementer",
		ContentKind:  "implement_reply",
		Status:       "ok",
		TargetHandle: "implementer",
	})
	if _, ok := model.handleLastStatus["implementer"]; ok {
		t.Fatal("expected successful reply to clear error state")
	}

	model.updateHandleTurnState(&agentpb.RoomMessagePostedEvent{
		FromHandle:   "implementer",
		ContentKind:  "implement_reply",
		Status:       "error",
		TargetHandle: "implementer",
	})
	if got := model.handleLastStatus["implementer"]; got != handleTurnError {
		t.Fatalf("reply error state = %q, want %q", got, handleTurnError)
	}

	model.updateHandleTurnState(&agentpb.RoomMessagePostedEvent{
		TargetHandle: "implementer",
		ContentKind:  "turn_request",
	})
	if _, ok := model.handleLastStatus["implementer"]; ok {
		t.Fatal("expected new turn request to clear prior error state")
	}
}

func TestShouldAnimateNodeBorderForWorkingTurn(t *testing.T) {
	model := newDashboardModel(&fakeDashboardClient{})
	node := topoNode{id: "implement-node", handles: []string{"implementer"}}

	model.busyTurns["turn-1"] = busyTurn{
		turnID:   "turn-1",
		toHandle: "implementer",
		status:   "working",
	}

	if model.isNodeActive(node) {
		t.Fatal("expected working busy turn not to mark node active for traffic semantics")
	}
	if !model.shouldAnimateNodeBorder(node) {
		t.Fatal("expected working busy turn to animate node border")
	}

	model.busyTurns["turn-1"] = busyTurn{
		turnID:   "turn-1",
		toHandle: "implementer",
		status:   "stalled",
	}
	if model.shouldAnimateNodeBorder(node) {
		t.Fatal("expected stalled turn not to animate node border")
	}
}

func TestRenderNodeBoxActiveBorderAnimates(t *testing.T) {
	node := topoNode{id: "implement-node", handles: []string{"implementer"}}
	boxA := renderNodeBox(node, false, handleTurnWorking, true, map[string]bool{"implementer": true}, 0)
	boxB := renderNodeBox(node, false, handleTurnWorking, true, map[string]bool{"implementer": true}, 6)
	if boxA == boxB {
		t.Fatal("expected active node border rendering to vary across animation frames")
	}
	if !strings.Contains(boxA, "implement-node") || !strings.Contains(boxB, "implement-node") {
		t.Fatal("expected node name to remain present in animated render")
	}
}
