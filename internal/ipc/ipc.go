package ipc

import (
	"context"
	"encoding/json"
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
)

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
