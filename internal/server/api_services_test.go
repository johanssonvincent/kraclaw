package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	agentsandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/johanssonvincent/kraclaw/internal/channel"
	"github.com/johanssonvincent/kraclaw/internal/ipc"
	"github.com/johanssonvincent/kraclaw/internal/provider"
	"github.com/johanssonvincent/kraclaw/internal/sandbox"
	"github.com/johanssonvincent/kraclaw/internal/store"
	kraclawv1 "github.com/johanssonvincent/kraclaw/pkg/pb/kraclawv1"
)

func TestCreateSandbox_NilController(t *testing.T) {
	svc := &sandboxService{log: testLogger()}
	_, err := svc.CreateSandbox(context.Background(), &kraclawv1.CreateSandboxRequest{
		GroupFolder: "test",
		GroupJid:    "test@jid",
	})
	if err == nil {
		t.Fatal("expected error for nil sandbox controller")
	}
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %v", status.Code(err))
	}
}

func createTestSandboxController() *sandbox.Controller {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = agentsandboxv1alpha1.AddToScheme(scheme)
	ctrlClient := ctrlfake.NewClientBuilder().WithScheme(scheme).Build()
	ctrl, _ := sandbox.New(fake.NewClientset(), ctrlClient, nil, "default", "agent:latest", nil, "redis://localhost:6379", "http://localhost:3001")
	return ctrl
}

func TestCreateSandbox_WithFakeK8s(t *testing.T) {
	ctrl := createTestSandboxController()
	svc := &sandboxService{
		sandbox: ctrl,
		log:     testLogger(),
	}

	resp, err := svc.CreateSandbox(context.Background(), &kraclawv1.CreateSandboxRequest{
		GroupFolder:   "mygroup",
		GroupJid:      "mygroup@jid",
		IsMain:        true,
		Timeout:       durationpb.New(5 * time.Minute),
		AssistantName: "test-bot",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Sandbox == nil {
		t.Fatal("sandbox is nil in response")
	}
	if resp.Sandbox.GroupFolder != "mygroup" {
		t.Fatalf("GroupFolder = %q, want %q", resp.Sandbox.GroupFolder, "mygroup")
	}
	if resp.Sandbox.State != "pending" {
		t.Fatalf("State = %q, want %q", resp.Sandbox.State, "pending")
	}
}

func TestCreateSandbox_MissingGroupFolder(t *testing.T) {
	ctrl := createTestSandboxController()
	svc := &sandboxService{
		sandbox: ctrl,
		log:     testLogger(),
	}

	_, err := svc.CreateSandbox(context.Background(), &kraclawv1.CreateSandboxRequest{
		GroupJid: "test@jid",
	})
	if err == nil {
		t.Fatal("expected error for missing group_folder")
	}
}

func TestCreateSandbox_WithPromptSendsIPC(t *testing.T) {
	ctrl := createTestSandboxController()
	broker := &mockIPCBroker{}
	svc := &sandboxService{
		sandbox: ctrl,
		ipc:     broker,
		log:     testLogger(),
	}

	_, err := svc.CreateSandbox(context.Background(), &kraclawv1.CreateSandboxRequest{
		GroupFolder: "mygroup",
		GroupJid:    "mygroup@jid",
		Prompt:      "hello agent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(broker.sentInputs) != 1 {
		t.Fatalf("expected 1 SendInput call, got %d", len(broker.sentInputs))
	}
	if broker.sentInputs[0].group != "mygroup" {
		t.Fatalf("SendInput group = %q, want %q", broker.sentInputs[0].group, "mygroup")
	}
	var text string
	if err := json.Unmarshal(broker.sentInputs[0].msg.Payload, &text); err != nil {
		t.Fatalf("unmarshal prompt payload: %v", err)
	}
	if text != "hello agent" {
		t.Fatalf("prompt payload = %q, want %q", text, "hello agent")
	}
}

func TestCreateSandbox_WithoutPromptSkipsIPC(t *testing.T) {
	ctrl := createTestSandboxController()
	broker := &mockIPCBroker{}
	svc := &sandboxService{
		sandbox: ctrl,
		ipc:     broker,
		log:     testLogger(),
	}

	_, err := svc.CreateSandbox(context.Background(), &kraclawv1.CreateSandboxRequest{
		GroupFolder: "mygroup",
		GroupJid:    "mygroup@jid",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(broker.sentInputs) != 0 {
		t.Fatalf("expected 0 SendInput calls when no prompt, got %d", len(broker.sentInputs))
	}
}

func TestPipeSandboxInput_NilBroker(t *testing.T) {
	svc := &sandboxService{log: testLogger()}
	_, err := svc.PipeSandboxInput(context.Background(), &kraclawv1.PipeSandboxInputRequest{
		GroupFolder: "test",
		Text:        "hello",
	})
	if err == nil {
		t.Fatal("expected error for nil IPC broker")
	}
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %v", status.Code(err))
	}
}

func TestPipeSandboxInput_EmptyText(t *testing.T) {
	broker := &mockIPCBroker{}
	svc := &sandboxService{ipc: broker, log: testLogger()}
	_, err := svc.PipeSandboxInput(context.Background(), &kraclawv1.PipeSandboxInputRequest{
		GroupFolder: "test",
		Text:        "",
	})
	if err == nil {
		t.Fatal("expected error for empty text")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestPipeSandboxInput_Valid(t *testing.T) {
	broker := &mockIPCBroker{}
	svc := &sandboxService{ipc: broker, log: testLogger()}
	_, err := svc.PipeSandboxInput(context.Background(), &kraclawv1.PipeSandboxInputRequest{
		GroupFolder: "mygroup",
		Text:        "hello world",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(broker.sentInputs) != 1 {
		t.Fatalf("expected 1 SendInput call, got %d", len(broker.sentInputs))
	}
	if broker.sentInputs[0].group != "mygroup" {
		t.Fatalf("group = %q, want %q", broker.sentInputs[0].group, "mygroup")
	}
	if broker.sentInputs[0].msg.Type != ipc.IPCMessageText {
		t.Fatalf("message type = %q, want %q", broker.sentInputs[0].msg.Type, ipc.IPCMessageText)
	}
	var text string
	if err := json.Unmarshal(broker.sentInputs[0].msg.Payload, &text); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if text != "hello world" {
		t.Fatalf("payload text = %q, want %q", text, "hello world")
	}
}

func TestPipeSandboxInput_SendError(t *testing.T) {
	broker := &mockIPCBroker{sendErr: fmt.Errorf("redis down")}
	svc := &sandboxService{ipc: broker, log: testLogger()}
	_, err := svc.PipeSandboxInput(context.Background(), &kraclawv1.PipeSandboxInputRequest{
		GroupFolder: "mygroup",
		Text:        "hello",
	})
	if err == nil {
		t.Fatal("expected error when SendInput fails")
	}
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal, got %v", status.Code(err))
	}
}

func TestStreamSandboxOutput_NilBroker(t *testing.T) {
	svc := &sandboxService{log: testLogger()}
	stream := &mockStreamServer{ctx: context.Background()}
	err := svc.StreamSandboxOutput(&kraclawv1.StreamOutputRequest{GroupFolder: "test"}, stream)
	if err == nil {
		t.Fatal("expected error for nil IPC broker")
	}
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %v", status.Code(err))
	}
}

func TestStreamSandboxOutput_TextMessages(t *testing.T) {
	ch := make(chan *ipc.IPCMessage, 2)
	payload, _ := json.Marshal("hello from agent")
	ch <- &ipc.IPCMessage{
		Group:   "test",
		Type:    ipc.IPCMessageText,
		Payload: payload,
	}
	close(ch)

	broker := &mockIPCBroker{outputCh: ch}
	svc := &sandboxService{ipc: broker, log: testLogger()}
	stream := &mockStreamServer{ctx: context.Background()}

	err := svc.StreamSandboxOutput(&kraclawv1.StreamOutputRequest{GroupFolder: "test"}, stream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stream.sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(stream.sent))
	}
	if stream.sent[0].Content != "hello from agent" {
		t.Fatalf("content = %q, want %q", stream.sent[0].Content, "hello from agent")
	}
	if stream.sent[0].Type != "message" {
		t.Fatalf("type = %q, want %q", stream.sent[0].Type, "message")
	}
}

func TestStreamSandboxOutput_MultipleMessages(t *testing.T) {
	ch := make(chan *ipc.IPCMessage, 3)
	for _, text := range []string{"first", "second", "third"} {
		payload, _ := json.Marshal(text)
		ch <- &ipc.IPCMessage{
			Group:   "test",
			Type:    ipc.IPCMessageText,
			Payload: payload,
		}
	}
	close(ch)

	broker := &mockIPCBroker{outputCh: ch}
	svc := &sandboxService{ipc: broker, log: testLogger()}
	stream := &mockStreamServer{ctx: context.Background()}

	err := svc.StreamSandboxOutput(&kraclawv1.StreamOutputRequest{GroupFolder: "test"}, stream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stream.sent) != 3 {
		t.Fatalf("expected 3 sent messages, got %d", len(stream.sent))
	}
	for i, want := range []string{"first", "second", "third"} {
		if stream.sent[i].Content != want {
			t.Fatalf("message[%d].Content = %q, want %q", i, stream.sent[i].Content, want)
		}
	}
}

func TestStreamSandboxOutput_SessionUpdate(t *testing.T) {
	ch := make(chan *ipc.IPCMessage, 1)
	payload, _ := json.Marshal("new-session-123")
	ch <- &ipc.IPCMessage{
		Group:   "test",
		Type:    ipc.IPCSessionUpdate,
		Payload: payload,
	}
	close(ch)

	broker := &mockIPCBroker{outputCh: ch}
	svc := &sandboxService{ipc: broker, log: testLogger()}
	stream := &mockStreamServer{ctx: context.Background()}

	err := svc.StreamSandboxOutput(&kraclawv1.StreamOutputRequest{GroupFolder: "test"}, stream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stream.sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(stream.sent))
	}
	if stream.sent[0].NewSessionId != "new-session-123" {
		t.Fatalf("NewSessionId = %q, want %q", stream.sent[0].NewSessionId, "new-session-123")
	}
}

func TestStreamSandboxOutput_ContextCancelled(t *testing.T) {
	ch := make(chan *ipc.IPCMessage) // never sends
	broker := &mockIPCBroker{outputCh: ch}
	svc := &sandboxService{ipc: broker, log: testLogger()}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	stream := &mockStreamServer{ctx: ctx}

	err := svc.StreamSandboxOutput(&kraclawv1.StreamOutputRequest{GroupFolder: "test"}, stream)
	if err != nil {
		t.Fatalf("expected nil on context cancel, got %v", err)
	}
	if len(stream.sent) != 0 {
		t.Fatalf("expected no messages sent on cancelled context, got %d", len(stream.sent))
	}
}

func TestStreamSandboxOutput_SendError(t *testing.T) {
	ch := make(chan *ipc.IPCMessage, 1)
	payload, _ := json.Marshal("hello")
	ch <- &ipc.IPCMessage{
		Group:   "test",
		Type:    ipc.IPCMessageText,
		Payload: payload,
	}
	close(ch)

	broker := &mockIPCBroker{outputCh: ch}
	svc := &sandboxService{ipc: broker, log: testLogger()}
	stream := &mockStreamServer{
		ctx:     context.Background(),
		sendErr: fmt.Errorf("broken pipe"),
	}

	err := svc.StreamSandboxOutput(&kraclawv1.StreamOutputRequest{GroupFolder: "test"}, stream)
	if err == nil {
		t.Fatal("expected error when stream.Send fails")
	}
}

// --- helpers ---

type sentInput struct {
	group string
	msg   *ipc.IPCMessage
}

type mockIPCBroker struct {
	sentInputs []sentInput
	sendErr    error
	outputCh   chan *ipc.IPCMessage
}

func (m *mockIPCBroker) PublishOutput(ctx context.Context, group, agentID string, msg *ipc.IPCMessage) error {
	return nil
}

func (m *mockIPCBroker) SubscribeOutput(ctx context.Context, group string) (<-chan *ipc.IPCMessage, error) {
	if m.outputCh != nil {
		return m.outputCh, nil
	}
	ch := make(chan *ipc.IPCMessage)
	close(ch)
	return ch, nil
}

func (m *mockIPCBroker) SendInput(ctx context.Context, group, agentID string, msg *ipc.IPCMessage) error {
	if m.sendErr != nil {
		return m.sendErr
	}
	m.sentInputs = append(m.sentInputs, sentInput{group: group, msg: msg})
	return nil
}

func (m *mockIPCBroker) ReadInput(ctx context.Context, group, agentID string) (<-chan *ipc.IPCMessage, error) {
	ch := make(chan *ipc.IPCMessage)
	close(ch)
	return ch, nil
}

func (m *mockIPCBroker) DeleteStreams(ctx context.Context, group string) error { return nil }
func (m *mockIPCBroker) Close() error                                          { return nil }

type mockStreamServer struct {
	ctx     context.Context
	sent    []*kraclawv1.SandboxOutput
	sendErr error
}

func (m *mockStreamServer) Send(out *kraclawv1.SandboxOutput) error {
	if m.sendErr != nil {
		return m.sendErr
	}
	m.sent = append(m.sent, out)
	return nil
}

func (m *mockStreamServer) Context() context.Context     { return m.ctx }
func (m *mockStreamServer) SetHeader(metadata.MD) error  { return nil }
func (m *mockStreamServer) SendHeader(metadata.MD) error { return nil }
func (m *mockStreamServer) SetTrailer(metadata.MD)       {}
func (m *mockStreamServer) SendMsg(interface{}) error    { return nil }
func (m *mockStreamServer) RecvMsg(interface{}) error    { return nil }

func testLogger() *slog.Logger {
	return slog.Default()
}

// --- mockGroupStore implements store.Store with no-op defaults ---

type mockGroupStore struct {
	upsertErr     error
	upsertedJID   string
	createTaskErr error
	createdTask   *store.ScheduledTask
	getTaskResult *store.ScheduledTask
	getTaskErr    error
	updateTaskErr error
	updatedTask   *store.ScheduledTask
}

func (m *mockGroupStore) UpsertGroup(_ context.Context, g *store.Group) error {
	if m.upsertErr != nil {
		return m.upsertErr
	}
	m.upsertedJID = g.JID
	return nil
}

func (m *mockGroupStore) GetGroup(context.Context, string) (*store.Group, error) { return nil, nil }
func (m *mockGroupStore) GetGroupByFolder(context.Context, string) (*store.Group, error) {
	return nil, nil
}
func (m *mockGroupStore) ListGroups(context.Context) ([]store.Group, error)  { return nil, nil }
func (m *mockGroupStore) DeleteGroup(context.Context, string) error          { return nil }
func (m *mockGroupStore) StoreMessage(context.Context, *store.Message) error { return nil }
func (m *mockGroupStore) StoreBatch(context.Context, []store.Message) error  { return nil }
func (m *mockGroupStore) GetNewMessages(context.Context, []string, time.Time, int) ([]store.Message, error) {
	return nil, nil
}
func (m *mockGroupStore) GetMessagesSince(context.Context, string, time.Time, int) ([]store.Message, error) {
	return nil, nil
}
func (m *mockGroupStore) UpsertChat(context.Context, *store.Chat) error          { return nil }
func (m *mockGroupStore) GetChat(context.Context, string) (*store.Chat, error)   { return nil, nil }
func (m *mockGroupStore) ListChats(context.Context) ([]store.Chat, error)        { return nil, nil }
func (m *mockGroupStore) CreateTask(_ context.Context, task *store.ScheduledTask) error {
	if m.createTaskErr != nil {
		return m.createTaskErr
	}
	m.createdTask = task
	return nil
}
func (m *mockGroupStore) GetTask(_ context.Context, id, groupFolder string) (*store.ScheduledTask, error) {
	if m.getTaskErr != nil {
		return nil, m.getTaskErr
	}
	if m.getTaskResult != nil && m.getTaskResult.ID == id {
		return m.getTaskResult, nil
	}
	return nil, fmt.Errorf("task %q not found", id)
}
func (m *mockGroupStore) ListTasks(context.Context) ([]store.ScheduledTask, error) { return nil, nil }
func (m *mockGroupStore) ListTasksByGroup(context.Context, string) ([]store.ScheduledTask, error) {
	return nil, nil
}
func (m *mockGroupStore) UpdateTask(_ context.Context, task *store.ScheduledTask) error {
	if m.updateTaskErr != nil {
		return m.updateTaskErr
	}
	m.updatedTask = task
	return nil
}
func (m *mockGroupStore) DeleteTask(context.Context, string, string) error { return nil }
func (m *mockGroupStore) GetDueTasks(context.Context) ([]store.ScheduledTask, error) {
	return nil, nil
}
func (m *mockGroupStore) LogTaskRun(context.Context, *store.TaskRunLog) error { return nil }
func (m *mockGroupStore) GetTaskRunLogs(context.Context, string, string, int) ([]store.TaskRunLog, error) {
	return nil, nil
}
func (m *mockGroupStore) GetSession(context.Context, string) (*store.Session, error) {
	return nil, nil
}
func (m *mockGroupStore) UpsertSession(context.Context, *store.Session) error { return nil }
func (m *mockGroupStore) DeleteSession(context.Context, string) error         { return nil }
func (m *mockGroupStore) GetState(context.Context, string) (string, error)    { return "", nil }
func (m *mockGroupStore) SetState(context.Context, string, string) error      { return nil }
func (m *mockGroupStore) GetAllowlist(context.Context, string) ([]store.SenderAllowlistEntry, error) {
	return nil, nil
}
func (m *mockGroupStore) UpsertAllowlistEntry(context.Context, *store.SenderAllowlistEntry) error {
	return nil
}
func (m *mockGroupStore) DeleteAllowlistEntry(context.Context, int64) error { return nil }
func (m *mockGroupStore) Close() error                                      { return nil }

// --- RegisterGroup tests ---

func TestRegisterGroup_NilStore(t *testing.T) {
	svc := &groupService{providers: provider.NewRegistry(), log: testLogger()}
	_, err := svc.RegisterGroup(context.Background(), &kraclawv1.RegisterGroupRequest{
		Jid:    "test@jid",
		Folder: "test",
	})
	if err == nil {
		t.Fatal("expected error for nil store")
	}
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %v", status.Code(err))
	}
}

func TestRegisterGroup_MissingJid(t *testing.T) {
	svc := &groupService{store: &mockGroupStore{}, providers: provider.NewRegistry(), log: testLogger()}
	_, err := svc.RegisterGroup(context.Background(), &kraclawv1.RegisterGroupRequest{
		Jid:    "",
		Folder: "test",
	})
	if err == nil {
		t.Fatal("expected error for missing jid")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestRegisterGroup_MissingFolder(t *testing.T) {
	svc := &groupService{store: &mockGroupStore{}, providers: provider.NewRegistry(), log: testLogger()}
	_, err := svc.RegisterGroup(context.Background(), &kraclawv1.RegisterGroupRequest{
		Jid:    "test@jid",
		Folder: "",
	})
	if err == nil {
		t.Fatal("expected error for missing folder")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestRegisterGroup_Valid(t *testing.T) {
	ms := &mockGroupStore{}
	svc := &groupService{store: ms, providers: provider.NewRegistry(), log: testLogger()}
	resp, err := svc.RegisterGroup(context.Background(), &kraclawv1.RegisterGroupRequest{
		Jid:             "mygroup@jid",
		Name:            "My Group",
		Folder:          "mygroup",
		TriggerPattern:  "!bot",
		IsMain:          true,
		RequiresTrigger: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ms.upsertedJID != "mygroup@jid" {
		t.Fatalf("upsertedJID = %q, want %q", ms.upsertedJID, "mygroup@jid")
	}
	if resp.Jid != "mygroup@jid" {
		t.Fatalf("Jid = %q, want %q", resp.Jid, "mygroup@jid")
	}
	if resp.Name != "My Group" {
		t.Fatalf("Name = %q, want %q", resp.Name, "My Group")
	}
	if resp.Folder != "mygroup" {
		t.Fatalf("Folder = %q, want %q", resp.Folder, "mygroup")
	}
	if resp.TriggerPattern != "!bot" {
		t.Fatalf("TriggerPattern = %q, want %q", resp.TriggerPattern, "!bot")
	}
	if !resp.IsMain {
		t.Fatal("IsMain = false, want true")
	}
	if !resp.RequiresTrigger {
		t.Fatal("RequiresTrigger = false, want true")
	}
}

func TestRegisterGroup_StoreError(t *testing.T) {
	ms := &mockGroupStore{upsertErr: fmt.Errorf("db connection lost")}
	svc := &groupService{store: ms, providers: provider.NewRegistry(), log: testLogger()}
	_, err := svc.RegisterGroup(context.Background(), &kraclawv1.RegisterGroupRequest{
		Jid:    "test@jid",
		Folder: "test",
	})
	if err == nil {
		t.Fatal("expected error when UpsertGroup fails")
	}
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal, got %v", status.Code(err))
	}
}

func TestRegisterGroup_InvalidContainerConfigJSON(t *testing.T) {
	svc := &groupService{store: &mockGroupStore{}, providers: provider.NewRegistry(), log: testLogger()}
	_, err := svc.RegisterGroup(context.Background(), &kraclawv1.RegisterGroupRequest{
		Jid:                 "tui:test",
		Folder:              "test",
		ContainerConfigJson: "not valid json",
	})
	if err == nil {
		t.Fatal("expected error for invalid container config JSON")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestRegisterGroup_ValidContainerConfigJSON(t *testing.T) {
	ms := &mockGroupStore{}
	svc := &groupService{store: ms, providers: provider.NewRegistry(), log: testLogger()}
	_, err := svc.RegisterGroup(context.Background(), &kraclawv1.RegisterGroupRequest{
		Jid:                 "tui:test",
		Folder:              "test",
		ContainerConfigJson: `{"timeout":5000}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ms.upsertedJID != "tui:test" {
		t.Fatalf("upsertedJID = %q, want %q", ms.upsertedJID, "tui:test")
	}
}

func TestRegisterGroup_RequiresTriggerNoPattern(t *testing.T) {
	svc := &groupService{store: &mockGroupStore{}, providers: provider.NewRegistry(), log: testLogger()}
	_, err := svc.RegisterGroup(context.Background(), &kraclawv1.RegisterGroupRequest{
		Jid:             "test@jid",
		Folder:          "test",
		RequiresTrigger: true,
		TriggerPattern:  "",
	})
	if err == nil {
		t.Fatal("expected error for requires_trigger with empty pattern")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestRegisterGroup_RequiresTriggerWithPattern(t *testing.T) {
	ms := &mockGroupStore{}
	svc := &groupService{store: ms, providers: provider.NewRegistry(), log: testLogger()}
	resp, err := svc.RegisterGroup(context.Background(), &kraclawv1.RegisterGroupRequest{
		Jid:             "test@jid",
		Folder:          "test",
		RequiresTrigger: true,
		TriggerPattern:  "!bot",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.TriggerPattern != "!bot" {
		t.Fatalf("TriggerPattern = %q, want %q", resp.TriggerPattern, "!bot")
	}
}

// --- CreateTask tests ---

func TestCreateTask_NilStore(t *testing.T) {
	svc := &taskService{log: testLogger()}
	_, err := svc.CreateTask(context.Background(), &kraclawv1.CreateTaskRequest{
		GroupFolder:   "test",
		ScheduleType:  "cron",
		ScheduleValue: "* * * * *",
	})
	if err == nil {
		t.Fatal("expected error for nil store")
	}
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %v", status.Code(err))
	}
}

func TestCreateTask_InvalidSchedule(t *testing.T) {
	ms := &mockGroupStore{}
	svc := &taskService{store: ms, log: testLogger()}
	_, err := svc.CreateTask(context.Background(), &kraclawv1.CreateTaskRequest{
		GroupFolder:   "test",
		ChatJid:       "chat@jid",
		Prompt:        "do something",
		ScheduleType:  "cron",
		ScheduleValue: "not-a-cron",
	})
	if err == nil {
		t.Fatal("expected error for invalid cron schedule")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestCreateTask_Valid(t *testing.T) {
	ms := &mockGroupStore{}
	svc := &taskService{store: ms, log: testLogger()}
	resp, err := svc.CreateTask(context.Background(), &kraclawv1.CreateTaskRequest{
		GroupFolder:   "test",
		ChatJid:       "chat@jid",
		Prompt:        "do something",
		ScheduleType:  "cron",
		ScheduleValue: "* * * * *",
		ContextMode:   "group",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Id == "" {
		t.Fatal("expected non-empty task ID")
	}
	if resp.GroupFolder != "test" {
		t.Fatalf("GroupFolder = %q, want %q", resp.GroupFolder, "test")
	}
	if resp.Status != "active" {
		t.Fatalf("Status = %q, want %q", resp.Status, "active")
	}
}

func TestCreateTask_MissingGroupFolder(t *testing.T) {
	ms := &mockGroupStore{}
	svc := &taskService{store: ms, log: testLogger()}
	_, err := svc.CreateTask(context.Background(), &kraclawv1.CreateTaskRequest{
		GroupFolder:   "",
		ScheduleType:  "cron",
		ScheduleValue: "* * * * *",
	})
	if err == nil {
		t.Fatal("expected error for missing group_folder")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

// --- UpdateTask tests ---

func TestUpdateTask_MissingGroupFolder(t *testing.T) {
	ms := &mockGroupStore{}
	svc := &taskService{store: ms, log: testLogger()}
	_, err := svc.UpdateTask(context.Background(), &kraclawv1.UpdateTaskRequest{
		Id:          "task-1",
		GroupFolder: "",
	})
	if err == nil {
		t.Fatal("expected error for missing group_folder")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestUpdateTask_InvalidSchedule(t *testing.T) {
	existingTask := &store.ScheduledTask{
		ID:            "task-1",
		GroupFolder:   "test",
		ChatJID:       "chat@jid",
		Prompt:        "original",
		ScheduleType:  store.ScheduleCron,
		ScheduleValue: "* * * * *",
		ContextMode:   store.ContextGroup,
		Status:        store.TaskActive,
	}
	ms := &mockGroupStore{
		getTaskResult: existingTask,
	}
	svc := &taskService{store: ms, log: testLogger()}
	_, err := svc.UpdateTask(context.Background(), &kraclawv1.UpdateTaskRequest{
		Id:            "task-1",
		GroupFolder:   "test",
		ScheduleType:  "cron",
		ScheduleValue: "bad-cron",
	})
	if err == nil {
		t.Fatal("expected error for invalid schedule")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestUpdateTask_Valid(t *testing.T) {
	existingTask := &store.ScheduledTask{
		ID:            "task-1",
		GroupFolder:   "test",
		ChatJID:       "chat@jid",
		Prompt:        "original",
		ScheduleType:  store.ScheduleCron,
		ScheduleValue: "* * * * *",
		ContextMode:   store.ContextGroup,
		Status:        store.TaskActive,
	}
	ms := &mockGroupStore{
		getTaskResult: existingTask,
	}
	svc := &taskService{store: ms, log: testLogger()}
	resp, err := svc.UpdateTask(context.Background(), &kraclawv1.UpdateTaskRequest{
		Id:          "task-1",
		GroupFolder: "test",
		Prompt:      "updated prompt",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Prompt != "updated prompt" {
		t.Fatalf("Prompt = %q, want %q", resp.Prompt, "updated prompt")
	}
}

// --- GetStatus tests ---

func TestGetStatus_ConnectedChannels(t *testing.T) {
	svc := &adminService{
		version:   "test",
		startedAt: time.Now(),
		channels: []channel.Channel{
			&mockChannel{name: "discord", connected: true},
			&mockChannel{name: "telegram", connected: true},
			&mockChannel{name: "tui", connected: false},
		},
		log: testLogger(),
	}
	resp, err := svc.GetStatus(context.Background(), &kraclawv1.GetStatusRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ConnectedChannels != 2 {
		t.Fatalf("ConnectedChannels = %d, want 2", resp.ConnectedChannels)
	}
}

func TestGetStatus_NoChannels(t *testing.T) {
	svc := &adminService{
		version:   "test",
		startedAt: time.Now(),
		log:       testLogger(),
	}
	resp, err := svc.GetStatus(context.Background(), &kraclawv1.GetStatusRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ConnectedChannels != 0 {
		t.Fatalf("ConnectedChannels = %d, want 0", resp.ConnectedChannels)
	}
}
