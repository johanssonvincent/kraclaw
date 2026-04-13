package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	grpcinsecure "google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	kraclawv1 "github.com/johanssonvincent/kraclaw/pkg/pb/kraclawv1"
)

type ServerStatus struct {
	Version           string
	ActiveSandboxes   int
	ConnectedChannels int
	PendingMessages   int
	ActiveTasks       int
	UptimeSince       string
	MysqlConnected    bool
	RedisConnected    bool
	K8sConnected      bool
}

type SandboxInfo struct {
	Name        string
	GroupFolder string
	GroupJID    string
	State       string
	StartTime   string
	EndTime     string
	SessionID   string
}

type GroupInfo struct {
	JID            string
	Name           string
	Folder         string
	TriggerPattern string
	IsMain         bool
}

type TaskInfo struct {
	ID            string
	GroupFolder   string
	ChatJID       string
	Prompt        string
	ScheduleType  string
	ScheduleValue string
	NextRun       string
	Status        string
}

type EventInfo struct {
	Timestamp string
	Type      string
	Source    string
	Message   string
}

// Style vars and chatTheme are defined in theme.go.

type tickMsg time.Time

type statusMsg struct {
	status *ServerStatus
	err    error
}

type sandboxesMsg struct {
	sandboxes []SandboxInfo
	err       error
}

type groupsMsg struct {
	groups []GroupInfo
	err    error
}

type tasksMsg struct {
	tasks []TaskInfo
	err   error
}

type eventStreamOpenedMsg struct {
	stream kraclawv1.AdminService_StreamEventsClient
	err    error
}

type eventReceivedMsg struct {
	event *EventInfo
	err   error
}

type apiClient struct {
	conn      *grpc.ClientConn
	admin     kraclawv1.AdminServiceClient
	groups    kraclawv1.GroupServiceClient
	tasks     kraclawv1.TaskServiceClient
	sandboxes kraclawv1.SandboxServiceClient
	channels  kraclawv1.ChannelServiceClient
}

func newAPIClient(serverAddr, caCertFile, clientCertFile, clientKeyFile, serverName string, insecure bool) (*apiClient, error) {
	var opts []grpc.DialOption

	if insecure {
		opts = append(opts, grpc.WithTransportCredentials(grpcinsecure.NewCredentials()))
	} else {
		tlsConfig, err := loadClientTLSConfig(serverAddr, caCertFile, clientCertFile, clientKeyFile, serverName)
		if err != nil {
			return nil, err
		}
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	}

	conn, err := grpc.NewClient(
		serverAddr,
		opts...,
	)
	if err != nil {
		return nil, fmt.Errorf("dial gRPC server: %w", err)
	}

	return &apiClient{
		conn:      conn,
		admin:     kraclawv1.NewAdminServiceClient(conn),
		groups:    kraclawv1.NewGroupServiceClient(conn),
		tasks:     kraclawv1.NewTaskServiceClient(conn),
		sandboxes: kraclawv1.NewSandboxServiceClient(conn),
		channels:  kraclawv1.NewChannelServiceClient(conn),
	}, nil
}

func (c *apiClient) Close() error {
	return c.conn.Close()
}

func loadClientTLSConfig(serverAddr, caCertFile, clientCertFile, clientKeyFile, serverName string) (*tls.Config, error) {
	if caCertFile == "" || clientCertFile == "" || clientKeyFile == "" {
		return nil, fmt.Errorf("ca-cert, client-cert, and client-key must all be set")
	}

	caPEM, err := os.ReadFile(caCertFile)
	if err != nil {
		return nil, fmt.Errorf("read CA certificate: %w", err)
	}

	rootCAs := x509.NewCertPool()
	if !rootCAs.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("parse CA certificate bundle")
	}

	cert, err := tls.LoadX509KeyPair(clientCertFile, clientKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load client certificate: %w", err)
	}

	if serverName == "" {
		host, _, err := net.SplitHostPort(serverAddr)
		if err != nil {
			host = serverAddr
		}
		serverName = host
	}

	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		ServerName:   serverName,
		RootCAs:      rootCAs,
		Certificates: []tls.Certificate{cert},
	}, nil
}

const (
	sidebarTargetWidth   = 28  // sidebar content width (inside border)
	sidebarCollapseWidth = 100 // collapse sidebar below this terminal width
)

// layoutDims holds computed panel dimensions for a single render frame.
type layoutDims struct {
	viewportWidth  int
	viewportHeight int
	sidebarWidth   int
	sidebarHeight  int
	inputWidth     int
	sidebarVisible bool
}

type model struct {
	activeTab  int
	tabs       []string
	width      int
	height     int
	serverAddr string

	status    *ServerStatus
	statusErr error

	sandboxes    []SandboxInfo
	sandboxesErr error

	groups    []GroupInfo
	groupsErr error

	tasks    []TaskInfo
	tasksErr error

	events    []EventInfo
	eventsErr error

	spinner spinner.Model
	loading bool

	api         *apiClient
	eventStream kraclawv1.AdminService_StreamEventsClient

	// Chat tab state
	chatState           chatState
	chatGroupCursor     int
	chatGroup           *GroupInfo
	chatMessages        []chatMessage
	chatInput           textarea.Model
	chatViewport        viewport.Model
	chatErr             error
	chatCancel          context.CancelFunc
	inboundStream       kraclawv1.ChannelService_StreamInboundClient
	chatRegInput        textinput.Model // for registering new group name
	chatModel           string
	chatWaitingForAgent bool
	modelPicker         modelPickerState

	// Creation picker (new-group provider/model selection flow)
	creationPendingGroupName string
	creationSelectedProvider string
	creationProviders        []*kraclawv1.ProviderInfo
	creationPicker           creationPickerState

	// Cached Glamour renderer for markdown in agent messages.
	// Rebuilt only when viewport width changes.
	mdRenderer      *glamour.TermRenderer
	mdRendererWidth int

	sidebar        sidebarModel
	sidebarVisible bool
}

// calculateLayout computes panel dimensions from the current terminal size.
func (m model) calculateLayout() layoutDims {
	d := layoutDims{}

	// Sidebar visibility (LAYOUT-03)
	d.sidebarVisible = m.width >= sidebarCollapseWidth

	// Sidebar dimensions (only when visible)
	if d.sidebarVisible {
		d.sidebarWidth = sidebarTargetWidth
	}

	// Vertical budget (lipgloss v2 Height includes borders):
	// View(): tab bar (2) + gap (2) + status bar (1) = 5
	// renderChat: title (1) + gap (1) + newline before input (1) + input box (5) = 8
	// Total reserved: 13
	reservedVertical := 13
	d.viewportHeight = m.height - reservedVertical
	if d.viewportHeight < 3 {
		d.viewportHeight = 3
	}

	// Horizontal budget (LAYOUT-01)
	// In lipgloss v2, Width() includes borders and padding, so
	// sidebarWidth IS the total rendered width — no adjustment needed.
	if d.sidebarVisible {
		d.viewportWidth = m.width - d.sidebarWidth - 1 // 1 col gap
	} else {
		d.viewportWidth = m.width
	}
	if d.viewportWidth < 20 {
		d.viewportWidth = 20
	}

	// Input box Width = m.width (fills terminal). Content area = width - border(2) - padding(2).
	// Textarea = content - prompt(2) = m.width - 6.
	d.inputWidth = m.width - 6
	if d.inputWidth < 20 {
		d.inputWidth = 20
	}

	// Sidebar height matches viewport for JoinHorizontal alignment
	d.sidebarHeight = d.viewportHeight

	return d
}

// ensureMarkdownRenderer creates or reuses a cached Glamour renderer.
// It only allocates a new renderer when the target width changes.
func (m *model) ensureMarkdownRenderer(width int) {
	if m.mdRenderer != nil && m.mdRendererWidth == width {
		return
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStylePath("dark"),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return
	}
	m.mdRenderer = r
	m.mdRendererWidth = width
}

const (
	tabChat = iota
	tabDashboard
	tabSandboxes
	tabGroups
	tabTasks
	tabEvents
)

func initialModel(serverAddr string, api *apiClient) model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = spinnerStyle

	ta := textarea.New()
	ta.Placeholder = "Type a message..."
	ta.Prompt = ""
	ta.CharLimit = 4096
	ta.MaxHeight = 5
	ta.ShowLineNumbers = false
	styles := ta.Styles()
	styles.Focused.Prompt = lipgloss.NewStyle()
	styles.Blurred.Prompt = lipgloss.NewStyle()
	styles.Focused.CursorLine = lipgloss.NewStyle()
	ta.SetStyles(styles)
	ta.SetWidth(80)
	ta.SetHeight(3)

	regInput := textinput.New()
	regInput.Placeholder = "Group name (e.g. my-project)"
	regInput.CharLimit = 128

	vp := viewport.New()
	vp.SetWidth(80)
	vp.SetHeight(20)

	return model{
		activeTab:    tabChat,
		tabs:         []string{"Chat", "Dashboard", "Sandboxes", "Groups", "Tasks", "Events"},
		serverAddr:   serverAddr,
		spinner:      s,
		loading:      true,
		api:          api,
		chatInput:    ta,
		chatRegInput: regInput,
		chatViewport: vp,
		sidebar:      newSidebarModel(),
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		func() tea.Msg { return m.spinner.Tick() },
		m.fetchAll(),
		m.openEventStream(),
		tickCmd(),
	)
}

func tickCmd() tea.Cmd {
	return tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m model) fetchAll() tea.Cmd {
	return tea.Batch(
		m.fetchStatus(),
		m.fetchSandboxes(),
		m.fetchGroups(),
		m.fetchTasks(),
	)
}

func (m model) fetchStatus() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		resp, err := m.api.admin.GetStatus(ctx, &kraclawv1.GetStatusRequest{})
		if err != nil {
			return statusMsg{err: err}
		}

		return statusMsg{
			status: &ServerStatus{
				Version:           resp.Version,
				ActiveSandboxes:   int(resp.ActiveSandboxes),
				ConnectedChannels: int(resp.ConnectedChannels),
				PendingMessages:   int(resp.PendingMessages),
				ActiveTasks:       int(resp.ActiveTasks),
				UptimeSince:       formatProtoTime(resp.UptimeSince),
				MysqlConnected:    resp.MysqlConnected,
				RedisConnected:    resp.RedisConnected,
				K8sConnected:      resp.K8SConnected,
			},
		}
	}
}

func (m model) fetchSandboxes() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		resp, err := m.api.sandboxes.ListSandboxes(ctx, &kraclawv1.ListSandboxesRequest{})
		if err != nil {
			return sandboxesMsg{err: err}
		}

		sandboxes := make([]SandboxInfo, 0, len(resp.Sandboxes))
		for _, sb := range resp.Sandboxes {
			sandboxes = append(sandboxes, SandboxInfo{
				Name:        sb.Name,
				GroupFolder: sb.GroupFolder,
				GroupJID:    sb.GroupJid,
				State:       sb.State,
				StartTime:   formatProtoTime(sb.StartTime),
				EndTime:     formatProtoTime(sb.EndTime),
				SessionID:   sb.SessionId,
			})
		}

		return sandboxesMsg{sandboxes: sandboxes}
	}
}

func (m model) fetchGroups() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		resp, err := m.api.groups.ListGroups(ctx, &kraclawv1.ListGroupsRequest{})
		if err != nil {
			return groupsMsg{err: err}
		}

		groups := make([]GroupInfo, 0, len(resp.Groups))
		for _, group := range resp.Groups {
			groups = append(groups, GroupInfo{
				JID:            group.Jid,
				Name:           group.Name,
				Folder:         group.Folder,
				TriggerPattern: group.TriggerPattern,
				IsMain:         group.IsMain,
			})
		}

		return groupsMsg{groups: groups}
	}
}

func (m model) fetchTasks() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		resp, err := m.api.tasks.ListTasks(ctx, &kraclawv1.ListTasksRequest{})
		if err != nil {
			return tasksMsg{err: err}
		}

		tasks := make([]TaskInfo, 0, len(resp.Tasks))
		for _, task := range resp.Tasks {
			tasks = append(tasks, TaskInfo{
				ID:            task.Id,
				GroupFolder:   task.GroupFolder,
				ChatJID:       task.ChatJid,
				Prompt:        task.Prompt,
				ScheduleType:  task.ScheduleType,
				ScheduleValue: task.ScheduleValue,
				NextRun:       formatProtoTime(task.NextRun),
				Status:        task.Status,
			})
		}

		return tasksMsg{tasks: tasks}
	}
}

func (m model) openEventStream() tea.Cmd {
	return func() tea.Msg {
		stream, err := m.api.admin.StreamEvents(context.Background(), &kraclawv1.StreamEventsRequest{})
		if err != nil {
			return eventStreamOpenedMsg{err: err}
		}
		return eventStreamOpenedMsg{stream: stream}
	}
}

func readEventCmd(stream kraclawv1.AdminService_StreamEventsClient) tea.Cmd {
	return func() tea.Msg {
		evt, err := stream.Recv()
		if err != nil {
			return eventReceivedMsg{err: err}
		}
		return eventReceivedMsg{
			event: &EventInfo{
				Timestamp: formatProtoTime(evt.Timestamp),
				Type:      evt.Type,
				Source:    evt.Source,
				Message:   evt.Message,
			},
		}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		// Chat tab has its own key handling
		if m.activeTab == tabChat {
			return m.updateChat(msg)
		}

		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "q":
			return m, tea.Quit
		case "tab":
			m.activeTab = (m.activeTab + 1) % len(m.tabs)
			return m, nil
		case "shift+tab":
			m.activeTab = (m.activeTab - 1 + len(m.tabs)) % len(m.tabs)
			return m, nil
		case "r":
			m.loading = true
			cmds := []tea.Cmd{m.fetchAll()}
			if m.eventStream == nil {
				cmds = append(cmds, m.openEventStream())
			}
			return m, tea.Batch(cmds...)
		}

	case tea.MouseWheelMsg, tea.MouseClickMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg:
		if m.activeTab == tabChat && m.chatState == chatStateChatting {
			var cmd tea.Cmd
			m.chatViewport, cmd = m.chatViewport.Update(msg)
			return m, cmd
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		d := m.calculateLayout()
		m.sidebarVisible = d.sidebarVisible
		m.chatViewport.SetWidth(d.viewportWidth)
		m.chatViewport.SetHeight(d.viewportHeight)
		m.chatInput.SetWidth(d.inputWidth)
		m.sidebar.width = d.sidebarWidth
		m.sidebar.height = d.sidebarHeight
		m.mdRenderer = nil // invalidate cached renderer on resize

		if m.chatState == chatStateChatting {
			contentWidth := m.chatViewport.Width() - 6
			if contentWidth < 10 {
				contentWidth = 10
			}
			m.ensureMarkdownRenderer(contentWidth)
			m.chatViewport.SetContent(m.formatChatMessages())
		}
		return m, nil

	case tickMsg:
		cmds := []tea.Cmd{m.fetchAll(), tickCmd()}
		if m.eventStream == nil {
			cmds = append(cmds, m.openEventStream())
		}
		return m, tea.Batch(cmds...)

	case statusMsg:
		m.loading = false
		m.status = msg.status
		m.statusErr = msg.err
		return m, nil

	case sandboxesMsg:
		m.sandboxes = msg.sandboxes
		m.sandboxesErr = msg.err
		return m, nil

	case groupsMsg:
		m.groups = msg.groups
		m.groupsErr = msg.err
		return m, nil

	case tasksMsg:
		m.tasks = msg.tasks
		m.tasksErr = msg.err
		return m, nil

	case eventStreamOpenedMsg:
		if msg.err != nil {
			m.eventsErr = msg.err
			m.eventStream = nil
			return m, nil
		}
		m.eventsErr = nil
		m.eventStream = msg.stream
		return m, readEventCmd(msg.stream)

	case eventReceivedMsg:
		if msg.err != nil {
			if !errors.Is(msg.err, io.EOF) && status.Code(msg.err) != codes.Canceled {
				m.eventsErr = msg.err
			}
			m.eventStream = nil
			return m, nil
		}

		m.eventsErr = nil
		m.events = append([]EventInfo{*msg.event}, m.events...)
		if len(m.events) > 200 {
			m.events = m.events[:200]
		}
		return m, readEventCmd(m.eventStream)

	case providersLoadedMsg:
		if msg.err != nil {
			m.chatErr = msg.err
			m.chatState = chatStateSelectGroup
			m.creationPendingGroupName = ""
			m.creationPicker = creationPickerState{}
			return m, nil
		}
		m.creationProviders = msg.providers
		m.creationPicker = creationPickerState{}
		for _, p := range msg.providers {
			m.creationPicker.items = append(m.creationPicker.items, creationPickerItem{
				id:    p.GetId(),
				label: p.GetDisplayName(),
			})
		}
		return m, nil

	case groupRegisteredMsg:
		if msg.err != nil {
			m.chatErr = msg.err
			m.chatState = chatStateSelectGroup
			return m, m.fetchGroups()
		}
		g := GroupInfo{
			JID:    msg.group.Jid,
			Name:   msg.group.Name,
			Folder: msg.group.Folder,
			IsMain: msg.group.IsMain,
		}
		m.chatGroup = &g
		m.chatState = chatStateConnecting
		m.chatErr = nil
		m.chatMessages = nil
		m.chatModel = ""
		return m, tea.Batch(m.fetchGroups(), m.openInboundStreamCmd(g.JID))

	case streamOpenedMsg:
		if msg.err != nil {
			m.chatErr = msg.err
			m.chatWaitingForAgent = false
			m.chatState = chatStateSelectGroup
			return m, nil
		}
		m.inboundStream = msg.stream
		m.chatCancel = msg.cancel
		m.chatState = chatStateChatting
		m.chatErr = nil
		m.chatInput.Focus()
		return m, readInboundCmd(msg.stream)

	case channelOutputMsg:
		if msg.err != nil {
			m.chatWaitingForAgent = false
			if !errors.Is(msg.err, io.EOF) && status.Code(msg.err) != codes.Canceled {
				m.chatErr = msg.err
			}
			m.inboundStream = nil
			m.chatMessages = append(m.chatMessages, chatMessage{
				sender:  "system",
				content: "[Stream disconnected. Press Esc to return.]",
			})
			contentWidth := m.chatViewport.Width() - 6
			if contentWidth < 10 {
				contentWidth = 10
			}
			m.ensureMarkdownRenderer(contentWidth)
			m.chatViewport.SetContent(m.formatChatMessages())
			m.chatViewport.GotoBottom()
			return m, nil
		}
		if msg.msg != nil && msg.msg.Model != "" {
			m.chatModel = msg.msg.Model
		}
		if msg.msg != nil && m.modelPicker.Open {
			content := msg.msg.Content
			if strings.HasPrefix(content, "Failed to fetch models:") {
				m.modelPicker.LastError = content
				m.modelPicker.Loading = false
			} else if strings.HasPrefix(content, "Models") {
				parsed := parseModelList(content)
				m.modelPicker.Loading = false
				m.modelPicker.LastError = ""
				if len(parsed) > 0 {
					m.modelPicker.Options = parsed
					if m.modelPicker.Cursor < 0 {
						m.modelPicker.Cursor = 0
					}
					if m.modelPicker.Cursor >= len(m.modelPicker.Options) {
						m.modelPicker.Cursor = len(m.modelPicker.Options) - 1
					}
				} else {
					m.modelPicker.Options = []modelOption{}
					m.modelPicker.Cursor = 0
				}
			}
		}
		if msg.msg != nil && msg.msg.Content != "" {
			m.chatWaitingForAgent = false
			wasAtBottom := m.chatViewport.AtBottom()
			m.chatMessages = append(m.chatMessages, chatMessage{
				sender:  "agent",
				content: msg.msg.Content,
			})
			contentWidth := m.chatViewport.Width() - 6
			if contentWidth < 10 {
				contentWidth = 10
			}
			m.ensureMarkdownRenderer(contentWidth)
			m.chatViewport.SetContent(m.formatChatMessages())
			if wasAtBottom {
				m.chatViewport.GotoBottom()
			}
		}
		if m.inboundStream != nil {
			return m, readInboundCmd(m.inboundStream)
		}
		return m, nil

	case inputSentMsg:
		if msg.err != nil {
			m.chatErr = msg.err
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m model) View() tea.View {
	var content string
	if m.width == 0 {
		content = "Loading..."
	} else {
		var b strings.Builder

		b.WriteString(m.renderTabBar())
		b.WriteString("\n\n")

		contentHeight := m.height - 5
		rendered := m.renderContent()
		lines := strings.Split(rendered, "\n")
		if len(lines) > contentHeight && contentHeight > 0 {
			lines = lines[:contentHeight]
		}
		b.WriteString(strings.Join(lines, "\n"))

		currentLines := strings.Count(b.String(), "\n") + 1
		for i := currentLines; i < m.height-1; i++ {
			b.WriteString("\n")
		}

		b.WriteString(m.renderStatusBar())
		content = b.String()
	}

	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m model) renderTabBar() string {
	var tabs []string
	for i, t := range m.tabs {
		if i == m.activeTab {
			tabs = append(tabs, activeTabStyle.Render(t))
		} else {
			tabs = append(tabs, inactiveTabStyle.Render(t))
		}
	}
	row := lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
	return tabBorder.Width(m.width).Render(row)
}

func (m model) renderContent() string {
	switch m.activeTab {
	case tabChat:
		return m.renderChat()
	case tabDashboard:
		return m.renderDashboard()
	case tabSandboxes:
		return m.renderSandboxes()
	case tabGroups:
		return m.renderGroups()
	case tabTasks:
		return m.renderTasks()
	case tabEvents:
		return m.renderEvents()
	default:
		return ""
	}
}

func (m model) renderDashboard() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Server Status"))
	b.WriteString("\n")

	if m.statusErr != nil {
		b.WriteString(errStyle.Render("  Connection error: " + m.statusErr.Error()))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("  Server may be offline. Press 'r' to retry."))
		return b.String()
	}

	if m.status == nil {
		if m.loading {
			b.WriteString("  " + m.spinner.View() + " Connecting to server...")
		} else {
			b.WriteString(dimStyle.Render("  No status data available."))
		}
		return b.String()
	}

	s := m.status

	fmt.Fprintf(&b, "  Version:            %s\n", s.Version)
	fmt.Fprintf(&b, "  Active Sandboxes:   %d\n", s.ActiveSandboxes)
	fmt.Fprintf(&b, "  Connected Channels: %d\n", s.ConnectedChannels)
	fmt.Fprintf(&b, "  Pending Messages:   %d\n", s.PendingMessages)
	fmt.Fprintf(&b, "  Active Tasks:       %d\n", s.ActiveTasks)

	if s.UptimeSince != "" {
		fmt.Fprintf(&b, "  Uptime Since:       %s\n", s.UptimeSince)
	}

	b.WriteString("\n")
	b.WriteString(titleStyle.Render("Dependencies"))
	b.WriteString("\n")
	fmt.Fprintf(&b, "  MySQL:       %s\n", connStatus(s.MysqlConnected))
	fmt.Fprintf(&b, "  Redis:       %s\n", connStatus(s.RedisConnected))
	fmt.Fprintf(&b, "  Kubernetes:  %s\n", connStatus(s.K8sConnected))

	b.WriteString("\n")
	b.WriteString(titleStyle.Render("Transport"))
	b.WriteString("\n")
	fmt.Fprintf(&b, "  gRPC mTLS:   %s\n", connStatus(m.statusErr == nil && m.status != nil))
	fmt.Fprintf(&b, "  Event Stream:%s\n", " "+connStatus(m.eventStream != nil && m.eventsErr == nil))

	return b.String()
}

func connStatus(ok bool) string {
	if ok {
		return okStyle.Render("OK")
	}
	return errStyle.Render("DOWN")
}

func (m model) renderSandboxes() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Sandboxes"))
	b.WriteString("\n")

	if m.sandboxesErr != nil {
		b.WriteString(errStyle.Render("  Error: " + m.sandboxesErr.Error()))
		return b.String()
	}

	if len(m.sandboxes) == 0 {
		b.WriteString(dimStyle.Render("  No sandboxes found."))
		return b.String()
	}

	header := fmt.Sprintf("  %-24s %-16s %-12s %-10s", "Name", "Group", "State", "Session")
	b.WriteString(headerStyle.Render(header))
	b.WriteString("\n")

	for i, s := range m.sandboxes {
		row := fmt.Sprintf("  %-24s %-16s %-12s %-10s",
			truncate(s.Name, 24),
			truncate(s.GroupFolder, 16),
			s.State,
			truncate(s.SessionID, 10),
		)
		if i%2 == 0 {
			b.WriteString(evenRowStyle.Render(row))
		} else {
			b.WriteString(oddRowStyle.Render(row))
		}
		b.WriteString("\n")
	}

	return b.String()
}

func (m model) renderGroups() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Groups"))
	b.WriteString("\n")

	if m.groupsErr != nil {
		b.WriteString(errStyle.Render("  Error: " + m.groupsErr.Error()))
		return b.String()
	}

	if len(m.groups) == 0 {
		b.WriteString(dimStyle.Render("  No groups registered."))
		return b.String()
	}

	header := fmt.Sprintf("  %-20s %-16s %-28s %-16s %-6s", "Name", "Folder", "JID", "Trigger", "Main")
	b.WriteString(headerStyle.Render(header))
	b.WriteString("\n")

	for i, g := range m.groups {
		mainStr := "no"
		if g.IsMain {
			mainStr = "yes"
		}
		row := fmt.Sprintf("  %-20s %-16s %-28s %-16s %-6s",
			truncate(g.Name, 20),
			truncate(g.Folder, 16),
			truncate(g.JID, 28),
			truncate(g.TriggerPattern, 16),
			mainStr,
		)
		if i%2 == 0 {
			b.WriteString(evenRowStyle.Render(row))
		} else {
			b.WriteString(oddRowStyle.Render(row))
		}
		b.WriteString("\n")
	}

	return b.String()
}

func (m model) renderTasks() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Tasks"))
	b.WriteString("\n")

	if m.tasksErr != nil {
		b.WriteString(errStyle.Render("  Error: " + m.tasksErr.Error()))
		return b.String()
	}

	if len(m.tasks) == 0 {
		b.WriteString(dimStyle.Render("  No tasks scheduled."))
		return b.String()
	}

	header := fmt.Sprintf("  %-12s %-16s %-10s %-14s %-10s %-20s", "ID", "Group", "Type", "Schedule", "Status", "Next Run")
	b.WriteString(headerStyle.Render(header))
	b.WriteString("\n")

	for i, t := range m.tasks {
		row := fmt.Sprintf("  %-12s %-16s %-10s %-14s %-10s %-20s",
			truncate(t.ID, 12),
			truncate(t.GroupFolder, 16),
			t.ScheduleType,
			truncate(t.ScheduleValue, 14),
			t.Status,
			truncate(t.NextRun, 20),
		)
		if i%2 == 0 {
			b.WriteString(evenRowStyle.Render(row))
		} else {
			b.WriteString(oddRowStyle.Render(row))
		}
		b.WriteString("\n")
	}

	return b.String()
}

func (m model) renderEvents() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Events"))
	b.WriteString("\n")

	if m.eventsErr != nil {
		b.WriteString(errStyle.Render("  Stream error: " + m.eventsErr.Error()))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  Waiting to reconnect..."))
		return b.String()
	}

	if len(m.events) == 0 {
		b.WriteString(dimStyle.Render("  No events recorded yet."))
		return b.String()
	}

	for i, e := range m.events {
		ts := truncate(e.Timestamp, 19)
		line := fmt.Sprintf("  %s  %-12s %-14s %s", ts, e.Type, e.Source, e.Message)
		if i%2 == 0 {
			b.WriteString(evenRowStyle.Render(line))
		} else {
			b.WriteString(oddRowStyle.Render(line))
		}
		b.WriteString("\n")
	}

	return b.String()
}

func (m model) renderStatusBar() string {
	connStr := "disconnected"
	connColor := errStyle
	if m.status != nil && m.statusErr == nil {
		connStr = "connected"
		connColor = okStyle
	}

	left := fmt.Sprintf(" %s | %s", m.serverAddr, connColor.Render(connStr))

	var right string
	if m.activeTab == tabChat && m.chatState == chatStateChatting {
		modelName := m.chatModel
		if modelName == "" {
			modelName = "default"
		}
		statusToken := "Idle"
		if m.chatWaitingForAgent {
			statusToken = "Waiting"
		}
		right = fmt.Sprintf(" Model: %s | Status: %s | Esc: back | Enter: send | Ctrl+C: quit ", truncate(modelName, 40), statusToken)
	} else {
		right = " Tab/Shift+Tab: switch | r: refresh | q: quit "
	}

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}

	bar := left + strings.Repeat(" ", gap) + right
	return statusBarStyle.Width(m.width).Render(bar)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

func formatProtoTime(ts *timestamppb.Timestamp) string {
	if ts == nil {
		return ""
	}
	t := ts.AsTime()
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

func runDebugChecks(serverAddr, caCert, clientCert, clientKey, serverName string) {
	dbg := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "[debug] "+format+"\n", args...)
	}

	// 1. Cert file checks
	dbg("=== Certificate file checks ===")
	for _, f := range []struct{ name, path string }{
		{"CA cert", caCert},
		{"Client cert", clientCert},
		{"Client key", clientKey},
	} {
		info, err := os.Stat(f.path)
		if err != nil {
			dbg("  %s (%s): %v", f.name, f.path, err)
		} else {
			dbg("  %s (%s): %d bytes", f.name, f.path, info.Size())
		}
	}

	// 2. CA cert details
	dbg("=== CA certificate details ===")
	caPEM, err := os.ReadFile(caCert)
	if err != nil {
		dbg("  failed to read CA cert: %v", err)
	} else {
		block, _ := pem.Decode(caPEM)
		if block == nil {
			dbg("  failed to decode CA PEM")
		} else {
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				dbg("  failed to parse CA cert: %v", err)
			} else {
				dbg("  Subject: %s", cert.Subject)
				dbg("  Issuer:  %s", cert.Issuer)
				dbg("  NotAfter: %s", cert.NotAfter)
			}
		}
	}

	// 3. Client cert details
	dbg("=== Client certificate details ===")
	clientPEM, err := os.ReadFile(clientCert)
	if err != nil {
		dbg("  failed to read client cert: %v", err)
	} else {
		block, _ := pem.Decode(clientPEM)
		if block == nil {
			dbg("  failed to decode client PEM")
		} else {
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				dbg("  failed to parse client cert: %v", err)
			} else {
				dbg("  Subject: %s", cert.Subject)
				dbg("  Issuer:  %s", cert.Issuer)
				dbg("  NotAfter: %s", cert.NotAfter)
				dbg("  DNS SANs: %v", cert.DNSNames)
				dbg("  IP SANs:  %v", cert.IPAddresses)
			}
		}
	}

	// 4. TCP connectivity
	dbg("=== TCP connectivity ===")
	tcpConn, err := net.DialTimeout("tcp", serverAddr, 3*time.Second)
	if err != nil {
		dbg("  TCP dial %s failed: %v", serverAddr, err)
		dbg("Stopping diagnostics — server not reachable.")
		return
	}
	_ = tcpConn.Close()
	dbg("  TCP dial %s: OK", serverAddr)

	// 5. TLS handshake
	dbg("=== TLS handshake ===")
	tlsConfig, err := loadClientTLSConfig(serverAddr, caCert, clientCert, clientKey, serverName)
	if err != nil {
		dbg("  failed to build TLS config: %v", err)
		return
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	tlsConn, err := tls.DialWithDialer(dialer, "tcp", serverAddr, tlsConfig)
	if err != nil {
		dbg("  TLS handshake failed: %v", err)
	} else {
		state := tlsConn.ConnectionState()
		dbg("  TLS handshake OK (version: 0x%04x, cipher: 0x%04x)", state.Version, state.CipherSuite)
		if len(state.PeerCertificates) > 0 {
			dbg("  Server cert subject: %s", state.PeerCertificates[0].Subject)
		}
		_ = tlsConn.Close()
	}

	dbg("=== Diagnostics complete, proceeding with normal startup ===")
}

func main() {
	serverAddr := flag.String("server", "localhost:50051", "Server gRPC address")
	flag.StringVar(serverAddr, "s", "localhost:50051", "Server gRPC address (shorthand)")

	caCertFile := flag.String("ca-cert", "/var/run/kraclaw/grpc-client-tls/ca.crt", "Path to the gRPC CA certificate")
	clientCertFile := flag.String("client-cert", "/var/run/kraclaw/grpc-client-tls/tls.crt", "Path to the client certificate for mTLS")
	clientKeyFile := flag.String("client-key", "/var/run/kraclaw/grpc-client-tls/tls.key", "Path to the client private key for mTLS")
	serverName := flag.String("server-name", "kraclaw-grpc.kraclaw.svc.cluster.local", "Expected TLS server name override")
	debug := flag.Bool("debug", false, "Run pre-flight TLS diagnostics before connecting")
	insecure := flag.Bool("insecure", false, "Connect without TLS (for in-pod localhost use)")
	flag.Parse()

	if *debug {
		runDebugChecks(*serverAddr, *caCertFile, *clientCertFile, *clientKeyFile, *serverName)
	}

	api, err := newAPIClient(*serverAddr, *caCertFile, *clientCertFile, *clientKeyFile, *serverName, *insecure)
	if err != nil {
		slog.Error("failed to create gRPC client", "error", err)
		os.Exit(1)
	}
	defer func() { _ = api.Close() }()

	slog.Info("starting kraclaw TUI", "server", *serverAddr)

	p := tea.NewProgram(initialModel(*serverAddr, api))

	if _, err := p.Run(); err != nil {
		slog.Error("TUI exited with error", "error", err)
		os.Exit(1)
	}
}
