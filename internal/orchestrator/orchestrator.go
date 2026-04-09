package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/johanssonvincent/kraclaw/internal/auth"
	"github.com/johanssonvincent/kraclaw/internal/channel"
	"github.com/johanssonvincent/kraclaw/internal/config"
	"github.com/johanssonvincent/kraclaw/internal/ipc"
	"github.com/johanssonvincent/kraclaw/internal/provider"
	"github.com/johanssonvincent/kraclaw/internal/queue"
	"github.com/johanssonvincent/kraclaw/internal/router"
	"github.com/johanssonvincent/kraclaw/internal/sandbox"
	"github.com/johanssonvincent/kraclaw/internal/scheduler"
	"github.com/johanssonvincent/kraclaw/internal/store"
)

const (
	pollInterval       = 2 * time.Second
	defaultIdleTimeout = 30 * time.Minute
)

// sandboxController is the interface the orchestrator uses to manage agent sandboxes.
// *sandbox.Controller satisfies this interface.
type sandboxController interface {
	CreateSandbox(ctx context.Context, cfg sandbox.SandboxConfig) (*sandbox.SandboxStatus, error)
	StopSandbox(ctx context.Context, name string) error
	HasActiveSandbox(ctx context.Context, groupFolder string) (bool, error)
	CleanupOrphans(ctx context.Context) error
	WatchSandboxes(ctx context.Context) (<-chan sandbox.SandboxEvent, error)
}

// Orchestrator wires all components together and runs the main message loop.
type Orchestrator struct {
	cfg     *config.Config
	store   store.Store
	queue   queue.Queue
	ipc     ipc.IPCBroker
	sandbox sandboxController

	router    *router.Router
	auth      *auth.Authorizer
	sched     *scheduler.Scheduler
	providers *provider.Registry

	channels []channel.Channel
	registry *channel.Registry

	// State loaded from MySQL router_state on startup.
	lastTimestamp          time.Time
	lastAgentTimestamp     map[string]time.Time   // chatJID -> cursor (last sent to agent)
	lastConfirmedTimestamp map[string]time.Time   // chatJID -> cursor (last confirmed by agent response)
	sessions               map[string]string      // groupFolder -> sessionID
	registeredGroups       map[string]store.Group // JID -> Group
	activeSandboxes        map[string]string      // chatJID -> current sandbox name

	rateLimiters   map[string]*TokenBucket
	rateLimitersMu sync.Mutex

	// prevLast* fields are written only in saveState which runs in the message loop goroutine.
	// They track last-saved state values to avoid redundant MySQL writes (PERF-04).
	prevLastTimestampStr string // serialized last_timestamp from last save
	prevAgentTsJSON      string // JSON-serialized last_agent_timestamp from last save
	prevConfirmedTsJSON  string // JSON-serialized last_confirmed_timestamp from last save

	ctx    context.Context
	notify chan struct{} // buffered signal to trigger immediate poll
	mu     sync.Mutex
	log    *slog.Logger

	marshalInitialInput func(v any) ([]byte, error)

	// ipcReconnectDelays controls the backoff schedule used by watchGroupOutput
	// when the IPC output channel closes unexpectedly. Exposed as a field so
	// tests can shrink the delays.
	ipcReconnectDelays []time.Duration
}

// New creates a new Orchestrator.
func New(
	cfg *config.Config,
	s store.Store,
	q queue.Queue,
	broker ipc.IPCBroker,
	ctrl *sandbox.Controller,
	reg *channel.Registry,
	log *slog.Logger,
) (*Orchestrator, error) {
	if cfg == nil {
		return nil, fmt.Errorf("orchestrator: config is required")
	}
	if s == nil {
		return nil, fmt.Errorf("orchestrator: store is required")
	}
	if q == nil {
		return nil, fmt.Errorf("orchestrator: queue is required")
	}
	if broker == nil {
		return nil, fmt.Errorf("orchestrator: ipc broker is required")
	}
	if reg == nil {
		return nil, fmt.Errorf("orchestrator: channel registry is required")
	}
	if log == nil {
		return nil, fmt.Errorf("orchestrator: logger is required")
	}
	// Note: ctrl (sandbox controller) can be nil — Phase 1 already added a nil guard for this.
	// Store as interface only when non-nil to preserve interface nil semantics for nil checks.
	var sc sandboxController
	if ctrl != nil {
		sc = ctrl
	}
	return &Orchestrator{
		cfg:                    cfg,
		store:                  s,
		queue:                  q,
		ipc:                    broker,
		sandbox:                sc,
		registry:               reg,
		providers:              provider.NewRegistry(),
		lastAgentTimestamp:     make(map[string]time.Time),
		lastConfirmedTimestamp: make(map[string]time.Time),
		sessions:               make(map[string]string),
		registeredGroups:       make(map[string]store.Group),
		activeSandboxes:        make(map[string]string),
		rateLimiters:           make(map[string]*TokenBucket),
		notify:                 make(chan struct{}, 1),
		log:                    log.With("component", "orchestrator"),
		marshalInitialInput:    json.Marshal,
		ipcReconnectDelays:     []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second},
	}, nil
}

// Start runs the orchestrator. It blocks until ctx is cancelled.
func (o *Orchestrator) Start(ctx context.Context) error {
	o.ctx = ctx

	// 1. Load state from MySQL.
	if err := o.loadState(ctx); err != nil {
		return fmt.Errorf("orchestrator: load state: %w", err)
	}

	// 2. Create router and auth from store.
	o.auth = auth.New(o.store)

	// 3. Connect all channels via registry.
	chCfg := channel.ChannelConfig{
		OnMessage:  o.onInboundMessage,
		OnChatMeta: o.onChatMeta,
		Groups:     o.groupsList,
	}
	chs, err := o.registry.ConnectAll(ctx, chCfg)
	if err != nil {
		return fmt.Errorf("orchestrator: connect channels: %w", err)
	}
	if len(chs) == 0 {
		return fmt.Errorf("orchestrator: no channels connected")
	}
	o.channels = chs
	rtr, err := router.New(chs, o.store)
	if err != nil {
		return fmt.Errorf("orchestrator: create router: %w", err)
	}
	o.router = rtr
	o.log.Info("channels connected", "count", len(chs))

	// 4. Start scheduler in goroutine with retry.
	sched, err := scheduler.New(o.store, o.executeScheduledTask, o.cfg.Scheduler.PollInterval)
	if err != nil {
		return fmt.Errorf("orchestrator: create scheduler: %w", err)
	}
	o.sched = sched
	go func() {
		for {
			if err := o.sched.Start(ctx); err != nil {
				if ctx.Err() != nil {
					return
				}
				o.log.Error("scheduler exited with error, retrying in 5s", "error", err)
				select {
				case <-time.After(5 * time.Second):
				case <-ctx.Done():
					return
				}
				continue
			}
			return
		}
	}()

	// 5. Clean up orphaned sandboxes from previous runs.
	if o.sandbox != nil {
		if err := o.sandbox.CleanupOrphans(ctx); err != nil {
			o.log.Error("orphan cleanup at startup failed", "error", err)
			// Non-fatal — continue startup.
		}
	}

	// 6. Reconcile the active group set against actual K8s state.
	// Sandboxes that completed or were deleted while the server was down leave
	// stale entries in the active group store.  If enough accumulate they exceed
	// MaxConcurrent and prevent any new sandbox from starting.
	o.reconcileActiveSet(ctx)

	// 7. Start periodic orphan cleanup.
	if o.sandbox != nil {
		go func() {
			ticker := time.NewTicker(10 * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					if err := o.sandbox.CleanupOrphans(ctx); err != nil {
						o.log.Error("periodic orphan cleanup failed", "error", err)
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	// 8. Start sandbox watcher with reconnect loop.
	if o.sandbox != nil {
		go o.sandboxWatcher(ctx)
	}

	// 9. Start message loop in goroutine.
	go o.messageLoop(ctx)

	// 10. Recover pending messages.
	o.recoverPendingMessages(ctx)

	// 11. Block on ctx.Done().
	<-ctx.Done()
	return ctx.Err()
}

// Stop gracefully shuts down the orchestrator.
func (o *Orchestrator) Stop(ctx context.Context) error {
	o.log.Info("stopping orchestrator")

	for _, ch := range o.channels {
		if err := ch.Disconnect(ctx); err != nil {
			o.log.Error("failed to disconnect channel", "channel", ch.Name(), "error", err)
		}
	}
	return nil
}

// loadState reads persisted cursors, sessions, and groups from MySQL.
func (o *Orchestrator) loadState(ctx context.Context) error {
	// lastTimestamp
	tsStr, err := o.store.GetState(ctx, "last_timestamp")
	if err != nil {
		return fmt.Errorf("get last_timestamp: %w", err)
	}
	if tsStr != "" {
		t, err := time.Parse(time.RFC3339Nano, tsStr)
		if err != nil {
			o.log.Warn("corrupted last_timestamp, resetting", "value", tsStr, "error", err)
		} else {
			o.lastTimestamp = t
		}
	}

	// lastAgentTimestamp (JSON map)
	agentTsStr, err := o.store.GetState(ctx, "last_agent_timestamp")
	if err != nil {
		return fmt.Errorf("get last_agent_timestamp: %w", err)
	}
	if agentTsStr != "" {
		var raw map[string]string
		if err := json.Unmarshal([]byte(agentTsStr), &raw); err != nil {
			o.log.Warn("corrupted last_agent_timestamp, resetting", "error", err)
		} else {
			for k, v := range raw {
				t, err := time.Parse(time.RFC3339Nano, v)
				if err == nil {
					o.lastAgentTimestamp[k] = t
				}
			}
		}
	}

	// lastConfirmedTimestamp (JSON map)
	confirmedTsStr, err := o.store.GetState(ctx, "last_confirmed_timestamp")
	if err != nil {
		return fmt.Errorf("get last_confirmed_timestamp: %w", err)
	}
	if confirmedTsStr != "" {
		var raw map[string]string
		if err := json.Unmarshal([]byte(confirmedTsStr), &raw); err != nil {
			o.log.Warn("corrupted last_confirmed_timestamp, resetting", "error", err)
		} else {
			for k, v := range raw {
				t, err := time.Parse(time.RFC3339Nano, v)
				if err == nil {
					o.lastConfirmedTimestamp[k] = t
				}
			}
		}
	}
	// Backfill confirmed timestamps from agent timestamps for existing groups.
	for k, v := range o.lastAgentTimestamp {
		if _, ok := o.lastConfirmedTimestamp[k]; !ok {
			o.lastConfirmedTimestamp[k] = v
		}
	}

	// Sessions
	groups, err := o.store.ListGroups(ctx)
	if err != nil {
		return fmt.Errorf("list groups: %w", err)
	}
	for _, g := range groups {
		o.registeredGroups[g.JID] = g

		sess, err := o.store.GetSession(ctx, g.Folder)
		if err != nil {
			o.log.Warn("failed to load session", "group", g.Folder, "error", err)
			continue
		}
		if sess != nil {
			o.sessions[g.Folder] = sess.SessionID
		}
	}

	o.log.Info("state loaded", "groups", len(o.registeredGroups))
	return nil
}

// saveState persists cursors to MySQL, skipping writes when values are unchanged (PERF-04).
func (o *Orchestrator) saveState(ctx context.Context) error {
	o.mu.Lock()
	ts := o.lastTimestamp
	agentTs := make(map[string]string, len(o.lastAgentTimestamp))
	for k, v := range o.lastAgentTimestamp {
		agentTs[k] = v.Format(time.RFC3339Nano)
	}
	confirmedTs := make(map[string]string, len(o.lastConfirmedTimestamp))
	for k, v := range o.lastConfirmedTimestamp {
		confirmedTs[k] = v.Format(time.RFC3339Nano)
	}
	o.mu.Unlock()

	newLastTsStr := ts.Format(time.RFC3339Nano)
	if newLastTsStr != o.prevLastTimestampStr {
		if err := o.store.SetState(ctx, "last_timestamp", newLastTsStr); err != nil {
			return fmt.Errorf("save last_timestamp: %w", err)
		}
		o.prevLastTimestampStr = newLastTsStr
	}

	agentData, err := json.Marshal(agentTs)
	if err != nil {
		return fmt.Errorf("marshal last_agent_timestamp: %w", err)
	}
	newAgentTsJSON := string(agentData)
	if newAgentTsJSON != o.prevAgentTsJSON {
		if err := o.store.SetState(ctx, "last_agent_timestamp", newAgentTsJSON); err != nil {
			return fmt.Errorf("save last_agent_timestamp: %w", err)
		}
		o.prevAgentTsJSON = newAgentTsJSON
	}

	confirmedData, err := json.Marshal(confirmedTs)
	if err != nil {
		return fmt.Errorf("marshal last_confirmed_timestamp: %w", err)
	}
	newConfirmedTsJSON := string(confirmedData)
	if newConfirmedTsJSON != o.prevConfirmedTsJSON {
		if err := o.store.SetState(ctx, "last_confirmed_timestamp", newConfirmedTsJSON); err != nil {
			return fmt.Errorf("save last_confirmed_timestamp: %w", err)
		}
		o.prevConfirmedTsJSON = newConfirmedTsJSON
	}

	return nil
}

// groupsList returns the current registered groups.
func (o *Orchestrator) groupsList() []store.Group {
	o.mu.Lock()
	defer o.mu.Unlock()
	groups := make([]store.Group, 0, len(o.registeredGroups))
	for _, g := range o.registeredGroups {
		groups = append(groups, g)
	}
	return groups
}

// onInboundMessage handles a new message from any channel.
func (o *Orchestrator) onInboundMessage(chatJID string, msg *channel.InboundMessage) {
	ctx := o.ctx

	if strings.HasPrefix(strings.TrimSpace(msg.Content), commandPrefix) {
		if o.handleSlashCommand(ctx, chatJID, msg.Content, msg.Sender) {
			return
		}
	}

	// Per-group rate limit check (PERF-01).
	o.rateLimitersMu.Lock()
	limiter, ok := o.rateLimiters[chatJID]
	if !ok {
		limiter = newTokenBucket(int64(o.cfg.Queue.RateLimitTokensPerSec))
		o.rateLimiters[chatJID] = limiter
	}
	o.rateLimitersMu.Unlock()

	if !limiter.TryAcquire(time.Now()) {
		o.log.Warn("message dropped: rate limit exceeded", "chat_jid", chatJID)
		return
	}

	// Per-group message size check (PERF-02).
	if o.cfg.Queue.MaxMessageSizeBytes > 0 && len(msg.Content) > o.cfg.Queue.MaxMessageSizeBytes {
		o.log.Warn("message dropped: exceeds size limit",
			"chat_jid", chatJID,
			"size", len(msg.Content),
			"max", o.cfg.Queue.MaxMessageSizeBytes)
		return
	}

	// Store the message.
	storeMsg := &store.Message{
		ID:         msg.ID,
		ChatJID:    msg.ChatJID,
		Sender:     msg.Sender,
		SenderName: msg.SenderName,
		Content:    msg.Content,
		Timestamp:  msg.Timestamp,
	}
	if err := o.store.StoreMessage(ctx, storeMsg); err != nil {
		o.log.Error("failed to store message", "chat_jid", chatJID, "error", err)
		return
	}

	// Signal messageLoop to poll immediately.
	select {
	case o.notify <- struct{}{}:
	default:
	}
}

// onChatMeta handles chat metadata updates from channels.
func (o *Orchestrator) onChatMeta(chatJID string, timestamp time.Time, name string, ch string, isGroup bool) {
	ctx := o.ctx
	chat := &store.Chat{
		JID:             chatJID,
		Name:            name,
		Channel:         ch,
		IsGroup:         isGroup,
		LastMessageTime: timestamp,
	}
	if err := o.store.UpsertChat(ctx, chat); err != nil {
		o.log.Error("failed to store chat metadata", "chat_jid", chatJID, "error", err)
	}
}

// messageLoop polls MySQL for new messages and dispatches them.
func (o *Orchestrator) messageLoop(ctx context.Context) {
	o.log.Info("message loop started")
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			o.log.Info("message loop stopped")
			return
		case <-ticker.C:
			o.pollMessages(ctx)
		case <-o.notify:
			o.pollMessages(ctx)
			ticker.Reset(pollInterval)
		}
	}
}

// refreshGroups reloads the registered groups from the store.
// It performs a full rebuild so deleted groups are removed from the dispatch map.
func (o *Orchestrator) refreshGroups(ctx context.Context) {
	groups, err := o.store.ListGroups(ctx)
	if err != nil {
		o.log.Error("failed to refresh groups", "error", err)
		return
	}

	newMap := make(map[string]store.Group, len(groups))
	for _, g := range groups {
		newMap[g.JID] = g
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	// Log removed groups for observability.
	for jid := range o.registeredGroups {
		if _, ok := newMap[jid]; !ok {
			o.log.Info("group removed from dispatch", "jid", jid)
		}
	}
	// Log new groups.
	for jid := range newMap {
		if _, ok := o.registeredGroups[jid]; !ok {
			o.log.Info("discovered new group", "jid", jid)
		}
	}
	o.registeredGroups = newMap
}

// pollMessages fetches new messages and dispatches per-group processing.
func (o *Orchestrator) pollMessages(ctx context.Context) {
	// Refresh groups to pick up dynamically registered ones.
	o.refreshGroups(ctx)

	o.mu.Lock()
	jids := make([]string, 0, len(o.registeredGroups))
	for jid := range o.registeredGroups {
		jids = append(jids, jid)
	}
	since := o.lastTimestamp
	o.mu.Unlock()

	if len(jids) == 0 {
		return
	}

	messages, err := o.store.GetNewMessages(ctx, jids, since, o.cfg.Queue.MessageLimit)
	if err != nil {
		o.log.Error("failed to poll messages", "error", err)
		return
	}
	if len(messages) == 0 {
		return
	}

	o.log.Info("new messages", "count", len(messages))

	// Advance the seen cursor.
	newTimestamp := messages[len(messages)-1].Timestamp
	o.mu.Lock()
	o.lastTimestamp = newTimestamp
	o.mu.Unlock()
	if err := o.saveState(ctx); err != nil {
		o.log.Error("failed to save state", "error", err)
	}

	// Deduplicate by group.
	byGroup := make(map[string][]store.Message)
	for _, msg := range messages {
		byGroup[msg.ChatJID] = append(byGroup[msg.ChatJID], msg)
	}

	for chatJID, groupMsgs := range byGroup {
		o.mu.Lock()
		group, ok := o.registeredGroups[chatJID]
		o.mu.Unlock()
		if !ok {
			continue
		}

		// Check trigger for non-main groups.
		if !group.IsMain && group.RequiresTrigger {
			triggered, err := o.hasTriggerMessage(ctx, chatJID, group, groupMsgs)
			if err != nil {
				o.log.Error("trigger check failed", "error", err)
				continue
			}
			if !triggered {
				continue
			}
		}

		// Check if there's an active container for this group.
		active, err := o.queue.IsActive(ctx, chatJID)
		if err != nil {
			o.log.Error("failed to check queue active state", "error", err)
			continue
		}

		if active {
			// Pipe messages to the active container via IPC.
			o.mu.Lock()
			agentTs := o.lastAgentTimestamp[chatJID]
			o.mu.Unlock()

			allPending, err := o.store.GetMessagesSince(ctx, chatJID, agentTs, o.cfg.Queue.MessageLimit)
			if err != nil {
				o.log.Error("failed to get pending messages", "error", err)
				continue
			}
			if len(allPending) == 0 {
				allPending = groupMsgs
			}

			formatted := o.router.FormatMessagesForAgent(allPending, o.cfg.Channels.AssistantName)
			payload, err := json.Marshal(map[string]string{"messages": formatted})
			if err != nil {
				o.log.Error("failed to marshal message payload", "group", group.Name, "error", err)
				continue
			}
			if err := o.ipc.SendInput(ctx, group.Folder, ipc.DefaultAgentID, &ipc.IPCMessage{
				Group:   group.Folder,
				Type:    ipc.IPCMessageText,
				Payload: payload,
			}); err != nil {
				o.log.Error("failed to pipe messages to container", "group", group.Name, "error", err)
				continue
			}

			o.log.Debug("piped messages to active container", "group", group.Name, "count", len(allPending))

			// Advance agent cursor.
			o.mu.Lock()
			o.lastAgentTimestamp[chatJID] = allPending[len(allPending)-1].Timestamp
			o.mu.Unlock()
			if err := o.saveState(ctx); err != nil {
				o.log.Error("failed to save state", "error", err)
			}
		} else {
			// Create a new sandbox for this group.
			go func(jid string, g store.Group) {
				defer func() {
					if r := recover(); r != nil {
						o.log.Error("panic in processGroupMessages", "group", g.Name, "panic", r)
					}
				}()
				if err := o.processGroupMessages(ctx, jid); err != nil {
					o.log.Error("failed to process group messages", "group", g.Name, "error", err)
				}
			}(chatJID, group)
		}
	}
}

// processGroupMessages fetches pending messages for a group and creates a sandbox.
func (o *Orchestrator) processGroupMessages(ctx context.Context, chatJID string) error {
	o.mu.Lock()
	group, ok := o.registeredGroups[chatJID]
	if !ok {
		o.mu.Unlock()
		return nil
	}
	agentTs := o.lastAgentTimestamp[chatJID]
	sessionID := o.sessions[group.Folder]
	o.mu.Unlock()

	messages, err := o.store.GetMessagesSince(ctx, chatJID, agentTs, o.cfg.Queue.MessageLimit)
	if err != nil {
		return fmt.Errorf("get messages since: %w", err)
	}
	if len(messages) == 0 {
		return nil
	}

	// Check trigger for non-main groups.
	if !group.IsMain && group.RequiresTrigger {
		triggered, err := o.hasTriggerMessage(ctx, chatJID, group, messages)
		if err != nil {
			return fmt.Errorf("trigger check: %w", err)
		}
		if !triggered {
			return nil
		}
	}

	// Enforce MAX_CONCURRENT limit — reject sandbox creation when at capacity (REL-01).
	activeCount, err := o.queue.ActiveCount(ctx)
	if err != nil {
		return fmt.Errorf("check active count: %w", err)
	}
	if activeCount >= int64(o.cfg.Queue.MaxConcurrent) {
		o.log.Debug("sandbox creation deferred: MAX_CONCURRENT reached",
			"group", group.Name,
			"active", activeCount,
			"max", o.cfg.Queue.MaxConcurrent)
		return nil // Message stays queued; retried on next poll.
	}

	formatted := o.router.FormatMessagesForAgent(messages, o.cfg.Channels.AssistantName)

	// Advance agent cursor before starting sandbox.
	previousCursor := agentTs
	newCursor := messages[len(messages)-1].Timestamp
	o.mu.Lock()
	o.lastAgentTimestamp[chatJID] = newCursor
	o.mu.Unlock()
	if err := o.saveState(ctx); err != nil {
		o.log.Error("failed to save state", "error", err)
	}

	o.log.Info("processing messages", "group", group.Name, "count", len(messages))

	// Determine timeout.
	timeout := o.cfg.Queue.IdleTimeout
	if group.ContainerConfig != nil && group.ContainerConfig.Timeout > 0 {
		timeout = time.Duration(group.ContainerConfig.Timeout) * time.Millisecond
	}

	modelName := ""
	if group.ContainerConfig != nil {
		modelName = group.ContainerConfig.Model
	}

	// Marshal initial input payload.
	payload, err := o.marshalInitialInput(map[string]string{"messages": formatted})
	if err != nil {
		return fmt.Errorf("marshal initial input: %w", err)
	}

	// Create sandbox.
	sbCfg := sandbox.SandboxConfig{
		GroupFolder:     group.Folder,
		GroupJID:        chatJID,
		SessionID:       sessionID,
		IsMain:          group.IsMain,
		Timeout:         timeout,
		Input:           formatted,
		AssistantName:   o.cfg.Channels.AssistantName,
		Model:           modelName,
		SessionsPVC:     o.cfg.K8s.SessionsPVC,
		GroupsPVC:       o.cfg.K8s.GroupsPVC,
		DataPVC:         o.cfg.K8s.DataPVC,
		ContainerConfig: group.ContainerConfig,
	}

	if o.sandbox == nil {
		// Roll back cursor on error.
		o.mu.Lock()
		o.lastAgentTimestamp[chatJID] = previousCursor
		o.mu.Unlock()
		if err := o.saveState(ctx); err != nil {
			o.log.Error("failed to save state", "error", err)
		}
		return fmt.Errorf("create sandbox: sandbox controller is nil (Kubernetes not connected)")
	}

	status, err := o.sandbox.CreateSandbox(ctx, sbCfg)
	if err != nil {
		// Roll back cursor on error.
		o.mu.Lock()
		o.lastAgentTimestamp[chatJID] = previousCursor
		o.mu.Unlock()
		if err := o.saveState(ctx); err != nil {
			o.log.Error("failed to save state", "error", err)
		}
		return fmt.Errorf("create sandbox: %w", err)
	}

	o.mu.Lock()
	o.activeSandboxes[chatJID] = status.Name
	o.mu.Unlock()

	if err := o.queue.MarkActive(ctx, chatJID); err != nil {
		o.log.Error("failed to mark group active, cleaning up sandbox", "group", group.Name, "error", err)
		if cleanupErr := o.sandbox.StopSandbox(ctx, status.Name); cleanupErr != nil {
			o.log.Error("failed to cleanup sandbox after MarkActive failure", "group", group.Name, "job", status.Name, "error", cleanupErr)
		}
		// Roll back cursor and clean up sandbox tracking.
		o.mu.Lock()
		delete(o.activeSandboxes, chatJID)
		o.lastAgentTimestamp[chatJID] = previousCursor
		o.mu.Unlock()
		if saveErr := o.saveState(ctx); saveErr != nil {
			o.log.Error("failed to save state", "error", saveErr)
		}
		return fmt.Errorf("mark active: %w", err)
	}

	// Subscribe to IPC output BEFORE spawning the watcher goroutine and BEFORE
	// SendInput, so we cannot miss the agent's first output (race fix).
	outputCh, err := o.ipc.SubscribeOutput(ctx, group.Folder)
	if err != nil {
		o.log.Error("failed to subscribe to IPC output, tearing down sandbox", "group", group.Name, "error", err)
		if cleanupErr := o.sandbox.StopSandbox(ctx, status.Name); cleanupErr != nil {
			o.log.Error("failed to stop sandbox after SubscribeOutput failure", "group", group.Name, "job", status.Name, "error", cleanupErr)
		}
		if markErr := o.queue.MarkInactive(ctx, chatJID); markErr != nil {
			o.log.Error("failed to mark group inactive after SubscribeOutput failure", "group", group.Name, "error", markErr)
		}
		o.mu.Lock()
		delete(o.activeSandboxes, chatJID)
		o.lastAgentTimestamp[chatJID] = previousCursor
		o.mu.Unlock()
		if saveErr := o.saveState(ctx); saveErr != nil {
			o.log.Error("failed to save state after SubscribeOutput failure", "group", group.Name, "error", saveErr)
		}
		return fmt.Errorf("subscribe output: %w", err)
	}

	// Spawn watchGroupOutput directly to listen for agent output (no event channel)
	go func(jid string, ch <-chan *ipc.IPCMessage) {
		defer func() {
			if r := recover(); r != nil {
				o.log.Error("panic in watchGroupOutput", "group_jid", jid, "panic", r)
			}
		}()
		o.watchGroupOutput(ctx, jid, ch)
	}(chatJID, outputCh)

	// Send initial messages via IPC so the agent can read them on startup.
	if err := o.ipc.SendInput(ctx, group.Folder, ipc.DefaultAgentID, &ipc.IPCMessage{
		Group:   group.Folder,
		Type:    ipc.IPCMessageText,
		Payload: payload,
	}); err != nil {
		o.log.Error("failed to send initial input to agent, tearing down sandbox", "group", group.Name, "error", err)
		if cleanupErr := o.sandbox.StopSandbox(ctx, status.Name); cleanupErr != nil {
			o.log.Error("failed to stop sandbox after SendInput failure", "group", group.Name, "job", status.Name, "error", cleanupErr)
		}
		if markErr := o.queue.MarkInactive(ctx, chatJID); markErr != nil {
			o.log.Error("failed to mark group inactive after SendInput failure", "group", group.Name, "error", markErr)
		}
		o.mu.Lock()
		delete(o.activeSandboxes, chatJID)
		o.lastAgentTimestamp[chatJID] = previousCursor
		o.mu.Unlock()
		if saveErr := o.saveState(ctx); saveErr != nil {
			o.log.Error("failed to save state after SendInput failure", "group", group.Name, "error", saveErr)
		}
		return fmt.Errorf("send initial input: %w", err)
	}

	// Mark initial messages as confirmed since they're pre-populated in the IPC stream
	// and the new agent will read them on startup.
	o.mu.Lock()
	o.lastConfirmedTimestamp[chatJID] = o.lastAgentTimestamp[chatJID]
	o.mu.Unlock()

	o.log.Info("sandbox created", "group", group.Name, "job", status.Name)
	return nil
}

// sandboxWatcher runs a self-healing loop that subscribes to Sandbox lifecycle events.
// When the watch channel closes (K8s API restart or network hiccup), it reconnects
// using exponential backoff (100ms base, 30s cap, 1.5x multiplier).
func (o *Orchestrator) sandboxWatcher(ctx context.Context) {
	const (
		baseBackoff = 100 * time.Millisecond
		maxBackoff  = 30 * time.Second
	)
	backoff := baseBackoff

	for {
		if err := o.runSandboxWatcher(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			o.log.Error("sandbox watcher failed, retrying with backoff",
				"error", err, "backoff", backoff)

			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}

			backoff = time.Duration(float64(backoff) * 1.5)
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		} else {
			backoff = baseBackoff // Reset on clean exit (ctx cancelled).
		}
	}
}

// runSandboxWatcher subscribes to Sandbox events for one watch lifecycle.
// Returns nil on clean context cancellation; error on watch failure.
func (o *Orchestrator) runSandboxWatcher(ctx context.Context) error {
	o.log.Info("sandbox watcher started")

	events, err := o.sandbox.WatchSandboxes(ctx)
	if err != nil {
		return fmt.Errorf("watch sandboxes: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			o.log.Info("sandbox watcher stopped")
			return nil
		case event, ok := <-events:
			if !ok {
				return fmt.Errorf("sandbox watch channel closed")
			}
			o.log.Debug("sandbox event", "type", event.Type, "sandbox", event.Status.Name,
				"state", event.Status.State)
			o.handleSandboxEvent(ctx, event)
		}
	}
}

// handleSandboxEvent processes a single Sandbox lifecycle event.
// When a sandbox reaches a terminal state (completed, failed) or is deleted,
// the owning group is marked inactive so the active-set count stays accurate
// and new sandboxes can be created on the next message poll.
//
// The watchGroupOutput goroutine is the primary path for marking groups inactive
// when an agent shuts down normally.  handleSandboxEvent acts as a safety net
// for cases where no watchGroupOutput is running — e.g. sandboxes that were
// already complete when the server restarted and are reported by the initial
// List inside WatchSandboxes, or sandboxes that disappear without sending an
// IPC shutdown message.
func (o *Orchestrator) handleSandboxEvent(ctx context.Context, event sandbox.SandboxEvent) {
	terminal := event.Type == "deleted" ||
		event.Status.State == sandbox.StateCompleted ||
		event.Status.State == sandbox.StateFailed

	if !terminal {
		return
	}

	// Resolve the group folder to a chat JID via the registered groups map.
	folder := event.Status.Group
	if folder == "" {
		return
	}

	o.mu.Lock()
	var chatJID string
	for jid, g := range o.registeredGroups {
		if g.Folder == folder {
			chatJID = jid
			break
		}
	}
	currentSandbox := o.activeSandboxes[chatJID]
	o.mu.Unlock()

	if chatJID == "" {
		// Group may have been deleted or not yet registered; nothing to do.
		return
	}

	// If the group has a tracked sandbox and this event is for a different
	// sandbox (e.g. orphan cleanup of an old resource), skip it — the
	// current sandbox is still running.
	if currentSandbox != "" && event.Status.Name != currentSandbox {
		o.log.Info("sandbox event: ignoring stale event for non-current sandbox",
			"event_sandbox", event.Status.Name, "current_sandbox", currentSandbox,
			"folder", folder, "jid", chatJID)
		return
	}

	active, err := o.queue.IsActive(ctx, chatJID)
	if err != nil {
		o.log.Warn("sandbox event: failed to check active state",
			"folder", folder, "jid", chatJID, "error", err)
		return
	}
	if !active {
		// Already inactive — watchGroupOutput already handled this or group was never active.
		return
	}

	o.log.Info("sandbox event: terminal state detected, marking group inactive",
		"type", event.Type, "sandbox", event.Status.Name, "state", event.Status.State,
		"folder", folder, "jid", chatJID)

	// Clear the tracked sandbox since it's terminal.
	o.mu.Lock()
	delete(o.activeSandboxes, chatJID)
	o.mu.Unlock()

	if err := o.queue.MarkInactive(ctx, chatJID); err != nil {
		o.log.Error("sandbox event: failed to mark group inactive",
			"folder", folder, "jid", chatJID, "error", err)
	}
}

// watchGroupOutput subscribes to IPC output for a single group and processes messages.
// It also periodically checks that the agent Job still exists to avoid getting stuck
// if the agent dies without sending a shutdown message.
func (o *Orchestrator) watchGroupOutput(ctx context.Context, chatJID string, ch <-chan *ipc.IPCMessage) {
	o.mu.Lock()
	group, ok := o.registeredGroups[chatJID]
	o.mu.Unlock()
	if !ok {
		o.log.Warn("watchGroupOutput: group not found in registeredGroups, skipping", "group_jid", chatJID)
		return
	}

	o.log.Debug("watching IPC output", "group", group.Name)

	liveness := time.NewTicker(10 * time.Second)
	defer liveness.Stop()

	// startupTimeout guards against the agent pod never starting (e.g. operator not
	// reconciling the SandboxClaim). If no IPC message arrives within the deadline we
	// treat the sandbox as failed and deactivate the group so the message can be retried.
	startupTimeout := o.cfg.K8s.SandboxStartupTimeout
	if startupTimeout <= 0 {
		startupTimeout = 5 * time.Minute
	}
	startupDeadline := time.NewTimer(startupTimeout)
	defer startupDeadline.Stop()
	agentConnected := false // set to true on first IPC message from this agent

	deactivate := func() {
		if err := o.queue.MarkInactive(ctx, chatJID); err != nil {
			o.log.Error("failed to mark group inactive", "group", group.Name, "error", err)
		}

		o.mu.Lock()
		delete(o.activeSandboxes, chatJID)
		o.mu.Unlock()

		// Roll back agent cursor to last confirmed position so any messages
		// that were piped to the dead agent but never processed get re-sent.
		o.mu.Lock()
		confirmed := o.lastConfirmedTimestamp[chatJID]
		sent := o.lastAgentTimestamp[chatJID]
		if confirmed.Before(sent) {
			o.log.Info("rolling back agent cursor to last confirmed",
				"group", group.Name,
				"sent", sent.Format(time.RFC3339Nano),
				"confirmed", confirmed.Format(time.RFC3339Nano))
			o.lastAgentTimestamp[chatJID] = confirmed
		}
		o.mu.Unlock()
		if err := o.saveState(ctx); err != nil {
			o.log.Error("failed to save state after deactivation — cursor rollback is in-memory only; messages may be re-sent or lost on restart",
				"group", group.Name, "error", err)
		}

		// Check MySQL for pending messages (not just the NATS queue).
		o.mu.Lock()
		agentTs := o.lastAgentTimestamp[chatJID]
		o.mu.Unlock()
		pending, err := o.store.GetMessagesSince(ctx, chatJID, agentTs, 1)
		pendingCheckFailed := false
		if err != nil {
			o.log.Error("failed to check pending messages; triggering recovery defensively", "group", group.Name, "error", err)
			pendingCheckFailed = true
		}
		// Also drain the NATS queue for scheduled tasks.
		// Dequeue returns nil,nil for both an empty queue and a malformed-message
		// that was ACK'd and skipped. Loop a few times so that skipped malformed
		// messages do not mask valid queued tasks.
		const maxMalformedRetries = 5
		var qMsg *queue.QueueMessage
		skipped := 0
		for range maxMalformedRetries {
			msg, deqErr := o.queue.Dequeue(ctx, chatJID)
			if deqErr != nil {
				o.log.Error("failed to dequeue message", "group", group.Name, "error", deqErr)
				break
			}
			if msg != nil {
				qMsg = msg
				break
			}
			skipped++
		}
		if skipped == maxMalformedRetries {
			o.log.Error("dequeue exhausted malformed-message retries: all messages were nil or malformed",
				"group", group.Name, "retries", maxMalformedRetries)
		}

		if len(pending) > 0 || qMsg != nil || pendingCheckFailed {
			go func() {
				if err := o.processGroupMessages(ctx, chatJID); err != nil {
					o.log.Error("failed to process queued messages", "group", group.Name, "error", err)
				}
			}()
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-startupDeadline.C:
			if !agentConnected {
				o.log.Error("sandbox startup timeout: agent pod never connected via IPC, deactivating group",
					"group", group.Name, "timeout", startupTimeout)
				deactivate()
				return
			}
		case <-liveness.C:
			if o.sandbox == nil {
				continue
			}
			active, err := o.sandbox.HasActiveSandbox(ctx, group.Folder)
			if err != nil {
				o.log.Warn("failed to check sandbox liveness", "group", group.Name, "error", err)
				continue
			}
			if !active {
				o.log.Info("agent job disappeared, marking group inactive", "group", group.Name)
				deactivate()
				return
			}
		case msg, ok := <-ch:
			if !ok {
				if ctx.Err() != nil {
					return
				}
				// Reconnect with exponential backoff before giving up.
				var reconnected bool
				var lastErr error
				for _, delay := range o.ipcReconnectDelays {
					select {
					case <-ctx.Done():
						return
					case <-time.After(delay):
					}
					newCh, err := o.ipc.SubscribeOutput(ctx, group.Folder)
					if err == nil {
						ch = newCh
						reconnected = true
						o.log.Info("reconnected to IPC output after iterator error", "group", group.Name)
						break
					}
					lastErr = err
					o.log.Warn("IPC reconnect failed, retrying", "group", group.Name, "error", err, "delay", delay)
				}
				if !reconnected {
					o.log.Error("IPC reconnect exhausted, deactivating group",
						"group", group.Name,
						"last_error", lastErr)
					deactivate()
					return
				}
				continue
			}
			agentConnected = true
			if o.handleIPCMessage(ctx, chatJID, group, msg) {
				deactivate()
				return
			}
		}
	}
}

// handleIPCMessage processes a single IPC message from an agent.
// Returns true if the agent has shut down and the watcher should stop.
func (o *Orchestrator) handleIPCMessage(ctx context.Context, chatJID string, group store.Group, msg *ipc.IPCMessage) bool {
	switch msg.Type {
	case ipc.IPCMessageText:
		// Agent wants to send a message to the chat.
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			o.log.Error("failed to unmarshal message payload", "error", err)
			return false
		}
		if err := o.router.RouteOutbound(ctx, chatJID, payload.Text); err != nil {
			// Outbound routing failed — do NOT advance the confirmed cursor
			// so the message remains eligible for retry on the next agent
			// invocation. Skip storing the bot reply as well, since delivery
			// was not confirmed.
			o.log.Error("failed to route outbound message; leaving cursor unadvanced for retry",
				"group", group.Name, "error", err)
			return false
		}

		// Store bot reply so FormatMessagesForAgent attributes it as an assistant turn.
		botMsg := &store.Message{
			ID:           uuid.New().String(),
			ChatJID:      chatJID,
			Sender:       "assistant",
			SenderName:   "assistant",
			Content:      payload.Text,
			Timestamp:    time.Now().UTC(),
			IsBotMessage: true,
		}
		if err := o.store.StoreMessage(ctx, botMsg); err != nil {
			o.log.Error("failed to store bot reply", "group", group.Name, "error", err)
		}

		// Agent responded, so all messages sent up to the current cursor are confirmed.
		o.mu.Lock()
		o.lastConfirmedTimestamp[chatJID] = o.lastAgentTimestamp[chatJID]
		o.mu.Unlock()

	case ipc.IPCSessionUpdate:
		// Agent is reporting a new session ID.
		var payload struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			o.log.Error("failed to unmarshal session_update payload", "error", err)
			return false
		}
		o.mu.Lock()
		o.sessions[group.Folder] = payload.SessionID
		o.mu.Unlock()

		if err := o.store.UpsertSession(ctx, &store.Session{
			GroupFolder: group.Folder,
			SessionID:   payload.SessionID,
		}); err != nil {
			o.log.Error("failed to persist session", "group", group.Name, "error", err)
		}

	case ipc.IPCTaskCreate:
		var task store.ScheduledTask
		if err := json.Unmarshal(msg.Payload, &task); err != nil {
			o.log.Error("failed to unmarshal task_create payload", "error", err)
			return false
		}
		task.GroupFolder = group.Folder
		task.ChatJID = chatJID
		if err := task.Validate(); err != nil {
			o.log.Error("task_create rejected: validation failed", "group", group.Name, "error", err)
			return false
		}
		if err := o.store.CreateTask(ctx, &task); err != nil {
			o.log.Error("failed to create task", "group", group.Name, "error", err)
		}

	case ipc.IPCTaskUpdate:
		var task store.ScheduledTask
		if err := json.Unmarshal(msg.Payload, &task); err != nil {
			o.log.Error("failed to unmarshal task_update payload", "error", err)
			return false
		}
		// Validate group ownership — reject if agent tries to update another group's task.
		if task.GroupFolder != group.Folder {
			o.log.Error("task update rejected: group mismatch",
				"agent_group", group.Folder,
				"task_group", task.GroupFolder)
			return false
		}
		if err := o.store.UpdateTask(ctx, &task); err != nil {
			o.log.Error("failed to update task", "group", group.Name, "error", err)
		}

	case ipc.IPCTaskDelete:
		var payload struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			o.log.Error("failed to unmarshal task_delete payload", "error", err)
			return false
		}
		// Always scope delete to agent's own group — mismatched IDs result in 0 rows deleted.
		if err := o.store.DeleteTask(ctx, payload.ID, group.Folder); err != nil {
			o.log.Error("failed to delete task", "group", group.Name, "error", err)
		}

	case ipc.IPCShutdown:
		o.log.Info("agent shutdown received", "group", group.Name)
		if err := o.ipc.DeleteStreams(ctx, group.Folder); err != nil {
			o.log.Error("failed to delete ipc streams on shutdown", "group", group.Name, "error", err)
		}
		return true

	default:
		o.log.Warn("unknown IPC message type", "type", msg.Type, "group", group.Name)
	}
	return false
}

// hasTriggerMessage checks whether any message in the batch matches the group's
// trigger pattern and is from an allowed sender.
func (o *Orchestrator) hasTriggerMessage(ctx context.Context, chatJID string, group store.Group, messages []store.Message) (bool, error) {
	for _, m := range messages {
		if o.router.MatchesTrigger(m.Content, group.TriggerPattern) {
			allowed, err := o.auth.IsAllowed(ctx, chatJID, m.Sender)
			if err != nil {
				return false, fmt.Errorf("allowlist check: %w", err)
			}
			if m.IsFromMe || allowed {
				return true, nil
			}
		}
	}
	return false, nil
}

// executeScheduledTask is the TaskExecutor callback for the scheduler.
func (o *Orchestrator) executeScheduledTask(ctx context.Context, task store.ScheduledTask) error {
	// Enqueue the task as a message for processing.
	qMsg := &queue.QueueMessage{
		GroupJID:  task.ChatJID,
		Content:   task.Prompt,
		Timestamp: time.Now(),
		IsTask:    true,
		TaskID:    task.ID,
	}
	return o.queue.Enqueue(ctx, task.ChatJID, qMsg)
}

// reconcileActiveSet removes stale entries from the active group store.
// On server restart, sandbox processes that completed while the server was down
// leave their group JIDs permanently in the store.  If enough accumulate they
// inflate ActiveCount past MaxConcurrent, silently blocking new sandbox creation.
// This method enumerates the active store and calls MarkInactive for any JID that
// has no corresponding running or pending K8s sandbox.
func (o *Orchestrator) reconcileActiveSet(ctx context.Context) {
	activeJIDs, err := o.queue.ActiveJIDs(ctx)
	if err != nil {
		o.log.Error("reconcile active set: failed to list active JIDs", "error", err)
		return
	}
	if len(activeJIDs) == 0 {
		return
	}

	var removed int
	for _, jid := range activeJIDs {
		// Determine the group folder from the registered groups map.
		o.mu.Lock()
		group, ok := o.registeredGroups[jid]
		o.mu.Unlock()

		if !ok {
			// JID not registered — stale entry from a deleted group; remove it.
			if err := o.queue.MarkInactive(ctx, jid); err != nil {
				o.log.Error("reconcile active set: failed to mark unregistered JID inactive",
					"jid", jid, "error", err)
			} else {
				o.log.Info("reconcile active set: removed stale JID (group not registered)", "jid", jid)
				removed++
			}
			continue
		}

		if o.sandbox == nil {
			// No K8s — treat all as inactive.
			if err := o.queue.MarkInactive(ctx, jid); err != nil {
				o.log.Error("reconcile active set: failed to mark inactive (no sandbox ctrl)",
					"jid", jid, "error", err)
			} else {
				removed++
			}
			continue
		}

		hasActive, err := o.sandbox.HasActiveSandbox(ctx, group.Folder)
		if err != nil {
			o.log.Warn("reconcile active set: failed to check sandbox liveness, skipping",
				"jid", jid, "folder", group.Folder, "error", err)
			continue
		}
		if !hasActive {
			if err := o.queue.MarkInactive(ctx, jid); err != nil {
				o.log.Error("reconcile active set: failed to mark inactive",
					"jid", jid, "folder", group.Folder, "error", err)
			} else {
				o.log.Info("reconcile active set: removed stale JID (no running sandbox)",
					"jid", jid, "folder", group.Folder)
				removed++
			}
		}
	}

	if removed > 0 {
		o.log.Info("reconcile active set: cleaned stale entries", "removed", removed, "total_was", len(activeJIDs))
	}
}

// recoverPendingMessages checks each group for unprocessed messages on startup.
func (o *Orchestrator) recoverPendingMessages(ctx context.Context) {
	o.mu.Lock()
	groups := make(map[string]store.Group, len(o.registeredGroups))
	for k, v := range o.registeredGroups {
		groups[k] = v
	}
	agentTimestamps := make(map[string]time.Time, len(o.lastAgentTimestamp))
	for k, v := range o.lastAgentTimestamp {
		agentTimestamps[k] = v
	}
	o.mu.Unlock()

	for chatJID, group := range groups {
		agentTs := agentTimestamps[chatJID]
		pending, err := o.store.GetMessagesSince(ctx, chatJID, agentTs, 1)
		if err != nil {
			o.log.Error("recovery: failed to check pending", "group", group.Name, "error", err)
			continue
		}
		if len(pending) > 0 {
			o.log.Info("recovery: found unprocessed messages", "group", group.Name, "count", len(pending))
			qMsg := &queue.QueueMessage{
				GroupJID:  chatJID,
				Content:   "recovery",
				Timestamp: time.Now(),
			}
			if err := o.queue.Enqueue(ctx, chatJID, qMsg); err != nil {
				o.log.Error("recovery: failed to enqueue", "group", group.Name, "error", err)
			}
		}
	}
}
