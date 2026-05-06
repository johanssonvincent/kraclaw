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
	"os/user"
	"sort"
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
	auth      kraclawv1.AuthServiceClient
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
		auth:      kraclawv1.NewAuthServiceClient(conn),
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

// Layout dimensions. Sidebar is gone; viewport fills the width minus 2 cols
// of margin. 2 rows at the bottom are reserved for key bar + status bar.
type layoutDims struct {
	viewportWidth  int
	viewportHeight int
	inputWidth     int
}

type model struct {
	activeTab  int
	tabs       []string
	width      int
	height     int
	serverAddr string
	useTLS     bool

	status    *ServerStatus
	statusErr error

	sandboxes         []SandboxInfo
	sandboxesErr      error
	sandboxCursor     int
	sandboxDetailOpen bool

	groups       []GroupInfo
	groupsErr    error
	groupsCursor int

	tasks             []TaskInfo
	tasksErr          error
	tasksCursor       int
	tasksFilterFolder string

	events    []EventInfo
	eventsErr error

	spinner spinner.Model
	loading bool
	now     time.Time

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
	chatRegInput        textinput.Model
	chatModel           string
	chatWaitingForAgent bool
	modelPicker         modelPickerState

	creationFlowID           int
	creationPendingGroupName string
	creationSelectedProvider string
	creationSelectedModelID  string
	creationProviders        []*kraclawv1.ProviderInfo
	creationPicker           creationPickerState
	creationProvidersLoaded  bool

	oauth oauthState

	mdRenderer      *glamour.TermRenderer
	mdRendererWidth int
}

func (m model) calculateLayout() layoutDims {
	d := layoutDims{}

	// Vertical budget: tab bar (1) + gap (1) + key bar (1) + status bar (1) +
	// group header (1) + composer meta (1) + composer box (5) = 11.
	// We only reserve for chat when chatting; non-chat tabs reserve 4.
	reservedVertical := 4
	if m.activeTab == tabMessages && m.chatState == chatStateChatting {
		reservedVertical = 11
	}
	d.viewportHeight = m.height - reservedVertical
	if d.viewportHeight < 3 {
		d.viewportHeight = 3
	}

	d.viewportWidth = m.width
	if d.viewportWidth < 20 {
		d.viewportWidth = 20
	}

	d.inputWidth = m.width - 6
	if d.inputWidth < 20 {
		d.inputWidth = 20
	}

	return d
}

func (m model) applyLayout() model {
	d := m.calculateLayout()
	m.chatViewport.SetWidth(d.viewportWidth)
	m.chatViewport.SetHeight(d.viewportHeight)
	m.chatInput.SetWidth(d.inputWidth)
	m.mdRenderer = nil

	if m.chatState == chatStateChatting {
		contentWidth := m.chatViewport.Width() - 6
		if contentWidth < 10 {
			contentWidth = 10
		}
		m.ensureMarkdownRenderer(contentWidth)
		m.chatViewport.SetContent(m.formatChatMessages())
	}

	return m
}

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

func (m model) refreshChatViewportContent() model {
	if m.chatState != chatStateChatting {
		return m
	}
	contentWidth := m.chatViewport.Width() - 6
	if contentWidth < 10 {
		contentWidth = 10
	}
	m.mdRenderer = nil
	m.ensureMarkdownRenderer(contentWidth)
	m.chatViewport.SetContent(m.formatChatMessages())
	return m
}

const (
	tabDashboard = iota
	tabSandboxes
	tabGroups
	tabTasks
	tabEvents
	tabMessages
	tabChannels
	tabConfig
)

var tabLabels = []string{
	"dashboard",
	"sandboxes",
	"groups",
	"tasks",
	"events",
	"messages",
	"channels",
	"config",
}

func initialModel(serverAddr string, api *apiClient) model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = spinnerStyle

	ta := textarea.New()
	ta.Placeholder = "type a message..."
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
	regInput.Placeholder = "group name (e.g. my-project)"
	regInput.CharLimit = 128

	vp := viewport.New()
	vp.SetWidth(80)
	vp.SetHeight(20)

	return model{
		activeTab:    tabMessages,
		tabs:         tabLabels,
		serverAddr:   serverAddr,
		spinner:      s,
		loading:      true,
		api:          api,
		chatInput:    ta,
		chatRegInput: regInput,
		chatViewport: vp,
		now:          time.Now(),
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
	return tea.Tick(1*time.Second, func(t time.Time) tea.Msg {
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
		// Global theme toggle works on every tab.
		if msg.String() == "ctrl+t" {
			next := "light"
			if activePalette.Name == "light" {
				next = "dark"
			}
			setTheme(next)
			_ = persistTheme(next) // best-effort; surface via slog only
			m = m.refreshChatViewportContent()
			return m, nil
		}

		if m.activeTab == tabMessages {
			return m.updateChat(msg)
		}

		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "tab":
			m.activeTab = (m.activeTab + 1) % len(m.tabs)
			return m, nil
		case "shift+tab":
			m.activeTab = (m.activeTab - 1 + len(m.tabs)) % len(m.tabs)
			return m, nil
		case "1":
			m.activeTab = tabDashboard
			return m, nil
		case "2":
			m.activeTab = tabSandboxes
			return m, nil
		case "3":
			m.activeTab = tabGroups
			return m, nil
		case "4":
			m.activeTab = tabTasks
			return m, nil
		case "5":
			m.activeTab = tabEvents
			return m, nil
		case "6":
			m.activeTab = tabMessages
			return m, nil
		case "7":
			m.activeTab = tabChannels
			return m, nil
		case "8":
			m.activeTab = tabConfig
			return m, nil
		case "r":
			m.loading = true
			cmds := []tea.Cmd{m.fetchAll()}
			if m.eventStream == nil {
				cmds = append(cmds, m.openEventStream())
			}
			return m, tea.Batch(cmds...)
		case "j", "down":
			return m.moveCursor(+1), nil
		case "k", "up":
			return m.moveCursor(-1), nil
		case "enter":
			if m.activeTab == tabSandboxes && len(m.sandboxes) > 0 {
				m.sandboxDetailOpen = true
			}
			return m, nil
		case "esc":
			m.sandboxDetailOpen = false
			return m, nil
		case "g":
			if m.activeTab == tabTasks {
				m.cycleTaskFilter()
			}
			return m, nil
		}

	case tea.MouseWheelMsg, tea.MouseClickMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg:
		if m.activeTab == tabMessages && m.chatState == chatStateChatting {
			var cmd tea.Cmd
			m.chatViewport, cmd = m.chatViewport.Update(msg)
			return m, cmd
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m = m.applyLayout()
		return m, nil

	case tickMsg:
		m.now = time.Time(msg)
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
		if m.sandboxCursor >= len(m.sandboxes) {
			m.sandboxCursor = 0
			m.sandboxDetailOpen = false
		}
		return m, nil

	case groupsMsg:
		m.groups = msg.groups
		m.groupsErr = msg.err
		if m.groupsCursor >= len(m.groups) {
			m.groupsCursor = 0
		}
		return m, nil

	case tasksMsg:
		m.tasks = msg.tasks
		m.tasksErr = msg.err
		filtered := m.filteredTasks()
		if m.tasksCursor >= len(filtered) {
			m.tasksCursor = 0
		}
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
		if (m.chatState != chatStateSelectProvider && m.chatState != chatStateSelectModel) || msg.flowID != m.creationFlowID {
			return m, nil
		}
		if msg.err != nil {
			slog.Error("failed to load providers",
				"grpc_code", status.Code(msg.err).String(),
				"err", msg.err)
			m.chatErr = msg.err
			m.chatState = chatStateSelectGroup
			m.creationPendingGroupName = ""
			m.creationSelectedProvider = ""
			m.creationProviders = nil
			m.creationPicker = creationPickerState{}
			m.creationProvidersLoaded = false
			return m, nil
		}
		m.creationProviders = msg.providers
		if m.chatState == chatStateSelectModel && m.creationSelectedProvider != "" {
			items := buildModelItems(msg.providers, m.creationSelectedProvider)
			m.creationPicker = creationPickerState{items: items}
			if len(items) == 0 {
				m.chatErr = fmt.Errorf("provider %q has no models configured — press Esc to cancel", m.creationSelectedProvider)
			} else {
				m.chatErr = nil
			}
		} else {
			items, _ := buildProviderItems(msg.providers, "")
			m.creationPicker = creationPickerState{items: items}
		}
		m.creationProvidersLoaded = true
		return m, nil

	case groupRegisteredMsg:
		if msg.err != nil {
			m.chatErr = msg.err
			m.chatState = chatStateSelectGroup
			m.creationPendingGroupName = ""
			m.creationSelectedProvider = ""
			m.creationProviders = nil
			m.creationPicker = creationPickerState{}
			m.creationProvidersLoaded = false
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
		m = m.applyLayout()
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
				content: "[stream disconnected. press esc to return.]",
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

	case authStartedMsg:
		return m.handleAuthStarted(msg)

	case authEventMsg:
		return m.handleAuthEvent(msg)

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	return m, nil
}

// moveCursor advances the focused list cursor on the active tab.
func (m model) moveCursor(delta int) model {
	switch m.activeTab {
	case tabSandboxes:
		m.sandboxCursor = clampCursor(m.sandboxCursor+delta, len(m.sandboxes))
	case tabGroups:
		m.groupsCursor = clampCursor(m.groupsCursor+delta, len(m.groups))
	case tabTasks:
		m.tasksCursor = clampCursor(m.tasksCursor+delta, len(m.filteredTasks()))
	}
	return m
}

func clampCursor(n, size int) int {
	if size == 0 {
		return 0
	}
	if n < 0 {
		return 0
	}
	if n >= size {
		return size - 1
	}
	return n
}

// cycleTaskFilter rotates the group filter chip through the unique folders
// present in the task list (including an empty "all" state).
func (m *model) cycleTaskFilter() {
	seen := map[string]struct{}{}
	var folders []string
	for _, t := range m.tasks {
		if _, ok := seen[t.GroupFolder]; ok {
			continue
		}
		seen[t.GroupFolder] = struct{}{}
		folders = append(folders, t.GroupFolder)
	}
	sort.Strings(folders)
	if len(folders) == 0 {
		m.tasksFilterFolder = ""
		return
	}
	idx := -1
	for i, f := range folders {
		if f == m.tasksFilterFolder {
			idx = i
			break
		}
	}
	idx++
	if idx >= len(folders) {
		m.tasksFilterFolder = ""
	} else {
		m.tasksFilterFolder = folders[idx]
	}
	m.tasksCursor = 0
}

func (m model) filteredTasks() []TaskInfo {
	if m.tasksFilterFolder == "" {
		return m.tasks
	}
	out := make([]TaskInfo, 0, len(m.tasks))
	for _, t := range m.tasks {
		if t.GroupFolder == m.tasksFilterFolder {
			out = append(out, t)
		}
	}
	return out
}

func (m model) View() tea.View {
	var content string
	if m.width == 0 {
		content = "loading..."
	} else {
		var b strings.Builder

		b.WriteString(m.renderTabBar())
		b.WriteString("\n\n")

		contentHeight := m.height - 4
		if contentHeight < 3 {
			contentHeight = 3
		}
		rendered := m.renderContent()
		lines := strings.Split(rendered, "\n")
		if len(lines) > contentHeight && contentHeight > 0 {
			lines = lines[:contentHeight]
		}
		b.WriteString(strings.Join(lines, "\n"))

		currentLines := strings.Count(b.String(), "\n") + 1
		for i := currentLines; i < m.height-2; i++ {
			b.WriteString("\n")
		}

		b.WriteString(m.renderKeyBar())
		b.WriteString("\n")
		b.WriteString(m.renderStatusBar())
		content = b.String()
	}

	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m model) renderTabBar() string {
	out := m.renderTabBarWithLabels(m.tabs)
	if m.width <= 0 || lipgloss.Width(out) <= m.width {
		return out
	}

	compact := []string{"dash", "sbx", "grp", "tsk", "evt", "msg", "chn", "cfg"}
	out = m.renderTabBarWithLabels(compact)
	if lipgloss.Width(out) > m.width {
		out = lipgloss.NewStyle().MaxWidth(m.width).Render(out)
	}
	return out
}

func (m model) renderTabBarWithLabels(labels []string) string {
	var tabs []string
	for i, t := range labels {
		num := fmt.Sprintf("[%d]", i+1)
		if i == m.activeTab {
			numSpan := activeTabNumStyle.Render(num)
			labelSpan := activeTabStyle.Render(t)
			tabs = append(tabs, numSpan+labelSpan)
		} else {
			numSpan := inactiveTabNumStyle.Render(num)
			labelSpan := inactiveTabStyle.Render(t)
			tabs = append(tabs, numSpan+labelSpan)
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
}

func (m model) renderContent() string {
	switch m.activeTab {
	case tabDashboard:
		return m.renderDashboard()
	case tabSandboxes:
		if m.sandboxDetailOpen {
			return m.renderSandboxDetail()
		}
		return m.renderSandboxes()
	case tabGroups:
		return m.renderGroups()
	case tabTasks:
		return m.renderTasks()
	case tabEvents:
		return m.renderEvents()
	case tabMessages:
		return m.renderChat()
	case tabChannels:
		return m.renderChannels()
	case tabConfig:
		return m.renderConfig()
	default:
		return ""
	}
}

func (m model) contentWidth() int {
	if m.width > 4 {
		return m.width - 2
	}
	return m.width
}

// renderDashboard — design variant C. Big metric row + systems row + recent list.
func (m model) renderDashboard() string {
	w := m.contentWidth()
	var b strings.Builder

	b.WriteString(" " + coralStyle.Render("overview") + dimStyle.Render(" · "+m.serverAddr) + "\n\n")

	if m.statusErr != nil {
		b.WriteString("  " + errStyle.Render("connection error: "+m.statusErr.Error()) + "\n")
		b.WriteString("  " + dimStyle.Render("server may be offline. press 'r' to retry.") + "\n")
		return b.String()
	}

	if m.status == nil {
		if m.loading {
			b.WriteString("  " + m.spinner.View() + " connecting to server...\n")
		} else {
			b.WriteString("  " + dimStyle.Render("no status data available.") + "\n")
		}
		return b.String()
	}

	s := m.status

	// Big-metric row
	metrics := []string{
		bigMetric(formatCount(s.ActiveSandboxes), "", "active sandboxes"),
		bigMetric(formatCount(s.PendingMessages), "", "queued"),
		bigMetric(formatCount(s.ConnectedChannels), "", "channels"),
		bigMetric(formatCount(s.ActiveTasks), "", "active tasks"),
	}
	sep := "   " + dimStyle.Render("│") + "   "
	b.WriteString("  " + lipgloss.JoinHorizontal(lipgloss.Top, metrics[0], sep, metrics[1], sep, metrics[2], sep, metrics[3]))
	b.WriteString("\n\n")

	b.WriteString(sectionRule(w, "systems") + "\n  ")
	systems := []string{
		dotIndicator(boolState(m.statusErr == nil && m.status != nil)) + " grpc",
		dotIndicator(boolState(m.eventStream != nil && m.eventsErr == nil)) + " events",
		dotIndicator(boolState(s.MysqlConnected)) + " mysql",
		dotIndicator(boolState(s.K8sConnected)) + " k8s",
	}
	b.WriteString(strings.Join(systems, "    "))
	b.WriteString("\n\n")

	b.WriteString(sectionRule(w, "recent") + "\n")
	limit := 6
	if len(m.events) < limit {
		limit = len(m.events)
	}
	if limit == 0 {
		b.WriteString("  " + dimStyle.Render("no events yet.") + "\n")
	}
	for i := 0; i < limit; i++ {
		e := m.events[i]
		ts := e.Timestamp
		if len(ts) >= 19 {
			ts = ts[11:19]
		}
		fmt.Fprintf(&b, "  %s  %s  %s\n",
			dimStyle.Render(ts),
			krakenStyle.Render(padRight(truncateWidth(e.Source, 14), 14)),
			truncateWidth(e.Message, w-30),
		)
	}

	return b.String()
}

// renderSandboxes — design variant A: dense table + inspector panel.
func (m model) renderSandboxes() string {
	w := m.contentWidth()
	var b strings.Builder

	counts := map[string]int{}
	for _, s := range m.sandboxes {
		counts[strings.ToLower(s.State)]++
	}
	summary := fmt.Sprintf("%d running · %d other", counts["running"], len(m.sandboxes)-counts["running"])
	b.WriteString(" " + coralStyle.Render("sandboxes") + " " + dimStyle.Render("· "+summary) + "\n\n")

	if m.sandboxesErr != nil {
		b.WriteString("  " + errStyle.Render("error: "+m.sandboxesErr.Error()) + "\n")
		return b.String()
	}
	if len(m.sandboxes) == 0 {
		b.WriteString(sectionRule(w, "") + "\n")
		b.WriteString("  " + dimStyle.Render("no sandboxes running.") + "\n")
		return b.String()
	}

	b.WriteString(sectionRule(w, "") + "\n")
	headerCols := fmt.Sprintf("  %-10s %-18s %-10s %-10s %-10s",
		"ID", "GROUP", "STATE", "STARTED", "SESSION")
	b.WriteString(dimStyle.Render(headerCols) + "\n")
	b.WriteString(sectionRule(w, "") + "\n")

	for i, s := range m.sandboxes {
		row := fmt.Sprintf("  %-10s %-18s %-10s %-10s %-10s",
			truncateWidth(s.Name, 10),
			truncateWidth(s.GroupFolder, 18),
			truncateWidth(s.State, 10),
			truncateWidth(shortTime(s.StartTime), 10),
			truncateWidth(s.SessionID, 10),
		)
		if i == m.sandboxCursor {
			b.WriteString(selStrongStyle.Render(padRight(row, w)))
		} else {
			b.WriteString(fgStyle.Render(row))
		}
		b.WriteString("\n")
	}

	// Inspector
	b.WriteString("\n" + sectionRule(w, "inspector") + "\n")
	if m.sandboxCursor < len(m.sandboxes) {
		s := m.sandboxes[m.sandboxCursor]
		b.WriteString("  " + dimStyle.Render("id        ") + fgStyle.Render(s.Name) + "\n")
		b.WriteString("  " + dimStyle.Render("group     ") + fgStyle.Render(s.GroupFolder) + "  " + dimStyle.Render(s.GroupJID) + "\n")
		b.WriteString("  " + dimStyle.Render("state     ") + stateStyle(s.State).Render(s.State) + "\n")
		b.WriteString("  " + dimStyle.Render("started   ") + fgStyle.Render(s.StartTime) + "\n")
		if s.EndTime != "" {
			b.WriteString("  " + dimStyle.Render("ended     ") + fgStyle.Render(s.EndTime) + "\n")
		}
		b.WriteString("  " + dimStyle.Render("session   ") + fgStyle.Render(s.SessionID) + "\n")
	}

	return b.String()
}

// renderSandboxDetail — Enter drill-down stub. Fields we don't have yet are
// dim "—" placeholders and we name the RPC the server needs.
func (m model) renderSandboxDetail() string {
	w := m.contentWidth()
	var b strings.Builder

	name := "(none)"
	if m.sandboxCursor < len(m.sandboxes) {
		name = m.sandboxes[m.sandboxCursor].Name
	}
	b.WriteString(" " + coralStyle.Render("sandbox") + " · " + fgStyle.Render(name) + "\n\n")

	b.WriteString(sectionRule(w, "resources") + "\n")
	b.WriteString("  " + dimStyle.Render("cpu       —") + "\n")
	b.WriteString("  " + dimStyle.Render("memory    —") + "\n")
	b.WriteString("  " + dimStyle.Render("node      —") + "\n\n")

	b.WriteString(sectionRule(w, "ipc") + "\n")
	b.WriteString("  " + dimStyle.Render("inbound   —") + "\n")
	b.WriteString("  " + dimStyle.Render("outbound  —") + "\n\n")

	b.WriteString(sectionRule(w, "log tail") + "\n")
	b.WriteString("  " + dimStyle.Render("—") + "\n\n")

	b.WriteString("  " + stubMessageStyle.Render("needs GetSandboxDetail + StreamLogs RPCs on AdminService") + "\n")
	return b.String()
}

// renderGroups — design variant C: per-group focus view.
func (m model) renderGroups() string {
	w := m.contentWidth()
	var b strings.Builder

	b.WriteString(" " + coralStyle.Render("groups") + " " + dimStyle.Render(fmt.Sprintf("· %d registered", len(m.groups))) + "\n\n")

	if m.groupsErr != nil {
		b.WriteString("  " + errStyle.Render("error: "+m.groupsErr.Error()) + "\n")
		return b.String()
	}
	if len(m.groups) == 0 {
		b.WriteString("  " + dimStyle.Render("no groups registered.") + "\n")
		return b.String()
	}

	// Left column: list of groups. Right column: details.
	listW := 28
	if listW > w/3 {
		listW = w / 3
	}

	var left strings.Builder
	for i, g := range m.groups {
		name := g.Name
		if name == "" {
			name = g.Folder
		}
		line := truncateWidth(name, listW-2)
		if i == m.groupsCursor {
			left.WriteString(selStrongStyle.Render(padRight("  "+line, listW)))
		} else {
			left.WriteString(fgStyle.Render(padRight("  "+line, listW)))
		}
		left.WriteString("\n")
	}

	var right strings.Builder
	sel := m.groups[m.groupsCursor]
	name := sel.Name
	if name == "" {
		name = sel.Folder
	}
	detailW := w - listW - 2
	right.WriteString(sectionRule(detailW, "identity") + "\n")
	right.WriteString("  " + dimStyle.Render("name     ") + fgStyle.Render(name) + "\n")
	right.WriteString("  " + dimStyle.Render("folder   ") + fgStyle.Render(sel.Folder) + "\n")
	right.WriteString("  " + dimStyle.Render("jid      ") + fgStyle.Render(sel.JID) + "\n")
	if sel.TriggerPattern != "" {
		right.WriteString("  " + dimStyle.Render("trigger  ") + fgStyle.Render(sel.TriggerPattern) + "\n")
	}
	right.WriteString("  " + dimStyle.Render("main     ") + fgStyle.Render(yesNo(sel.IsMain)) + "\n\n")

	right.WriteString(sectionRule(detailW, "active sandbox") + "\n")
	var sb *SandboxInfo
	for i := range m.sandboxes {
		if m.sandboxes[i].GroupJID == sel.JID {
			sb = &m.sandboxes[i]
			break
		}
	}
	if sb == nil {
		right.WriteString("  " + dimStyle.Render("none") + "\n\n")
	} else {
		right.WriteString("  " + dimStyle.Render("id       ") + fgStyle.Render(sb.Name) + "\n")
		right.WriteString("  " + dimStyle.Render("state    ") + stateStyle(sb.State).Render(sb.State) + "\n")
		right.WriteString("  " + dimStyle.Render("started  ") + fgStyle.Render(sb.StartTime) + "\n\n")
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, left.String(), "  ", right.String())
}

// renderTasks — design variant A: table + inspector + group filter chip.
func (m model) renderTasks() string {
	w := m.contentWidth()
	var b strings.Builder

	filterChip := "all groups"
	if m.tasksFilterFolder != "" {
		filterChip = m.tasksFilterFolder
	}
	b.WriteString(" " + coralStyle.Render("tasks") + " " +
		dimStyle.Render(fmt.Sprintf("· %d scheduled", len(m.tasks))) +
		"  " + dimStyle.Render("filter:") + " " + coralStyle.Render(filterChip) + "\n\n")

	if m.tasksErr != nil {
		b.WriteString("  " + errStyle.Render("error: "+m.tasksErr.Error()) + "\n")
		return b.String()
	}

	tasks := m.filteredTasks()
	b.WriteString(sectionRule(w, "") + "\n")
	header := fmt.Sprintf("  %-10s %-14s %-8s %-14s %-8s %-18s",
		"ID", "GROUP", "TYPE", "SCHEDULE", "STATUS", "NEXT RUN")
	b.WriteString(dimStyle.Render(header) + "\n")
	b.WriteString(sectionRule(w, "") + "\n")

	if len(tasks) == 0 {
		b.WriteString("  " + dimStyle.Render("no tasks scheduled.") + "\n")
		return b.String()
	}

	for i, t := range tasks {
		row := fmt.Sprintf("  %-10s %-14s %-8s %-14s %-8s %-18s",
			truncateWidth(t.ID, 10),
			truncateWidth(t.GroupFolder, 14),
			truncateWidth(t.ScheduleType, 8),
			truncateWidth(t.ScheduleValue, 14),
			truncateWidth(t.Status, 8),
			truncateWidth(shortTime(t.NextRun), 18),
		)
		if i == m.tasksCursor {
			b.WriteString(selStrongStyle.Render(padRight(row, w)))
		} else {
			b.WriteString(fgStyle.Render(row))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n" + sectionRule(w, "inspector") + "\n")
	if m.tasksCursor < len(tasks) {
		t := tasks[m.tasksCursor]
		b.WriteString("  " + dimStyle.Render("id       ") + fgStyle.Render(t.ID) + "\n")
		b.WriteString("  " + dimStyle.Render("group    ") + fgStyle.Render(t.GroupFolder) + "\n")
		b.WriteString("  " + dimStyle.Render("chat     ") + fgStyle.Render(t.ChatJID) + "\n")
		b.WriteString("  " + dimStyle.Render("schedule ") + fgStyle.Render(t.ScheduleType+" "+t.ScheduleValue) + "\n")
		b.WriteString("  " + dimStyle.Render("next run ") + fgStyle.Render(t.NextRun) + "\n")
		if t.Prompt != "" {
			b.WriteString("  " + dimStyle.Render("prompt   ") + fgStyle.Render(truncateWidth(t.Prompt, w-12)) + "\n")
		}
	}

	return b.String()
}

// renderEvents — restyled single-pane event stream.
func (m model) renderEvents() string {
	w := m.contentWidth()
	var b strings.Builder

	b.WriteString(" " + coralStyle.Render("events") + " " +
		dimStyle.Render(fmt.Sprintf("· %d buffered", len(m.events))) + "\n\n")

	if m.eventsErr != nil {
		b.WriteString("  " + errStyle.Render("stream error: "+m.eventsErr.Error()) + "\n")
		b.WriteString("  " + dimStyle.Render("waiting to reconnect...") + "\n")
		return b.String()
	}

	b.WriteString(sectionRule(w, "") + "\n")
	if len(m.events) == 0 {
		b.WriteString("  " + dimStyle.Render("no events recorded yet.") + "\n")
		return b.String()
	}
	for _, e := range m.events {
		ts := e.Timestamp
		if len(ts) >= 19 {
			ts = ts[11:19]
		}
		typ := strings.ToLower(e.Type)
		var typStyle lipgloss.Style
		switch {
		case strings.Contains(typ, "error"), strings.Contains(typ, "fail"):
			typStyle = errStyle
		case strings.Contains(typ, "warn"):
			typStyle = warnStyle
		case strings.Contains(typ, "ok"), strings.Contains(typ, "ready"):
			typStyle = okStyle
		default:
			typStyle = fgStyle
		}
		line := fmt.Sprintf("  %s  %s  %s  %s",
			dimStyle.Render(ts),
			krakenStyle.Render(padRight(truncateWidth(e.Source, 14), 14)),
			typStyle.Render(padRight(truncateWidth(e.Type, 12), 12)),
			truncateWidth(e.Message, w-42),
		)
		b.WriteString(line + "\n")
	}

	return b.String()
}

// renderChannels — stub. Shell with coral line naming the RPC we need.
func (m model) renderChannels() string {
	w := m.contentWidth()
	var b strings.Builder

	b.WriteString(" " + coralStyle.Render("channels") + " " + dimStyle.Render("· platform adapters") + "\n\n")
	b.WriteString(sectionRule(w, "adapters") + "\n")
	b.WriteString("  " + dimStyle.Render(fmt.Sprintf("%-12s %-10s %-10s %-10s", "NAME", "STATE", "LAG", "LAST SEEN")) + "\n")
	b.WriteString(sectionRule(w, "") + "\n")
	b.WriteString("  " + dimStyle.Render("—") + "\n\n")

	b.WriteString("  " + stubMessageStyle.Render("needs ListChannels RPC + heartbeat metrics on Channel interface") + "\n")
	return b.String()
}

// renderConfig — stub. Section rules + coral line naming the RPC.
func (m model) renderConfig() string {
	w := m.contentWidth()
	var b strings.Builder

	b.WriteString(" " + coralStyle.Render("config") + " " + dimStyle.Render("· resolved environment") + "\n\n")

	for _, section := range []string{"core", "grpc", "nats", "mysql", "kubernetes", "credproxy"} {
		b.WriteString(sectionRule(w, section) + "\n")
		b.WriteString("  " + dimStyle.Render("—") + "\n\n")
	}

	b.WriteString("  " + stubMessageStyle.Render("needs GetConfig RPC returning redacted envconfig") + "\n")
	return b.String()
}

// renderKeyBar is the per-screen key-hint footer above the status bar.
func (m model) renderKeyBar() string {
	hints := m.keyHints()
	return keyBar(hints, m.width)
}

func (m model) keyHints() [][2]string {
	switch m.activeTab {
	case tabDashboard:
		return [][2]string{{"[1-8]", "switch"}, {"[r]", "refresh"}, {"[^t]", "theme"}, {"[q]", "quit"}}
	case tabSandboxes:
		if m.sandboxDetailOpen {
			return [][2]string{{"[esc]", "back"}, {"[r]", "refresh"}, {"[q]", "quit"}}
		}
		return [][2]string{{"[⏎]", "detail"}, {"[j/k]", "nav"}, {"[r]", "refresh"}, {"[^t]", "theme"}, {"[q]", "quit"}}
	case tabGroups:
		return [][2]string{{"[j/k]", "nav"}, {"[r]", "refresh"}, {"[^t]", "theme"}, {"[q]", "quit"}}
	case tabTasks:
		return [][2]string{{"[j/k]", "nav"}, {"[g]", "filter"}, {"[r]", "refresh"}, {"[^t]", "theme"}, {"[q]", "quit"}}
	case tabEvents:
		return [][2]string{{"[r]", "refresh"}, {"[^t]", "theme"}, {"[q]", "quit"}}
	case tabMessages:
		return m.messagesKeyHints()
	case tabChannels, tabConfig:
		return [][2]string{{"[1-8]", "switch"}, {"[^t]", "theme"}, {"[q]", "quit"}}
	}
	return nil
}

func (m model) messagesKeyHints() [][2]string {
	switch m.chatState {
	case chatStateChatting:
		return [][2]string{{"[⏎]", "send"}, {"[^m]", "model"}, {"[esc]", "back"}, {"[^t]", "theme"}, {"[^c]", "quit"}}
	case chatStateSelectGroup:
		return [][2]string{{"[⏎]", "select"}, {"[n]", "new"}, {"[j/k]", "nav"}, {"[^t]", "theme"}, {"[q]", "quit"}}
	default:
		return [][2]string{{"[⏎]", "ok"}, {"[esc]", "back"}, {"[^t]", "theme"}, {"[^c]", "quit"}}
	}
}

// renderStatusBar: kraclaw ctl │ grpc://addr │ ● TLS │ user@host │ UTC HH:MM:SS │ theme:dark
func (m model) renderStatusBar() string {
	tlsCell := dimStyle.Render("● insecure")
	if m.useTLS {
		tlsCell = okStyle.Render("● TLS")
	}
	cells := []string{
		coralBold.Render("kraclaw") + " ctl",
		"grpc://" + m.serverAddr,
		tlsCell,
		userAtHost(),
		"UTC " + m.now.UTC().Format("15:04:05"),
		"theme:" + activePalette.Name,
	}
	return statusLine(cells, m.width)
}

func userAtHost() string {
	u := "user"
	if current, err := user.Current(); err == nil && current.Username != "" {
		u = current.Username
	}
	h, err := os.Hostname()
	if err != nil || h == "" {
		h = "local"
	}
	// Keep hostname short.
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	return u + "@" + h
}

// shortTime returns the HH:MM:SS portion of a "YYYY-MM-DD HH:MM:SS" timestamp,
// or the input as-is when the format doesn't match.
func shortTime(ts string) string {
	if len(ts) >= 19 {
		return ts[11:19]
	}
	return ts
}

// stateStyle picks a foreground style for a sandbox/task state.
func stateStyle(state string) lipgloss.Style {
	switch strings.ToLower(state) {
	case "running", "ready", "completed":
		return okStyle
	case "failed", "error", "errored":
		return errStyle
	case "starting", "pending", "paused", "waiting":
		return warnStyle
	default:
		return fgStyle
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
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

	dbg("=== TCP connectivity ===")
	tcpConn, err := net.DialTimeout("tcp", serverAddr, 3*time.Second)
	if err != nil {
		dbg("  TCP dial %s failed: %v", serverAddr, err)
		dbg("Stopping diagnostics — server not reachable.")
		return
	}
	_ = tcpConn.Close()
	dbg("  TCP dial %s: OK", serverAddr)

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

	m := initialModel(*serverAddr, api)
	m.useTLS = !*insecure
	p := tea.NewProgram(m)

	if _, err := p.Run(); err != nil {
		slog.Error("TUI exited with error", "error", err)
		os.Exit(1)
	}
}
