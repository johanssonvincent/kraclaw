package ipc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
)

// IPCMessageType represents the type of an IPC message.
type IPCMessageType string

const (
	IPCMessageText   IPCMessageType = "message"
	IPCSessionUpdate IPCMessageType = "session_update"
	IPCTaskCreate    IPCMessageType = "task_create"
	IPCTaskUpdate    IPCMessageType = "task_update"
	IPCTaskDelete    IPCMessageType = "task_delete"
	IPCSetModel      IPCMessageType = "set_model"
	IPCShutdown      IPCMessageType = "shutdown"

	// DefaultAgentID is the well-known agent identifier used for the primary
	// agent in each group. All call sites must reference this constant instead
	// of the bare string "main".
	DefaultAgentID = "main"
)

// SanitizeGroupID returns the first 16 bytes of the SHA-256 hex digest of the
// group JID (32 hex characters). Exported so pkg/agent can reuse it without
// duplication.
func SanitizeGroupID(groupJID string) string {
	h := sha256.Sum256([]byte(groupJID))
	return hex.EncodeToString(h[:16])
}

// agentIDUnsafeRe matches any character that is not alphanumeric, dash, or underscore.
// Compiled once at package init for performance.
var agentIDUnsafeRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// SanitizeAgentID replaces any character not in [a-zA-Z0-9_-] with "_" and
// truncates the result to 32 characters. Safe IDs (e.g. "main") are returned
// unchanged. This prevents NATS subject and durable-name injection when an
// agentID contains dots, slashes, spaces, or wildcards.
func SanitizeAgentID(agentID string) string {
	safe := agentIDUnsafeRe.ReplaceAllString(agentID, "_")
	if len(safe) > 32 {
		safe = safe[:32]
	}
	return safe
}

// IPCMessage represents a message exchanged between agent and server.
type IPCMessage struct {
	Group   string          `json:"group"`
	AgentID string          `json:"agent_id"`
	Type    IPCMessageType  `json:"type"`
	Payload json.RawMessage `json:"payload"`
	ID      string          `json:"id"` // Message ID set by broker on receive
}

// IPCBroker defines the interface for IPC communication between server and agents.
type IPCBroker interface {
	// SendInput sends a message from the server to a specific agent in a group.
	SendInput(ctx context.Context, group, agentID string, msg *IPCMessage) error
	// PublishOutput sends a message from an agent to the server.
	PublishOutput(ctx context.Context, group, agentID string, msg *IPCMessage) error
	// SubscribeOutput returns a channel receiving output from all agents in a group (wildcard).
	SubscribeOutput(ctx context.Context, group string) (<-chan *IPCMessage, error)
	// ReadInput returns a channel receiving input messages for a specific agent.
	ReadInput(ctx context.Context, group, agentID string) (<-chan *IPCMessage, error)
	// DeleteStreams removes all IPC data for a group (all agents).
	DeleteStreams(ctx context.Context, group string) error
	Close() error
}
