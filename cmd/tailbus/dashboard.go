package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
type errMsg struct{ error }

// topoNode represents a node in the topology view.
type topoNode struct {
	id        string
	handles   []string
	addr      string
	connected bool
	isLocal   bool
	isRelay   bool
}

// Model
type dashboardModel struct {
	client   agentpb.AgentAPIClient
	status   *agentpb.GetNodeStatusResponse
	activity []activityEntry
	width    int
	height   int
	err      error
	quitting bool
	topView  topViewMode
}

type activityEntry struct {
	time  time.Time
	label string
	style lipgloss.Style
}

const maxActivity = 50

func newDashboardModel(client agentpb.AgentAPIClient) dashboardModel {
	return dashboardModel{
		client: client,
	}
}

func (m dashboardModel) Init() tea.Cmd {
	return tea.Batch(
		m.fetchStatus,
		m.watchActivity,
		tickCmd(),
	)
}

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m dashboardModel) fetchStatus() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := m.client.GetNodeStatus(ctx, &agentpb.GetNodeStatusRequest{})
	if err != nil {
		return errMsg{err}
	}
	return statusMsg(resp)
}

func (m dashboardModel) watchActivity() tea.Msg {
	stream, err := m.client.WatchActivity(context.Background(), &agentpb.WatchActivityRequest{})
	if err != nil {
		return errMsg{err}
	}
	event, err := stream.Recv()
	if err != nil {
		return errMsg{err}
	}
	return activityMsg(event)
}

func (m dashboardModel) watchNext() tea.Msg {
	return m.watchActivity()
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

	case activityMsg:
		event := (*agentpb.ActivityEvent)(msg)
		entry := formatActivity(event)
		m.activity = append(m.activity, entry)
		if len(m.activity) > maxActivity {
			m.activity = m.activity[len(m.activity)-maxActivity:]
		}
		return m, m.watchNext

	case tickMsg:
		return m, tea.Batch(m.fetchStatus, tickCmd())

	case errMsg:
		m.err = msg.error
		return m, nil
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
			label: fmt.Sprintf("OPEN %s -> %s (%s)", e.SessionOpened.FromHandle, e.SessionOpened.ToHandle, e.SessionOpened.SessionId[:8]),
			style: actSessStyle,
		}
	case *agentpb.ActivityEvent_SessionResolved:
		return activityEntry{
			time:  ts,
			label: fmt.Sprintf("RESOLVE %s (%s)", e.SessionResolved.FromHandle, e.SessionResolved.SessionId[:8]),
			style: actSessStyle,
		}
	case *agentpb.ActivityEvent_HandleRegistered:
		return activityEntry{
			time:  ts,
			label: fmt.Sprintf("REG %q", e.HandleRegistered.Handle),
			style: actRegStyle,
		}
	default:
		return activityEntry{time: ts, label: "???", style: helpStyle}
	}
}

// buildTopoNodes converts status into a list of topology nodes.
func (m dashboardModel) buildTopoNodes() []topoNode {
	if m.status == nil {
		return nil
	}

	var nodes []topoNode

	// Local node
	var localHandles []string
	for _, h := range m.status.Handles {
		localHandles = append(localHandles, h.Name)
	}
	sort.Strings(localHandles)
	nodes = append(nodes, topoNode{
		id:        m.status.NodeId,
		handles:   localHandles,
		connected: true,
		isLocal:   true,
	})

	// Peer nodes
	for _, p := range m.status.Peers {
		handles := make([]string, len(p.Handles))
		copy(handles, p.Handles)
		sort.Strings(handles)
		nodes = append(nodes, topoNode{
			id:        p.NodeId,
			handles:   handles,
			addr:      p.AdvertiseAddr,
			connected: p.Connected,
		})
	}

	// Relay nodes
	for _, r := range m.status.Relays {
		nodes = append(nodes, topoNode{
			id:        r.NodeId,
			addr:      r.Addr,
			connected: r.Connected,
			isRelay:   true,
		})
	}

	return nodes
}

// renderTopology dispatches to compact or graph mode based on width and node count.
func (m dashboardModel) renderTopology(width, height int) string {
	nodes := m.buildTopoNodes()
	if len(nodes) == 0 {
		return headerStyle.Render("TOPOLOGY") + "\n" + helpStyle.Render("  (no data)")
	}

	// Compact mode: narrow terminal or many nodes
	if width < 100 || len(nodes) > 6 {
		return m.renderTopologyCompact(nodes, width)
	}
	return m.renderTopologyGraph(nodes, width, height)
}

// renderTopologyCompact renders a simple list view of the topology.
func (m dashboardModel) renderTopologyCompact(nodes []topoNode, width int) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("TOPOLOGY") + "\n")

	for _, n := range nodes {
		var icon, label, suffix string

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
			icon = localNodeStyle.Render("\u25cf")
			label = localNodeStyle.Render(n.id) + helpStyle.Render(" (this node)")
			if len(n.handles) > 0 {
				suffix = ": " + strings.Join(n.handles, ", ")
			}
		} else {
			if n.connected {
				icon = connectedStyle.Render("\u25cf")
				label = remoteNodeStyle.Render(n.id)
				suffix = " " + helpStyle.Render("[direct --]")
			} else {
				icon = disconnectedNodeStyle.Render("\u25cb")
				label = disconnectedNodeStyle.Render(n.id)
				suffix = " " + helpStyle.Render("[disconnected]")
			}
			if len(n.handles) > 0 {
				suffix += ": " + strings.Join(n.handles, ", ")
			}
		}

		line := fmt.Sprintf("  %s %s%s", icon, label, suffix)
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
	localBox := renderNodeBox(local, true)

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
		remoteBox := renderNodeBox(remote, false)
		edge := renderEdge(remote, maxLocalW)

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

// renderNodeBox renders an ASCII box for a topology node.
func renderNodeBox(n topoNode, isLocal bool) string {
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

	var b strings.Builder

	// Top border
	b.WriteString("  +" + strings.Repeat("-", innerW+2) + "+\n")

	// Node name line with icon
	icon := "\u25cf" // ●
	var nameStyle lipgloss.Style
	if isLocal {
		nameStyle = localNodeStyle
	} else if n.isRelay {
		icon = "\u25c7" // ◇
		nameStyle = relayNodeStyle
	} else if n.connected {
		nameStyle = remoteNodeStyle
	} else {
		icon = "\u25cb" // ○
		nameStyle = disconnectedNodeStyle
	}

	name := n.id
	if len(name) > innerW-2 {
		name = name[:innerW-2]
	}
	nameLine := icon + " " + nameStyle.Render(name)
	nameW := lipgloss.Width(icon+" ") + lipgloss.Width(nameStyle.Render(name))
	padR := innerW - nameW + 2
	if padR < 0 {
		padR = 0
	}
	b.WriteString("  | " + nameLine + strings.Repeat(" ", padR) + "|\n")

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
			b.WriteString("  | " + helpStyle.Render(more) + strings.Repeat(" ", padR) + "|\n")
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
		b.WriteString("  | " + helpStyle.Render(hName) + strings.Repeat(" ", padR) + "|\n")
	}

	// Bottom border
	b.WriteString("  +" + strings.Repeat("-", innerW+2) + "+")

	return b.String()
}

// renderEdge renders the connection line between local and a remote node.
func renderEdge(remote topoNode, localWidth int) string {
	_ = localWidth
	edgeW := 12

	var lines []string
	if remote.isRelay {
		if remote.connected {
			line := relayEdgeStyle.Render("~~~relay~~~")
			pad := edgeW - 10
			if pad < 0 {
				pad = 0
			}
			lines = append(lines, strings.Repeat(" ", pad/2)+line+strings.Repeat(" ", (pad+1)/2))
		} else {
			line := disconnectedNodeStyle.Render("- -relay- -")
			pad := edgeW - 11
			if pad < 0 {
				pad = 0
			}
			lines = append(lines, strings.Repeat(" ", pad/2)+line+strings.Repeat(" ", (pad+1)/2))
		}
	} else if remote.connected {
		line := directEdgeStyle.Render("---direct--")
		pad := edgeW - 11
		if pad < 0 {
			pad = 0
		}
		lines = append(lines, strings.Repeat(" ", pad/2)+line+strings.Repeat(" ", (pad+1)/2))
	} else {
		line := disconnectedNodeStyle.Render("- - - - - -")
		pad := edgeW - 11
		if pad < 0 {
			pad = 0
		}
		lines = append(lines, strings.Repeat(" ", pad/2)+line+strings.Repeat(" ", (pad+1)/2))
	}

	// Pad to same height as a box (roughly 5 lines), edge on line 2
	result := make([]string, 5)
	for i := range result {
		if i == 1 && len(lines) > 0 {
			result[i] = lines[0]
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

	for _, p := range m.status.Peers {
		status := disconnectedNodeStyle.Render("[disconnected]")
		if p.Connected {
			status = connectedStyle.Render("[connected]")
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

	for _, r := range m.status.Relays {
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
	b.WriteString(sectionStyle.Width(totalWidth - 4).Render(topContent) + "\n")

	// Bottom row: handles+sessions (left) | activity (right)
	leftW := totalWidth/2 - 2
	rightW := totalWidth - leftW - 4

	hsContent := m.renderHandlesSessions(leftW)
	actContent := m.renderActivity(rightW)
	bottomRow := lipgloss.JoinHorizontal(lipgloss.Top,
		sectionStyle.Width(leftW).Render(hsContent),
		sectionStyle.Width(rightW).Render(actContent),
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
func (m dashboardModel) renderHandlesSessions(width int) string {
	var b strings.Builder

	// Handles
	b.WriteString(headerStyle.Render("HANDLES") + "\n")
	if m.status == nil || len(m.status.Handles) == 0 {
		b.WriteString(helpStyle.Render("  (none)") + "\n")
	} else {
		for _, h := range m.status.Handles {
			line := fmt.Sprintf("  %s (%d subs)", h.Name, h.SubscriberCount)
			if len(line) > width-2 {
				line = line[:width-2]
			}
			b.WriteString(line + "\n")
		}
	}

	// Sessions
	b.WriteString(headerStyle.Render("SESSIONS") + "\n")
	if m.status == nil || len(m.status.Sessions) == 0 {
		b.WriteString(helpStyle.Render("  (none)"))
	} else {
		for _, s := range m.status.Sessions {
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
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m dashboardModel) renderActivity(width int) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("ACTIVITY") + "\n")

	if len(m.activity) == 0 {
		b.WriteString(helpStyle.Render("  (waiting for events...)"))
		return b.String()
	}

	// Show most recent events (bottom = newest)
	maxLines := 10
	start := 0
	if len(m.activity) > maxLines {
		start = len(m.activity) - maxLines
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

func runDashboard(client agentpb.AgentAPIClient) error {
	p := tea.NewProgram(
		newDashboardModel(client),
		tea.WithAltScreen(),
	)
	_, err := p.Run()
	return err
}
