package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/client-go/kubernetes"

	"github.com/johanssonvincent/kraclaw/internal/channel"
	"github.com/johanssonvincent/kraclaw/internal/ipc"
	"github.com/johanssonvincent/kraclaw/internal/sandbox"
	"github.com/johanssonvincent/kraclaw/internal/store"
	kraclawv1 "github.com/johanssonvincent/kraclaw/pkg/pb/kraclawv1"
)

type adminService struct {
	kraclawv1.UnimplementedAdminServiceServer

	version   string
	startedAt time.Time
	db        *sql.DB
	rdb       *redis.Client
	k8s       kubernetes.Interface
	store     store.Store
	sandbox   *sandbox.Controller
	events    *eventHub
	channels  []channel.Channel
	log       *slog.Logger
}

type groupService struct {
	kraclawv1.UnimplementedGroupServiceServer

	store store.Store
	log   *slog.Logger
}

type taskService struct {
	kraclawv1.UnimplementedTaskServiceServer

	store store.Store
	log   *slog.Logger
}

type sandboxService struct {
	kraclawv1.UnimplementedSandboxServiceServer

	store   store.Store
	sandbox *sandbox.Controller
	ipc     ipc.IPCBroker
	log     *slog.Logger
}

func registerAPIServices(grpcServer *grpc.Server, cfg Config, events *eventHub) {
	admin := &adminService{
		version:   cfg.Version,
		startedAt: cfg.StartedAt,
		db:        cfg.DB,
		rdb:       cfg.Redis,
		k8s:       cfg.Kubernetes,
		store:     cfg.Store,
		sandbox:   cfg.Sandbox,
		events:    events,
		channels:  cfg.Channels,
		log:       cfg.Log.With("component", "grpc-admin"),
	}
	groups := &groupService{
		store: cfg.Store,
		log:   cfg.Log.With("component", "grpc-groups"),
	}
	tasks := &taskService{
		store: cfg.Store,
		log:   cfg.Log.With("component", "grpc-tasks"),
	}
	sandboxes := &sandboxService{
		store:   cfg.Store,
		sandbox: cfg.Sandbox,
		ipc:     cfg.IPC,
		log:     cfg.Log.With("component", "grpc-sandboxes"),
	}

	channels := &channelService{
		tui:      cfg.TUIChannel,
		channels: cfg.Channels,
		store:    cfg.Store,
		log:      cfg.Log.With("component", "grpc-channels"),
	}

	kraclawv1.RegisterAdminServiceServer(grpcServer, admin)
	kraclawv1.RegisterGroupServiceServer(grpcServer, groups)
	kraclawv1.RegisterTaskServiceServer(grpcServer, tasks)
	kraclawv1.RegisterSandboxServiceServer(grpcServer, sandboxes)
	kraclawv1.RegisterChannelServiceServer(grpcServer, channels)
}

func (s *adminService) GetStatus(ctx context.Context, _ *kraclawv1.GetStatusRequest) (*kraclawv1.ServerStatus, error) {
	mysqlConnected := s.pingMySQL(ctx)
	redisConnected := s.pingRedis(ctx)
	k8sConnected := s.pingKubernetes(ctx)

	activeSandboxes := int32(0)
	if s.sandbox != nil {
		if sandboxes, err := s.sandbox.ListSandboxes(ctx); err == nil {
			for _, sb := range sandboxes {
				if sb.State == sandbox.StatePending || sb.State == sandbox.StateRunning {
					activeSandboxes++
				}
			}
		} else {
			s.log.Warn("failed to list sandboxes for status", "error", err)
		}
	}

	activeTasks := int32(0)
	if s.store != nil {
		if tasks, err := s.store.ListTasks(ctx); err == nil {
			for _, task := range tasks {
				if task.Status == store.TaskActive {
					activeTasks++
				}
			}
		} else {
			s.log.Warn("failed to list tasks for status", "error", err)
		}
	}

	connectedChannels := int32(0)
	for _, ch := range s.channels {
		if ch.IsConnected() {
			connectedChannels++
		}
	}

	return &kraclawv1.ServerStatus{
		Version:           s.version,
		ActiveSandboxes:   activeSandboxes,
		ConnectedChannels: connectedChannels,
		PendingMessages:   int32(math.Round(metricValue("kraclaw_queue_depth"))),
		ActiveTasks:       activeTasks,
		UptimeSince:       timestamppb.New(s.startedAt),
		MysqlConnected:    mysqlConnected,
		RedisConnected:    redisConnected,
		K8SConnected:      k8sConnected,
	}, nil
}

func (s *adminService) StreamEvents(req *kraclawv1.StreamEventsRequest, stream kraclawv1.AdminService_StreamEventsServer) error {
	ch, unsubscribe := s.events.subscribe()
	defer unsubscribe()

	filters := make(map[string]struct{}, len(req.EventTypes))
	for _, eventType := range req.EventTypes {
		filters[eventType] = struct{}{}
	}

	for {
		select {
		case <-stream.Context().Done():
			return nil
		case evt, ok := <-ch:
			if !ok {
				return nil
			}
			if len(filters) > 0 {
				if _, ok := filters[evt.Type]; !ok {
					continue
				}
			}
			if err := stream.Send(evt); err != nil {
				return err
			}
		}
	}
}

func (s *adminService) GetMetrics(context.Context, *kraclawv1.GetMetricsRequest) (*kraclawv1.Metrics, error) {
	return &kraclawv1.Metrics{
		TotalMessagesReceived: int64(math.Round(metricValue("kraclaw_messages_received_total"))),
		TotalMessagesSent:     int64(math.Round(metricValue("kraclaw_messages_sent_total"))),
		TotalSandboxesCreated: int64(math.Round(metricValue("kraclaw_sandboxes_created_total"))),
		TotalTasksExecuted:    int64(math.Round(metricValue("kraclaw_tasks_executed_total"))),
		ProxyRequests:         int64(math.Round(metricValue("kraclaw_proxy_requests_total"))),
	}, nil
}

func (s *groupService) ListGroups(ctx context.Context, _ *kraclawv1.ListGroupsRequest) (*kraclawv1.ListGroupsResponse, error) {
	if s.store == nil {
		return nil, status.Error(codes.Unavailable, "group store not configured")
	}

	groups, err := s.store.ListGroups(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list groups: %v", err)
	}

	resp := &kraclawv1.ListGroupsResponse{
		Groups: make([]*kraclawv1.Group, 0, len(groups)),
	}
	for _, group := range groups {
		resp.Groups = append(resp.Groups, &kraclawv1.Group{
			Jid:             group.JID,
			Name:            group.Name,
			Folder:          group.Folder,
			TriggerPattern:  group.TriggerPattern,
			IsMain:          group.IsMain,
			RequiresTrigger: group.RequiresTrigger,
			AddedAt:         toProtoTimestamp(group.AddedAt),
		})
	}

	return resp, nil
}

func (s *groupService) RegisterGroup(ctx context.Context, req *kraclawv1.RegisterGroupRequest) (*kraclawv1.Group, error) {
	if s.store == nil {
		return nil, status.Error(codes.Unavailable, "group store not configured")
	}

	if req.Jid == "" {
		return nil, status.Error(codes.InvalidArgument, "jid is required")
	}
	if req.Folder == "" {
		return nil, status.Error(codes.InvalidArgument, "folder is required")
	}

	group := store.Group{
		JID:             req.Jid,
		Name:            req.Name,
		Folder:          req.Folder,
		TriggerPattern:  req.TriggerPattern,
		IsMain:          req.IsMain,
		RequiresTrigger: req.RequiresTrigger,
	}

	if err := group.Validate(); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid group: %v", err)
	}

	if req.ContainerConfigJson != "" {
		cc, err := store.ParseContainerConfig([]byte(req.ContainerConfigJson))
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid container config: %v", err)
		}
		group.ContainerConfig = cc
	}

	if err := s.store.UpsertGroup(ctx, &group); err != nil {
		s.log.Error("failed to upsert group", "jid", req.Jid, "error", err)
		return nil, status.Error(codes.Internal, "failed to register group")
	}

	s.log.Info("group registered", "jid", req.Jid, "name", req.Name, "folder", req.Folder)

	return &kraclawv1.Group{
		Jid:             group.JID,
		Name:            group.Name,
		Folder:          group.Folder,
		TriggerPattern:  group.TriggerPattern,
		IsMain:          group.IsMain,
		RequiresTrigger: group.RequiresTrigger,
		AddedAt:         toProtoTimestamp(time.Now()),
	}, nil
}

func (s *taskService) ListTasks(ctx context.Context, req *kraclawv1.ListTasksRequest) (*kraclawv1.ListTasksResponse, error) {
	if s.store == nil {
		return nil, status.Error(codes.Unavailable, "task store not configured")
	}

	var (
		tasks []store.ScheduledTask
		err   error
	)
	if req.GroupFolder != "" {
		tasks, err = s.store.ListTasksByGroup(ctx, req.GroupFolder)
	} else {
		tasks, err = s.store.ListTasks(ctx)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list tasks: %v", err)
	}

	resp := &kraclawv1.ListTasksResponse{
		Tasks: make([]*kraclawv1.Task, 0, len(tasks)),
	}
	for _, task := range tasks {
		resp.Tasks = append(resp.Tasks, &kraclawv1.Task{
			Id:            task.ID,
			GroupFolder:   task.GroupFolder,
			ChatJid:       task.ChatJID,
			Prompt:        task.Prompt,
			ScheduleType:  string(task.ScheduleType),
			ScheduleValue: task.ScheduleValue,
			ContextMode:   string(task.ContextMode),
			NextRun:       toProtoTimestampPtr(task.NextRun),
			LastRun:       toProtoTimestampPtr(task.LastRun),
			LastResult:    stringPtrValue(task.LastResult),
			Status:        string(task.Status),
			CreatedAt:     toProtoTimestamp(task.CreatedAt),
		})
	}

	return resp, nil
}

func (s *taskService) CreateTask(ctx context.Context, req *kraclawv1.CreateTaskRequest) (*kraclawv1.Task, error) {
	if s.store == nil {
		return nil, status.Error(codes.Unavailable, "task store not configured")
	}
	if req.GroupFolder == "" {
		return nil, status.Error(codes.InvalidArgument, "group_folder is required")
	}

	task := store.ScheduledTask{
		ID:            uuid.New().String(),
		GroupFolder:   req.GroupFolder,
		ChatJID:       req.ChatJid,
		Prompt:        req.Prompt,
		ScheduleType:  store.ScheduleType(req.ScheduleType),
		ScheduleValue: req.ScheduleValue,
		ContextMode:   store.ContextMode(req.ContextMode),
		Status:        store.TaskActive,
		CreatedAt:     time.Now().UTC(),
	}
	if err := task.Validate(); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid task: %v", err)
	}
	if err := s.store.CreateTask(ctx, &task); err != nil {
		s.log.Error("failed to create task", "group_folder", req.GroupFolder, "error", err)
		return nil, status.Errorf(codes.Internal, "create task: %v", err)
	}

	return taskToProto(task), nil
}

func (s *taskService) UpdateTask(ctx context.Context, req *kraclawv1.UpdateTaskRequest) (*kraclawv1.Task, error) {
	if s.store == nil {
		return nil, status.Error(codes.Unavailable, "task store not configured")
	}
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "task id is required")
	}
	if req.GroupFolder == "" {
		return nil, status.Error(codes.InvalidArgument, "group_folder is required")
	}

	existing, err := s.store.GetTask(ctx, req.Id, req.GroupFolder)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "task not found: %v", err)
	}

	// Verify group ownership
	if existing.GroupFolder != req.GroupFolder {
		return nil, status.Error(codes.NotFound, "task not found in group")
	}

	// Apply updates — only overwrite non-empty fields
	if req.Prompt != "" {
		existing.Prompt = req.Prompt
	}
	if req.ScheduleType != "" {
		existing.ScheduleType = store.ScheduleType(req.ScheduleType)
	}
	if req.ScheduleValue != "" {
		existing.ScheduleValue = req.ScheduleValue
	}
	if req.ContextMode != "" {
		existing.ContextMode = store.ContextMode(req.ContextMode)
	}

	if err := existing.Validate(); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid task: %v", err)
	}
	if err := s.store.UpdateTask(ctx, existing); err != nil {
		s.log.Error("failed to update task", "id", req.Id, "error", err)
		return nil, status.Errorf(codes.Internal, "update task: %v", err)
	}

	return taskToProto(*existing), nil
}

func taskToProto(t store.ScheduledTask) *kraclawv1.Task {
	return &kraclawv1.Task{
		Id:            t.ID,
		GroupFolder:   t.GroupFolder,
		ChatJid:       t.ChatJID,
		Prompt:        t.Prompt,
		ScheduleType:  string(t.ScheduleType),
		ScheduleValue: t.ScheduleValue,
		ContextMode:   string(t.ContextMode),
		NextRun:       toProtoTimestampPtr(t.NextRun),
		LastRun:       toProtoTimestampPtr(t.LastRun),
		LastResult:    stringPtrValue(t.LastResult),
		Status:        string(t.Status),
		CreatedAt:     toProtoTimestamp(t.CreatedAt),
	}
}

func (s *sandboxService) ListSandboxes(ctx context.Context, _ *kraclawv1.ListSandboxesRequest) (*kraclawv1.ListSandboxesResponse, error) {
	if s.sandbox == nil {
		return &kraclawv1.ListSandboxesResponse{}, nil
	}

	sandboxes, err := s.sandbox.ListSandboxes(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list sandboxes: %v", err)
	}

	resp := &kraclawv1.ListSandboxesResponse{
		Sandboxes: make([]*kraclawv1.Sandbox, 0, len(sandboxes)),
	}
	for _, sb := range sandboxes {
		groupJID := ""
		sessionID := ""
		if s.store != nil {
			if group, err := s.store.GetGroupByFolder(ctx, sb.Group); err == nil && group != nil {
				groupJID = group.JID
			}
			if session, err := s.store.GetSession(ctx, sb.Group); err == nil && session != nil {
				sessionID = session.SessionID
			}
		}

		resp.Sandboxes = append(resp.Sandboxes, &kraclawv1.Sandbox{
			Name:        sb.Name,
			GroupFolder: sb.Group,
			GroupJid:    groupJID,
			State:       string(sb.State),
			StartTime:   toProtoTimestampPtr(sb.StartTime),
			EndTime:     toProtoTimestampPtr(sb.EndTime),
			SessionId:   sessionID,
		})
	}

	return resp, nil
}

func (s *sandboxService) CreateSandbox(ctx context.Context, req *kraclawv1.CreateSandboxRequest) (*kraclawv1.CreateSandboxResponse, error) {
	if s.sandbox == nil {
		return nil, status.Error(codes.Unavailable, "sandbox controller not configured")
	}

	cfg := sandbox.SandboxConfig{
		GroupFolder:   req.GroupFolder,
		GroupJID:      req.GroupJid,
		SessionID:     req.SessionId,
		IsMain:        req.IsMain,
		AssistantName: req.AssistantName,
	}
	if req.Timeout != nil {
		cfg.Timeout = req.Timeout.AsDuration()
	}

	sb, err := s.sandbox.CreateSandbox(ctx, cfg)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create sandbox: %v", err)
	}

	// If prompt provided, send it as initial input via IPC
	if req.Prompt != "" && s.ipc != nil {
		payload, err := json.Marshal(req.Prompt)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "marshal prompt: %v", err)
		}
		msg := &ipc.IPCMessage{
			Group:   req.GroupFolder,
			Type:    ipc.IPCMessageText,
			Payload: payload,
		}
		if err := s.ipc.SendInput(ctx, req.GroupFolder, msg); err != nil {
			s.log.Warn("failed to send initial prompt via IPC", "group", req.GroupFolder, "error", err)
		}
	}

	resp := &kraclawv1.CreateSandboxResponse{
		Sandbox: &kraclawv1.Sandbox{
			Name:        sb.Name,
			GroupFolder: sb.Group,
			GroupJid:    req.GroupJid,
			State:       string(sb.State),
			StartTime:   toProtoTimestampPtr(sb.StartTime),
			EndTime:     toProtoTimestampPtr(sb.EndTime),
			SessionId:   req.SessionId,
		},
	}
	return resp, nil
}

func (s *sandboxService) PipeSandboxInput(ctx context.Context, req *kraclawv1.PipeSandboxInputRequest) (*kraclawv1.PipeSandboxInputResponse, error) {
	if s.ipc == nil {
		return nil, status.Error(codes.Unavailable, "IPC broker not configured")
	}
	if req.Text == "" {
		return nil, status.Error(codes.InvalidArgument, "text is required")
	}

	payload, err := json.Marshal(req.Text)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshal text: %v", err)
	}
	msg := &ipc.IPCMessage{
		Group:   req.GroupFolder,
		Type:    ipc.IPCMessageText,
		Payload: payload,
	}
	if err := s.ipc.SendInput(ctx, req.GroupFolder, msg); err != nil {
		return nil, status.Errorf(codes.Internal, "send input: %v", err)
	}

	return &kraclawv1.PipeSandboxInputResponse{}, nil
}

func (s *sandboxService) StreamSandboxOutput(req *kraclawv1.StreamOutputRequest, stream grpc.ServerStreamingServer[kraclawv1.SandboxOutput]) error {
	if s.ipc == nil {
		return status.Error(codes.Unavailable, "IPC broker not configured")
	}

	ch, err := s.ipc.SubscribeOutput(stream.Context(), req.GroupFolder)
	if err != nil {
		return status.Errorf(codes.Internal, "subscribe output: %v", err)
	}

	for {
		select {
		case <-stream.Context().Done():
			return nil
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			out := &kraclawv1.SandboxOutput{
				Type: string(msg.Type),
			}
			switch msg.Type {
			case ipc.IPCMessageText:
				var text string
				if err := json.Unmarshal(msg.Payload, &text); err != nil {
					out.Content = string(msg.Payload)
				} else {
					out.Content = text
				}
			case ipc.IPCSessionUpdate:
				var sessionID string
				if err := json.Unmarshal(msg.Payload, &sessionID); err == nil {
					out.NewSessionId = sessionID
				}
			default:
				out.Content = string(msg.Payload)
			}
			if err := stream.Send(out); err != nil {
				return err
			}
		}
	}
}

func (s *adminService) pingMySQL(ctx context.Context) bool {
	if s.db == nil {
		return false
	}
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return s.db.PingContext(pingCtx) == nil
}

func (s *adminService) pingRedis(ctx context.Context) bool {
	if s.rdb == nil {
		return false
	}
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return s.rdb.Ping(pingCtx).Err() == nil
}

func (s *adminService) pingKubernetes(ctx context.Context) bool {
	if s.k8s == nil {
		return false
	}
	_, err := s.k8s.Discovery().ServerVersion()
	return err == nil
}

func metricValue(name string) float64 {
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return 0
	}

	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		return metricFamilyValue(family)
	}

	return 0
}

func metricFamilyValue(family *dto.MetricFamily) float64 {
	total := 0.0
	for _, metric := range family.GetMetric() {
		switch family.GetType() {
		case dto.MetricType_COUNTER:
			total += metric.GetCounter().GetValue()
		case dto.MetricType_GAUGE:
			total += metric.GetGauge().GetValue()
		case dto.MetricType_UNTYPED:
			total += metric.GetUntyped().GetValue()
		}
	}
	return total
}

func toProtoTimestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func toProtoTimestampPtr(t *time.Time) *timestamppb.Timestamp {
	if t == nil || t.IsZero() {
		return nil
	}
	return timestamppb.New(*t)
}

func stringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func sandboxEventToProto(evt sandbox.SandboxEvent) *kraclawv1.Event {
	metadata := map[string]string{
		"name":  evt.Status.Name,
		"group": evt.Status.Group,
		"state": string(evt.Status.State),
	}

	return &kraclawv1.Event{
		Timestamp: timestamppb.Now(),
		Type:      evt.Type,
		Source:    "sandbox",
		Message:   fmt.Sprintf("%s sandbox %s (%s)", evt.Type, evt.Status.Name, evt.Status.State),
		Metadata:  metadata,
	}
}
