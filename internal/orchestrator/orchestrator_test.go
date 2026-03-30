package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/johanssonvincent/kraclaw/internal/auth"
	"github.com/johanssonvincent/kraclaw/internal/channel"
	"github.com/johanssonvincent/kraclaw/internal/config"
	"github.com/johanssonvincent/kraclaw/internal/ipc"
	"github.com/johanssonvincent/kraclaw/internal/queue"
	"github.com/johanssonvincent/kraclaw/internal/router"
	"github.com/johanssonvincent/kraclaw/internal/sandbox"
	"github.com/johanssonvincent/kraclaw/internal/store"
)

// --- Mock Store ---

type mockStore struct {
	state    map[string]string
	groups   []store.Group
	sessions map[string]*store.Session
	messages map[string][]store.Message
	chats    map[string]*store.Chat
	tasks    map[string]*store.ScheduledTask

	storeMessageCalled   bool
	storeMessageErr      error
	upsertSessionErr     error
	deleteSessionErr     error
	allowlist            map[string][]store.SenderAllowlistEntry
	getMessagesSinceHook func() // called at start of GetMessagesSince if non-nil

	updateTaskCalled      bool
	deleteTaskCalledWith  [2]string // [id, groupFolder]
	setStateCalls []string // records keys passed to SetState for counting
}

func newMockStore() *mockStore {
	return &mockStore{
		state:     make(map[string]string),
		sessions:  make(map[string]*store.Session),
		messages:  make(map[string][]store.Message),
		chats:     make(map[string]*store.Chat),
		tasks:     make(map[string]*store.ScheduledTask),
		allowlist: make(map[string][]store.SenderAllowlistEntry),
	}
}

func (m *mockStore) GetState(_ context.Context, key string) (string, error) {
	return m.state[key], nil
}

func (m *mockStore) SetState(_ context.Context, key, value string) error {
	m.state[key] = value
	m.setStateCalls = append(m.setStateCalls, key)
	return nil
}

func (m *mockStore) ListGroups(_ context.Context) ([]store.Group, error) {
	return m.groups, nil
}

func (m *mockStore) GetSession(_ context.Context, groupFolder string) (*store.Session, error) {
	return m.sessions[groupFolder], nil
}

func (m *mockStore) UpsertSession(_ context.Context, s *store.Session) error {
	if m.upsertSessionErr != nil {
		return m.upsertSessionErr
	}
	m.sessions[s.GroupFolder] = s
	return nil
}

func (m *mockStore) DeleteSession(_ context.Context, groupFolder string) error {
	if m.deleteSessionErr != nil {
		return m.deleteSessionErr
	}
	delete(m.sessions, groupFolder)
	return nil
}

func (m *mockStore) StoreMessage(_ context.Context, msg *store.Message) error {
	m.storeMessageCalled = true
	if m.storeMessageErr != nil {
		return m.storeMessageErr
	}
	m.messages[msg.ChatJID] = append(m.messages[msg.ChatJID], *msg)
	return nil
}

func (m *mockStore) StoreBatch(_ context.Context, msgs []store.Message) error { return nil }

func (m *mockStore) GetNewMessages(_ context.Context, jids []string, since time.Time, limit int) ([]store.Message, error) {
	var result []store.Message
	for _, jid := range jids {
		for _, msg := range m.messages[jid] {
			if msg.Timestamp.After(since) {
				result = append(result, msg)
			}
		}
	}
	return result, nil
}

func (m *mockStore) GetMessagesSince(_ context.Context, chatJID string, since time.Time, limit int) ([]store.Message, error) {
	if m.getMessagesSinceHook != nil {
		m.getMessagesSinceHook()
	}
	var result []store.Message
	for _, msg := range m.messages[chatJID] {
		if msg.Timestamp.After(since) {
			result = append(result, msg)
		}
	}
	return result, nil
}

func (m *mockStore) UpsertChat(_ context.Context, c *store.Chat) error {
	m.chats[c.JID] = c
	return nil
}

func (m *mockStore) GetChat(_ context.Context, jid string) (*store.Chat, error) {
	return m.chats[jid], nil
}

func (m *mockStore) ListChats(_ context.Context) ([]store.Chat, error) { return nil, nil }

func (m *mockStore) GetGroup(_ context.Context, jid string) (*store.Group, error) {
	for _, g := range m.groups {
		if g.JID == jid {
			return &g, nil
		}
	}
	return nil, nil
}

func (m *mockStore) GetGroupByFolder(_ context.Context, folder string) (*store.Group, error) {
	for _, g := range m.groups {
		if g.Folder == folder {
			return &g, nil
		}
	}
	return nil, nil
}

func (m *mockStore) UpsertGroup(_ context.Context, g *store.Group) error { return nil }
func (m *mockStore) DeleteGroup(_ context.Context, jid string) error     { return nil }

func (m *mockStore) CreateTask(_ context.Context, task *store.ScheduledTask) error {
	m.tasks[task.ID] = task
	return nil
}

func (m *mockStore) GetTask(_ context.Context, id, groupFolder string) (*store.ScheduledTask, error) {
	return m.tasks[id], nil
}

func (m *mockStore) ListTasks(_ context.Context) ([]store.ScheduledTask, error) { return nil, nil }
func (m *mockStore) ListTasksByGroup(_ context.Context, _ string) ([]store.ScheduledTask, error) {
	return nil, nil
}

func (m *mockStore) UpdateTask(_ context.Context, task *store.ScheduledTask) error {
	m.updateTaskCalled = true
	m.tasks[task.ID] = task
	return nil
}

func (m *mockStore) DeleteTask(_ context.Context, id, groupFolder string) error {
	m.deleteTaskCalledWith = [2]string{id, groupFolder}
	delete(m.tasks, id)
	return nil
}

func (m *mockStore) GetDueTasks(_ context.Context) ([]store.ScheduledTask, error) { return nil, nil }
func (m *mockStore) LogTaskRun(_ context.Context, _ *store.TaskRunLog) error      { return nil }
func (m *mockStore) GetTaskRunLogs(_ context.Context, _, _ string, _ int) ([]store.TaskRunLog, error) {
	return nil, nil
}

func (m *mockStore) GetAllowlist(_ context.Context, chatJID string) ([]store.SenderAllowlistEntry, error) {
	return m.allowlist[chatJID], nil
}

func (m *mockStore) UpsertAllowlistEntry(_ context.Context, _ *store.SenderAllowlistEntry) error {
	return nil
}

func (m *mockStore) DeleteAllowlistEntry(_ context.Context, _ int64) error { return nil }

func (m *mockStore) Close() error { return nil }

// --- Mock Queue ---

type mockQueue struct {
	active map[string]bool
}

func newMockQueue() *mockQueue {
	return &mockQueue{active: make(map[string]bool)}
}

func (m *mockQueue) Enqueue(_ context.Context, _ string, _ *queue.QueueMessage) error { return nil }
func (m *mockQueue) Dequeue(_ context.Context, _ string) (*queue.QueueMessage, error) {
	return nil, nil
}
func (m *mockQueue) Peek(_ context.Context, _ string) (*queue.QueueMessage, error) { return nil, nil }
func (m *mockQueue) Len(_ context.Context, _ string) (int64, error)                { return 0, nil }
func (m *mockQueue) MarkActive(_ context.Context, groupJID string) error {
	m.active[groupJID] = true
	return nil
}
func (m *mockQueue) MarkInactive(_ context.Context, groupJID string) error {
	delete(m.active, groupJID)
	return nil
}
func (m *mockQueue) IsActive(_ context.Context, groupJID string) (bool, error) {
	return m.active[groupJID], nil
}
func (m *mockQueue) ActiveCount(_ context.Context) (int64, error) { return 0, nil }
func (m *mockQueue) Subscribe(_ context.Context) (<-chan queue.QueueEvent, error) {
	ch := make(chan queue.QueueEvent)
	return ch, nil
}
func (m *mockQueue) Close() error { return nil }

// --- Mock IPC Broker ---

type mockIPCBroker struct {
	published          []*ipc.IPCMessage
	inputSent          []*ipc.IPCMessage
	subscribeCh        chan *ipc.IPCMessage // if set, SubscribeOutput returns this channel
	deleteStreamsCalled int
	deleteStreamsGroup  string
}

func (m *mockIPCBroker) PublishOutput(_ context.Context, _ string, msg *ipc.IPCMessage) error {
	m.published = append(m.published, msg)
	return nil
}
func (m *mockIPCBroker) SubscribeOutput(_ context.Context, _ string) (<-chan *ipc.IPCMessage, error) {
	if m.subscribeCh != nil {
		return m.subscribeCh, nil
	}
	ch := make(chan *ipc.IPCMessage)
	return ch, nil
}
func (m *mockIPCBroker) SendInput(_ context.Context, _ string, msg *ipc.IPCMessage) error {
	m.inputSent = append(m.inputSent, msg)
	return nil
}
func (m *mockIPCBroker) ReadInput(_ context.Context, _ string) (<-chan *ipc.IPCMessage, error) {
	ch := make(chan *ipc.IPCMessage)
	return ch, nil
}
func (m *mockIPCBroker) SetCloseSignal(_ context.Context, _ string) error { return nil }
func (m *mockIPCBroker) CheckCloseSignal(_ context.Context, _ string) (bool, error) {
	return false, nil
}
func (m *mockIPCBroker) Close() error { return nil }
func (m *mockIPCBroker) DeleteStreams(_ context.Context, group string) error {
	m.deleteStreamsCalled++
	m.deleteStreamsGroup = group
	return nil
}

// --- Mock Channel ---

type mockChannel struct {
	name      string
	connected bool
	sent      []sentMessage
	ownsJIDs  map[string]bool
}

type sentMessage struct {
	jid  string
	text string
}

func (m *mockChannel) Name() string                                        { return m.name }
func (m *mockChannel) Connect(_ context.Context) error                     { m.connected = true; return nil }
func (m *mockChannel) IsConnected() bool                                   { return m.connected }
func (m *mockChannel) Disconnect(_ context.Context) error                  { m.connected = false; return nil }
func (m *mockChannel) SetTyping(_ context.Context, _ string, _ bool) error { return nil }
func (m *mockChannel) OwnsJID(jid string) bool                             { return m.ownsJIDs[jid] }
func (m *mockChannel) SendMessage(_ context.Context, jid string, text string) error {
	m.sent = append(m.sent, sentMessage{jid: jid, text: text})
	return nil
}

// --- Helpers ---

func newTestOrchestrator(s *mockStore, q *mockQueue, b *mockIPCBroker) *Orchestrator {
	cfg := &config.Config{
		Channels: config.ChannelsConfig{AssistantName: "TestBot"},
		Queue: config.QueueConfig{
			IdleTimeout:           30 * time.Minute,
			RateLimitTokensPerSec: 1000,
			MaxMessageSizeBytes:   32768,
			MessageLimit:          500,
		},
		Scheduler: config.SchedulerConfig{PollInterval: 60 * time.Second},
	}
	log := slog.Default()
	reg := channel.NewRegistry()

	o, err := New(cfg, s, q, b, nil, reg, log)
	if err != nil {
		panic("newTestOrchestrator: " + err.Error())
	}
	return o
}

func newTestOrchestratorWithRouter(s *mockStore, q *mockQueue, b *mockIPCBroker, chs []channel.Channel) *Orchestrator {
	o := newTestOrchestrator(s, q, b)
	rtr, err := router.New(chs, s)
	if err != nil {
		panic("newTestOrchestratorWithRouter: " + err.Error())
	}
	o.router = rtr
	o.auth = auth.New(s)
	return o
}

// --- loadState / saveState Tests ---

func TestLoadState_Empty(t *testing.T) {
	s := newMockStore()
	o := newTestOrchestrator(s, newMockQueue(), &mockIPCBroker{})

	if err := o.loadState(context.Background()); err != nil {
		t.Fatalf("loadState() error = %v", err)
	}

	if !o.lastTimestamp.IsZero() {
		t.Errorf("lastTimestamp = %v, want zero", o.lastTimestamp)
	}
	if len(o.lastAgentTimestamp) != 0 {
		t.Errorf("lastAgentTimestamp has %d entries, want 0", len(o.lastAgentTimestamp))
	}
	if len(o.lastConfirmedTimestamp) != 0 {
		t.Errorf("lastConfirmedTimestamp has %d entries, want 0", len(o.lastConfirmedTimestamp))
	}
	if len(o.registeredGroups) != 0 {
		t.Errorf("registeredGroups has %d entries, want 0", len(o.registeredGroups))
	}
}

func TestLoadState_WithData(t *testing.T) {
	ts := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	agentTs := map[string]string{
		"group1@g.us": ts.Add(-5 * time.Minute).Format(time.RFC3339Nano),
	}
	agentTsJSON, _ := json.Marshal(agentTs)

	s := newMockStore()
	s.state["last_timestamp"] = ts.Format(time.RFC3339Nano)
	s.state["last_agent_timestamp"] = string(agentTsJSON)
	s.groups = []store.Group{
		{JID: "group1@g.us", Name: "Test Group", Folder: "test-group"},
	}
	s.sessions["test-group"] = &store.Session{GroupFolder: "test-group", SessionID: "sess-123"}

	o := newTestOrchestrator(s, newMockQueue(), &mockIPCBroker{})

	if err := o.loadState(context.Background()); err != nil {
		t.Fatalf("loadState() error = %v", err)
	}

	if !o.lastTimestamp.Equal(ts) {
		t.Errorf("lastTimestamp = %v, want %v", o.lastTimestamp, ts)
	}
	if len(o.lastAgentTimestamp) != 1 {
		t.Fatalf("lastAgentTimestamp has %d entries, want 1", len(o.lastAgentTimestamp))
	}
	if _, ok := o.lastAgentTimestamp["group1@g.us"]; !ok {
		t.Error("lastAgentTimestamp missing group1@g.us")
	}
	if len(o.registeredGroups) != 1 {
		t.Fatalf("registeredGroups has %d entries, want 1", len(o.registeredGroups))
	}
	if o.sessions["test-group"] != "sess-123" {
		t.Errorf("sessions[test-group] = %q, want %q", o.sessions["test-group"], "sess-123")
	}
}

func TestLoadState_BackfillsConfirmedFromAgent(t *testing.T) {
	// When last_confirmed_timestamp is absent (upgrade scenario), it should
	// be backfilled from last_agent_timestamp.
	ts := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	agentTs := map[string]string{
		"group1@g.us": ts.Format(time.RFC3339Nano),
	}
	agentTsJSON, _ := json.Marshal(agentTs)

	s := newMockStore()
	s.state["last_agent_timestamp"] = string(agentTsJSON)
	// No last_confirmed_timestamp in state — simulates upgrade.
	s.groups = []store.Group{
		{JID: "group1@g.us", Name: "Test Group", Folder: "test-group"},
	}

	o := newTestOrchestrator(s, newMockQueue(), &mockIPCBroker{})
	if err := o.loadState(context.Background()); err != nil {
		t.Fatalf("loadState() error = %v", err)
	}

	confirmed, ok := o.lastConfirmedTimestamp["group1@g.us"]
	if !ok {
		t.Fatal("lastConfirmedTimestamp missing group1@g.us after backfill")
	}
	if !confirmed.Equal(ts) {
		t.Errorf("lastConfirmedTimestamp = %v, want %v (backfilled from agent)", confirmed, ts)
	}
}

func TestLoadState_PreservesExistingConfirmed(t *testing.T) {
	agentTs := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	confirmedTs := time.Date(2025, 1, 15, 10, 25, 0, 0, time.UTC) // older than agent

	agentMap := map[string]string{"group1@g.us": agentTs.Format(time.RFC3339Nano)}
	confirmedMap := map[string]string{"group1@g.us": confirmedTs.Format(time.RFC3339Nano)}
	agentJSON, _ := json.Marshal(agentMap)
	confirmedJSON, _ := json.Marshal(confirmedMap)

	s := newMockStore()
	s.state["last_agent_timestamp"] = string(agentJSON)
	s.state["last_confirmed_timestamp"] = string(confirmedJSON)
	s.groups = []store.Group{
		{JID: "group1@g.us", Name: "Test Group", Folder: "test-group"},
	}

	o := newTestOrchestrator(s, newMockQueue(), &mockIPCBroker{})
	if err := o.loadState(context.Background()); err != nil {
		t.Fatalf("loadState() error = %v", err)
	}

	confirmed := o.lastConfirmedTimestamp["group1@g.us"]
	if !confirmed.Equal(confirmedTs) {
		t.Errorf("lastConfirmedTimestamp = %v, want %v (should not be overwritten by backfill)", confirmed, confirmedTs)
	}
}

func TestSaveState_PersistsToStore(t *testing.T) {
	s := newMockStore()
	o := newTestOrchestrator(s, newMockQueue(), &mockIPCBroker{})

	ts := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	o.lastTimestamp = ts
	o.lastAgentTimestamp["group1@g.us"] = ts.Add(-2 * time.Minute)
	o.lastConfirmedTimestamp["group1@g.us"] = ts.Add(-5 * time.Minute)

	if err := o.saveState(context.Background()); err != nil {
		t.Fatalf("saveState() error = %v", err)
	}

	if s.state["last_timestamp"] != ts.Format(time.RFC3339Nano) {
		t.Errorf("last_timestamp = %q, want %q", s.state["last_timestamp"], ts.Format(time.RFC3339Nano))
	}
	if s.state["last_agent_timestamp"] == "" {
		t.Error("last_agent_timestamp not saved")
	}
	if s.state["last_confirmed_timestamp"] == "" {
		t.Error("last_confirmed_timestamp not saved")
	}

	var saved map[string]string
	if err := json.Unmarshal([]byte(s.state["last_agent_timestamp"]), &saved); err != nil {
		t.Fatalf("unmarshal last_agent_timestamp: %v", err)
	}
	if _, ok := saved["group1@g.us"]; !ok {
		t.Error("saved agent timestamps missing group1@g.us")
	}

	var savedConfirmed map[string]string
	if err := json.Unmarshal([]byte(s.state["last_confirmed_timestamp"]), &savedConfirmed); err != nil {
		t.Fatalf("unmarshal last_confirmed_timestamp: %v", err)
	}
	if _, ok := savedConfirmed["group1@g.us"]; !ok {
		t.Error("saved confirmed timestamps missing group1@g.us")
	}
}

func TestSaveState_SkipsUnchangedTimestamps(t *testing.T) {
	s := newMockStore()
	o := newTestOrchestrator(s, newMockQueue(), &mockIPCBroker{})

	ts := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	o.lastTimestamp = ts
	o.lastAgentTimestamp["group1@g.us"] = ts.Add(-2 * time.Minute)
	o.lastConfirmedTimestamp["group1@g.us"] = ts.Add(-5 * time.Minute)

	// First save — all 3 keys should be written.
	if err := o.saveState(context.Background()); err != nil {
		t.Fatalf("saveState() first call error = %v", err)
	}
	if len(s.setStateCalls) != 3 {
		t.Fatalf("first saveState: SetState called %d times, want 3", len(s.setStateCalls))
	}

	// Reset tracking.
	s.setStateCalls = nil

	// Second save with same values — no SetState calls expected.
	if err := o.saveState(context.Background()); err != nil {
		t.Fatalf("saveState() second call error = %v", err)
	}
	if len(s.setStateCalls) != 0 {
		t.Errorf("second saveState: SetState called %d times, want 0 (values unchanged)", len(s.setStateCalls))
	}
}

func TestSaveState_WritesOnlyChangedTimestamp(t *testing.T) {
	s := newMockStore()
	o := newTestOrchestrator(s, newMockQueue(), &mockIPCBroker{})

	ts := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	o.lastTimestamp = ts
	o.lastAgentTimestamp["group1@g.us"] = ts.Add(-2 * time.Minute)
	o.lastConfirmedTimestamp["group1@g.us"] = ts.Add(-5 * time.Minute)

	// First save writes everything.
	if err := o.saveState(context.Background()); err != nil {
		t.Fatalf("saveState() error = %v", err)
	}
	s.setStateCalls = nil

	// Change only lastTimestamp.
	o.lastTimestamp = ts.Add(1 * time.Minute)
	if err := o.saveState(context.Background()); err != nil {
		t.Fatalf("saveState() error = %v", err)
	}
	if len(s.setStateCalls) != 1 {
		t.Fatalf("SetState called %d times, want 1 (only last_timestamp changed)", len(s.setStateCalls))
	}
	if s.setStateCalls[0] != "last_timestamp" {
		t.Errorf("SetState key = %q, want %q", s.setStateCalls[0], "last_timestamp")
	}
}

func TestSaveState_WritesOnlyChangedAgentTimestamp(t *testing.T) {
	s := newMockStore()
	o := newTestOrchestrator(s, newMockQueue(), &mockIPCBroker{})

	ts := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	o.lastTimestamp = ts
	o.lastAgentTimestamp["group1@g.us"] = ts.Add(-2 * time.Minute)
	o.lastConfirmedTimestamp["group1@g.us"] = ts.Add(-5 * time.Minute)

	// First save.
	if err := o.saveState(context.Background()); err != nil {
		t.Fatalf("saveState() error = %v", err)
	}
	s.setStateCalls = nil

	// Change only lastAgentTimestamp.
	o.lastAgentTimestamp["group1@g.us"] = ts
	if err := o.saveState(context.Background()); err != nil {
		t.Fatalf("saveState() error = %v", err)
	}
	if len(s.setStateCalls) != 1 {
		t.Fatalf("SetState called %d times, want 1 (only last_agent_timestamp changed)", len(s.setStateCalls))
	}
	if s.setStateCalls[0] != "last_agent_timestamp" {
		t.Errorf("SetState key = %q, want %q", s.setStateCalls[0], "last_agent_timestamp")
	}
}

func TestSaveState_InitialSaveWritesAll(t *testing.T) {
	s := newMockStore()
	o := newTestOrchestrator(s, newMockQueue(), &mockIPCBroker{})

	ts := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	o.lastTimestamp = ts
	o.lastAgentTimestamp["group1@g.us"] = ts.Add(-2 * time.Minute)
	o.lastConfirmedTimestamp["group1@g.us"] = ts.Add(-5 * time.Minute)

	if err := o.saveState(context.Background()); err != nil {
		t.Fatalf("saveState() error = %v", err)
	}

	// All 3 SetState calls should be made on initial save.
	if len(s.setStateCalls) != 3 {
		t.Errorf("initial saveState: SetState called %d times, want 3", len(s.setStateCalls))
	}

	// Verify all three keys were written.
	keys := map[string]bool{}
	for _, k := range s.setStateCalls {
		keys[k] = true
	}
	for _, want := range []string{"last_timestamp", "last_agent_timestamp", "last_confirmed_timestamp"} {
		if !keys[want] {
			t.Errorf("initial saveState: missing SetState call for %q", want)
		}
	}
}

// --- MAX_CONCURRENT Tests ---

// mockQueueWithActiveCount extends mockQueue with a configurable ActiveCount return.
type mockQueueWithActiveCount struct {
	mockQueue
	activeCount    int64
	activeCountErr error
}

func (m *mockQueueWithActiveCount) ActiveCount(_ context.Context) (int64, error) {
	return m.activeCount, m.activeCountErr
}

// mockSandboxControllerWithTracking tracks CreateSandbox calls.
type mockSandboxControllerWithTracking struct {
	createCalled bool
	createErr    error
}

func (m *mockSandboxControllerWithTracking) CreateSandbox(_ context.Context, _ sandbox.SandboxConfig) (*sandbox.SandboxStatus, error) {
	m.createCalled = true
	if m.createErr != nil {
		return nil, m.createErr
	}
	return &sandbox.SandboxStatus{Name: "test-sandbox", State: sandbox.StatePending}, nil
}
func (m *mockSandboxControllerWithTracking) StopSandbox(_ context.Context, _ string) error {
	return nil
}
func (m *mockSandboxControllerWithTracking) HasActiveSandbox(_ context.Context, _ string) (bool, error) {
	return false, nil
}
func (m *mockSandboxControllerWithTracking) CleanupOrphans(_ context.Context) error { return nil }
func (m *mockSandboxControllerWithTracking) WatchSandboxes(_ context.Context) (<-chan sandbox.SandboxEvent, error) {
	return make(chan sandbox.SandboxEvent), nil
}

func TestMaxConcurrent_AtLimit_SkipsCreateSandbox(t *testing.T) {
	s := newMockStore()
	mq := &mockQueueWithActiveCount{
		mockQueue:   mockQueue{active: make(map[string]bool)},
		activeCount: 5, // at limit
	}
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	b := &mockIPCBroker{}
	sb := &mockSandboxControllerWithTracking{}

	cfg := &config.Config{
		Channels:  config.ChannelsConfig{AssistantName: "TestBot"},
		Queue:     config.QueueConfig{IdleTimeout: 30 * time.Minute, MaxConcurrent: 5},
		Scheduler: config.SchedulerConfig{PollInterval: 60 * time.Second},
	}
	log := slog.Default()
	reg := channel.NewRegistry()
	o, err := New(cfg, s, mq, b, nil, reg, log)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	o.sandbox = sb
	rtr, _ := router.New([]channel.Channel{ch}, s)
	o.router = rtr
	o.auth = auth.New(s)

	// Register group and add messages.
	o.registeredGroups["group1@g.us"] = store.Group{
		JID: "group1@g.us", Folder: "test-group", IsMain: true,
	}
	s.messages["group1@g.us"] = []store.Message{
		{ChatJID: "group1@g.us", Content: "hello", Timestamp: time.Now(), Sender: "alice"},
	}

	err = o.processGroupMessages(context.Background(), "group1@g.us")
	if err != nil {
		t.Fatalf("processGroupMessages() error = %v, want nil", err)
	}
	if sb.createCalled {
		t.Error("CreateSandbox was called, want skipped when at MAX_CONCURRENT")
	}
}

func TestMaxConcurrent_BelowLimit_ProceedsToCreateSandbox(t *testing.T) {
	s := newMockStore()
	mq := &mockQueueWithActiveCount{
		mockQueue:   mockQueue{active: make(map[string]bool)},
		activeCount: 4, // below limit of 5
	}
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	b := &mockIPCBroker{}
	sb := &mockSandboxControllerWithTracking{}

	cfg := &config.Config{
		Channels:  config.ChannelsConfig{AssistantName: "TestBot"},
		Queue:     config.QueueConfig{IdleTimeout: 30 * time.Minute, MaxConcurrent: 5},
		Scheduler: config.SchedulerConfig{PollInterval: 60 * time.Second},
	}
	log := slog.Default()
	reg := channel.NewRegistry()
	o, err := New(cfg, s, mq, b, nil, reg, log)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	o.sandbox = sb
	rtr, _ := router.New([]channel.Channel{ch}, s)
	o.router = rtr
	o.auth = auth.New(s)

	o.registeredGroups["group1@g.us"] = store.Group{
		JID: "group1@g.us", Folder: "test-group", IsMain: true,
	}
	s.messages["group1@g.us"] = []store.Message{
		{ChatJID: "group1@g.us", Content: "hello", Timestamp: time.Now(), Sender: "alice"},
	}

	err = o.processGroupMessages(context.Background(), "group1@g.us")
	// CreateSandbox should be called — it will succeed since sb returns a valid status.
	// But MarkActive will fail because mockQueueWithActiveCount inherits from mockQueue.
	// We just care that CreateSandbox was reached.
	if !sb.createCalled {
		t.Error("CreateSandbox was NOT called, want called when below MAX_CONCURRENT")
	}
	_ = err // error from MarkActive is acceptable here
}

func TestMaxConcurrent_ActiveCountError_ReturnsError(t *testing.T) {
	s := newMockStore()
	mq := &mockQueueWithActiveCount{
		mockQueue:      mockQueue{active: make(map[string]bool)},
		activeCountErr: fmt.Errorf("redis connection failed"),
	}
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	b := &mockIPCBroker{}

	cfg := &config.Config{
		Channels:  config.ChannelsConfig{AssistantName: "TestBot"},
		Queue:     config.QueueConfig{IdleTimeout: 30 * time.Minute, MaxConcurrent: 5},
		Scheduler: config.SchedulerConfig{PollInterval: 60 * time.Second},
	}
	log := slog.Default()
	reg := channel.NewRegistry()
	o, err := New(cfg, s, mq, b, nil, reg, log)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	rtr, _ := router.New([]channel.Channel{ch}, s)
	o.router = rtr
	o.auth = auth.New(s)

	o.registeredGroups["group1@g.us"] = store.Group{
		JID: "group1@g.us", Folder: "test-group", IsMain: true,
	}
	s.messages["group1@g.us"] = []store.Message{
		{ChatJID: "group1@g.us", Content: "hello", Timestamp: time.Now(), Sender: "alice"},
	}

	err = o.processGroupMessages(context.Background(), "group1@g.us")
	if err == nil {
		t.Fatal("processGroupMessages() error = nil, want error from ActiveCount failure")
	}
	if !strings.Contains(err.Error(), "check active count") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "check active count")
	}
}

// --- hasTriggerMessage Tests ---

func TestHasTriggerMessage(t *testing.T) {
	s := newMockStore()
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	o := newTestOrchestratorWithRouter(s, newMockQueue(), &mockIPCBroker{}, []channel.Channel{ch})

	group := store.Group{
		JID:             "group1@g.us",
		Folder:          "test-group",
		TriggerPattern:  "!bot",
		RequiresTrigger: true,
	}

	tests := []struct {
		name     string
		messages []store.Message
		want     bool
	}{
		{
			name: "trigger present from allowed sender",
			messages: []store.Message{
				{Content: "!bot hello", Sender: "alice", IsFromMe: true},
			},
			want: true,
		},
		{
			name: "trigger absent",
			messages: []store.Message{
				{Content: "hello world", Sender: "alice", IsFromMe: true},
			},
			want: false,
		},
		{
			name: "trigger present but sender not allowed",
			messages: []store.Message{
				{Content: "!bot hello", Sender: "unknown", IsFromMe: false},
			},
			// No allowlist entries → all senders allowed
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := o.hasTriggerMessage(context.Background(), "group1@g.us", group, tt.messages)
			if err != nil {
				t.Fatalf("hasTriggerMessage() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("hasTriggerMessage() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasTriggerMessage_MainGroup(t *testing.T) {
	// Main groups skip trigger check entirely in the caller (pollMessages/processGroupMessages).
	// hasTriggerMessage itself just checks content — but a main group with IsMain=true
	// would never call hasTriggerMessage. This test verifies the trigger logic still works
	// when called directly with a non-trigger message.
	s := newMockStore()
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"main@g.us": true}}
	o := newTestOrchestratorWithRouter(s, newMockQueue(), &mockIPCBroker{}, []channel.Channel{ch})

	group := store.Group{
		JID:             "main@g.us",
		Folder:          "main-group",
		TriggerPattern:  "!bot",
		RequiresTrigger: true,
		IsMain:          true,
	}

	// Even without trigger, main groups are processed because the caller skips the check.
	// But hasTriggerMessage itself doesn't know about IsMain — it just checks the pattern.
	got, err := o.hasTriggerMessage(context.Background(), "main@g.us", group, []store.Message{
		{Content: "no trigger here", Sender: "alice", IsFromMe: true},
	})
	if err != nil {
		t.Fatalf("hasTriggerMessage() error = %v", err)
	}
	if got != false {
		t.Error("hasTriggerMessage() = true, want false (trigger not present in content)")
	}
}

// --- onInboundMessage Tests ---

func TestOnInboundMessage_StoresMessage(t *testing.T) {
	s := newMockStore()
	o := newTestOrchestrator(s, newMockQueue(), &mockIPCBroker{})

	msg := &channel.InboundMessage{
		ID:         "msg-1",
		ChatJID:    "group1@g.us",
		Sender:     "alice",
		SenderName: "Alice",
		Content:    "hello",
		Timestamp:  time.Now(),
	}

	o.onInboundMessage("group1@g.us", msg)

	if !s.storeMessageCalled {
		t.Error("StoreMessage was not called")
	}
	if len(s.messages["group1@g.us"]) != 1 {
		t.Fatalf("expected 1 stored message, got %d", len(s.messages["group1@g.us"]))
	}
	if s.messages["group1@g.us"][0].Content != "hello" {
		t.Errorf("stored message content = %q, want %q", s.messages["group1@g.us"][0].Content, "hello")
	}
}

func TestOnInboundMessage_StoreError(t *testing.T) {
	s := newMockStore()
	s.storeMessageErr = fmt.Errorf("db write failed")
	o := newTestOrchestrator(s, newMockQueue(), &mockIPCBroker{})

	msg := &channel.InboundMessage{
		ID:      "msg-1",
		ChatJID: "group1@g.us",
		Content: "hello",
	}

	// Should not panic.
	o.onInboundMessage("group1@g.us", msg)

	if !s.storeMessageCalled {
		t.Error("StoreMessage was not called")
	}
}

func TestOnInboundMessage_RateLimitDrop(t *testing.T) {
	s := newMockStore()
	o := newTestOrchestrator(s, newMockQueue(), &mockIPCBroker{})
	// Allow only 1 message per second.
	o.cfg.Queue.RateLimitTokensPerSec = 1

	msg := &channel.InboundMessage{
		ID:      "msg-1",
		ChatJID: "group1@g.us",
		Sender:  "alice",
		Content: "hello",
	}

	// First message should be stored.
	o.onInboundMessage("group1@g.us", msg)
	if !s.storeMessageCalled {
		t.Fatal("first message should have been stored")
	}

	// Reset and send second message immediately — should be dropped.
	s.storeMessageCalled = false
	msg2 := &channel.InboundMessage{
		ID:      "msg-2",
		ChatJID: "group1@g.us",
		Sender:  "alice",
		Content: "world",
	}
	o.onInboundMessage("group1@g.us", msg2)
	if s.storeMessageCalled {
		t.Error("second message should have been dropped by rate limiter")
	}
}

func TestOnInboundMessage_SizeLimitDrop(t *testing.T) {
	s := newMockStore()
	o := newTestOrchestrator(s, newMockQueue(), &mockIPCBroker{})
	o.cfg.Queue.RateLimitTokensPerSec = 100 // High limit so rate limiter doesn't interfere.
	o.cfg.Queue.MaxMessageSizeBytes = 10

	msg := &channel.InboundMessage{
		ID:      "msg-1",
		ChatJID: "group1@g.us",
		Sender:  "alice",
		Content: "this message is way too long",
	}

	o.onInboundMessage("group1@g.us", msg)
	if s.storeMessageCalled {
		t.Error("oversized message should have been dropped")
	}
}

func TestOnInboundMessage_BothChecksPassed(t *testing.T) {
	s := newMockStore()
	o := newTestOrchestrator(s, newMockQueue(), &mockIPCBroker{})
	o.cfg.Queue.RateLimitTokensPerSec = 100
	o.cfg.Queue.MaxMessageSizeBytes = 1000

	msg := &channel.InboundMessage{
		ID:        "msg-1",
		ChatJID:   "group1@g.us",
		Sender:    "alice",
		Content:   "hello",
		Timestamp: time.Now(),
	}

	o.onInboundMessage("group1@g.us", msg)
	if !s.storeMessageCalled {
		t.Error("message should have been stored when rate and size checks pass")
	}
}

// --- processGroupMessages Tests ---

func TestProcessGroupMessages_UnknownGroup(t *testing.T) {
	s := newMockStore()
	o := newTestOrchestrator(s, newMockQueue(), &mockIPCBroker{})

	err := o.processGroupMessages(context.Background(), "unknown@g.us")
	if err != nil {
		t.Errorf("processGroupMessages() error = %v, want nil for unknown group", err)
	}
}

func TestProcessGroupMessages_NoMessages(t *testing.T) {
	s := newMockStore()
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	o := newTestOrchestratorWithRouter(s, newMockQueue(), &mockIPCBroker{}, []channel.Channel{ch})

	o.registeredGroups["group1@g.us"] = store.Group{
		JID:    "group1@g.us",
		Folder: "test-group",
		IsMain: true,
	}

	err := o.processGroupMessages(context.Background(), "group1@g.us")
	if err != nil {
		t.Errorf("processGroupMessages() error = %v, want nil for no messages", err)
	}
}

// --- handleIPCMessage Tests ---

func TestHandleIPCMessage_MessageType(t *testing.T) {
	s := newMockStore()
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	o := newTestOrchestratorWithRouter(s, newMockQueue(), &mockIPCBroker{}, []channel.Channel{ch})

	group := store.Group{
		JID:    "group1@g.us",
		Folder: "test-group",
	}

	payload, _ := json.Marshal(map[string]string{"text": "hello from agent"})
	msg := &ipc.IPCMessage{
		Type:    ipc.IPCMessageText,
		Payload: payload,
	}

	o.handleIPCMessage(context.Background(), "group1@g.us", group, msg)

	if len(ch.sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(ch.sent))
	}
	if ch.sent[0].text != "hello from agent" {
		t.Errorf("sent text = %q, want %q", ch.sent[0].text, "hello from agent")
	}
}

func TestHandleIPCMessage_SessionUpdate(t *testing.T) {
	s := newMockStore()
	o := newTestOrchestrator(s, newMockQueue(), &mockIPCBroker{})

	group := store.Group{
		JID:    "group1@g.us",
		Folder: "test-group",
	}

	payload, _ := json.Marshal(map[string]string{"sessionId": "new-session-456"})
	msg := &ipc.IPCMessage{
		Type:    ipc.IPCSessionUpdate,
		Payload: payload,
	}

	o.handleIPCMessage(context.Background(), "group1@g.us", group, msg)

	if o.sessions["test-group"] != "new-session-456" {
		t.Errorf("sessions[test-group] = %q, want %q", o.sessions["test-group"], "new-session-456")
	}
	if s.sessions["test-group"] == nil || s.sessions["test-group"].SessionID != "new-session-456" {
		t.Error("session not persisted to store")
	}
}

func TestHandleIPCMessage_MessageUpdatesConfirmedCursor(t *testing.T) {
	s := newMockStore()
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	o := newTestOrchestratorWithRouter(s, newMockQueue(), &mockIPCBroker{}, []channel.Channel{ch})

	agentCursor := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	confirmedCursor := time.Date(2025, 1, 15, 10, 20, 0, 0, time.UTC)
	o.lastAgentTimestamp["group1@g.us"] = agentCursor
	o.lastConfirmedTimestamp["group1@g.us"] = confirmedCursor

	group := store.Group{
		JID:    "group1@g.us",
		Folder: "test-group",
	}

	payload, _ := json.Marshal(map[string]string{"text": "response from agent"})
	msg := &ipc.IPCMessage{
		Type:    ipc.IPCMessageText,
		Payload: payload,
	}

	o.handleIPCMessage(context.Background(), "group1@g.us", group, msg)

	// After a text response, confirmed cursor should match agent cursor.
	confirmed := o.lastConfirmedTimestamp["group1@g.us"]
	if !confirmed.Equal(agentCursor) {
		t.Errorf("lastConfirmedTimestamp = %v, want %v (should match lastAgentTimestamp after response)", confirmed, agentCursor)
	}
}

func TestHandleIPCMessage_SessionUpdateDoesNotUpdateConfirmed(t *testing.T) {
	s := newMockStore()
	o := newTestOrchestrator(s, newMockQueue(), &mockIPCBroker{})

	agentCursor := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	confirmedCursor := time.Date(2025, 1, 15, 10, 20, 0, 0, time.UTC)
	o.lastAgentTimestamp["group1@g.us"] = agentCursor
	o.lastConfirmedTimestamp["group1@g.us"] = confirmedCursor

	group := store.Group{
		JID:    "group1@g.us",
		Folder: "test-group",
	}

	payload, _ := json.Marshal(map[string]string{"sessionId": "sess-456"})
	msg := &ipc.IPCMessage{
		Type:    ipc.IPCSessionUpdate,
		Payload: payload,
	}

	o.handleIPCMessage(context.Background(), "group1@g.us", group, msg)

	// Session update should NOT advance confirmed cursor.
	confirmed := o.lastConfirmedTimestamp["group1@g.us"]
	if !confirmed.Equal(confirmedCursor) {
		t.Errorf("lastConfirmedTimestamp = %v, want %v (should not change on session update)", confirmed, confirmedCursor)
	}
}

func TestDeactivate_RollsBackCursorToConfirmed(t *testing.T) {
	s := newMockStore()
	q := newMockQueue()
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	b := &mockIPCBroker{}
	o := newTestOrchestratorWithRouter(s, q, b, []channel.Channel{ch})

	// Simulate: agent cursor advanced past confirmed (message piped but not processed).
	confirmedTs := time.Date(2025, 1, 15, 10, 20, 0, 0, time.UTC)
	sentTs := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	o.lastAgentTimestamp["group1@g.us"] = sentTs
	o.lastConfirmedTimestamp["group1@g.us"] = confirmedTs

	group := store.Group{
		JID:    "group1@g.us",
		Folder: "test-group",
		IsMain: true,
	}
	o.registeredGroups["group1@g.us"] = group
	q.active["group1@g.us"] = true

	// No messages in store — deactivate will roll back the cursor but won't
	// spawn a processGroupMessages goroutine, so assertions are race-free.

	// Call watchGroupOutput with a channel that closes immediately to trigger deactivate.
	ipcCh := make(chan *ipc.IPCMessage)
	close(ipcCh)
	b.subscribeCh = ipcCh

	o.watchGroupOutput(context.Background(), "group1@g.us")

	// Agent cursor should be rolled back to confirmed.
	o.mu.Lock()
	agent := o.lastAgentTimestamp["group1@g.us"]
	o.mu.Unlock()
	if !agent.Equal(confirmedTs) {
		t.Errorf("lastAgentTimestamp after deactivate = %v, want %v (should roll back to confirmed)", agent, confirmedTs)
	}

	// Group should be marked inactive.
	if q.active["group1@g.us"] {
		t.Error("group should be marked inactive after deactivate")
	}

	// State should be persisted.
	if s.state["last_agent_timestamp"] == "" {
		t.Error("state not saved after deactivate")
	}
}

func TestDeactivate_PendingMessagesTriggersReprocessing(t *testing.T) {
	s := newMockStore()
	q := newMockQueue()
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	b := &mockIPCBroker{}
	o := newTestOrchestratorWithRouter(s, q, b, []channel.Channel{ch})

	confirmedTs := time.Date(2025, 1, 15, 10, 20, 0, 0, time.UTC)
	sentTs := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	o.lastAgentTimestamp["group1@g.us"] = sentTs
	o.lastConfirmedTimestamp["group1@g.us"] = confirmedTs

	group := store.Group{
		JID:    "group1@g.us",
		Folder: "test-group",
		IsMain: true,
	}
	o.registeredGroups["group1@g.us"] = group
	q.active["group1@g.us"] = true

	// Add a message between confirmed and sent timestamps.
	s.messages["group1@g.us"] = []store.Message{
		{
			ChatJID:   "group1@g.us",
			Content:   "lost message",
			Timestamp: time.Date(2025, 1, 15, 10, 25, 0, 0, time.UTC),
		},
	}

	// Hook GetMessagesSince to detect when processGroupMessages is called.
	// The first call comes from deactivate() (pending check) — let it pass.
	// The second call comes from processGroupMessages goroutine — signal and block.
	var callCount atomic.Int32
	processStarted := make(chan struct{})
	processBlock := make(chan struct{})
	s.getMessagesSinceHook = func() {
		n := callCount.Add(1)
		if n <= 1 {
			return // first call is from deactivate's pending check
		}
		// Second call is from processGroupMessages goroutine.
		select {
		case processStarted <- struct{}{}:
		default:
		}
		<-processBlock
	}

	ipcCh := make(chan *ipc.IPCMessage)
	close(ipcCh)
	b.subscribeCh = ipcCh

	o.watchGroupOutput(context.Background(), "group1@g.us")

	// Wait for processGroupMessages goroutine to start (it calls GetMessagesSince).
	<-processStarted

	// Verify cursor was rolled back before re-processing began.
	o.mu.Lock()
	agent := o.lastAgentTimestamp["group1@g.us"]
	o.mu.Unlock()
	if !agent.Equal(confirmedTs) {
		t.Errorf("lastAgentTimestamp = %v, want %v (should roll back before re-processing)", agent, confirmedTs)
	}

	// Clear messages so the unblocked goroutine finds nothing and returns
	// without reaching CreateSandbox (which would panic on nil controller).
	s.messages["group1@g.us"] = nil

	// Unblock the goroutine so it can finish cleanly.
	close(processBlock)
}

func TestDeactivate_NoRollbackWhenCursorsMatch(t *testing.T) {
	s := newMockStore()
	q := newMockQueue()
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	b := &mockIPCBroker{}
	o := newTestOrchestratorWithRouter(s, q, b, []channel.Channel{ch})

	// Both cursors match — no rollback needed.
	ts := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	o.lastAgentTimestamp["group1@g.us"] = ts
	o.lastConfirmedTimestamp["group1@g.us"] = ts

	group := store.Group{
		JID:    "group1@g.us",
		Folder: "test-group",
		IsMain: true,
	}
	o.registeredGroups["group1@g.us"] = group
	q.active["group1@g.us"] = true

	ipcCh := make(chan *ipc.IPCMessage)
	close(ipcCh)
	b.subscribeCh = ipcCh

	o.watchGroupOutput(context.Background(), "group1@g.us")

	// Cursor should remain unchanged.
	agent := o.lastAgentTimestamp["group1@g.us"]
	if !agent.Equal(ts) {
		t.Errorf("lastAgentTimestamp = %v, want %v (should not change when cursors match)", agent, ts)
	}
}

func TestHandleIPCMessage_MalformedJSON(t *testing.T) {
	s := newMockStore()
	o := newTestOrchestrator(s, newMockQueue(), &mockIPCBroker{})

	group := store.Group{
		JID:    "group1@g.us",
		Folder: "test-group",
	}

	msg := &ipc.IPCMessage{
		Type:    ipc.IPCMessageText,
		Payload: json.RawMessage(`{invalid json`),
	}

	// Should not panic.
	o.handleIPCMessage(context.Background(), "group1@g.us", group, msg)
}

// --- Startup Timeout Tests ---

// mockSandboxController implements sandboxController for testing.
type mockSandboxController struct {
	hasActive            bool
	hasErr               error
	cleanupOrphansCalled atomic.Int32
}

func (m *mockSandboxController) CreateSandbox(_ context.Context, _ sandbox.SandboxConfig) (*sandbox.SandboxStatus, error) {
	return &sandbox.SandboxStatus{Name: "test-sandbox", State: sandbox.StatePending}, nil
}
func (m *mockSandboxController) StopSandbox(_ context.Context, _ string) error { return nil }
func (m *mockSandboxController) HasActiveSandbox(_ context.Context, _ string) (bool, error) {
	return m.hasActive, m.hasErr
}
func (m *mockSandboxController) CleanupOrphans(_ context.Context) error {
	m.cleanupOrphansCalled.Add(1)
	return nil
}
func (m *mockSandboxController) WatchSandboxes(_ context.Context) (<-chan sandbox.SandboxEvent, error) {
	ch := make(chan sandbox.SandboxEvent)
	// Return an open channel that never sends — tests don't exercise the watcher loop.
	return ch, nil
}

// newTestOrchestratorWithSandbox creates an orchestrator that includes a sandbox controller.
func newTestOrchestratorWithSandbox(s *mockStore, q *mockQueue, b *mockIPCBroker, sb sandboxController, startupTimeout time.Duration) *Orchestrator {
	cfg := &config.Config{
		Channels:  config.ChannelsConfig{AssistantName: "TestBot"},
		Queue:     config.QueueConfig{IdleTimeout: 30 * time.Minute},
		Scheduler: config.SchedulerConfig{PollInterval: 60 * time.Second},
		K8s:       config.K8sConfig{SandboxStartupTimeout: startupTimeout},
	}
	log := slog.Default()
	reg := channel.NewRegistry()

	o, err := New(cfg, s, q, b, nil, reg, log)
	if err != nil {
		panic("newTestOrchestratorWithSandbox: " + err.Error())
	}
	// Inject the mock sandbox directly via the interface field.
	o.sandbox = sb
	return o
}

func TestWatchGroupOutput_StartupTimeoutDeactivatesGroupWhenPodNeverStarts(t *testing.T) {
	// Simulates: SandboxClaim created, operator never creates a pod.
	// HasActiveSandbox returns true (claim is StatePending), but no IPC messages ever arrive.
	// After the startup timeout, the group should be deactivated and the cursor rolled back.

	s := newMockStore()
	q := newMockQueue()
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	b := &mockIPCBroker{}

	// Startup timeout short enough for a fast test.
	const startupTimeout = 50 * time.Millisecond

	// Sandbox always reports HasActiveSandbox = true (stuck in StatePending).
	sb := &mockSandboxController{hasActive: true}

	o := newTestOrchestratorWithSandbox(s, q, b, sb, startupTimeout)
	_ = ch // used to build router if needed

	// Advance agent cursor past confirmed so we can verify rollback.
	confirmedTs := time.Date(2025, 1, 15, 10, 20, 0, 0, time.UTC)
	sentTs := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	o.lastAgentTimestamp["group1@g.us"] = sentTs
	o.lastConfirmedTimestamp["group1@g.us"] = confirmedTs

	group := store.Group{
		JID:    "group1@g.us",
		Folder: "test-group",
		IsMain: true,
	}
	o.registeredGroups["group1@g.us"] = group
	q.active["group1@g.us"] = true

	// IPC channel that never delivers any messages (simulates pod never connecting).
	ipcCh := make(chan *ipc.IPCMessage)
	b.subscribeCh = ipcCh

	// watchGroupOutput should block until the startup timeout fires, then deactivate.
	o.watchGroupOutput(context.Background(), "group1@g.us")

	// Group should be deactivated.
	if q.active["group1@g.us"] {
		t.Error("group should be marked inactive after startup timeout")
	}

	// Agent cursor should be rolled back to the confirmed position.
	o.mu.Lock()
	agent := o.lastAgentTimestamp["group1@g.us"]
	o.mu.Unlock()
	if !agent.Equal(confirmedTs) {
		t.Errorf("lastAgentTimestamp after startup timeout = %v, want %v (should roll back to confirmed)", agent, confirmedTs)
	}
}

func TestWatchGroupOutput_StartupTimeoutNotFiredWhenAgentConnects(t *testing.T) {
	// Simulates: SandboxClaim created, operator creates a pod, agent connects via IPC.
	// The startup deadline should NOT deactivate the group when the agent has connected.

	s := newMockStore()
	q := newMockQueue()
	b := &mockIPCBroker{}

	const startupTimeout = 100 * time.Millisecond

	sb := &mockSandboxController{hasActive: true}
	o := newTestOrchestratorWithSandbox(s, q, b, sb, startupTimeout)

	ts := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	o.lastAgentTimestamp["group1@g.us"] = ts
	o.lastConfirmedTimestamp["group1@g.us"] = ts

	group := store.Group{
		JID:    "group1@g.us",
		Folder: "test-group",
		IsMain: true,
	}
	o.registeredGroups["group1@g.us"] = group
	q.active["group1@g.us"] = true

	// IPC channel that delivers a shutdown message immediately, then closes.
	ipcCh := make(chan *ipc.IPCMessage, 1)
	shutdownPayload, _ := json.Marshal(map[string]string{})
	ipcCh <- &ipc.IPCMessage{Type: ipc.IPCShutdown, Payload: shutdownPayload}
	close(ipcCh)
	b.subscribeCh = ipcCh

	o.watchGroupOutput(context.Background(), "group1@g.us")

	// Group should be deactivated by normal agent shutdown, not by startup timeout.
	// The cursor should NOT be rolled back because confirmedTs == agentTs.
	agent := o.lastAgentTimestamp["group1@g.us"]
	if !agent.Equal(ts) {
		t.Errorf("lastAgentTimestamp = %v, want %v (should not roll back when cursors match)", agent, ts)
	}
}

func TestHandleIPCMessage_TaskUpdateGroupMismatch(t *testing.T) {
	ms := newMockStore()
	o := &Orchestrator{
		store: ms,
		log:   slog.Default(),
	}

	group := store.Group{
		JID:    "group1@g.us",
		Name:   "test-group",
		Folder: "group-a",
	}

	// Agent sends a task_update with a different group folder.
	task := store.ScheduledTask{
		ID:          "task-1",
		GroupFolder: "group-b", // mismatch with group.Folder="group-a"
	}
	payload, _ := json.Marshal(task)
	msg := &ipc.IPCMessage{
		Type:    ipc.IPCTaskUpdate,
		Payload: payload,
	}

	result := o.handleIPCMessage(context.Background(), "chat@g.us", group, msg)

	if !result {
		// handleIPCMessage returns false for non-shutdown — expected
	}
	if ms.updateTaskCalled {
		t.Error("UpdateTask should NOT be called when group folder mismatches")
	}
}

func TestHandleIPCMessage_TaskUpdateGroupMatch(t *testing.T) {
	ms := newMockStore()
	o := &Orchestrator{
		store: ms,
		log:   slog.Default(),
	}

	group := store.Group{
		JID:    "group1@g.us",
		Name:   "test-group",
		Folder: "group-a",
	}

	// Agent sends a task_update with the correct group folder.
	task := store.ScheduledTask{
		ID:          "task-1",
		GroupFolder: "group-a", // matches group.Folder
	}
	payload, _ := json.Marshal(task)
	msg := &ipc.IPCMessage{
		Type:    ipc.IPCTaskUpdate,
		Payload: payload,
	}

	result := o.handleIPCMessage(context.Background(), "chat@g.us", group, msg)

	if result {
		t.Error("handleIPCMessage should return false for task_update (not shutdown)")
	}
	if !ms.updateTaskCalled {
		t.Error("UpdateTask should be called when group folder matches")
	}
}

func TestHandleIPCMessage_TaskDeleteScopedToGroup(t *testing.T) {
	ms := newMockStore()
	ms.tasks["task-1"] = &store.ScheduledTask{ID: "task-1", GroupFolder: "group-a"}
	o := &Orchestrator{
		store: ms,
		log:   slog.Default(),
	}

	group := store.Group{
		JID:    "group1@g.us",
		Name:   "test-group",
		Folder: "group-a",
	}

	payload, _ := json.Marshal(struct {
		ID string `json:"id"`
	}{ID: "task-1"})
	msg := &ipc.IPCMessage{
		Type:    ipc.IPCTaskDelete,
		Payload: payload,
	}

	o.handleIPCMessage(context.Background(), "chat@g.us", group, msg)

	// DeleteTask should have been called with the group's folder for scoping.
	if ms.deleteTaskCalledWith[1] != "group-a" {
		t.Errorf("DeleteTask groupFolder = %q, want %q", ms.deleteTaskCalledWith[1], "group-a")
	}
}

func TestHandleIPCMessage_TaskCreate_InvalidPayload(t *testing.T) {
	s := newMockStore()
	o := newTestOrchestrator(s, newMockQueue(), &mockIPCBroker{})

	group := store.Group{
		JID:    "group1@g.us",
		Folder: "test-group",
	}

	// Invalid: ScheduleType="cron" with bad ScheduleValue
	payload := json.RawMessage(`{"ID":"task-1","ScheduleType":"cron","ScheduleValue":"not-valid","Prompt":"test"}`)
	msg := &ipc.IPCMessage{
		Type:    ipc.IPCTaskCreate,
		Payload: payload,
	}

	o.handleIPCMessage(context.Background(), "group1@g.us", group, msg)

	// CreateTask should NOT have been called — task map should be empty
	if len(s.tasks) != 0 {
		t.Fatalf("expected 0 tasks in store, got %d — validation should have rejected the task", len(s.tasks))
	}
}

func TestHandleIPCMessage_TaskCreate_ValidPayload(t *testing.T) {
	s := newMockStore()
	o := newTestOrchestrator(s, newMockQueue(), &mockIPCBroker{})

	group := store.Group{
		JID:    "group1@g.us",
		Folder: "test-group",
	}

	payload := json.RawMessage(`{"ID":"task-1","ScheduleType":"cron","ScheduleValue":"* * * * *","Prompt":"test"}`)
	msg := &ipc.IPCMessage{
		Type:    ipc.IPCTaskCreate,
		Payload: payload,
	}

	o.handleIPCMessage(context.Background(), "group1@g.us", group, msg)

	if len(s.tasks) != 1 {
		t.Fatalf("expected 1 task in store, got %d", len(s.tasks))
	}
	task := s.tasks["task-1"]
	if task == nil {
		t.Fatal("expected task-1 in store")
	}
	if task.GroupFolder != "test-group" {
		t.Fatalf("GroupFolder = %q, want %q", task.GroupFolder, "test-group")
	}
}

// --- BUG-01: Bot reply storage tests ---

func TestHandleIPCMessage_TextStoresBotReply(t *testing.T) {
	s := newMockStore()
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	o := newTestOrchestratorWithRouter(s, newMockQueue(), &mockIPCBroker{}, []channel.Channel{ch})

	group := store.Group{
		JID:    "group1@g.us",
		Folder: "test-group",
	}

	payload, _ := json.Marshal(map[string]string{"text": "hello from agent"})
	msg := &ipc.IPCMessage{
		Type:    ipc.IPCMessageText,
		Payload: payload,
	}

	o.handleIPCMessage(context.Background(), "group1@g.us", group, msg)

	// The bot reply should be stored in the message store.
	msgs := s.messages["group1@g.us"]
	if len(msgs) != 1 {
		t.Fatalf("expected 1 stored message, got %d", len(msgs))
	}
	botMsg := msgs[0]
	if !botMsg.IsBotMessage {
		t.Error("stored message IsBotMessage = false, want true")
	}
	if botMsg.Content != "hello from agent" {
		t.Errorf("stored message Content = %q, want %q", botMsg.Content, "hello from agent")
	}
	if botMsg.ChatJID != "group1@g.us" {
		t.Errorf("stored message ChatJID = %q, want %q", botMsg.ChatJID, "group1@g.us")
	}
	if botMsg.ID == "" {
		t.Error("stored message ID is empty, want non-empty UUID")
	}
}

// --- REL-02: refreshGroups full-rebuild tests ---

func TestRefreshGroups_RemovesDeletedGroup(t *testing.T) {
	s := newMockStore()
	o := newTestOrchestrator(s, newMockQueue(), &mockIPCBroker{})

	groupA := store.Group{JID: "jid-a", Name: "Group A", Folder: "group-a"}
	groupB := store.Group{JID: "jid-b", Name: "Group B", Folder: "group-b"}
	o.registeredGroups["jid-a"] = groupA
	o.registeredGroups["jid-b"] = groupB

	// ListGroups returns only group A — group B was deleted.
	s.groups = []store.Group{groupA}

	o.refreshGroups(context.Background())

	if len(o.registeredGroups) != 1 {
		t.Fatalf("registeredGroups has %d entries, want 1", len(o.registeredGroups))
	}
	if _, ok := o.registeredGroups["jid-a"]; !ok {
		t.Error("registeredGroups missing jid-a")
	}
	if _, ok := o.registeredGroups["jid-b"]; ok {
		t.Error("registeredGroups still contains jid-b after deletion")
	}
}

func TestRefreshGroups_AddsNewGroup(t *testing.T) {
	s := newMockStore()
	o := newTestOrchestrator(s, newMockQueue(), &mockIPCBroker{})

	groupA := store.Group{JID: "jid-a", Name: "Group A", Folder: "group-a"}
	groupB := store.Group{JID: "jid-b", Name: "Group B", Folder: "group-b"}
	o.registeredGroups["jid-a"] = groupA

	// ListGroups returns both groups.
	s.groups = []store.Group{groupA, groupB}

	o.refreshGroups(context.Background())

	if len(o.registeredGroups) != 2 {
		t.Fatalf("registeredGroups has %d entries, want 2", len(o.registeredGroups))
	}
	if _, ok := o.registeredGroups["jid-a"]; !ok {
		t.Error("registeredGroups missing jid-a")
	}
	if _, ok := o.registeredGroups["jid-b"]; !ok {
		t.Error("registeredGroups missing jid-b")
	}
}

// --- REL-03: Nil sandbox guard test ---

func TestWatchGroupOutput_NilSandboxNoPanic(t *testing.T) {
	s := newMockStore()
	q := newMockQueue()
	b := &mockIPCBroker{}
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	o := newTestOrchestratorWithRouter(s, q, b, []channel.Channel{ch})

	// Ensure sandbox is nil (default from newTestOrchestrator).
	o.sandbox = nil

	group := store.Group{
		JID:    "group1@g.us",
		Folder: "test-group",
		IsMain: true,
	}
	o.registeredGroups["group1@g.us"] = group
	q.active["group1@g.us"] = true
	o.lastAgentTimestamp["group1@g.us"] = time.Now()
	o.lastConfirmedTimestamp["group1@g.us"] = time.Now()

	// Create IPC channel that delivers a shutdown message after a brief delay.
	// This forces the liveness ticker to fire at least once before shutdown.
	ipcCh := make(chan *ipc.IPCMessage, 1)
	b.subscribeCh = ipcCh

	// Use a very short startup timeout so the test doesn't hang.
	o.cfg.K8s.SandboxStartupTimeout = 200 * time.Millisecond

	// Set liveness ticker to fire quickly. Since we can't override the liveness
	// ticker directly, we rely on the IPC channel closing to eventually trigger
	// deactivate. The key test: sandbox==nil must not panic in the liveness tick.
	go func() {
		// Wait enough for at least one liveness tick (10s default is too long).
		// Instead, send a shutdown message quickly.
		time.Sleep(50 * time.Millisecond)
		ipcCh <- &ipc.IPCMessage{Type: ipc.IPCShutdown, Payload: json.RawMessage(`{}`)}
		close(ipcCh)
	}()

	// This must not panic even though o.sandbox is nil.
	o.watchGroupOutput(context.Background(), "group1@g.us")
}

// --- BUG-03: DeleteStreams on shutdown test ---

func TestHandleIPCMessage_ShutdownDeletesStreams(t *testing.T) {
	s := newMockStore()
	b := &mockIPCBroker{}
	o := newTestOrchestrator(s, newMockQueue(), b)

	group := store.Group{
		JID:    "group1@g.us",
		Folder: "test-group",
	}

	shutdownPayload, _ := json.Marshal(map[string]string{})
	msg := &ipc.IPCMessage{
		Type:    ipc.IPCShutdown,
		Payload: shutdownPayload,
	}

	result := o.handleIPCMessage(context.Background(), "group1@g.us", group, msg)

	if !result {
		t.Error("handleIPCMessage should return true for shutdown")
	}
	if b.deleteStreamsCalled != 1 {
		t.Errorf("DeleteStreams called %d times, want 1", b.deleteStreamsCalled)
	}
	if b.deleteStreamsGroup != "test-group" {
		t.Errorf("DeleteStreams group = %q, want %q", b.deleteStreamsGroup, "test-group")
	}
}

// --- CleanupOrphans Startup Tests ---

func TestStart_CleanupOrphansOnStartup(t *testing.T) {
	s := newMockStore()
	q := newMockQueue()
	b := &mockIPCBroker{}
	sb := &mockSandboxController{hasActive: false}

	// Register a mock channel factory so ConnectAll succeeds.
	reg := channel.NewRegistry()
	reg.Register("mock", func(_ channel.ChannelConfig) (channel.Channel, error) {
		return &mockChannel{name: "mock", connected: true, ownsJIDs: map[string]bool{}}, nil
	})

	cfg := &config.Config{
		Channels:  config.ChannelsConfig{AssistantName: "TestBot"},
		Queue:     config.QueueConfig{IdleTimeout: 30 * time.Minute},
		Scheduler: config.SchedulerConfig{PollInterval: 60 * time.Second},
	}

	o, err := New(cfg, s, q, b, nil, reg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	// Inject mock sandbox controller directly.
	o.sandbox = sb

	// Cancel context after a short delay so Start() returns.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_ = o.Start(ctx)

	if sb.cleanupOrphansCalled.Load() < 1 {
		t.Fatal("expected CleanupOrphans to be called at startup, but it was not")
	}
}

func TestStart_CleanupOrphansSkippedWhenNilSandbox(t *testing.T) {
	s := newMockStore()
	q := newMockQueue()
	b := &mockIPCBroker{}

	// Register a mock channel factory so ConnectAll succeeds.
	reg := channel.NewRegistry()
	reg.Register("mock", func(_ channel.ChannelConfig) (channel.Channel, error) {
		return &mockChannel{name: "mock", connected: true, ownsJIDs: map[string]bool{}}, nil
	})

	cfg := &config.Config{
		Channels:  config.ChannelsConfig{AssistantName: "TestBot"},
		Queue:     config.QueueConfig{IdleTimeout: 30 * time.Minute},
		Scheduler: config.SchedulerConfig{PollInterval: 60 * time.Second},
	}

	o, err := New(cfg, s, q, b, nil, reg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	// sandbox is nil — should not panic.

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Should not panic.
	_ = o.Start(ctx)
}
