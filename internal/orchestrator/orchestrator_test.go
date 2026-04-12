package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
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
	getMessagesSinceErr  error  // if non-nil, GetMessagesSince returns this error

	updateTaskCalled     bool
	deleteTaskCalledWith [2]string // [id, groupFolder]
	setStateCalls        []string  // records keys passed to SetState for counting
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
	if m.getMessagesSinceErr != nil {
		return nil, m.getMessagesSinceErr
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

func (m *mockStore) MarkGroupActive(_ context.Context, _ string) error       { return nil }
func (m *mockStore) MarkGroupInactive(_ context.Context, _ string) error     { return nil }
func (m *mockStore) IsGroupActive(_ context.Context, _ string) (bool, error) { return false, nil }
func (m *mockStore) ActiveGroupCount(_ context.Context) (int64, error)       { return 0, nil }
func (m *mockStore) ActiveGroupJIDs(_ context.Context) ([]string, error)     { return nil, nil }

// --- Mock Queue ---

type mockQueue struct {
	active          map[string]bool
	markActiveErr   error // if non-nil, MarkActive returns this error
	markInactiveErr error // if non-nil, MarkInactive returns this error and skips delete
	isActiveErr     error // if non-nil, IsActive returns false, isActiveErr
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
	if m.markActiveErr != nil {
		return m.markActiveErr
	}
	m.active[groupJID] = true
	return nil
}
func (m *mockQueue) MarkInactive(_ context.Context, groupJID string) error {
	if m.markInactiveErr != nil {
		return m.markInactiveErr
	}
	delete(m.active, groupJID)
	return nil
}
func (m *mockQueue) IsActive(_ context.Context, groupJID string) (bool, error) {
	if m.isActiveErr != nil {
		return false, m.isActiveErr
	}
	return m.active[groupJID], nil
}
func (m *mockQueue) ActiveCount(_ context.Context) (int64, error) { return 0, nil }
func (m *mockQueue) ActiveJIDs(_ context.Context) ([]string, error) {
	jids := make([]string, 0, len(m.active))
	for jid := range m.active {
		jids = append(jids, jid)
	}
	return jids, nil
}
func (m *mockQueue) Close() error { return nil }

// --- Mock IPC Broker ---

type mockIPCBroker struct {
	published           []*ipc.IPCMessage
	inputSent           []*ipc.IPCMessage
	subscribeCh         chan *ipc.IPCMessage // if set, SubscribeOutput returns this channel
	subscribeCount      int                  // number of SubscribeOutput calls
	deleteStreamsCalled int
	deleteStreamsGroup  string
	sendInputFn         func(ctx context.Context, group, agentID string, msg *ipc.IPCMessage) error
	subscribeOutputFn   func(ctx context.Context, group string) (<-chan *ipc.IPCMessage, <-chan error, error)
}

func (m *mockIPCBroker) PublishOutput(_ context.Context, _ string, _ string, msg *ipc.IPCMessage) error {
	m.published = append(m.published, msg)
	return nil
}
func (m *mockIPCBroker) SubscribeOutput(ctx context.Context, group string) (<-chan *ipc.IPCMessage, <-chan error, error) {
	m.subscribeCount++
	if m.subscribeOutputFn != nil {
		return m.subscribeOutputFn(ctx, group)
	}
	if m.subscribeCh != nil {
		// Return the preset channel on the first call only. Subsequent calls
		// (e.g. from the watchGroupOutput reconnect path) fail so tests
		// exercising reconnect reach deactivate quickly.
		if m.subscribeCount > 1 {
			return nil, nil, errors.New("mockIPCBroker: subscribeCh already consumed")
		}
		return m.subscribeCh, make(chan error), nil
	}
	ch := make(chan *ipc.IPCMessage)
	return ch, make(chan error), nil
}
func (m *mockIPCBroker) SendInput(ctx context.Context, group, agentID string, msg *ipc.IPCMessage) error {
	if m.sendInputFn != nil {
		return m.sendInputFn(ctx, group, agentID, msg)
	}
	m.inputSent = append(m.inputSent, msg)
	return nil
}
func (m *mockIPCBroker) ReadInput(_ context.Context, _ string, _ string) (<-chan *ipc.IPCMessage, error) {
	ch := make(chan *ipc.IPCMessage)
	return ch, nil
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
	// Shrink IPC reconnect backoff so tests that exercise the watchGroupOutput
	// reconnect path don't burn the 1+2+4+8 second production schedule.
	o.ipcReconnectDelays = []time.Duration{time.Millisecond, time.Millisecond}
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

// mockSandboxControllerWithTracking tracks CreateSandbox and StopSandbox calls.
type mockSandboxControllerWithTracking struct {
	createCalled atomic.Bool
	createErr    error
	stopCalled   atomic.Bool
}

func (m *mockSandboxControllerWithTracking) CreateSandbox(_ context.Context, _ sandbox.SandboxConfig) (*sandbox.SandboxStatus, error) {
	m.createCalled.Store(true)
	if m.createErr != nil {
		return nil, m.createErr
	}
	return &sandbox.SandboxStatus{Name: "test-sandbox", State: sandbox.StatePending}, nil
}
func (m *mockSandboxControllerWithTracking) StopSandbox(_ context.Context, _ string) error {
	m.stopCalled.Store(true)
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

	// claimSandboxSlot must be held before entering processGroupMessages, matching
	// the production calling convention (pollMessages always claims first).
	release, ok := o.claimSandboxSlot("group1@g.us")
	if !ok {
		t.Fatal("failed to claim sandbox slot for test group")
	}
	defer release()

	_, err = o.processGroupMessages(context.Background(), "group1@g.us")
	if err != nil {
		t.Fatalf("processGroupMessages() error = %v, want nil", err)
	}
	if sb.createCalled.Load() {
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

	// claimSandboxSlot must be held before entering processGroupMessages, matching
	// the production calling convention (pollMessages always claims first).
	release, ok := o.claimSandboxSlot("group1@g.us")
	if !ok {
		t.Fatal("failed to claim sandbox slot for test group")
	}
	defer release()

	_, err = o.processGroupMessages(context.Background(), "group1@g.us")
	// CreateSandbox should be called — it will succeed since sb returns a valid status.
	// But MarkActive will fail because mockQueueWithActiveCount inherits from mockQueue.
	// We just care that CreateSandbox was reached.
	if !sb.createCalled.Load() {
		t.Error("CreateSandbox was NOT called, want called when below MAX_CONCURRENT")
	}
	_ = err // error from MarkActive is acceptable here
}

func TestMaxConcurrent_ActiveCountError_ReturnsError(t *testing.T) {
	s := newMockStore()
	mq := &mockQueueWithActiveCount{
		mockQueue:      mockQueue{active: make(map[string]bool)},
		activeCountErr: fmt.Errorf("queue connection failed"),
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

	_, err = o.processGroupMessages(context.Background(), "group1@g.us")
	if err == nil {
		t.Fatal("processGroupMessages() error = nil, want error from ActiveCount failure")
	}
	if !strings.Contains(err.Error(), "check active count") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "check active count")
	}
}

// --- SendInput / MarkActive failure tests ---

func TestProcessGroupMessages_SendInputFailure_TeardownAndErrors(t *testing.T) {
	s := newMockStore()
	q := newMockQueue()
	sb := &mockSandboxControllerWithTracking{}
	b := &mockIPCBroker{
		sendInputFn: func(_ context.Context, _, _ string, _ *ipc.IPCMessage) error {
			return fmt.Errorf("NATS publish timeout")
		},
	}
	cfg := &config.Config{
		Channels:  config.ChannelsConfig{AssistantName: "TestBot"},
		Queue:     config.QueueConfig{IdleTimeout: 30 * time.Minute, MaxConcurrent: 10, MessageLimit: 500},
		Scheduler: config.SchedulerConfig{PollInterval: 60 * time.Second},
	}
	log := slog.Default()
	reg := channel.NewRegistry()
	o, err := New(cfg, s, q, b, nil, reg, log)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	o.sandbox = sb
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	rtr, _ := router.New([]channel.Channel{ch}, s)
	o.router = rtr
	o.auth = auth.New(s)

	before := time.Now().Add(-time.Second)
	o.registeredGroups["group1@g.us"] = store.Group{
		JID:    "group1@g.us",
		Folder: "test-group",
		IsMain: true,
	}
	s.messages["group1@g.us"] = []store.Message{
		{ChatJID: "group1@g.us", Content: "hello", Timestamp: before, Sender: "alice"},
	}
	o.lastAgentTimestamp["group1@g.us"] = before.Add(-time.Second)
	previousCursor := o.lastAgentTimestamp["group1@g.us"]

	_, err = o.processGroupMessages(context.Background(), "group1@g.us")
	if err == nil {
		t.Fatal("expected error from SendInput failure, got nil")
	}
	if !strings.Contains(err.Error(), "send initial input") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "send initial input")
	}
	if !sb.createCalled.Load() {
		t.Error("CreateSandbox must be called before SendInput")
	}
	if !sb.stopCalled.Load() {
		t.Error("StopSandbox must be called on SendInput failure")
	}
	if q.active["group1@g.us"] {
		t.Error("group must be marked inactive after SendInput failure")
	}
	if got := o.lastAgentTimestamp["group1@g.us"]; !got.Equal(previousCursor) {
		t.Errorf("cursor = %v, want rolled back to %v", got, previousCursor)
	}
}

func TestProcessGroupMessages_MarkActiveFailure_StopsSandbox(t *testing.T) {
	s := newMockStore()
	q := newMockQueue()
	sb := &mockSandboxControllerWithTracking{}
	b := &mockIPCBroker{}
	cfg := &config.Config{
		Channels:  config.ChannelsConfig{AssistantName: "TestBot"},
		Queue:     config.QueueConfig{IdleTimeout: 30 * time.Minute, MaxConcurrent: 10, MessageLimit: 500},
		Scheduler: config.SchedulerConfig{PollInterval: 60 * time.Second},
	}
	log := slog.Default()
	reg := channel.NewRegistry()
	o, err := New(cfg, s, q, b, nil, reg, log)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	o.sandbox = sb
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	rtr, _ := router.New([]channel.Channel{ch}, s)
	o.router = rtr
	o.auth = auth.New(s)

	before := time.Now().Add(-time.Second)
	o.registeredGroups["group1@g.us"] = store.Group{
		JID:    "group1@g.us",
		Folder: "test-group",
		IsMain: true,
	}
	s.messages["group1@g.us"] = []store.Message{
		{ChatJID: "group1@g.us", Content: "hello", Timestamp: before, Sender: "alice"},
	}
	o.lastAgentTimestamp["group1@g.us"] = before.Add(-time.Second)
	previousCursor := o.lastAgentTimestamp["group1@g.us"]

	q.markActiveErr = fmt.Errorf("NATS MarkActive failed")

	_, err = o.processGroupMessages(context.Background(), "group1@g.us")
	if err == nil {
		t.Fatal("expected MarkActive failure error, got nil")
	}
	if !strings.Contains(err.Error(), "mark active") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "mark active")
	}
	if !sb.stopCalled.Load() {
		t.Error("StopSandbox must be called on MarkActive failure")
	}
	if q.active["group1@g.us"] {
		t.Error("group must not be active after MarkActive failure")
	}
	if got := o.lastAgentTimestamp["group1@g.us"]; !got.Equal(previousCursor) {
		t.Errorf("cursor = %v, want rolled back to %v", got, previousCursor)
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

	_, err := o.processGroupMessages(context.Background(), "unknown@g.us")
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

	_, err := o.processGroupMessages(context.Background(), "group1@g.us")
	if err != nil {
		t.Errorf("processGroupMessages() error = %v, want nil for no messages", err)
	}
}

func TestProcessGroupMessages_MarshalInitialInputFailure_ReturnsEarly(t *testing.T) {
	s := newMockStore()
	mq := &mockQueueWithActiveCount{
		mockQueue:   mockQueue{active: make(map[string]bool)},
		activeCount: 0,
	}
	b := &mockIPCBroker{}
	sb := &mockSandboxControllerWithTracking{}
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}

	cfg := &config.Config{
		Channels:  config.ChannelsConfig{AssistantName: "TestBot"},
		Queue:     config.QueueConfig{IdleTimeout: 30 * time.Minute, MaxConcurrent: 5},
		Scheduler: config.SchedulerConfig{PollInterval: 60 * time.Second},
	}
	o, err := New(cfg, s, mq, b, nil, channel.NewRegistry(), slog.Default())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	o.sandbox = sb
	rtr, _ := router.New([]channel.Channel{ch}, s)
	o.router = rtr
	o.auth = auth.New(s)
	o.registeredGroups["group1@g.us"] = store.Group{JID: "group1@g.us", Folder: "test-group", IsMain: true}
	s.messages["group1@g.us"] = []store.Message{{ChatJID: "group1@g.us", Content: "hello", Timestamp: time.Now(), Sender: "alice"}}

	o.marshalInitialInput = func(v any) ([]byte, error) {
		return nil, fmt.Errorf("marshal boom")
	}

	_, err = o.processGroupMessages(context.Background(), "group1@g.us")
	if err == nil {
		t.Fatal("processGroupMessages() error = nil, want wrapped marshal error")
	}
	if !strings.Contains(err.Error(), "marshal initial input") {
		t.Fatalf("error = %q, want context %q", err.Error(), "marshal initial input")
	}
	if sb.createCalled.Load() {
		t.Fatal("CreateSandbox was called, want processGroupMessages to return early")
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

	o.watchGroupOutput(context.Background(), "group1@g.us", ipcCh, make(chan error))

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
	processStarted := make(chan struct{}, 1)
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

	o.watchGroupOutput(context.Background(), "group1@g.us", ipcCh, make(chan error))

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

// TestDeactivate_PendingCheckFailedTriggersReprocessing verifies that when
// GetMessagesSince returns an error during deactivation, pendingCheckFailed is
// set and processGroupMessages is still triggered defensively.
func TestDeactivate_PendingCheckFailedTriggersReprocessing(t *testing.T) {
	s := newMockStore()
	q := newMockQueue()
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	b := &mockIPCBroker{}
	o := newTestOrchestratorWithRouter(s, q, b, []channel.Channel{ch})

	group := store.Group{
		JID:    "group1@g.us",
		Folder: "test-group",
		IsMain: true,
	}
	o.registeredGroups["group1@g.us"] = group
	q.active["group1@g.us"] = true

	// First GetMessagesSince call (from deactivate's pending check) returns an error,
	// triggering pendingCheckFailed = true. The hook clears the error on the second
	// call so processGroupMessages can proceed without panicking.
	var callCount atomic.Int32
	processStarted := make(chan struct{}, 1)
	processBlock := make(chan struct{})
	s.getMessagesSinceErr = errors.New("store unavailable")
	s.getMessagesSinceHook = func() {
		if callCount.Add(1) < 2 {
			return // first call: leave error set so deactivate sees it
		}
		s.getMessagesSinceErr = nil
		select {
		case processStarted <- struct{}{}:
		default:
		}
		<-processBlock
	}

	ipcCh := make(chan *ipc.IPCMessage)
	close(ipcCh)
	b.subscribeCh = ipcCh

	o.watchGroupOutput(context.Background(), "group1@g.us", ipcCh, make(chan error))

	// processGroupMessages must have been triggered even though the pending check failed.
	select {
	case <-processStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("processGroupMessages was not triggered after pendingCheckFailed")
	}

	// Clear messages and unblock the goroutine so it finishes cleanly.
	s.messages["group1@g.us"] = nil
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

	o.watchGroupOutput(context.Background(), "group1@g.us", ipcCh, make(chan error))

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
	o.watchGroupOutput(context.Background(), "group1@g.us", ipcCh, make(chan error))

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

	o.watchGroupOutput(context.Background(), "group1@g.us", ipcCh, make(chan error))

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

	_ = o.handleIPCMessage(context.Background(), "chat@g.us", group, msg)

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
	o.watchGroupOutput(context.Background(), "group1@g.us", ipcCh, make(chan error))
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

// --- reconcileActiveSet Tests ---

// mockSandboxWithHasActive allows controlling HasActiveSandbox per group folder.
type mockSandboxWithHasActive struct {
	mockSandboxControllerWithTracking
	hasActive map[string]bool // folder -> whether a sandbox is running
}

func (m *mockSandboxWithHasActive) HasActiveSandbox(_ context.Context, groupFolder string) (bool, error) {
	return m.hasActive[groupFolder], nil
}

func TestReconcileActiveSet_RemovesStaleJIDs(t *testing.T) {
	s := newMockStore()
	mq := newMockQueue()
	ctx := context.Background()

	// Pre-populate the active set with stale JIDs.
	_ = mq.MarkActive(ctx, "tui:stale-1")
	_ = mq.MarkActive(ctx, "tui:stale-2")
	_ = mq.MarkActive(ctx, "tui:live-1")

	b := &mockIPCBroker{}
	cfg := &config.Config{
		Channels:  config.ChannelsConfig{AssistantName: "TestBot"},
		Queue:     config.QueueConfig{IdleTimeout: 30 * time.Minute, MaxConcurrent: 5},
		Scheduler: config.SchedulerConfig{PollInterval: 60 * time.Second},
	}
	reg := channel.NewRegistry()
	o, err := New(cfg, s, mq, b, nil, reg, slog.Default())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	sb := &mockSandboxWithHasActive{
		hasActive: map[string]bool{
			"live-folder": true, // tui:live-1 has a running sandbox
			// stale-1 and stale-2 have no running sandboxes
		},
	}
	o.sandbox = sb

	// Register only the groups that are in the active set (stale ones will be
	// unregistered, which should also cause removal).
	o.registeredGroups["tui:stale-1"] = store.Group{JID: "tui:stale-1", Folder: "stale-1-folder"}
	o.registeredGroups["tui:stale-2"] = store.Group{JID: "tui:stale-2", Folder: "stale-2-folder"}
	o.registeredGroups["tui:live-1"] = store.Group{JID: "tui:live-1", Folder: "live-folder"}

	o.reconcileActiveSet(ctx)

	active1, _ := mq.IsActive(ctx, "tui:stale-1")
	if active1 {
		t.Error("tui:stale-1 should have been removed from active set")
	}
	active2, _ := mq.IsActive(ctx, "tui:stale-2")
	if active2 {
		t.Error("tui:stale-2 should have been removed from active set")
	}
	liveActive, _ := mq.IsActive(ctx, "tui:live-1")
	if !liveActive {
		t.Error("tui:live-1 should remain in active set (has running sandbox)")
	}
}

func TestReconcileActiveSet_RemovesUnregisteredJIDs(t *testing.T) {
	s := newMockStore()
	mq := newMockQueue()
	ctx := context.Background()

	_ = mq.MarkActive(ctx, "tui:deleted-group")

	b := &mockIPCBroker{}
	cfg := &config.Config{
		Channels:  config.ChannelsConfig{AssistantName: "TestBot"},
		Queue:     config.QueueConfig{IdleTimeout: 30 * time.Minute, MaxConcurrent: 5},
		Scheduler: config.SchedulerConfig{PollInterval: 60 * time.Second},
	}
	reg := channel.NewRegistry()
	o, err := New(cfg, s, mq, b, nil, reg, slog.Default())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	o.sandbox = &mockSandboxControllerWithTracking{}
	// registeredGroups is empty — tui:deleted-group is not registered.

	o.reconcileActiveSet(ctx)

	active, _ := mq.IsActive(ctx, "tui:deleted-group")
	if active {
		t.Error("tui:deleted-group should have been removed from active set (unregistered)")
	}
}

func TestReconcileActiveSet_EmptySet_NoOp(t *testing.T) {
	s := newMockStore()
	mq := newMockQueue()
	ctx := context.Background()

	b := &mockIPCBroker{}
	cfg := &config.Config{
		Channels:  config.ChannelsConfig{AssistantName: "TestBot"},
		Queue:     config.QueueConfig{IdleTimeout: 30 * time.Minute, MaxConcurrent: 5},
		Scheduler: config.SchedulerConfig{PollInterval: 60 * time.Second},
	}
	reg := channel.NewRegistry()
	o, err := New(cfg, s, mq, b, nil, reg, slog.Default())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	o.sandbox = &mockSandboxControllerWithTracking{}

	// Should not panic and should not call MarkInactive for any JID.
	o.reconcileActiveSet(ctx)

	count, _ := mq.ActiveCount(ctx)
	if count != 0 {
		t.Errorf("ActiveCount = %d, want 0", count)
	}
}

// --- handleSandboxEvent Tests ---

func TestHandleSandboxEvent_CompletedSandbox_MarksGroupInactive(t *testing.T) {
	s := newMockStore()
	mq := newMockQueue()
	ctx := context.Background()

	_ = mq.MarkActive(ctx, "tui:test-1")

	b := &mockIPCBroker{}
	cfg := &config.Config{
		Channels:  config.ChannelsConfig{AssistantName: "TestBot"},
		Queue:     config.QueueConfig{IdleTimeout: 30 * time.Minute, MaxConcurrent: 5},
		Scheduler: config.SchedulerConfig{PollInterval: 60 * time.Second},
	}
	reg := channel.NewRegistry()
	o, err := New(cfg, s, mq, b, nil, reg, slog.Default())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	o.registeredGroups["tui:test-1"] = store.Group{
		JID: "tui:test-1", Folder: "test-1-folder",
	}

	event := sandbox.SandboxEvent{
		Type: "updated",
		Status: sandbox.SandboxStatus{
			Name:  "kraclaw-agent-test-1-abc123",
			Group: "test-1-folder",
			State: sandbox.StateCompleted,
		},
	}

	o.handleSandboxEvent(ctx, event)

	active, _ := mq.IsActive(ctx, "tui:test-1")
	if active {
		t.Error("tui:test-1 should be inactive after sandbox completed event")
	}
}

func TestHandleSandboxEvent_DeletedSandbox_MarksGroupInactive(t *testing.T) {
	s := newMockStore()
	mq := newMockQueue()
	ctx := context.Background()

	_ = mq.MarkActive(ctx, "tui:test-2")

	b := &mockIPCBroker{}
	cfg := &config.Config{
		Channels:  config.ChannelsConfig{AssistantName: "TestBot"},
		Queue:     config.QueueConfig{IdleTimeout: 30 * time.Minute, MaxConcurrent: 5},
		Scheduler: config.SchedulerConfig{PollInterval: 60 * time.Second},
	}
	reg := channel.NewRegistry()
	o, err := New(cfg, s, mq, b, nil, reg, slog.Default())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	o.registeredGroups["tui:test-2"] = store.Group{
		JID: "tui:test-2", Folder: "test-2-folder",
	}

	event := sandbox.SandboxEvent{
		Type: "deleted",
		Status: sandbox.SandboxStatus{
			Name:  "kraclaw-agent-test-2-def456",
			Group: "test-2-folder",
			State: sandbox.StateRunning,
		},
	}

	o.handleSandboxEvent(ctx, event)

	active, _ := mq.IsActive(ctx, "tui:test-2")
	if active {
		t.Error("tui:test-2 should be inactive after sandbox deleted event")
	}
}

func TestHandleSandboxEvent_RunningUpdate_NoChange(t *testing.T) {
	s := newMockStore()
	mq := newMockQueue()
	ctx := context.Background()

	_ = mq.MarkActive(ctx, "tui:test-3")

	b := &mockIPCBroker{}
	cfg := &config.Config{
		Channels:  config.ChannelsConfig{AssistantName: "TestBot"},
		Queue:     config.QueueConfig{IdleTimeout: 30 * time.Minute, MaxConcurrent: 5},
		Scheduler: config.SchedulerConfig{PollInterval: 60 * time.Second},
	}
	reg := channel.NewRegistry()
	o, err := New(cfg, s, mq, b, nil, reg, slog.Default())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	o.registeredGroups["tui:test-3"] = store.Group{
		JID: "tui:test-3", Folder: "test-3-folder",
	}

	event := sandbox.SandboxEvent{
		Type: "updated",
		Status: sandbox.SandboxStatus{
			Name:  "kraclaw-agent-test-3-ghi789",
			Group: "test-3-folder",
			State: sandbox.StateRunning,
		},
	}

	o.handleSandboxEvent(ctx, event)

	active, _ := mq.IsActive(ctx, "tui:test-3")
	if !active {
		t.Error("tui:test-3 should remain active after a running update event")
	}
}

func TestHandleSandboxEvent_UnknownFolder_NoOp(t *testing.T) {
	s := newMockStore()
	mq := newMockQueue()
	ctx := context.Background()

	b := &mockIPCBroker{}
	cfg := &config.Config{
		Channels:  config.ChannelsConfig{AssistantName: "TestBot"},
		Queue:     config.QueueConfig{IdleTimeout: 30 * time.Minute, MaxConcurrent: 5},
		Scheduler: config.SchedulerConfig{PollInterval: 60 * time.Second},
	}
	reg := channel.NewRegistry()
	o, err := New(cfg, s, mq, b, nil, reg, slog.Default())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	// No groups registered.

	event := sandbox.SandboxEvent{
		Type: "deleted",
		Status: sandbox.SandboxStatus{
			Name:  "kraclaw-agent-ghost-abc",
			Group: "ghost-folder",
			State: sandbox.StateCompleted,
		},
	}

	// Should not panic.
	o.handleSandboxEvent(ctx, event)

	count, _ := mq.ActiveCount(ctx)
	if count != 0 {
		t.Errorf("ActiveCount = %d, want 0 (no group registered)", count)
	}
}

func TestHandleSandboxEvent_StaleSandboxDeletion_PreservesActiveState(t *testing.T) {
	s := newMockStore()
	mq := newMockQueue()
	ctx := context.Background()

	_ = mq.MarkActive(ctx, "tui:test-stale")

	b := &mockIPCBroker{}
	cfg := &config.Config{
		Channels:  config.ChannelsConfig{AssistantName: "TestBot"},
		Queue:     config.QueueConfig{IdleTimeout: 30 * time.Minute, MaxConcurrent: 5},
		Scheduler: config.SchedulerConfig{PollInterval: 60 * time.Second},
	}
	reg := channel.NewRegistry()
	o, err := New(cfg, s, mq, b, nil, reg, slog.Default())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	o.registeredGroups["tui:test-stale"] = store.Group{
		JID: "tui:test-stale", Folder: "stale-folder",
	}
	// Simulate: a new sandbox is the current one.
	o.activeSandboxes["tui:test-stale"] = "kraclaw-agent-stale-new-xyz"

	// An OLD sandbox for the same folder is deleted (orphan cleanup).
	event := sandbox.SandboxEvent{
		Type: "deleted",
		Status: sandbox.SandboxStatus{
			Name:  "kraclaw-agent-stale-old-abc",
			Group: "stale-folder",
			State: sandbox.StateCompleted,
		},
	}

	o.handleSandboxEvent(ctx, event)

	// Group should STILL be active because the deleted sandbox is not the current one.
	active, _ := mq.IsActive(ctx, "tui:test-stale")
	if !active {
		t.Error("tui:test-stale should remain active — deleted sandbox was stale, not the current one")
	}
}

func TestHandleSandboxEvent_CurrentSandboxDeletion_MarksInactive(t *testing.T) {
	s := newMockStore()
	mq := newMockQueue()
	ctx := context.Background()

	_ = mq.MarkActive(ctx, "tui:test-current")

	b := &mockIPCBroker{}
	cfg := &config.Config{
		Channels:  config.ChannelsConfig{AssistantName: "TestBot"},
		Queue:     config.QueueConfig{IdleTimeout: 30 * time.Minute, MaxConcurrent: 5},
		Scheduler: config.SchedulerConfig{PollInterval: 60 * time.Second},
	}
	reg := channel.NewRegistry()
	o, err := New(cfg, s, mq, b, nil, reg, slog.Default())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	o.registeredGroups["tui:test-current"] = store.Group{
		JID: "tui:test-current", Folder: "current-folder",
	}
	o.activeSandboxes["tui:test-current"] = "kraclaw-agent-current-abc"

	// The CURRENT sandbox is deleted.
	event := sandbox.SandboxEvent{
		Type: "deleted",
		Status: sandbox.SandboxStatus{
			Name:  "kraclaw-agent-current-abc",
			Group: "current-folder",
			State: sandbox.StateCompleted,
		},
	}

	o.handleSandboxEvent(ctx, event)

	active, _ := mq.IsActive(ctx, "tui:test-current")
	if active {
		t.Error("tui:test-current should be inactive — the current sandbox was deleted")
	}
}

// TestWatchGroupOutput_ReconnectSuccess exercises the happy path of the
// exponential-backoff reconnect loop: when the IPC output channel closes
// mid-stream (as happens on an iterator error), watchGroupOutput must call
// SubscribeOutput again, assign the returned channel to `ch`, and continue
// consuming messages — without deactivating the group.
func TestWatchGroupOutput_ReconnectSuccess(t *testing.T) {
	s := newMockStore()
	q := newMockQueue()
	b := &mockIPCBroker{}

	// channel1 delivers one session_update message and then closes, simulating
	// an iterator error mid-stream. channel2 is a fresh channel returned by the
	// second SubscribeOutput call; it delivers a shutdown so the watcher exits
	// cleanly.
	channel1 := make(chan *ipc.IPCMessage, 1)
	channel2 := make(chan *ipc.IPCMessage, 1)

	var subCalls int
	b.subscribeOutputFn = func(_ context.Context, _ string) (<-chan *ipc.IPCMessage, <-chan error, error) {
		subCalls++
		return channel2, make(chan error), nil
	}

	o := newTestOrchestrator(s, q, b)

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

	// First message on channel1: a benign session_update that handleIPCMessage
	// can process without a router.
	sessionPayload, _ := json.Marshal(map[string]string{"sessionId": "sess-from-ch1"})
	channel1 <- &ipc.IPCMessage{Type: ipc.IPCSessionUpdate, Payload: sessionPayload}
	close(channel1)

	// Second message on channel2 after reconnect: a session_update that
	// proves channel2 is being consumed, followed by a shutdown to exit.
	go func() {
		sessionPayload2, _ := json.Marshal(map[string]string{"sessionId": "sess-from-ch2"})
		channel2 <- &ipc.IPCMessage{Type: ipc.IPCSessionUpdate, Payload: sessionPayload2}
		// Small delay so the session_update is processed before shutdown.
		time.Sleep(20 * time.Millisecond)
		shutdownPayload, _ := json.Marshal(map[string]string{})
		channel2 <- &ipc.IPCMessage{Type: ipc.IPCShutdown, Payload: shutdownPayload}
		close(channel2)
	}()

	o.watchGroupOutput(context.Background(), "group1@g.us", channel1, make(chan error))

	// SubscribeOutput must have been called exactly once — the reconnect call.
	if subCalls != 1 {
		t.Errorf("SubscribeOutput reconnect calls = %d, want 1", subCalls)
	}

	// Both session_update messages must have been processed: the latest write
	// wins, and since shutdown is what exits the loop, the last observed
	// session ID is the one from channel2.
	got := o.sessions["test-group"]
	if got != "sess-from-ch2" {
		t.Errorf("session after reconnect = %q, want %q (channel2 message was not processed)", got, "sess-from-ch2")
	}

	// Ensure the session from channel1 was also processed (i.e. the earlier
	// message before the reconnect got through) — the store would have
	// observed an UpsertSession for "sess-from-ch1" prior to the final value.
	// We can assert that at least one UpsertSession call landed with the
	// channel1 value by verifying the store saw the ch2 value, which only
	// happens if the reconnect path advanced past channel1's close.
	// (Implicit in subCalls==1 and got=="sess-from-ch2".)
}

// TestWatchGroupOutput_ReconnectExhaustedLogsLastError verifies that when all
// reconnect attempts fail, watchGroupOutput deactivates the group.
func TestWatchGroupOutput_ReconnectExhaustedLogsLastError(t *testing.T) {
	s := newMockStore()
	q := newMockQueue()
	b := &mockIPCBroker{}

	// Every SubscribeOutput call returns an error, simulating a persistently
	// broken IPC connection during the reconnect phase.
	reconnectErr := errors.New("ipc: connection refused")
	b.subscribeOutputFn = func(_ context.Context, _ string) (<-chan *ipc.IPCMessage, <-chan error, error) {
		return nil, nil, reconnectErr
	}

	o := newTestOrchestrator(s, q, b)

	group := store.Group{
		JID:    "group1@g.us",
		Folder: "test-group",
		IsMain: true,
	}
	o.registeredGroups["group1@g.us"] = group
	q.active["group1@g.us"] = true
	o.activeSandboxes["group1@g.us"] = "kraclaw-agent-test-abc"

	// channel1 closes immediately, triggering the reconnect loop.
	channel1 := make(chan *ipc.IPCMessage)
	close(channel1)

	o.watchGroupOutput(context.Background(), "group1@g.us", channel1, make(chan error))

	// After all reconnect delays are exhausted, the group must be deactivated.
	if q.active["group1@g.us"] {
		t.Error("group still active after reconnect exhaustion; expected MarkInactive to be called")
	}
	if _, exists := o.activeSandboxes["group1@g.us"]; exists {
		t.Error("activeSandboxes entry still present after reconnect exhaustion; expected it to be removed")
	}
	// SubscribeOutput must be called exactly once per reconnect delay — no more, no less.
	wantCalls := len(o.ipcReconnectDelays)
	if b.subscribeCount != wantCalls {
		t.Errorf("SubscribeOutput called %d times, want %d (one per reconnect delay)", b.subscribeCount, wantCalls)
	}
}

// mockQueueRecording wraps mockQueue and records Enqueue calls for assertion.
type mockQueueRecording struct {
	*mockQueue
	mu          sync.Mutex
	enqueued    []string // groupJIDs passed to Enqueue
	enqueueErr  error
	dequeueOnce *queue.QueueMessage // if non-nil, returned once then cleared
	dequeueErr  error               // if non-nil, Dequeue returns this error
}

func (m *mockQueueRecording) Enqueue(_ context.Context, groupJID string, _ *queue.QueueMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enqueued = append(m.enqueued, groupJID)
	return m.enqueueErr
}

func (m *mockQueueRecording) Dequeue(_ context.Context, _ string) (*queue.QueueMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dequeueErr != nil {
		return nil, m.dequeueErr
	}
	if m.dequeueOnce != nil {
		msg := m.dequeueOnce
		m.dequeueOnce = nil
		return msg, nil
	}
	return nil, nil
}

func (m *mockQueueRecording) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.enqueued)
}

// TestRecoverPendingMessages covers the startup recovery path that checks each
// registered group for unprocessed messages and enqueues a recovery marker
// when any are found.
func TestRecoverPendingMessages(t *testing.T) {
	ts := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)

	tests := []struct {
		name         string
		setupStore   func(s *mockStore)
		enqueueErr   error
		wantEnqueues int
	}{
		{
			name: "no pending messages",
			setupStore: func(s *mockStore) {
				// No messages in store for the group.
			},
			wantEnqueues: 0,
		},
		{
			name: "pending messages exist",
			setupStore: func(s *mockStore) {
				s.messages["group1@g.us"] = []store.Message{
					{ChatJID: "group1@g.us", Content: "pending", Timestamp: ts.Add(1 * time.Minute)},
				}
			},
			wantEnqueues: 1,
		},
		{
			name: "GetMessagesSince error is logged and does not abort loop",
			setupStore: func(s *mockStore) {
				// Force GetMessagesSince to return an error for every group.
				// Expectation: recoverPendingMessages logs and continues, so
				// Enqueue is never called.
				s.getMessagesSinceErr = errors.New("store boom")
			},
			wantEnqueues: 0,
		},
		{
			name: "Enqueue error is logged",
			setupStore: func(s *mockStore) {
				s.messages["group1@g.us"] = []store.Message{
					{ChatJID: "group1@g.us", Content: "pending", Timestamp: ts.Add(1 * time.Minute)},
				}
			},
			enqueueErr:   errors.New("enqueue failed"),
			wantEnqueues: 1, // Enqueue was called; error is logged, not propagated.
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newMockStore()
			tc.setupStore(s)

			q := &mockQueueRecording{mockQueue: newMockQueue(), enqueueErr: tc.enqueueErr}
			o := newTestOrchestrator(s, q.mockQueue, &mockIPCBroker{})
			// Replace queue with the recording wrapper.
			o.queue = q

			o.registeredGroups["group1@g.us"] = store.Group{
				JID:    "group1@g.us",
				Folder: "test-group",
				Name:   "Test Group",
			}
			o.lastAgentTimestamp["group1@g.us"] = ts

			o.recoverPendingMessages(context.Background())

			if got := q.count(); got != tc.wantEnqueues {
				t.Errorf("Enqueue call count = %d, want %d", got, tc.wantEnqueues)
			}
		})
	}
}

func TestHandleSandboxEvent_UntrackedSandbox_StillMarksInactive(t *testing.T) {
	s := newMockStore()
	mq := newMockQueue()
	ctx := context.Background()

	_ = mq.MarkActive(ctx, "tui:test-untracked")

	b := &mockIPCBroker{}
	cfg := &config.Config{
		Channels:  config.ChannelsConfig{AssistantName: "TestBot"},
		Queue:     config.QueueConfig{IdleTimeout: 30 * time.Minute, MaxConcurrent: 5},
		Scheduler: config.SchedulerConfig{PollInterval: 60 * time.Second},
	}
	reg := channel.NewRegistry()
	o, err := New(cfg, s, mq, b, nil, reg, slog.Default())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	o.registeredGroups["tui:test-untracked"] = store.Group{
		JID: "tui:test-untracked", Folder: "untracked-folder",
	}
	// No activeSandboxes entry — simulates server restart.

	event := sandbox.SandboxEvent{
		Type: "deleted",
		Status: sandbox.SandboxStatus{
			Name:  "kraclaw-agent-untracked-xyz",
			Group: "untracked-folder",
			State: sandbox.StateCompleted,
		},
	}

	o.handleSandboxEvent(ctx, event)

	active, _ := mq.IsActive(ctx, "tui:test-untracked")
	if active {
		t.Error("tui:test-untracked should be inactive — untracked sandbox means safety-net should fire")
	}
}

func TestWatchGroupOutput_ReconnectUsesGroupFolder(t *testing.T) {
	s := newMockStore()
	q := newMockQueue()
	b := &mockIPCBroker{}
	o := newTestOrchestrator(s, q, b)

	// JID and Folder are deliberately different — this is the normal production case
	// where Folder is a sanitized filesystem path, not the raw group JID.
	group := store.Group{
		JID:    "group1@g.us",
		Folder: "test-group-folder",
		IsMain: true,
	}
	o.registeredGroups["group1@g.us"] = group
	q.active["group1@g.us"] = true

	// Capture the group argument passed to SubscribeOutput on reconnect.
	var reconnectGroup string
	b.subscribeOutputFn = func(ctx context.Context, grp string) (<-chan *ipc.IPCMessage, <-chan error, error) {
		reconnectGroup = grp
		// Return a channel with an agent shutdown message so watchGroupOutput terminates cleanly.
		ch := make(chan *ipc.IPCMessage, 1)
		ch <- &ipc.IPCMessage{Type: ipc.IPCShutdown}
		return ch, make(chan error), nil
	}

	// A closed initial channel triggers the reconnect path immediately.
	// Before the fix, reconnect called SubscribeOutput with chatJID instead of
	// group.Folder, subscribing to a stream keyed on the wrong SHA-256 hash.
	initialCh := make(chan *ipc.IPCMessage)
	close(initialCh)

	o.watchGroupOutput(context.Background(), "group1@g.us", initialCh, make(chan error))

	if reconnectGroup != group.Folder {
		t.Errorf("SubscribeOutput reconnect arg = %q, want group.Folder %q (was chatJID %q before fix)", reconnectGroup, group.Folder, group.JID)
	}
}

// --- Double-Sandbox TOCTOU Fix Tests ---

// TestClaimSandboxSlot_AtomicClaimAndRelease is a unit test for the
// claimSandboxSlot helper. It verifies atomic claim semantics: one winner per
// chatJID, no collisions across different JIDs, and release restores availability.
func TestClaimSandboxSlot_AtomicClaimAndRelease(t *testing.T) {
	o := &Orchestrator{}

	// First claim wins.
	release, ok := o.claimSandboxSlot("group1@g.us")
	if !ok {
		t.Fatal("first claimSandboxSlot() = false, want true")
	}
	if release == nil {
		t.Fatal("first claimSandboxSlot() release = nil, want non-nil")
	}

	// Second claim for same JID must be rejected.
	_, ok2 := o.claimSandboxSlot("group1@g.us")
	if ok2 {
		t.Error("second claimSandboxSlot() = true, want false (slot already held)")
	}

	// Different JID must not collide with the held slot.
	release2, ok3 := o.claimSandboxSlot("group2@g.us")
	if !ok3 {
		t.Error("claimSandboxSlot() for different JID = false, want true")
	}
	if release2 != nil {
		release2()
	}

	// After release, the same JID is claimable again.
	release()
	release3, ok4 := o.claimSandboxSlot("group1@g.us")
	if !ok4 {
		t.Error("claimSandboxSlot() after release = false, want true")
	}
	if release3 != nil {
		release3()
	}
}

// waitSlotReleased polls o.inflightSandboxes until jid is absent or timeout
// elapses. Uses a ticker to avoid busy-wait under -race.
func waitSlotReleased(t *testing.T, o *Orchestrator, jid string, timeout time.Duration) {
	t.Helper()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case <-tick.C:
			if _, held := o.inflightSandboxes.Load(jid); !held {
				return
			}
		case <-deadline.C:
			t.Errorf("in-flight slot for %q was not released within %v", jid, timeout)
			return
		}
	}
}

// TestPollMessages_PanicReleasesSlot verifies that a panic inside
// processGroupMessages still releases the in-flight slot via the deferred
// release() in the spawn goroutine's defer stack.
func TestPollMessages_PanicReleasesSlot(t *testing.T) {
	s := newMockStore()
	q := newMockQueue()
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	b := &mockIPCBroker{}
	o := newTestOrchestratorWithRouter(s, q, b, []channel.Channel{ch})

	// Inject a panic via store hook — fires inside processGroupMessages.
	s.getMessagesSinceHook = func() { panic("injected panic for slot-release test") }

	group := store.Group{JID: "group1@g.us", Folder: "test-group", IsMain: true}
	s.groups = append(s.groups, group)
	o.registeredGroups["group1@g.us"] = group
	s.messages["group1@g.us"] = []store.Message{
		{ChatJID: "group1@g.us", Content: "hello", Timestamp: time.Now(), Sender: "alice"},
	}

	o.pollMessages(context.Background())

	// Allow the spawned goroutine to run, panic, recover, and release the slot.
	waitSlotReleased(t, o, "group1@g.us", 2*time.Second)

	// The panic fired inside GetMessagesSince — before MarkActive — so the group
	// was never inserted into activeSandboxes. With the wasActive guard, the panic handler
	// skips MarkInactive and must not leave a stale activeSandboxes entry.
	o.mu.Lock()
	_, inActive := o.activeSandboxes["group1@g.us"]
	o.mu.Unlock()
	if inActive {
		t.Error("activeSandboxes still contains group1@g.us after pre-MarkActive panic; handler should have skipped cleanup")
	}
}

// mockSandboxWithGate is a sandbox mock that supports an atomic call counter and
// a synchronisation gate so concurrent spawn tests can inspect in-progress state.
type mockSandboxWithGate struct {
	createCount atomic.Int32
	createErr   error
	// If createStarted is non-nil, it is signalled before createGate is waited on.
	createStarted chan struct{}
	// If createGate is non-nil, CreateSandbox blocks until the gate is closed.
	createGate chan struct{}
	// If createDone is non-nil, it is closed when CreateSandbox returns.
	createDone chan struct{}
}

func (m *mockSandboxWithGate) CreateSandbox(_ context.Context, _ sandbox.SandboxConfig) (*sandbox.SandboxStatus, error) {
	if m.createDone != nil {
		defer close(m.createDone)
	}
	m.createCount.Add(1)
	if m.createStarted != nil {
		select {
		case m.createStarted <- struct{}{}:
		default:
		}
	}
	if m.createGate != nil {
		<-m.createGate
	}
	if m.createErr != nil {
		return nil, m.createErr
	}
	return &sandbox.SandboxStatus{Name: "test-sandbox", State: sandbox.StatePending}, nil
}
func (m *mockSandboxWithGate) StopSandbox(_ context.Context, _ string) error { return nil }
func (m *mockSandboxWithGate) HasActiveSandbox(_ context.Context, _ string) (bool, error) {
	return false, nil
}
func (m *mockSandboxWithGate) CleanupOrphans(_ context.Context) error { return nil }
func (m *mockSandboxWithGate) WatchSandboxes(_ context.Context) (<-chan sandbox.SandboxEvent, error) {
	return make(chan sandbox.SandboxEvent), nil
}

// TestPollMessages_ConcurrentSpawn_SingleSandbox is the primary regression test
// for the double-sandbox TOCTOU. It verifies that two concurrent pollMessages
// calls for the same group result in exactly one CreateSandbox call, even when
// the first goroutine is still inside processGroupMessages when the second poll
// fires.
func TestPollMessages_ConcurrentSpawn_SingleSandbox(t *testing.T) {
	s := newMockStore()
	mq := &mockQueueWithActiveCount{
		mockQueue:   mockQueue{active: make(map[string]bool)},
		activeCount: 0,
	}
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	b := &mockIPCBroker{}

	createStarted := make(chan struct{}, 1)
	createGate := make(chan struct{})
	createDone := make(chan struct{})
	sb := &mockSandboxWithGate{
		createStarted: createStarted,
		createGate:    createGate,
		createDone:    createDone,
	}

	cfg := &config.Config{
		Channels:  config.ChannelsConfig{AssistantName: "TestBot"},
		Queue:     config.QueueConfig{IdleTimeout: 30 * time.Minute, MaxConcurrent: 5},
		Scheduler: config.SchedulerConfig{PollInterval: 60 * time.Second},
	}
	reg := channel.NewRegistry()
	o, err := New(cfg, s, mq, b, nil, reg, slog.Default())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	o.sandbox = sb
	rtr, _ := router.New([]channel.Channel{ch}, s)
	o.router = rtr
	o.auth = auth.New(s)

	group := store.Group{JID: "group1@g.us", Folder: "test-group", IsMain: true}
	// Seed both the store (for refreshGroups) and the in-memory map.
	s.groups = append(s.groups, group)
	o.registeredGroups["group1@g.us"] = group
	s.messages["group1@g.us"] = []store.Message{
		{ChatJID: "group1@g.us", Content: "hello", Timestamp: time.Now(), Sender: "alice"},
	}

	// First pollMessages: wins the claim, spawns a goroutine that blocks in CreateSandbox.
	o.pollMessages(context.Background())

	// Wait until the first goroutine has entered CreateSandbox (slot is still held).
	select {
	case <-createStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first goroutine to reach CreateSandbox")
	}

	// Second pollMessages: the in-flight slot is already held — must skip.
	o.pollMessages(context.Background())

	// Unblock the first goroutine.
	close(createGate)

	// Wait for CreateSandbox to return. Note: defer release() fires only after
	// processGroupMessages returns entirely (MarkActive, SubscribeOutput, etc.
	// still run after CreateSandbox). waitSlotReleased polls until the slot is
	// gone rather than assuming release() is immediate after createDone.
	select {
	case <-createDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first goroutine to finish CreateSandbox")
	}
	waitSlotReleased(t, o, "group1@g.us", 2*time.Second)

	if got := sb.createCount.Load(); got != 1 {
		t.Errorf("CreateSandbox called %d times, want 1", got)
	}
}

// TestMaxConcurrent_IncludesInflight verifies that in-flight spawns for OTHER
// groups count toward the MaxConcurrent limit, closing the cross-group TOCTOU
// between ActiveCount() and MarkActive().
func TestMaxConcurrent_IncludesInflight(t *testing.T) {
	s := newMockStore()
	// One group is already committed-active.
	mq := &mockQueueWithActiveCount{
		mockQueue:   mockQueue{active: make(map[string]bool)},
		activeCount: 1,
	}
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	b := &mockIPCBroker{}
	sb := &mockSandboxControllerWithTracking{}

	const maxConcurrent = 3
	cfg := &config.Config{
		Channels:  config.ChannelsConfig{AssistantName: "TestBot"},
		Queue:     config.QueueConfig{IdleTimeout: 30 * time.Minute, MaxConcurrent: maxConcurrent},
		Scheduler: config.SchedulerConfig{PollInterval: 60 * time.Second},
	}
	reg := channel.NewRegistry()
	o, err := New(cfg, s, mq, b, nil, reg, slog.Default())
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
	// processGroupMessages is called directly; no need to seed s.groups.

	// Pre-seed inflightSandboxes with (maxConcurrent - 1) OTHER groups so that
	// activeCount(1) + inflight(maxConcurrent-1) == maxConcurrent.
	// Range excludes own JID, so others = maxConcurrent-1 = 2.
	// activeCount(1) + others(2) = maxConcurrent(3) → must skip CreateSandbox.
	for i := range maxConcurrent - 1 {
		o.inflightSandboxes.Store(fmt.Sprintf("other%d@g.us", i), struct{}{})
	}
	// Also claim the slot for the group under test (as pollMessages would do).
	release, ok := o.claimSandboxSlot("group1@g.us")
	if !ok {
		t.Fatal("failed to claim sandbox slot for test group")
	}
	defer release()

	_, err = o.processGroupMessages(context.Background(), "group1@g.us")
	if err != nil {
		t.Fatalf("processGroupMessages() error = %v, want nil", err)
	}
	if sb.createCalled.Load() {
		t.Error("CreateSandbox was called, want skipped: activeCount+inflight >= MaxConcurrent")
	}
}

// TestDeactivateRecovery_TakesSlotWhenFree is the positive companion to
// TestDeactivateRecovery_SkipsWhenSlotHeld. It verifies that when the slot is
// NOT pre-held, the recovery goroutine claims it (inflightSandboxes.Load returns
// true during processGroupMessages) and releases it after watchGroupOutput returns.
func TestDeactivateRecovery_TakesSlotWhenFree(t *testing.T) {
	s := newMockStore()
	q := newMockQueue()
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	b := &mockIPCBroker{}
	o := newTestOrchestratorWithRouter(s, q, b, []channel.Channel{ch})

	group := store.Group{JID: "group1@g.us", Folder: "test-group", IsMain: true}
	o.registeredGroups["group1@g.us"] = group
	q.active["group1@g.us"] = true

	// Seed a pending message so the recovery path fires.
	s.messages["group1@g.us"] = []store.Message{
		{ChatJID: "group1@g.us", Content: "pending", Timestamp: time.Now(), Sender: "alice"},
	}

	var slotHeldDuringRecovery atomic.Bool
	var callCount atomic.Int32
	recoveryEntered := make(chan struct{}, 1)
	s.getMessagesSinceHook = func() {
		if callCount.Add(1) < 2 {
			return // first call is deactivate()'s own pending check
		}
		if _, held := o.inflightSandboxes.Load("group1@g.us"); held {
			slotHeldDuringRecovery.Store(true)
		}
		select {
		case recoveryEntered <- struct{}{}:
		default:
		}
	}

	ipcCh := make(chan *ipc.IPCMessage)
	close(ipcCh)
	b.subscribeCh = ipcCh
	o.watchGroupOutput(context.Background(), "group1@g.us", ipcCh, make(chan error))

	select {
	case <-recoveryEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("recovery goroutine was not spawned")
	}
	if !slotHeldDuringRecovery.Load() {
		t.Error("slot was NOT held during recovery goroutine execution")
	}
	waitSlotReleased(t, o, "group1@g.us", 2*time.Second)
}

// TestDeactivateRecovery_SkipsWhenSlotHeld verifies that when the
// watchGroupOutput recovery goroutine tries to spawn a processGroupMessages call
// but the in-flight slot is already held, it skips rather than spawning a second
// concurrent goroutine for the same group.
func TestDeactivateRecovery_SkipsWhenSlotHeld(t *testing.T) {
	s := newMockStore()
	q := newMockQueue()
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	b := &mockIPCBroker{}
	o := newTestOrchestratorWithRouter(s, q, b, []channel.Channel{ch})

	group := store.Group{JID: "group1@g.us", Folder: "test-group", IsMain: true}
	o.registeredGroups["group1@g.us"] = group
	q.active["group1@g.us"] = true

	// Seed a pending message so the recovery path would normally fire.
	s.messages["group1@g.us"] = []store.Message{
		{ChatJID: "group1@g.us", Content: "pending", Timestamp: time.Now(), Sender: "alice"},
	}

	// Track whether processGroupMessages was entered (second GetMessagesSince call).
	var callCount atomic.Int32
	recoveryEntered := make(chan struct{}, 1)
	s.getMessagesSinceHook = func() {
		if callCount.Add(1) < 2 {
			return // first call is deactivate()'s own pending check
		}
		select {
		case recoveryEntered <- struct{}{}:
		default:
		}
	}

	// Pre-claim the slot BEFORE watchGroupOutput can claim it.
	release, ok := o.claimSandboxSlot("group1@g.us")
	if !ok {
		t.Fatal("failed to pre-claim sandbox slot")
	}
	defer release()

	// Close IPC channel to trigger deactivate + recovery path.
	ipcCh := make(chan *ipc.IPCMessage)
	close(ipcCh)
	b.subscribeCh = ipcCh

	o.watchGroupOutput(context.Background(), "group1@g.us", ipcCh, make(chan error))

	// Recovery goroutine must have been skipped — no second GetMessagesSince call.
	// The slot claim fails, so the recovery goroutine is never spawned.
	// callCount reflects only the synchronous pending-check call.
	if got := callCount.Load(); got != 1 {
		t.Errorf("GetMessagesSince called %d times, want 1 (recovery goroutine must have been skipped)", got)
	}
	// Drain recoveryEntered in case of a latent send from a scheduler-paused goroutine.
	select {
	case <-recoveryEntered:
		t.Error("recovery goroutine was spawned despite pre-held in-flight slot")
	default:
	}
}

// TestDeactivateRecovery_ReenqueuesDequeuedMsgWhenSlotHeld verifies that when
// the in-flight slot is already held and a non-nil qMsg was dequeued, the
// message is re-enqueued so it is not silently lost.
func TestDeactivateRecovery_ReenqueuesDequeuedMsgWhenSlotHeld(t *testing.T) {
	s := newMockStore()
	mq := &mockQueueRecording{mockQueue: newMockQueue()}
	mq.active["group1@g.us"] = true
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	b := &mockIPCBroker{}
	// Build the orchestrator with the base mock, then replace queue with the recording wrapper.
	o := newTestOrchestratorWithRouter(s, mq.mockQueue, b, []channel.Channel{ch})
	o.queue = mq

	group := store.Group{JID: "group1@g.us", Folder: "test-group", IsMain: true}
	o.registeredGroups["group1@g.us"] = group

	// Arrange for Dequeue to return a non-nil message once.
	mq.dequeueOnce = &queue.QueueMessage{GroupJID: "group1@g.us", Content: "task payload", IsTask: true}

	// Track processGroupMessages spawns (second GetMessagesSince call).
	var callCount atomic.Int32
	recoveryEntered := make(chan struct{}, 1)
	s.getMessagesSinceHook = func() {
		if callCount.Add(1) < 2 {
			return // first call is deactivate()'s own pending check
		}
		select {
		case recoveryEntered <- struct{}{}:
		default:
		}
	}

	// Pre-claim the slot so the recovery goroutine cannot claim it.
	release, ok := o.claimSandboxSlot("group1@g.us")
	if !ok {
		t.Fatal("failed to pre-claim sandbox slot")
	}
	defer release()

	ipcCh := make(chan *ipc.IPCMessage)
	close(ipcCh)
	b.subscribeCh = ipcCh

	o.watchGroupOutput(context.Background(), "group1@g.us", ipcCh, make(chan error))

	// Enqueue must have been called exactly once (to re-enqueue the dequeued message).
	if got := mq.count(); got != 1 {
		t.Errorf("Enqueue called %d times, want 1 (dequeued message must be re-enqueued)", got)
	}
	// processGroupMessages must NOT have been spawned.
	select {
	case <-recoveryEntered:
		t.Error("recovery goroutine was spawned despite pre-held in-flight slot")
	default:
	}
}

// TestDeactivateRecovery_ReenqueueErrorLoggedAndMessageLost verifies that when
// re-enqueueing a dequeued message fails, the error is absorbed (not propagated)
// and processGroupMessages is still not spawned.
func TestDeactivateRecovery_ReenqueueErrorLoggedAndMessageLost(t *testing.T) {
	s := newMockStore()
	mq := &mockQueueRecording{
		mockQueue:  newMockQueue(),
		enqueueErr: errors.New("enqueue failure"),
	}
	mq.active["group1@g.us"] = true
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	b := &mockIPCBroker{}
	// Build the orchestrator with the base mock, then replace queue with the recording wrapper.
	o := newTestOrchestratorWithRouter(s, mq.mockQueue, b, []channel.Channel{ch})
	o.queue = mq

	group := store.Group{JID: "group1@g.us", Folder: "test-group", IsMain: true}
	o.registeredGroups["group1@g.us"] = group

	mq.dequeueOnce = &queue.QueueMessage{GroupJID: "group1@g.us", Content: "task payload", IsTask: true}

	var callCount atomic.Int32
	recoveryEntered := make(chan struct{}, 1)
	s.getMessagesSinceHook = func() {
		if callCount.Add(1) < 2 {
			return // first call is deactivate()'s own pending check
		}
		select {
		case recoveryEntered <- struct{}{}:
		default:
		}
	}

	release, ok := o.claimSandboxSlot("group1@g.us")
	if !ok {
		t.Fatal("failed to pre-claim sandbox slot")
	}
	defer release()

	ipcCh := make(chan *ipc.IPCMessage)
	close(ipcCh)
	b.subscribeCh = ipcCh

	// Must not deadlock or panic despite Enqueue returning an error.
	o.watchGroupOutput(context.Background(), "group1@g.us", ipcCh, make(chan error))

	// processGroupMessages must NOT have been spawned.
	select {
	case <-recoveryEntered:
		t.Error("recovery goroutine was spawned despite pre-held in-flight slot")
	default:
	}
}

// TestDeactivate_EmptyQueueSkipsRecovery is a regression guard for the
// skipped == maxMalformedRetries path. When Dequeue returns (nil, nil) five
// times in a row the queue is empty — deactivate() must NOT spawn a recovery
// goroutine, because there is nothing to process. Only a Dequeue error or
// actual pending messages should trigger recovery.
func TestDeactivate_EmptyQueueSkipsRecovery(t *testing.T) {
	s := newMockStore()
	mq := &mockQueueRecording{mockQueue: newMockQueue()}
	mq.active["group1@g.us"] = true
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	b := &mockIPCBroker{}
	o := newTestOrchestratorWithRouter(s, mq.mockQueue, b, []channel.Channel{ch})
	o.queue = mq

	group := store.Group{JID: "group1@g.us", Folder: "test-group", IsMain: true}
	o.registeredGroups["group1@g.us"] = group
	// No pending MySQL messages and Dequeue always returns (nil, nil) — empty queue.

	var callCount atomic.Int32
	recoveryEntered := make(chan struct{}, 1)
	s.getMessagesSinceHook = func() {
		if callCount.Add(1) < 2 {
			return
		}
		select {
		case recoveryEntered <- struct{}{}:
		default:
		}
	}

	// Close IPC channel to trigger deactivate. Dequeue always returns (nil, nil)
	// by default, so all 5 retries will be exhausted — queue is empty, no recovery.
	ipcCh := make(chan *ipc.IPCMessage)
	close(ipcCh)
	b.subscribeCh = ipcCh
	o.watchGroupOutput(context.Background(), "group1@g.us", ipcCh, make(chan error))

	select {
	case <-recoveryEntered:
		t.Fatal("recovery goroutine was spawned despite empty queue (exhaustion must not trigger recovery)")
	case <-time.After(100 * time.Millisecond):
		// expected: no recovery goroutine spawned
	}
	if got := callCount.Load(); got != 1 {
		t.Errorf("GetMessagesSince called %d times, want 1 (only deactivate's own pending check)", got)
	}
}

// TestMaxConcurrent_ExcludesOwnJID is a regression guard for the jid != chatJID
// guard inside the inflightSandboxes.Range call. If that guard were removed,
// the group under test would count itself among "others" and the total would
// reach MaxConcurrent, suppressing CreateSandbox. With the guard in place,
// others == 1 (the foreign JID), 0+1 < 2 == MaxConcurrent, and CreateSandbox
// IS called.
func TestMaxConcurrent_ExcludesOwnJID(t *testing.T) {
	s := newMockStore()
	// activeCount = 0, MaxConcurrent = 2. One OTHER group is in-flight.
	// The group under test claims its own slot. With correct own-JID exclusion:
	//   others = 1, activeCount = 0 → total = 1 < 2 → CreateSandbox called.
	// Without own-JID exclusion:
	//   others = 2, activeCount = 0 → total = 2 >= 2 → CreateSandbox skipped.
	mq := &mockQueueWithActiveCount{
		mockQueue:   mockQueue{active: make(map[string]bool)},
		activeCount: 0,
	}
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	b := &mockIPCBroker{}
	sb := &mockSandboxControllerWithTracking{}

	const maxConcurrent = 2
	cfg := &config.Config{
		Channels:  config.ChannelsConfig{AssistantName: "TestBot"},
		Queue:     config.QueueConfig{IdleTimeout: 30 * time.Minute, MaxConcurrent: maxConcurrent},
		Scheduler: config.SchedulerConfig{PollInterval: 60 * time.Second},
	}
	reg := channel.NewRegistry()
	o, err := New(cfg, s, mq, b, nil, reg, slog.Default())
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

	// Pre-seed one OTHER group in inflightSandboxes.
	o.inflightSandboxes.Store("other1@g.us", struct{}{})

	// Claim the slot for the group under test (as pollMessages would do).
	release, ok := o.claimSandboxSlot("group1@g.us")
	if !ok {
		t.Fatal("failed to claim sandbox slot for test group")
	}
	defer release()

	_, err = o.processGroupMessages(context.Background(), "group1@g.us")
	if err != nil {
		t.Fatalf("processGroupMessages() error = %v, want nil", err)
	}
	if !sb.createCalled.Load() {
		t.Error("CreateSandbox was NOT called; own-JID exclusion guard may be broken")
	}
}

// TestDeactivateRecovery_DequeueErrorTriggersRecovery verifies that when Dequeue
// fails during the deactivate() check, the error is treated as a defensive trigger
// for recovery (pendingCheckFailed = true) so the re-enqueue / respawn path runs
// rather than silently dropping any work sitting in the NATS queue.
func TestDeactivateRecovery_DequeueErrorTriggersRecovery(t *testing.T) {
	s := newMockStore()
	mq := &mockQueueRecording{
		mockQueue:  newMockQueue(),
		dequeueErr: errors.New("NATS transient error"),
	}
	mq.active["group1@g.us"] = true
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	b := &mockIPCBroker{}
	o := newTestOrchestratorWithRouter(s, mq.mockQueue, b, []channel.Channel{ch})
	o.queue = mq

	group := store.Group{JID: "group1@g.us", Folder: "test-group", IsMain: true}
	o.registeredGroups["group1@g.us"] = group

	// Track whether the recovery goroutine was spawned (second GetMessagesSince call).
	var callCount atomic.Int32
	var slotHeldDuringRecovery atomic.Bool
	recoveryEntered := make(chan struct{}, 1)
	s.getMessagesSinceHook = func() {
		if callCount.Add(1) < 2 {
			return // first call is deactivate()'s own pending check
		}
		if _, held := o.inflightSandboxes.Load("group1@g.us"); held {
			slotHeldDuringRecovery.Store(true)
		}
		select {
		case recoveryEntered <- struct{}{}:
		default:
		}
	}

	ipcCh := make(chan *ipc.IPCMessage)
	close(ipcCh)
	b.subscribeCh = ipcCh
	o.watchGroupOutput(context.Background(), "group1@g.us", ipcCh, make(chan error))

	select {
	case <-recoveryEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("recovery goroutine was not spawned after Dequeue error")
	}
	if !slotHeldDuringRecovery.Load() {
		t.Error("slot was NOT held during recovery goroutine execution")
	}
	waitSlotReleased(t, o, "group1@g.us", 2*time.Second)
}

// TestDeactivateRecovery_PendingCheckFailedAndSlotHeld covers the combined path
// where Dequeue fails (triggering pendingCheckFailed = true) AND the in-flight
// slot is already held. Expects: no recovery goroutine is spawned (callCount == 1
// from deactivate()'s own pending-check call), and the recoveryEntered channel
// remains empty.
func TestDeactivateRecovery_PendingCheckFailedAndSlotHeld(t *testing.T) {
	s := newMockStore()
	mq := &mockQueueRecording{
		mockQueue:  newMockQueue(),
		dequeueErr: errors.New("NATS transient error"),
	}
	mq.active["group1@g.us"] = true
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	b := &mockIPCBroker{}
	o := newTestOrchestratorWithRouter(s, mq.mockQueue, b, []channel.Channel{ch})
	o.queue = mq

	group := store.Group{JID: "group1@g.us", Folder: "test-group", IsMain: true}
	o.registeredGroups["group1@g.us"] = group

	// Pre-claim the slot so the recovery path sees it as in-flight.
	preRelease, ok := o.claimSandboxSlot("group1@g.us")
	if !ok {
		t.Fatal("failed to pre-claim sandbox slot")
	}
	defer preRelease()

	// Track calls into GetMessagesSince to confirm no second (recovery) call.
	var callCount atomic.Int32
	recoveryEntered := make(chan struct{}, 1)
	s.getMessagesSinceHook = func() {
		if callCount.Add(1) >= 2 {
			select {
			case recoveryEntered <- struct{}{}:
			default:
			}
		}
	}

	ipcCh := make(chan *ipc.IPCMessage)
	close(ipcCh)
	b.subscribeCh = ipcCh
	o.watchGroupOutput(context.Background(), "group1@g.us", ipcCh, make(chan error))

	// Give a moment for any unexpected goroutine to surface.
	select {
	case <-recoveryEntered:
		t.Error("recovery goroutine was spawned despite slot being held")
	case <-time.After(100 * time.Millisecond):
		// expected: no recovery goroutine
	}

	if got := callCount.Load(); got != 1 {
		t.Errorf("GetMessagesSince called %d times, want 1 (only deactivate's own check)", got)
	}
}

// TestDeactivate_InactiveGroupSkipsCleanup verifies that deactivate() is a no-op
// when IsActive returns false — MarkInactive must not be called and no recovery
// goroutine must be spawned.
func TestDeactivate_InactiveGroupSkipsCleanup(t *testing.T) {
	s := newMockStore()
	q := newMockQueue()
	// q.active["group1@g.us"] deliberately NOT set — IsActive returns (false, nil).
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	b := &mockIPCBroker{}
	o := newTestOrchestratorWithRouter(s, q, b, []channel.Channel{ch})

	group := store.Group{JID: "group1@g.us", Folder: "test-group", IsMain: true}
	o.registeredGroups["group1@g.us"] = group

	var callCount atomic.Int32
	s.getMessagesSinceHook = func() { callCount.Add(1) }

	ipcCh := make(chan *ipc.IPCMessage)
	close(ipcCh)
	b.subscribeCh = ipcCh
	o.watchGroupOutput(context.Background(), "group1@g.us", ipcCh, make(chan error))

	if got := callCount.Load(); got != 0 {
		t.Errorf("GetMessagesSince called %d times, want 0 (inactive group skips all cleanup)", got)
	}
	if q.active["group1@g.us"] {
		t.Error("MarkInactive was called (active key set), want skipped")
	}
}

// TestDeactivate_MarkInactiveFailureSkipsRecovery verifies that when MarkInactive
// fails, deactivate() logs and returns early — the activeSandboxes entry is
// preserved so handleSandboxEvent can retry cleanup, and no recovery goroutine
// is spawned.
func TestDeactivate_MarkInactiveFailureSkipsRecovery(t *testing.T) {
	s := newMockStore()
	q := newMockQueue()
	q.active["group1@g.us"] = true
	q.markInactiveErr = errors.New("db error")
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	b := &mockIPCBroker{}
	o := newTestOrchestratorWithRouter(s, q, b, []channel.Channel{ch})

	group := store.Group{JID: "group1@g.us", Folder: "test-group", IsMain: true}
	o.registeredGroups["group1@g.us"] = group

	// Pre-seed activeSandboxes so we can verify it is NOT removed on MarkInactive failure.
	o.mu.Lock()
	o.activeSandboxes["group1@g.us"] = "test-sandbox-job"
	o.mu.Unlock()

	var callCount atomic.Int32
	s.getMessagesSinceHook = func() { callCount.Add(1) }

	ipcCh := make(chan *ipc.IPCMessage)
	close(ipcCh)
	b.subscribeCh = ipcCh
	o.watchGroupOutput(context.Background(), "group1@g.us", ipcCh, make(chan error))

	if got := callCount.Load(); got != 0 {
		t.Errorf("GetMessagesSince called %d times, want 0 (MarkInactive failure skips pending check)", got)
	}
	o.mu.Lock()
	_, stillHeld := o.activeSandboxes["group1@g.us"]
	o.mu.Unlock()
	if !stillHeld {
		t.Error("activeSandboxes entry was removed, want preserved so handleSandboxEvent can retry")
	}
}

// TestDeactivate_IsActiveErrorReturnsEarly verifies that deactivate() returns early
// when IsActive returns an error — MarkInactive must not be called (queue active map
// entry must survive) and no recovery goroutine must be spawned.
func TestDeactivate_IsActiveErrorReturnsEarly(t *testing.T) {
	s := newMockStore()
	q := newMockQueue()
	q.active["group1@g.us"] = true
	q.isActiveErr = errors.New("db error")
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	b := &mockIPCBroker{}
	o := newTestOrchestratorWithRouter(s, q, b, []channel.Channel{ch})

	group := store.Group{JID: "group1@g.us", Folder: "test-group", IsMain: true}
	o.registeredGroups["group1@g.us"] = group

	var callCount atomic.Int32
	s.getMessagesSinceHook = func() { callCount.Add(1) }

	ipcCh := make(chan *ipc.IPCMessage)
	close(ipcCh)
	b.subscribeCh = ipcCh
	o.watchGroupOutput(context.Background(), "group1@g.us", ipcCh, make(chan error))

	if got := callCount.Load(); got != 0 {
		t.Errorf("GetMessagesSince called %d times, want 0 (IsActive error skips all cleanup)", got)
	}
	if !q.active["group1@g.us"] {
		t.Error("MarkInactive was called (active key deleted), want skipped on IsActive error")
	}
}

// countingQueue wraps mockQueue and counts MarkInactive invocations.
type countingQueue struct {
	*mockQueue
	markInactiveCalls atomic.Int32
}

func (c *countingQueue) MarkInactive(ctx context.Context, groupJID string) error {
	c.markInactiveCalls.Add(1)
	return c.mockQueue.MarkInactive(ctx, groupJID)
}

// TestDeactivate_OnceGuardPreventsDoubleCleanup verifies that the sync.Once
// wrapper inside deactivate() prevents MarkInactive from being called more than
// once when two concurrent deactivate() triggers fire: the channel-close path
// (normal) and the panic-recovery path (inside the recovery goroutine).
func TestDeactivate_OnceGuardPreventsDoubleCleanup(t *testing.T) {
	s := newMockStore()
	inner := newMockQueue()
	inner.active["group1@g.us"] = true
	cq := &countingQueue{mockQueue: inner}

	b := &mockIPCBroker{}
	cfg := &config.Config{
		Channels:  config.ChannelsConfig{AssistantName: "TestBot"},
		Queue:     config.QueueConfig{IdleTimeout: 30 * time.Minute, RateLimitTokensPerSec: 1000, MaxMessageSizeBytes: 32768, MessageLimit: 500},
		Scheduler: config.SchedulerConfig{PollInterval: 60 * time.Second},
	}
	reg := channel.NewRegistry()
	log := slog.Default()
	o, err := New(cfg, s, cq, b, nil, reg, log)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	o.ipcReconnectDelays = []time.Duration{time.Millisecond, time.Millisecond}

	group := store.Group{JID: "group1@g.us", Folder: "test-group", IsMain: true}
	s.groups = append(s.groups, group)
	o.registeredGroups["group1@g.us"] = group

	// Seed a message so deactivate's pending check finds work and spawns the
	// recovery goroutine.
	s.messages["group1@g.us"] = []store.Message{
		{ChatJID: "group1@g.us", Content: "hello", Timestamp: time.Now(), Sender: "alice"},
	}

	// First call to GetMessagesSince (inside deactivate's pending check) succeeds.
	// Second call (inside processGroupMessages in the recovery goroutine) panics,
	// which triggers the defer-recover path that calls deactivate() a second time.
	var callSeq atomic.Int32
	s.getMessagesSinceHook = func() {
		if callSeq.Add(1) >= 2 {
			panic("injected once-guard panic")
		}
	}

	// Pre-closed channel triggers the channel-close deactivate path immediately.
	ipcCh := make(chan *ipc.IPCMessage)
	close(ipcCh)
	b.subscribeCh = ipcCh

	o.watchGroupOutput(context.Background(), "group1@g.us", ipcCh, make(chan error))

	// Wait for the recovery goroutine to panic, recover, and release its slot.
	waitSlotReleased(t, o, "group1@g.us", 2*time.Second)

	// Both the channel-close path and the panic-recovery path called deactivate(),
	// but sync.Once must ensure MarkInactive ran exactly once.
	if got := cq.markInactiveCalls.Load(); got != 1 {
		t.Errorf("MarkInactive called %d times, want 1 (sync.Once must prevent double cleanup)", got)
	}
}
