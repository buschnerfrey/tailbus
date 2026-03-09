package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("62")).
			Padding(0, 1)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("220"))

	statusBarStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243"))

	sectionStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("62")).
			Padding(0, 1)

	keyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("62")).
			Bold(true)

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243"))

	connectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("42"))

	openStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("220"))

	resolvedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243"))

	actMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("81"))

	actSessStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("220"))

	actRegStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("42"))

	actRoomStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("213"))

	// Topology styles
	localNodeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("42")).
			Bold(true)

	remoteNodeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("81"))

	relayNodeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("220"))

	disconnectedNodeStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("243"))

	directEdgeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("42"))

	relayEdgeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("220"))

	dropStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196"))

	queueWarnStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("220"))

	stalledTurnStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("214")).
				Bold(true)

	workingTurnDotStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("42")).
				Bold(true)

	stalledTurnDotStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("220")).
				Bold(true)

	errorTurnDotStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("196")).
				Bold(true)

	idleTurnDotStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("15"))

	flashEdgeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("51")).
			Bold(true)

	flashNodeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("51")).
			Bold(true)

	flowStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("51"))
)

// Top panel view mode
type topViewMode int

const (
	topViewTopology topViewMode = iota
	topViewDetail
)

// Messages
type statusMsg *agentpb.GetNodeStatusResponse
type activityMsg *agentpb.ActivityEvent
type tickMsg time.Time
type animTickMsg time.Time
type statusErrMsg struct{ error }
type activityErrMsg struct{ error }
type watchRetryMsg struct{}

const watchRetryDelay = 1500 * time.Millisecond
const busyTurnStaleAfter = 30 * time.Second

// edgeFlash represents a recent message flow between two handles.
type edgeFlash struct {
	fromHandle string
	toHandle   string
	at         time.Time
}

const flashDuration = 2 * time.Second

type busyTurn struct {
	turnID     string
	roomID     string
	fromHandle string
	toHandle   string
	round      uint32
	since      time.Time
	status     string
	lastUpdate time.Time
}

type handleTurnState string

const (
	handleTurnIdle    handleTurnState = "idle"
	handleTurnWorking handleTurnState = "working"
	handleTurnStalled handleTurnState = "stalled"
	handleTurnError   handleTurnState = "error"
)

// topoNode represents a node in the topology view.
type topoNode struct {
	id           string
	handles      []string
	addr         string
	connected    bool
	connectivity string // "direct", "relay", or "offline"
	isLocal      bool
	isRelay      bool
}

// Model
type dashboardClient interface {
	GetNodeStatus(ctx context.Context, in *agentpb.GetNodeStatusRequest, opts ...grpc.CallOption) (*agentpb.GetNodeStatusResponse, error)
	WatchActivity(ctx context.Context, in *agentpb.WatchActivityRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[agentpb.ActivityEvent], error)
}

type dashboardCloser interface {
	Close() error
}

type dashboardClientDialer func() (dashboardClient, dashboardCloser, error)

type reconnectingDashboardClient struct {
	dial dashboardClientDialer
}

type reconnectingActivityStream struct {
	stream grpc.ServerStreamingClient[agentpb.ActivityEvent]
	conn   dashboardCloser
	once   sync.Once
}

type dashboardModel struct {
	client           dashboardClient
	status           *agentpb.GetNodeStatusResponse
	activity         []activityEntry
	flashes          []edgeFlash
	busyTurns        map[string]busyTurn
	handleLastStatus map[string]handleTurnState
	animFrame        int
	width            int
	height           int
	err              error
	quitting         bool
	topView          topViewMode
}

type activityEntry struct {
	time  time.Time
	label string
	style lipgloss.Style
}

const maxActivity = 50

func newDashboardModel(client dashboardClient) dashboardModel {
	return dashboardModel{
		client:           client,
		busyTurns:        make(map[string]busyTurn),
		handleLastStatus: make(map[string]handleTurnState),
	}
}

func newReconnectingDashboardClient(socketPath, token string) dashboardClient {
	return &reconnectingDashboardClient{
		dial: newGRPCDashboardDialer(socketPath, token),
	}
}

func newGRPCDashboardDialer(socketPath, token string) dashboardClientDialer {
	return func() (dashboardClient, dashboardCloser, error) {
		dialOpts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
		if token != "" {
			dialOpts = append(dialOpts, grpc.WithPerRPCCredentials(tokenCreds{token: token}))
		}
		conn, err := grpc.NewClient("unix://"+socketPath, dialOpts...)
		if err != nil {
			return nil, nil, err
		}
		return agentpb.NewAgentAPIClient(conn), conn, nil
	}
}

func (c *reconnectingDashboardClient) GetNodeStatus(ctx context.Context, in *agentpb.GetNodeStatusRequest, opts ...grpc.CallOption) (*agentpb.GetNodeStatusResponse, error) {
	client, conn, err := c.dial()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return client.GetNodeStatus(ctx, in, opts...)
}

func (c *reconnectingDashboardClient) WatchActivity(ctx context.Context, in *agentpb.WatchActivityRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[agentpb.ActivityEvent], error) {
	client, conn, err := c.dial()
	if err != nil {
		return nil, err
	}
	stream, err := client.WatchActivity(ctx, in, opts...)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &reconnectingActivityStream{stream: stream, conn: conn}, nil
}

func (s *reconnectingActivityStream) Header() (metadata.MD, error) { return s.stream.Header() }
func (s *reconnectingActivityStream) Trailer() metadata.MD         { return s.stream.Trailer() }
func (s *reconnectingActivityStream) Context() context.Context     { return s.stream.Context() }

func (s *reconnectingActivityStream) CloseSend() error {
	err := s.stream.CloseSend()
	s.closeConn()
	return err
}

func (s *reconnectingActivityStream) SendMsg(m any) error {
	err := s.stream.SendMsg(m)
	if err != nil {
		s.closeConn()
	}
	return err
}

func (s *reconnectingActivityStream) RecvMsg(m any) error {
	err := s.stream.RecvMsg(m)
	if err != nil {
		s.closeConn()
	}
	return err
}

func (s *reconnectingActivityStream) Recv() (*agentpb.ActivityEvent, error) {
	event, err := s.stream.Recv()
	if err != nil {
		s.closeConn()
	}
	return event, err
}

func (s *reconnectingActivityStream) closeConn() {
	s.once.Do(func() {
		if s.conn != nil {
			_ = s.conn.Close()
		}
	})
}

func (m dashboardModel) Init() tea.Cmd {
	return tea.Batch(
		m.fetchStatus,
		m.watchActivity,
		tickCmd(),
		animTickCmd(),
	)
}

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func animTickCmd() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(t time.Time) tea.Msg {
		return animTickMsg(t)
	})
}

func (m dashboardModel) fetchStatus() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := m.client.GetNodeStatus(ctx, &agentpb.GetNodeStatusRequest{})
	if err != nil {
		return statusErrMsg{err}
	}
	return statusMsg(resp)
}

func (m dashboardModel) watchActivity() tea.Msg {
	stream, err := m.client.WatchActivity(context.Background(), &agentpb.WatchActivityRequest{})
	if err != nil {
		return activityErrMsg{err}
	}
	defer stream.CloseSend()
	event, err := stream.Recv()
	if err != nil {
		return activityErrMsg{err}
	}
	return activityMsg(event)
}

func (m dashboardModel) watchNext() tea.Msg {
	return m.watchActivity()
}

func watchRetryCmd() tea.Cmd {
	return tea.Tick(watchRetryDelay, func(time.Time) tea.Msg {
		return watchRetryMsg{}
	})
}

func shouldClearStatus(err error) bool {
	if err == nil {
		return false
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.Canceled, codes.DeadlineExceeded:
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "connection refused") ||
		strings.Contains(text, "no such file") ||
		strings.Contains(text, "transport is closing") ||
		strings.Contains(text, "error reading from server: eof") ||
		strings.Contains(text, "closed network connection")
}

func (m dashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "r":
			return m, m.fetchStatus
		case "c":
			m.activity = nil
			return m, nil
		case "tab":
			if m.topView == topViewTopology {
				m.topView = topViewDetail
			} else {
				m.topView = topViewTopology
			}
			return m, nil
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case statusMsg:
		m.status = (*agentpb.GetNodeStatusResponse)(msg)
		m.err = nil
		return m, nil

	case statusErrMsg:
		m.err = msg.error
		if shouldClearStatus(msg.error) {
			m.status = nil
		}
		return m, nil

	case activityMsg:
		event := (*agentpb.ActivityEvent)(msg)
		if shouldDisplayActivity(event) {
			entry := formatActivity(event)
			m.activity = append(m.activity, entry)
			if len(m.activity) > maxActivity {
				m.activity = m.activity[len(m.activity)-maxActivity:]
			}
		}
		// Record edge flashes for flow animation
		now := time.Now()
		switch e := event.Event.(type) {
		case *agentpb.ActivityEvent_MessageRouted:
			m.flashes = append(m.flashes, edgeFlash{
				fromHandle: e.MessageRouted.FromHandle,
				toHandle:   e.MessageRouted.ToHandle,
				at:         now,
			})
		case *agentpb.ActivityEvent_SessionOpened:
			m.flashes = append(m.flashes, edgeFlash{
				fromHandle: e.SessionOpened.FromHandle,
				toHandle:   e.SessionOpened.ToHandle,
				at:         now,
			})
		case *agentpb.ActivityEvent_RoomMessagePosted:
			if e.RoomMessagePosted == nil {
				return m, m.watchNext
			}
			m.updateBusyTurns(e.RoomMessagePosted, now)
			m.updateHandleTurnState(e.RoomMessagePosted)
			for _, member := range e.RoomMessagePosted.MemberHandles {
				if member == "" || member == e.RoomMessagePosted.FromHandle {
					continue
				}
				m.flashes = append(m.flashes, edgeFlash{
					fromHandle: e.RoomMessagePosted.FromHandle,
					toHandle:   member,
					at:         now,
				})
			}
		}
		return m, m.watchNext

	case activityErrMsg:
		m.err = msg.error
		return m, watchRetryCmd()

	case watchRetryMsg:
		return m, m.watchNext

	case animTickMsg:
		m.animFrame = (m.animFrame + 1) % 120
		// Expire old flashes
		now := time.Now()
		active := m.flashes[:0]
		for _, f := range m.flashes {
			if now.Sub(f.at) < flashDuration {
				active = append(active, f)
			}
		}
		m.flashes = active
		m.pruneBusyTurns(now)
		return m, animTickCmd()

	case tickMsg:
		return m, tea.Batch(m.fetchStatus, tickCmd())

	}

	return m, nil
}

func formatActivity(event *agentpb.ActivityEvent) activityEntry {
	ts := time.Now()
	if event.Timestamp != nil {
		ts = event.Timestamp.AsTime()
	}

	switch e := event.Event.(type) {
	case *agentpb.ActivityEvent_MessageRouted:
		dest := "[LOCAL]"
		if e.MessageRouted.Remote {
			dest = "[REMOTE -->]"
		}
		traceTag := ""
		if e.MessageRouted.TraceId != "" {
			tid := e.MessageRouted.TraceId
			if len(tid) > 8 {
				tid = tid[:8]
			}
			traceTag = fmt.Sprintf(" t:%s", tid)
		}
		return activityEntry{
			time:  ts,
			label: fmt.Sprintf("MSG %s -> %s %s%s", e.MessageRouted.FromHandle, e.MessageRouted.ToHandle, dest, traceTag),
			style: actMsgStyle,
		}
	case *agentpb.ActivityEvent_SessionOpened:
		return activityEntry{
			time:  ts,
			label: fmt.Sprintf("OPEN %s -> %s (%s)", e.SessionOpened.FromHandle, e.SessionOpened.ToHandle, shortID(e.SessionOpened.SessionId)),
			style: actSessStyle,
		}
	case *agentpb.ActivityEvent_SessionResolved:
		return activityEntry{
			time:  ts,
			label: fmt.Sprintf("RESOLVE %s (%s)", e.SessionResolved.FromHandle, shortID(e.SessionResolved.SessionId)),
			style: actSessStyle,
		}
	case *agentpb.ActivityEvent_HandleRegistered:
		return activityEntry{
			time:  ts,
			label: fmt.Sprintf("REG %q", e.HandleRegistered.Handle),
			style: actRegStyle,
		}
	case *agentpb.ActivityEvent_RoomCreated:
		roomID := shortID(e.RoomCreated.RoomId)
		return activityEntry{
			time:  ts,
			label: fmt.Sprintf("ROOM+ %s by %s [%d members]", roomID, e.RoomCreated.CreatedBy, len(e.RoomCreated.MemberHandles)),
			style: actRoomStyle,
		}
	case *agentpb.ActivityEvent_RoomMessagePosted:
		roomID := shortID(e.RoomMessagePosted.RoomId)
		traceTag := ""
		if e.RoomMessagePosted.TraceId != "" {
			traceTag = fmt.Sprintf(" t:%s", shortID(e.RoomMessagePosted.TraceId))
		}
		switch e.RoomMessagePosted.ContentKind {
		case "investigation_started":
			label := fmt.Sprintf("START %s by %s%s", roomID, e.RoomMessagePosted.FromHandle, traceTag)
			return activityEntry{time: ts, label: label, style: actRoomStyle}
		case "specialist_discovered":
			target := e.RoomMessagePosted.TargetHandle
			if target == "" {
				target = "?"
			}
			label := fmt.Sprintf("DISCOVER %s -> %s%s", roomID, target, traceTag)
			return activityEntry{time: ts, label: label, style: actRoomStyle}
		case "turn_request":
			label := fmt.Sprintf("TURN %s r%d %s -> %s%s", roomID, e.RoomMessagePosted.Round, e.RoomMessagePosted.FromHandle, e.RoomMessagePosted.TargetHandle, traceTag)
			return activityEntry{time: ts, label: label, style: actRoomStyle}
		case "turn_progress":
			target := e.RoomMessagePosted.TargetHandle
			if target == "" {
				target = e.RoomMessagePosted.FromHandle
			}
			label := fmt.Sprintf("PING %s r%d %s [%s]%s", roomID, e.RoomMessagePosted.Round, target, "working", traceTag)
			return activityEntry{time: ts, label: label, style: actRoomStyle}
		case "solver_reply":
			status := e.RoomMessagePosted.Status
			if status == "" {
				status = "ok"
			}
			label := fmt.Sprintf("REPLY %s r%d %s [%s]%s", roomID, e.RoomMessagePosted.Round, e.RoomMessagePosted.FromHandle, status, traceTag)
			return activityEntry{time: ts, label: label, style: actRoomStyle}
		case "turn_timeout":
			label := fmt.Sprintf("TIMEOUT %s r%d %s%s", roomID, e.RoomMessagePosted.Round, e.RoomMessagePosted.TargetHandle, traceTag)
			return activityEntry{time: ts, label: label, style: actRoomStyle}
		case "problem_opened":
			label := fmt.Sprintf("PROBLEM %s by %s%s", roomID, e.RoomMessagePosted.FromHandle, traceTag)
			return activityEntry{time: ts, label: label, style: actRoomStyle}
		case "final_summary":
			label := fmt.Sprintf("FINAL %s by %s%s", roomID, e.RoomMessagePosted.FromHandle, traceTag)
			return activityEntry{time: ts, label: label, style: actRoomStyle}
		}
		if isTurnReplyKind(e.RoomMessagePosted.ContentKind) {
			status := e.RoomMessagePosted.Status
			if status == "" {
				status = "ok"
			}
			label := fmt.Sprintf("REPLY %s r%d %s [%s]%s", roomID, e.RoomMessagePosted.Round, e.RoomMessagePosted.FromHandle, status, traceTag)
			return activityEntry{time: ts, label: label, style: actRoomStyle}
		}
		recipients := len(e.RoomMessagePosted.MemberHandles) - 1
		if recipients < 0 {
			recipients = 0
		}
		return activityEntry{
			time:  ts,
			label: fmt.Sprintf("ROOM %s #%d %s -> %d peers%s", roomID, e.RoomMessagePosted.RoomSeq, e.RoomMessagePosted.FromHandle, recipients, traceTag),
			style: actRoomStyle,
		}
	case *agentpb.ActivityEvent_RoomMemberJoined:
		return activityEntry{
			time:  ts,
			label: fmt.Sprintf("JOIN %s -> room %s", e.RoomMemberJoined.Handle, shortID(e.RoomMemberJoined.RoomId)),
			style: actRoomStyle,
		}
	case *agentpb.ActivityEvent_RoomMemberLeft:
		return activityEntry{
			time:  ts,
			label: fmt.Sprintf("LEAVE %s <- room %s", e.RoomMemberLeft.Handle, shortID(e.RoomMemberLeft.RoomId)),
			style: actRoomStyle,
		}
	case *agentpb.ActivityEvent_RoomClosed:
		return activityEntry{
			time:  ts,
			label: fmt.Sprintf("ROOM- %s by %s", shortID(e.RoomClosed.RoomId), e.RoomClosed.ClosedBy),
			style: actRoomStyle,
		}
	default:
		return activityEntry{time: ts, label: "???", style: helpStyle}
	}
}

func shouldDisplayActivity(event *agentpb.ActivityEvent) bool {
	if event == nil {
		return false
	}
	switch e := event.Event.(type) {
	case *agentpb.ActivityEvent_RoomMessagePosted:
		return e.RoomMessagePosted != nil && e.RoomMessagePosted.ContentKind != "turn_progress"
	default:
		return true
	}
}

func isTurnReplyKind(kind string) bool {
	return strings.HasSuffix(kind, "_reply") || kind == "apply_result"
}

func (m *dashboardModel) updateBusyTurns(event *agentpb.RoomMessagePostedEvent, now time.Time) {
	if event == nil {
		return
	}
	turnID := event.TurnId
	if turnID == "" {
		return
	}
	switch event.ContentKind {
	case "turn_request":
		if event.TargetHandle == "" {
			return
		}
		m.busyTurns[turnID] = busyTurn{
			turnID:     turnID,
			roomID:     event.RoomId,
			fromHandle: event.FromHandle,
			toHandle:   event.TargetHandle,
			round:      event.Round,
			since:      now,
			status:     "working",
			lastUpdate: now,
		}
	case "turn_progress":
		target := event.TargetHandle
		if target == "" {
			target = event.FromHandle
		}
		if target == "" {
			return
		}
		bt := m.busyTurns[turnID]
		if bt.turnID == "" {
			bt = busyTurn{
				turnID:     turnID,
				roomID:     event.RoomId,
				fromHandle: event.FromHandle,
				toHandle:   target,
				round:      event.Round,
				since:      now,
			}
		}
		bt.status = "working"
		bt.lastUpdate = now
		m.busyTurns[turnID] = bt
	case "turn_timeout":
		delete(m.busyTurns, turnID)
	default:
		if isTurnReplyKind(event.ContentKind) {
			delete(m.busyTurns, turnID)
		}
	}
}

func (m *dashboardModel) updateHandleTurnState(event *agentpb.RoomMessagePostedEvent) {
	if event == nil {
		return
	}
	switch event.ContentKind {
	case "turn_request", "turn_progress":
		target := event.TargetHandle
		if target == "" {
			target = event.FromHandle
		}
		if target != "" {
			delete(m.handleLastStatus, target)
		}
	case "turn_timeout":
		target := event.TargetHandle
		if target == "" {
			target = event.FromHandle
		}
		if target != "" {
			m.handleLastStatus[target] = handleTurnError
		}
	default:
		if !isTurnReplyKind(event.ContentKind) {
			return
		}
		handle := event.FromHandle
		if handle == "" {
			handle = event.TargetHandle
		}
		if handle == "" {
			return
		}
		if event.Status == "" || event.Status == "ok" {
			delete(m.handleLastStatus, handle)
		} else {
			m.handleLastStatus[handle] = handleTurnError
		}
	}
}

func (m *dashboardModel) pruneBusyTurns(now time.Time) {
	for turnID, turn := range m.busyTurns {
		last := turn.lastUpdate
		if last.IsZero() {
			last = turn.since
		}
		if !last.IsZero() && now.Sub(last) > busyTurnStaleAfter && turn.status == "working" {
			turn.status = "stalled"
			m.busyTurns[turnID] = turn
		}
	}
}

func (m dashboardModel) handleState(handle string) handleTurnState {
	if handle == "" {
		return handleTurnIdle
	}
	state := handleTurnIdle
	for _, turn := range m.busyTurns {
		if turn.toHandle != handle {
			continue
		}
		switch turn.status {
		case "working":
			return handleTurnWorking
		case "stalled":
			state = handleTurnStalled
		}
	}
	if state == handleTurnStalled {
		return state
	}
	if m.handleLastStatus[handle] == handleTurnError {
		return handleTurnError
	}
	return handleTurnIdle
}

func (m dashboardModel) nodeState(n topoNode) handleTurnState {
	state := handleTurnIdle
	for _, handle := range n.handles {
		switch m.handleState(handle) {
		case handleTurnWorking:
			return handleTurnWorking
		case handleTurnStalled:
			state = handleTurnStalled
		case handleTurnError:
			if state != handleTurnStalled {
				state = handleTurnError
			}
		}
	}
	return state
}

func dotStyleForState(state handleTurnState) lipgloss.Style {
	switch state {
	case handleTurnWorking:
		return workingTurnDotStyle
	case handleTurnStalled:
		return stalledTurnDotStyle
	case handleTurnError:
		return errorTurnDotStyle
	default:
		return idleTurnDotStyle
	}
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// flashDirection represents the direction of message flow between two nodes.
type flashDirection int

const (
	flashNone    flashDirection = 0
	flashAtoB    flashDirection = 1 // local → remote
	flashBtoA    flashDirection = 2 // remote → local
	flashBothDir flashDirection = 3 // both directions
)

// flashDirBetween returns the direction(s) of active flashes between two nodes.
// a is the local node (left), b is the remote node (right).
func (m dashboardModel) flashDirBetween(a, b topoNode) flashDirection {
	aHandles := make(map[string]bool, len(a.handles))
	for _, h := range a.handles {
		aHandles[h] = true
	}
	bHandles := make(map[string]bool, len(b.handles))
	for _, h := range b.handles {
		bHandles[h] = true
	}
	dir := flashNone
	for _, f := range m.flashes {
		if aHandles[f.fromHandle] && bHandles[f.toHandle] {
			dir |= flashAtoB
		}
		if bHandles[f.fromHandle] && aHandles[f.toHandle] {
			dir |= flashBtoA
		}
	}
	return dir
}

// isNodeActive checks if a node is the destination of any active flash.
func (m dashboardModel) isNodeActive(n topoNode) bool {
	handles := make(map[string]bool, len(n.handles))
	for _, h := range n.handles {
		handles[h] = true
	}
	for _, f := range m.flashes {
		if handles[f.toHandle] {
			return true
		}
	}
	return false
}

// buildTopoNodes converts status into a list of topology nodes.
func (m dashboardModel) buildTopoNodes() []topoNode {
	if m.status == nil {
		return nil
	}

	// Local node
	var localHandles []string
	for _, h := range m.status.Handles {
		localHandles = append(localHandles, h.Name)
	}
	sort.Strings(localHandles)
	local := topoNode{
		id:        m.status.NodeId,
		handles:   localHandles,
		connected: true,
		isLocal:   true,
	}

	var peers []topoNode

	// Peer nodes (skip self — already shown as local node)
	for _, p := range m.status.Peers {
		if p.NodeId == m.status.NodeId {
			continue
		}
		handles := make([]string, len(p.Handles))
		copy(handles, p.Handles)
		sort.Strings(handles)
		connectivity := p.Connectivity
		if connectivity == "" {
			// Backward compat: old daemons without connectivity field
			if p.Connected {
				connectivity = "direct"
			} else {
				connectivity = "offline"
			}
		}
		peers = append(peers, topoNode{
			id:           p.NodeId,
			handles:      handles,
			addr:         p.AdvertiseAddr,
			connected:    p.Connected,
			connectivity: connectivity,
		})
	}
	sort.Slice(peers, func(i, j int) bool {
		return peers[i].id < peers[j].id
	})

	// Relay nodes
	var relays []topoNode
	for _, r := range m.status.Relays {
		relays = append(relays, topoNode{
			id:        r.NodeId,
			addr:      r.Addr,
			connected: r.Connected,
			isRelay:   true,
		})
	}
	sort.Slice(relays, func(i, j int) bool {
		return relays[i].id < relays[j].id
	})

	nodes := []topoNode{local}
	nodes = append(nodes, peers...)
	nodes = append(nodes, relays...)
	return nodes
}

// detectFlowChains finds chains of open sessions (A→B, B→C => A→B→C).
func (m dashboardModel) detectFlowChains() [][]string {
	if m.status == nil {
		return nil
	}

	// Collect open sessions as directed edges
	type edge struct{ from, to string }
	var edges []edge
	outgoing := make(map[string][]string) // from -> [to, ...]
	hasIncoming := make(map[string]bool)

	for _, s := range m.status.Sessions {
		if s.State != "open" {
			continue
		}
		edges = append(edges, edge{s.FromHandle, s.ToHandle})
		outgoing[s.FromHandle] = append(outgoing[s.FromHandle], s.ToHandle)
		hasIncoming[s.ToHandle] = true
	}

	if len(edges) == 0 {
		return nil
	}

	// Find chain roots (nodes with no incoming edges)
	roots := make(map[string]bool)
	for _, e := range edges {
		if !hasIncoming[e.from] {
			roots[e.from] = true
		}
	}

	// Build chains from each root via DFS
	var chains [][]string
	visited := make(map[string]bool)
	var sortedRoots []string
	for root := range roots {
		sortedRoots = append(sortedRoots, root)
	}
	sort.Strings(sortedRoots)
	for _, root := range sortedRoots {
		chain := []string{root}
		current := root
		visited[current] = true
		for {
			nexts := outgoing[current]
			if len(nexts) == 0 {
				break
			}
			next := nexts[0] // follow first path
			if visited[next] {
				break
			}
			visited[next] = true
			chain = append(chain, next)
			current = next
		}
		if len(chain) > 1 {
			chains = append(chains, chain)
		}
	}

	return chains
}

// renderFlowSummary renders the FLOW: line showing session chains.
func (m dashboardModel) renderFlowSummary() string {
	chains := m.detectFlowChains()
	if len(chains) == 0 {
		return ""
	}

	var parts []string
	for _, chain := range chains {
		var formatted []string
		for _, h := range chain {
			formatted = append(formatted, "@"+h)
		}
		parts = append(parts, strings.Join(formatted, " ──► "))
	}

	return flowStyle.Render("FLOW: "+strings.Join(parts, "  |  ")) + "\n"
}

// renderTopology dispatches to compact or graph mode based on width and node count.
func (m dashboardModel) renderTopology(width, height int) string {
	nodes := m.buildTopoNodes()
	if len(nodes) == 0 {
		return headerStyle.Render("TOPOLOGY") + "\n" + helpStyle.Render("  (no data)")
	}

	// Show flow chain summary if any
	flowLine := m.renderFlowSummary()

	// Compact mode: narrow terminal or many nodes
	if width < 100 || len(nodes) > 6 {
		return flowLine + m.renderTopologyCompact(nodes, width)
	}
	return flowLine + m.renderTopologyGraph(nodes, width, height)
}

// renderTopologyCompact renders a simple list view of the topology.
func (m dashboardModel) renderTopologyCompact(nodes []topoNode, width int) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("TOPOLOGY") + "\n")

	for _, n := range nodes {
		var icon, label, suffix string
		activeH := m.activeHandlesFor(n)
		nodeState := m.nodeState(n)
		dotStyle := dotStyleForState(nodeState)

		if n.isRelay {
			if n.connected {
				icon = relayNodeStyle.Render("\u25c7")
			} else {
				icon = disconnectedNodeStyle.Render("\u25c7")
			}
			status := "[disconnected]"
			if n.connected {
				status = "[connected]"
			}
			label = relayNodeStyle.Render(n.id)
			suffix = " " + helpStyle.Render(status)
		} else if n.isLocal {
			icon = dotStyle.Render("\u25cf")
			label = localNodeStyle.Render(n.id) + helpStyle.Render(" (this node)")
			if len(n.handles) > 0 {
				suffix = ": " + renderHandleList(n.handles, activeH, m.animFrame)
			}
		} else {
			switch n.connectivity {
			case "direct":
				icon = dotStyle.Render("\u25cf")
				label = remoteNodeStyle.Render(n.id)
				suffix = " " + helpStyle.Render("[direct]")
			case "relay":
				icon = relayNodeStyle.Render("\u25cf")
				label = remoteNodeStyle.Render(n.id)
				suffix = " " + relayNodeStyle.Render("[relay]")
			default:
				icon = disconnectedNodeStyle.Render("\u25cb")
				label = disconnectedNodeStyle.Render(n.id)
				suffix = " " + helpStyle.Render("[offline]")
			}
			if len(n.handles) > 0 {
				suffix += ": " + renderHandleList(n.handles, activeH, m.animFrame)
			}
		}

		prefix := "  "
		if m.isNodeActive(n) {
			prefix = flashEdgeStyle.Render("► ")
		}
		line := fmt.Sprintf("%s%s %s%s", prefix, icon, label, suffix)
		if len(line) > width-2 {
			line = line[:width-2]
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderTopologyGraph renders an ASCII box graph of the topology.
func (m dashboardModel) renderTopologyGraph(nodes []topoNode, width, height int) string {
	if len(nodes) == 0 {
		return headerStyle.Render("TOPOLOGY") + "\n" + helpStyle.Render("  (no data)")
	}

	var b strings.Builder
	viewLabel := "[Tab: detail view]"
	headerText := headerStyle.Render("TOPOLOGY")
	pad := width - lipgloss.Width(headerText) - len(viewLabel) - 2
	if pad < 1 {
		pad = 1
	}
	b.WriteString(headerText + strings.Repeat(" ", pad) + helpStyle.Render(viewLabel) + "\n")

	// Separate local node from others
	var local topoNode
	var remotes []topoNode
	for _, n := range nodes {
		if n.isLocal {
			local = n
		} else {
			remotes = append(remotes, n)
		}
	}

	// Build local node box
	localActive := m.isNodeActive(local)
	localActiveHandles := m.activeHandlesFor(local)
	localBox := renderNodeBox(local, true, m.nodeState(local), localActive, localActiveHandles, m.animFrame)

	if len(remotes) == 0 {
		b.WriteString(localBox)
		return strings.TrimRight(b.String(), "\n")
	}

	// Build remote boxes with edges
	localLines := strings.Split(localBox, "\n")
	maxLocalW := 0
	for _, l := range localLines {
		if w := lipgloss.Width(l); w > maxLocalW {
			maxLocalW = w
		}
	}

	// Calculate how many remotes fit vertically
	maxRemotes := height - 3 // leave room for header
	if maxRemotes < 1 {
		maxRemotes = 1
	}
	if len(remotes) > maxRemotes {
		remotes = remotes[:maxRemotes]
	}

	// Render each remote with edge from local
	for i, remote := range remotes {
		remoteActive := m.isNodeActive(remote)
		remoteActiveHandles := m.activeHandlesFor(remote)
		remoteBox := renderNodeBox(remote, false, m.nodeState(remote), remoteActive, remoteActiveHandles, m.animFrame)
		flashDir := m.flashDirBetween(local, remote)
		edge := renderEdge(remote, maxLocalW, flashDir, m.animFrame)

		if i > 0 {
			b.WriteString("\n")
		}

		// Join local box (only for first remote) with edge and remote box
		remoteLines := strings.Split(remoteBox, "\n")
		edgeLines := strings.Split(edge, "\n")

		if i == 0 {
			// Join local box + edge + remote box horizontally
			maxLines := len(localLines)
			if len(remoteLines) > maxLines {
				maxLines = len(remoteLines)
			}
			if len(edgeLines) > maxLines {
				maxLines = len(edgeLines)
			}

			for j := 0; j < maxLines; j++ {
				ll := ""
				if j < len(localLines) {
					ll = localLines[j]
				}
				// Pad local column
				llw := lipgloss.Width(ll)
				if llw < maxLocalW {
					ll += strings.Repeat(" ", maxLocalW-llw)
				}

				el := ""
				if j < len(edgeLines) {
					el = edgeLines[j]
				}

				rl := ""
				if j < len(remoteLines) {
					rl = remoteLines[j]
				}

				b.WriteString(ll + el + rl + "\n")
			}
		} else {
			// Subsequent remotes: pad left to align with edge column
			for j := 0; j < len(remoteLines) || j < len(edgeLines); j++ {
				padding := strings.Repeat(" ", maxLocalW)

				el := ""
				if j < len(edgeLines) {
					el = edgeLines[j]
				}

				rl := ""
				if j < len(remoteLines) {
					rl = remoteLines[j]
				}

				b.WriteString(padding + el + rl + "\n")
			}
		}
	}

	return strings.TrimRight(b.String(), "\n")
}

// renderHandleList renders a comma-separated handle list, highlighting active ones.
func renderHandleList(handles []string, active map[string]bool, animFrame int) string {
	parts := make([]string, len(handles))
	for i, h := range handles {
		if active[h] {
			parts[i] = shimmerText(h, animFrame, helpStyle, flashNodeStyle)
		} else {
			parts[i] = h
		}
	}
	return strings.Join(parts, ", ")
}

// activeHandlesFor returns the set of handles on a node that are destinations
// of recent message flashes.
func (m dashboardModel) activeHandlesFor(n topoNode) map[string]bool {
	result := make(map[string]bool)
	handles := make(map[string]bool, len(n.handles))
	for _, h := range n.handles {
		handles[h] = true
	}
	for _, f := range m.flashes {
		if handles[f.toHandle] {
			result[f.toHandle] = true
		}
	}
	return result
}

// shimmerText renders text with a bright highlight sweeping left-to-right.
// Characters within a 3-rune window at the current position get highlightStyle,
// others get baseStyle.
func shimmerText(text string, frame int, baseStyle, highlightStyle lipgloss.Style) string {
	runes := []rune(text)
	runeCount := len(runes)
	if runeCount == 0 {
		return ""
	}
	const shimmerWidth = 3
	pos := frame % (runeCount + shimmerWidth)
	var b strings.Builder
	for i, r := range runes {
		if i >= pos-shimmerWidth && i < pos {
			b.WriteString(highlightStyle.Render(string(r)))
		} else {
			b.WriteString(baseStyle.Render(string(r)))
		}
	}
	return b.String()
}

// renderNodeBox renders an ASCII box for a topology node.
func renderNodeBox(n topoNode, isLocal bool, state handleTurnState, active bool, activeHandles map[string]bool, animFrame int) string {
	// Determine box width
	nameLen := len(n.id)
	maxContent := nameLen + 4 // icon + space + name + padding
	for _, h := range n.handles {
		if l := len(h) + 2; l > maxContent {
			maxContent = l
		}
	}
	boxW := maxContent + 4
	if boxW < 14 {
		boxW = 14
	}
	if boxW > 20 {
		boxW = 20
	}
	innerW := boxW - 4 // 2 border + 2 padding

	// Choose border style based on active state
	borderStyle := lipgloss.NewStyle()
	if active {
		borderStyle = flashNodeStyle
	}

	var b strings.Builder

	// Top border
	topBorder := renderAnimatedHorizontalBorder(innerW+2, animFrame, active)
	b.WriteString("  " + topBorder + "\n")

	// Node name line with icon
	icon := "\u25cf" // ●
	iconStyle := dotStyleForState(state)
	var nameStyle lipgloss.Style
	if isLocal {
		nameStyle = localNodeStyle
	} else if n.isRelay {
		icon = "\u25c7" // ◇
		nameStyle = relayNodeStyle
	} else {
		switch n.connectivity {
		case "direct":
			nameStyle = remoteNodeStyle
		case "relay":
			nameStyle = relayNodeStyle
		default:
			icon = "\u25cb" // ○
			nameStyle = disconnectedNodeStyle
		}
	}

	name := n.id
	if len(name) > innerW-2 {
		name = name[:innerW-2]
	}
	var renderedName string
	if active {
		renderedName = shimmerText(name, animFrame, nameStyle, flashNodeStyle)
	} else {
		renderedName = nameStyle.Render(name)
	}
	nameLine := iconStyle.Render(icon) + " " + renderedName
	nameW := lipgloss.Width(iconStyle.Render(icon)+" ") + lipgloss.Width(nameStyle.Render(name))
	padR := innerW - nameW + 2
	if padR < 0 {
		padR = 0
	}
	leftBorder := borderStyle.Render("|")
	rightBorder := borderStyle.Render("|")
	if active {
		leftBorder = animatedBorderSide(animFrame, 0)
		rightBorder = animatedBorderSide(animFrame, 2)
	}
	b.WriteString("  " + leftBorder + " " + nameLine + strings.Repeat(" ", padR) + rightBorder + "\n")

	// Handle lines (up to 3)
	maxHandles := 3
	for i, h := range n.handles {
		if i >= maxHandles {
			remaining := len(n.handles) - maxHandles
			more := fmt.Sprintf("+%d more", remaining)
			moreW := len(more)
			padR := innerW - moreW
			if padR < 0 {
				padR = 0
			}
			leftBorder = borderStyle.Render("|")
			rightBorder = borderStyle.Render("|")
			if active {
				leftBorder = animatedBorderSide(animFrame, i+1)
				rightBorder = animatedBorderSide(animFrame, i+3)
			}
			b.WriteString("  " + leftBorder + " " + helpStyle.Render(more) + strings.Repeat(" ", padR) + rightBorder + "\n")
			break
		}
		hName := h
		if len(hName) > innerW {
			hName = hName[:innerW]
		}
		padR := innerW - len(hName)
		if padR < 0 {
			padR = 0
		}
		var renderedH string
		if activeHandles[h] {
			renderedH = shimmerText(hName, animFrame, helpStyle, flashNodeStyle)
		} else {
			renderedH = helpStyle.Render(hName)
		}
		leftBorder = borderStyle.Render("|")
		rightBorder = borderStyle.Render("|")
		if active {
			leftBorder = animatedBorderSide(animFrame, i+1)
			rightBorder = animatedBorderSide(animFrame, i+3)
		}
		b.WriteString("  " + leftBorder + " " + renderedH + strings.Repeat(" ", padR) + rightBorder + "\n")
	}

	// Bottom border
	bottomBorder := renderAnimatedHorizontalBorder(innerW+2, animFrame+4, active)
	b.WriteString("  " + bottomBorder)

	return b.String()
}

func animatedBorderSide(frame, offset int) string {
	if ((frame/3)+offset)%2 == 0 {
		return flashNodeStyle.Render("|")
	}
	return flashNodeStyle.Reverse(true).Render("|")
}

func renderAnimatedHorizontalBorder(width, frame int, active bool) string {
	border := []rune(strings.Repeat("-", width))
	if active && width > 0 {
		border[(frame/2)%width] = '='
		return flashNodeStyle.Render("+" + string(border) + "+")
	}
	return lipgloss.NewStyle().Render("+" + string(border) + "+")
}

// renderEdge renders the connection line between local and a remote node.
func renderEdge(remote topoNode, localWidth int, flashDir flashDirection, animFrame int) string {
	_ = localWidth
	edgeW := 14

	var edgeLine string
	if flashDir != flashNone {
		// Direction-aware animated arrows
		outPatterns := []string{
			"──►─►─►──",
			"───►─►─►─",
			"────►─►─►",
			"►────►─►─",
		}
		inPatterns := []string{
			"──◄─◄─◄──",
			"─◄─◄─◄───",
			"◄─◄─◄────",
			"─◄─◄────◄",
		}
		bothPatterns := []string{
			"◄─►──◄─►─",
			"─◄─►──◄─►",
			"►─◄─►──◄─",
			"─►─◄─►──◄",
		}
		var patterns []string
		switch flashDir {
		case flashBtoA:
			patterns = inPatterns
		case flashBothDir:
			patterns = bothPatterns
		default:
			patterns = outPatterns
		}
		edgeLine = flashEdgeStyle.Render(patterns[animFrame%len(patterns)])
	} else if remote.isRelay {
		if remote.connected {
			edgeLine = relayEdgeStyle.Render("~~~relay~~~")
		} else {
			edgeLine = disconnectedNodeStyle.Render("- -relay- -")
		}
	} else {
		switch remote.connectivity {
		case "direct":
			edgeLine = directEdgeStyle.Render("───direct───")
		case "relay":
			edgeLine = relayEdgeStyle.Render("~~~relay~~~~")
		default:
			edgeLine = disconnectedNodeStyle.Render("- - - - - -")
		}
	}

	lineW := lipgloss.Width(edgeLine)
	pad := edgeW - lineW
	if pad < 0 {
		pad = 0
	}
	paddedLine := strings.Repeat(" ", pad/2) + edgeLine + strings.Repeat(" ", (pad+1)/2)

	// Pad to same height as a box (roughly 5 lines), edge on line 2
	result := make([]string, 5)
	for i := range result {
		if i == 1 {
			result[i] = paddedLine
		} else {
			result[i] = strings.Repeat(" ", edgeW)
		}
	}
	return strings.Join(result, "\n")
}

// renderDetailView renders the full-width peer detail table (Tab alternate).
func (m dashboardModel) renderDetailView(width int) string {
	var b strings.Builder
	viewLabel := "[Tab: topology view]"
	headerText := headerStyle.Render("PEERS (DETAIL)")
	pad := width - lipgloss.Width(headerText) - len(viewLabel) - 2
	if pad < 1 {
		pad = 1
	}
	b.WriteString(headerText + strings.Repeat(" ", pad) + helpStyle.Render(viewLabel) + "\n")

	if m.status == nil || (len(m.status.Peers) == 0 && len(m.status.Relays) == 0) {
		b.WriteString(helpStyle.Render("  (no peers)"))
		return b.String()
	}

	peers := append([]*agentpb.PeerStatus(nil), m.status.Peers...)
	sort.Slice(peers, func(i, j int) bool {
		return peers[i].NodeId < peers[j].NodeId
	})
	for _, p := range peers {
		connectivity := p.Connectivity
		if connectivity == "" {
			if p.Connected {
				connectivity = "direct"
			} else {
				connectivity = "offline"
			}
		}
		var status string
		switch connectivity {
		case "direct":
			status = connectedStyle.Render("[direct]")
		case "relay":
			status = relayNodeStyle.Render("[relay]")
		default:
			status = disconnectedNodeStyle.Render("[offline]")
		}
		line := fmt.Sprintf("  %s  %s  %s", remoteNodeStyle.Render(p.NodeId), p.AdvertiseAddr, status)
		if len(line) > width-2 {
			line = line[:width-2]
		}
		b.WriteString(line + "\n")
		if len(p.Handles) > 0 {
			handles := "    handles: " + strings.Join(p.Handles, ", ")
			if len(handles) > width-2 {
				handles = handles[:width-2]
			}
			b.WriteString(helpStyle.Render(handles) + "\n")
		}
	}

	relays := append([]*agentpb.RelayStatus(nil), m.status.Relays...)
	sort.Slice(relays, func(i, j int) bool {
		return relays[i].NodeId < relays[j].NodeId
	})
	for _, r := range relays {
		status := disconnectedNodeStyle.Render("[disconnected]")
		if r.Connected {
			status = relayNodeStyle.Render("[connected]")
		}
		line := fmt.Sprintf("  %s %s  %s  %s", relayNodeStyle.Render("\u25c7"), relayNodeStyle.Render(r.NodeId), r.Addr, status)
		if len(line) > width-2 {
			line = line[:width-2]
		}
		b.WriteString(line + "\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

func (m dashboardModel) View() string {
	if m.quitting {
		return ""
	}

	if m.width == 0 {
		return "Loading..."
	}

	var b strings.Builder

	// Title bar
	title := titleStyle.Render(" tailbus dashboard ")
	statusInfo := ""
	if m.status != nil {
		uptime := time.Since(m.status.StartedAt.AsTime()).Truncate(time.Second)
		var totalMsgs, openSess, resolvedSess int64
		if m.status.Counters != nil {
			totalMsgs = m.status.Counters.MessagesRouted
		}
		for _, s := range m.status.Sessions {
			if s.State == "open" {
				openSess++
			} else {
				resolvedSess++
			}
		}
		statusInfo = statusBarStyle.Render(fmt.Sprintf(
			"  Node: %s  |  Uptime: %s  |  Msgs: %d  |  Sess: %d/%d",
			m.status.NodeId, uptime, totalMsgs, openSess, resolvedSess,
		))
	}
	b.WriteString(title + statusInfo + "\n")

	if m.err != nil {
		b.WriteString(fmt.Sprintf("\n  Error: %v\n", m.err))
	}

	totalWidth := m.width
	if totalWidth < 40 {
		totalWidth = 40
	}

	// Top panel: topology or detail view
	topHeight := m.height/2 - 4
	if topHeight < 5 {
		topHeight = 5
	}
	topW := totalWidth - 4

	var topContent string
	if m.topView == topViewTopology {
		topContent = m.renderTopology(topW, topHeight)
	} else {
		topContent = m.renderDetailView(topW)
	}
	b.WriteString(sectionStyle.Width(totalWidth-4).Render(topContent) + "\n")

	// Bottom row: handles+sessions (left) | activity (right)
	// Constrain bottom height so topology stays visible
	bottomHeight := m.height/2 - 2
	if bottomHeight < 6 {
		bottomHeight = 6
	}
	leftW := totalWidth/2 - 2
	rightW := totalWidth - leftW - 4

	hsContent := m.renderHandlesSessions(leftW, bottomHeight-2)
	actContent := m.renderActivity(rightW, bottomHeight-2)
	bottomRow := lipgloss.JoinHorizontal(lipgloss.Top,
		sectionStyle.Width(leftW).MaxHeight(bottomHeight).Render(hsContent),
		sectionStyle.Width(rightW).MaxHeight(bottomHeight).Render(actContent),
	)
	b.WriteString(bottomRow + "\n")

	// Help bar
	help := fmt.Sprintf("  %s quit  %s refresh  %s clear  %s switch view",
		keyStyle.Render("q"),
		keyStyle.Render("r"),
		keyStyle.Render("c"),
		keyStyle.Render("Tab"),
	)
	b.WriteString(helpStyle.Render(help) + "\n")

	return b.String()
}

// renderHandlesSessions renders both handles and sessions in one merged panel.
func (m dashboardModel) renderHandlesSessions(width, maxLines int) string {
	var b strings.Builder
	lines := 0

	// Build active handles set from flashes
	activeHandles := make(map[string]bool)
	for _, f := range m.flashes {
		activeHandles[f.toHandle] = true
	}

	// Handles
	b.WriteString(headerStyle.Render("HANDLES") + "\n")
	lines++
	if m.status == nil || len(m.status.Handles) == 0 {
		b.WriteString(helpStyle.Render("  (none)") + "\n")
		lines++
	} else {
		for _, h := range m.status.Handles {
			if lines >= maxLines-2 {
				break
			}
			hName := h.Name
			if activeHandles[hName] {
				hName = shimmerText(hName, m.animFrame, helpStyle, flashNodeStyle)
			}
			line := fmt.Sprintf("  %s (%d subs) \u2193%d \u2191%d",
				hName, h.SubscriberCount, h.MessagesIn, h.MessagesOut)
			if h.QueueDepth > 0 {
				qs := fmt.Sprintf(" q:%d", h.QueueDepth)
				if h.QueueDepth > 32 {
					line += queueWarnStyle.Render(qs)
				} else {
					line += qs
				}
			}
			if h.Drops > 0 {
				line += " " + dropStyle.Render(fmt.Sprintf("drop:%d", h.Drops))
			}
			if lipgloss.Width(line) > width-2 {
				line = line[:width-2]
			}
			b.WriteString(line + "\n")
			lines++
		}
	}

	// Rooms
	remaining := maxLines - lines
	b.WriteString(headerStyle.Render("ROOMS") + "\n")
	lines++
	remaining--
	if m.status == nil || len(m.status.Rooms) == 0 {
		b.WriteString(helpStyle.Render("  (none)") + "\n")
		lines++
	} else {
		shown := 0
		roomLimit := remaining
		if roomLimit > 6 {
			roomLimit = 6
		}
		for _, room := range m.status.Rooms {
			if shown >= roomLimit {
				break
			}
			title := room.Title
			if title == "" {
				title = "(untitled)"
			}
			stateStr := openStyle.Render("[open]")
			if room.Status != "open" {
				stateStr = resolvedStyle.Render("[" + room.Status + "]")
			}
			seq := int64(room.NextSeq) - 1
			if seq < 0 {
				seq = 0
			}
			line := fmt.Sprintf("  %s %s m:%d seq:%d %s", shortID(room.RoomId), title, len(room.Members), seq, stateStr)
			if lipgloss.Width(line) > width-2 {
				line = line[:width-2]
			}
			b.WriteString(line + "\n")
			lines++
			shown++
		}
	}

	// Work
	remaining = maxLines - lines
	b.WriteString(headerStyle.Render("WORK") + "\n")
	lines++
	remaining--
	if len(m.busyTurns) == 0 {
		b.WriteString(helpStyle.Render("  (idle)") + "\n")
		lines++
	} else {
		turns := make([]busyTurn, 0, len(m.busyTurns))
		for _, turn := range m.busyTurns {
			turns = append(turns, turn)
		}
		sort.Slice(turns, func(i, j int) bool {
			return turns[i].since.Before(turns[j].since)
		})
		shown := 0
		workLimit := remaining
		if workLimit > 4 {
			workLimit = 4
		}
		for _, turn := range turns {
			if shown >= workLimit {
				break
			}
			statusText := stalledTurnStyle.Render(turn.status)
			if turn.status == "working" {
				statusText = shimmerText("working", m.animFrame, helpStyle, flashNodeStyle)
			}
			line := fmt.Sprintf("  %s r%d %s (%s)", turn.toHandle, turn.round, statusText, time.Since(turn.since).Round(time.Second))
			if lipgloss.Width(line) > width-2 {
				line = line[:width-2]
			}
			b.WriteString(line + "\n")
			lines++
			shown++
		}
	}

	// Sessions
	remaining = maxLines - lines
	b.WriteString(headerStyle.Render("SESSIONS") + "\n")
	lines++
	remaining--
	if m.status == nil || len(m.status.Sessions) == 0 {
		b.WriteString(helpStyle.Render("  (none)"))
	} else {
		shown := 0
		for _, s := range m.status.Sessions {
			if shown >= remaining {
				break
			}
			idShort := s.SessionId
			if len(idShort) > 8 {
				idShort = idShort[:8]
			}
			stateStr := openStyle.Render("[open]")
			if s.State == "resolved" {
				stateStr = resolvedStyle.Render("[resolved]")
			}
			line := fmt.Sprintf("  %s %s -> %s %s", idShort, s.FromHandle, s.ToHandle, stateStr)
			if len(line) > width-2 {
				line = line[:width-2]
			}
			b.WriteString(line + "\n")
			shown++
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m dashboardModel) renderActivity(width, maxLines int) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("ACTIVITY") + "\n")

	if len(m.activity) == 0 {
		b.WriteString(helpStyle.Render("  (waiting for events...)"))
		return b.String()
	}

	// Show most recent events (bottom = newest), capped to available height
	showLines := maxLines - 1 // subtract header
	if showLines < 1 {
		showLines = 1
	}
	start := 0
	if len(m.activity) > showLines {
		start = len(m.activity) - showLines
	}

	for _, entry := range m.activity[start:] {
		ts := entry.time.Format("15:04:05")
		line := fmt.Sprintf("  %s %s", ts, entry.label)
		if len(line) > width-2 {
			line = line[:width-2]
		}
		b.WriteString(entry.style.Render(line) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func runDashboard(socketPath, token string) error {
	p := tea.NewProgram(
		newDashboardModel(newReconnectingDashboardClient(socketPath, token)),
		tea.WithAltScreen(),
	)
	_, err := p.Run()
	return err
}
