package queue

import (
	"context"
	"fmt"
	"time"
)

// QueueMessage represents a message in the processing queue.
type QueueMessage struct {
	GroupJID  string    `json:"groupJid"`
	Content   string    `json:"content"`
	Sender    string    `json:"sender"`
	Timestamp time.Time `json:"timestamp"`
	IsTask    bool      `json:"isTask,omitempty"`
	TaskID    string    `json:"taskId,omitempty"`
}

// NewQueueMessage creates a QueueMessage, validating that GroupJID is non-empty.
// An empty GroupJID would route to a deterministic-but-wrong stream derived from
// SanitizeGroupID(""), silently processing the wrong group's queue.
func NewQueueMessage(groupJID, content string) (*QueueMessage, error) {
	if groupJID == "" {
		return nil, fmt.Errorf("queue message: GroupJID must not be empty")
	}
	return &QueueMessage{GroupJID: groupJID, Content: content}, nil
}

// groupActiveStore is the subset of the store package needed by NATSQueue for
// active group tracking. Using a local interface avoids an import cycle and
// makes NATSQueue easy to test with a mock.
type groupActiveStore interface {
	MarkGroupActive(ctx context.Context, jid string) error
	MarkGroupInactive(ctx context.Context, jid string) error
	IsGroupActive(ctx context.Context, jid string) (bool, error)
	ActiveGroupCount(ctx context.Context) (int64, error)
	ActiveGroupJIDs(ctx context.Context) ([]string, error)
}

// TODO: consider splitting into MessageQueue (Enqueue/Dequeue/Peek/Len) and
// GroupActivityTracker (MarkActive/MarkInactive/IsActive/ActiveCount/ActiveJIDs)
// to make each responsibility independently mockable and testable.
// Queue defines the interface for the message processing queue.
type Queue interface {
	Enqueue(ctx context.Context, groupJID string, msg *QueueMessage) error
	Dequeue(ctx context.Context, groupJID string) (*QueueMessage, error)
	Peek(ctx context.Context, groupJID string) (*QueueMessage, error)
	Len(ctx context.Context, groupJID string) (int64, error)
	MarkActive(ctx context.Context, groupJID string) error
	MarkInactive(ctx context.Context, groupJID string) error
	IsActive(ctx context.Context, groupJID string) (bool, error)
	ActiveCount(ctx context.Context) (int64, error)
	// ActiveJIDs returns all group JIDs currently in the active set.
	ActiveJIDs(ctx context.Context) ([]string, error)
	Close() error
}
