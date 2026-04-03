package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// ErrGroupNotFound is returned when an operation targets a group JID that does
// not exist in the database.
var ErrGroupNotFound = errors.New("group not found")

// ContainerConfig holds per-group container settings.
type ContainerConfig struct {
	AdditionalMounts []AdditionalMount `json:"additionalMounts,omitempty"`
	Timeout          int               `json:"timeout,omitempty"` // milliseconds, default 300000
	Model            string            `json:"model,omitempty"`
	Provider         string            `json:"provider,omitempty"` // "openai", "anthropic" — empty defaults to "anthropic"
}

type AdditionalMount struct {
	HostPath      string `json:"hostPath"`
	ContainerPath string `json:"containerPath,omitempty"`
	ReadOnly      bool   `json:"readonly,omitempty"`
}

// Group represents a registered chat group.
type Group struct {
	JID             string
	Name            string
	Folder          string
	TriggerPattern  string
	AddedAt         time.Time
	ContainerConfig *ContainerConfig
	RequiresTrigger bool
	IsMain          bool
}

// Validate checks that required Group fields are set.
func (g *Group) Validate() error {
	if g.JID == "" {
		return fmt.Errorf("group JID is required")
	}
	if g.Folder == "" {
		return fmt.Errorf("group folder is required")
	}
	if g.RequiresTrigger && g.TriggerPattern == "" {
		return fmt.Errorf("trigger pattern required when requires_trigger is true")
	}
	return nil
}

// Message represents a stored chat message.
type Message struct {
	ID           string
	ChatJID      string
	Sender       string
	SenderName   string
	Content      string
	Timestamp    time.Time
	IsFromMe     bool
	IsBotMessage bool
}

// Chat represents chat metadata.
type Chat struct {
	JID             string
	Name            string
	Channel         string
	IsGroup         bool
	LastMessageTime time.Time
}

// ScheduleType for scheduled tasks.
type ScheduleType string

const (
	ScheduleCron     ScheduleType = "cron"
	ScheduleInterval ScheduleType = "interval"
	ScheduleOnce     ScheduleType = "once"
)

// ContextMode for task execution.
type ContextMode string

const (
	ContextGroup    ContextMode = "group"
	ContextIsolated ContextMode = "isolated"
)

// TaskStatus for task lifecycle.
type TaskStatus string

const (
	TaskActive    TaskStatus = "active"
	TaskPaused    TaskStatus = "paused"
	TaskCompleted TaskStatus = "completed"
)

// ScheduledTask represents a scheduled task.
type ScheduledTask struct {
	ID            string
	GroupFolder   string
	ChatJID       string
	Prompt        string
	ScheduleType  ScheduleType
	ScheduleValue string
	ContextMode   ContextMode
	NextRun       *time.Time
	LastRun       *time.Time
	LastResult    *string
	Status        TaskStatus
	CreatedAt     time.Time
}

// Validate checks that the ScheduledTask has valid fields.
func (t *ScheduledTask) Validate() error {
	if t.ID == "" {
		return fmt.Errorf("task ID is required")
	}
	switch t.ScheduleType {
	case ScheduleCron:
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		if _, err := parser.Parse(t.ScheduleValue); err != nil {
			return fmt.Errorf("invalid cron expression %q: %w", t.ScheduleValue, err)
		}
	case ScheduleInterval:
		if d, err := time.ParseDuration(t.ScheduleValue); err != nil || d <= 0 {
			return fmt.Errorf("invalid interval %q", t.ScheduleValue)
		}
	case ScheduleOnce:
		if _, err := time.Parse(time.RFC3339, t.ScheduleValue); err != nil {
			return fmt.Errorf("invalid once schedule %q: %w", t.ScheduleValue, err)
		}
	default:
		return fmt.Errorf("unknown schedule type %q", t.ScheduleType)
	}
	return nil
}

// TaskRunStatus represents the outcome of a task run.
type TaskRunStatus string

const (
	RunSuccess TaskRunStatus = "success"
	RunError   TaskRunStatus = "error"
)

// TaskRunLog records a single task execution.
type TaskRunLog struct {
	ID          int64
	TaskID      string
	GroupFolder string
	RunAt       time.Time
	DurationMs  int
	Status      TaskRunStatus
	Result      *string
	Error       *string
}

// Session stores agent session continuity per group.
type Session struct {
	GroupFolder string
	SessionID   string
}

// AllowlistMode represents the action for an allowlist entry.
type AllowlistMode string

const (
	ModeTrigger AllowlistMode = "trigger"
	ModeDrop    AllowlistMode = "drop"
)

// SenderAllowlistEntry represents a sender allowlist rule.
type SenderAllowlistEntry struct {
	ID           int64
	ChatJID      string
	AllowPattern string
	Mode         AllowlistMode
}

// GroupStore handles group CRUD operations.
type GroupStore interface {
	GetGroup(ctx context.Context, jid string) (*Group, error)
	GetGroupByFolder(ctx context.Context, folder string) (*Group, error)
	ListGroups(ctx context.Context) ([]Group, error)
	UpsertGroup(ctx context.Context, g *Group) error
	DeleteGroup(ctx context.Context, jid string) error
}

// MessageStore handles message CRUD operations.
type MessageStore interface {
	StoreMessage(ctx context.Context, msg *Message) error
	StoreBatch(ctx context.Context, msgs []Message) error
	GetNewMessages(ctx context.Context, jids []string, since time.Time, limit int) ([]Message, error)
	GetMessagesSince(ctx context.Context, chatJID string, since time.Time, limit int) ([]Message, error)
}

// ChatStore handles chat metadata operations.
type ChatStore interface {
	UpsertChat(ctx context.Context, c *Chat) error
	GetChat(ctx context.Context, jid string) (*Chat, error)
	ListChats(ctx context.Context) ([]Chat, error)
}

// TaskStore handles group-scoped scheduled task operations.
// All mutation methods require a groupFolder parameter to enforce isolation.
// This interface is for normal callers — compile-time enforcement prevents unscoped access.
type TaskStore interface {
	CreateTask(ctx context.Context, task *ScheduledTask) error
	GetTask(ctx context.Context, id, groupFolder string) (*ScheduledTask, error)
	ListTasksByGroup(ctx context.Context, groupFolder string) ([]ScheduledTask, error)
	UpdateTask(ctx context.Context, task *ScheduledTask) error
	DeleteTask(ctx context.Context, id, groupFolder string) error
	GetDueTasks(ctx context.Context) ([]ScheduledTask, error)
	LogTaskRun(ctx context.Context, log *TaskRunLog) error
	GetTaskRunLogs(ctx context.Context, taskID, groupFolder string, limit int) ([]TaskRunLog, error)
}

// AdminTaskStore provides unscoped task access for platform operations such as dashboards.
// Only admin APIs and monitoring should receive this interface.
type AdminTaskStore interface {
	ListTasks(ctx context.Context) ([]ScheduledTask, error)
}

// SessionStore handles session persistence.
type SessionStore interface {
	GetSession(ctx context.Context, groupFolder string) (*Session, error)
	UpsertSession(ctx context.Context, s *Session) error
	DeleteSession(ctx context.Context, groupFolder string) error
}

// RouterStateStore handles key-value router state.
type RouterStateStore interface {
	GetState(ctx context.Context, key string) (string, error)
	SetState(ctx context.Context, key, value string) error
}

// AllowlistStore handles sender allowlist operations.
type AllowlistStore interface {
	GetAllowlist(ctx context.Context, chatJID string) ([]SenderAllowlistEntry, error)
	UpsertAllowlistEntry(ctx context.Context, entry *SenderAllowlistEntry) error
	DeleteAllowlistEntry(ctx context.Context, id int64) error
}

// GroupActiveStore tracks which groups currently have a running agent Job.
type GroupActiveStore interface {
	MarkGroupActive(ctx context.Context, jid string) error
	MarkGroupInactive(ctx context.Context, jid string) error
	IsGroupActive(ctx context.Context, jid string) (bool, error)
	ActiveGroupCount(ctx context.Context) (int64, error)
	ActiveGroupJIDs(ctx context.Context) ([]string, error)
}

// Store combines all store interfaces.
type Store interface {
	GroupStore
	MessageStore
	ChatStore
	TaskStore
	AdminTaskStore
	SessionStore
	RouterStateStore
	AllowlistStore
	GroupActiveStore
	Close() error
}

// ContainerConfigJSON is a helper for JSON serialization in MySQL.
func ContainerConfigJSON(cc *ContainerConfig) ([]byte, error) {
	if cc == nil {
		return nil, nil
	}
	return json.Marshal(cc)
}

// ParseContainerConfig parses JSON into ContainerConfig.
func ParseContainerConfig(data []byte) (*ContainerConfig, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var cc ContainerConfig
	if err := json.Unmarshal(data, &cc); err != nil {
		return nil, err
	}
	return &cc, nil
}
