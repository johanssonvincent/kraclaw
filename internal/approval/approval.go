package approval

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Action represents an action that requires approval.
type Action string

const (
	// ActionFileWrite represents a file write operation.
	ActionFileWrite Action = "file_write"

	// ActionFileDelete represents a file deletion operation.
	ActionFileDelete Action = "file_delete"

	// ActionAPIKeyUsage represents an API key usage operation.
	ActionAPIKeyUsage Action = "api_key_usage"

	// ActionNetworkRequest represents a network request operation.
	ActionNetworkRequest Action = "network_request"

	// ActionCodeExecution represents a code execution operation.
	ActionCodeExecution Action = "code_execution"

	// ActionDatabaseWrite represents a database write operation.
	ActionDatabaseWrite Action = "database_write"
)

// Request represents an approval request.
type Request struct {
	ID          string    `json:"id"`
	GroupJID    string    `json:"group_jid"`
	AgentID     string    `json:"agent_id"`
	Action      Action    `json:"action"`
	Description string    `json:"description"`
	Payload     any       `json:"payload,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	Status      Status    `json:"status"`
	ApprovedBy  string    `json:"approved_by,omitempty"`
}

// Status represents the status of an approval request.
type Status string

const (
	// StatusPending means the request is awaiting approval.
	StatusPending Status = "pending"

	// StatusApproved means the request was approved.
	StatusApproved Status = "approved"

	// StatusDenied means the request was denied.
	StatusDenied Status = "denied"

	// StatusExpired means the request expired.
	StatusExpired Status = "expired"
)

// Config holds approval gate configuration.
type Config struct {
	// Enabled controls whether approval gates are active.
	Enabled bool `envconfig:"APPROVAL_ENABLED" default:"false"`

	// Actions lists the actions that require approval.
	Actions []Action `envconfig:"APPROVAL_ACTIONS" default:"file_write,file_delete,code_execution,database_write"`

	// Timeout is the duration after which a pending request expires.
	Timeout time.Duration `envconfig:"APPROVAL_TIMEOUT" default:"5m"`

	// AutoApprovePatterns is a list of regex patterns that auto-approve matching requests.
	AutoApprovePatterns []string `envconfig:"APPROVAL_AUTO_APPROVE_PATTERNS"`

	// RequireExplicitDeny controls whether denied requests should be explicitly
	// communicated back to the agent.
	RequireExplicitDeny bool `envconfig:"APPROVAL_REQUIRE_EXPLICIT_DENY" default:"true"`
}

// Gate manages approval requests.
type Gate struct {
	cfg Config
	mu  sync.RWMutex

	// pending stores pending approval requests.
	pending map[string]*Request

	// history stores completed approval requests.
	history []*Request

	// handlers are callbacks for approval events.
	onApproved func(*Request)
	onDenied   func(*Request)
	onExpired  func(*Request)

	log *slog.Logger
}

// New creates a new approval gate.
func New(cfg Config) *Gate {
	return &Gate{
		cfg:     cfg,
		pending: make(map[string]*Request),
		log:     slog.With("component", "approval"),
	}
}

// Enabled returns whether approval gates are active.
func (g *Gate) Enabled() bool {
	return g.cfg.Enabled
}

// RequiresApproval checks if an action requires approval.
func (g *Gate) RequiresApproval(action Action) bool {
	if !g.cfg.Enabled {
		return false
	}

	for _, a := range g.cfg.Actions {
		if a == action {
			return true
		}
	}

	return false
}

// RequestApproval creates a new approval request and waits for a response.
func (g *Gate) RequestApproval(ctx context.Context, req *Request) (*Request, error) {
	if !g.RequiresApproval(req.Action) {
		return req, nil
	}

	// Generate ID if not provided.
	if req.ID == "" {
		req.ID = fmt.Sprintf("approval-%d-%s", time.Now().UnixNano(), req.Action)
	}

	req.CreatedAt = time.Now()
	req.ExpiresAt = req.CreatedAt.Add(g.cfg.Timeout)
	req.Status = StatusPending

	// Check for auto-approval patterns.
	if g.shouldAutoApprove(req) {
		req.Status = StatusApproved
		req.ApprovedBy = "auto-approval"
		g.mu.Lock()
		g.history = append(g.history, req)
		g.mu.Unlock()

		g.log.Info("approval auto-approved", "id", req.ID, "action", req.Action)
		return req, nil
	}

	// Store pending request.
	g.mu.Lock()
	g.pending[req.ID] = req
	g.mu.Unlock()

	g.log.Info("approval requested", "id", req.ID, "action", req.Action, "description", req.Description)

	// Wait for approval or timeout.
	select {
	case <-ctx.Done():
		g.mu.Lock()
		delete(g.pending, req.ID)
		g.mu.Unlock()

		return nil, fmt.Errorf("approval request cancelled: %w", ctx.Err())

	case result := <-g.waitForApproval(req.ID):
		return result, nil
	}
}

// waitForApproval blocks until the request is approved, denied, or expires.
func (g *Gate) waitForApproval(id string) <-chan *Request {
	ch := make(chan *Request, 1)

	go func() {
		defer close(ch)

		// Start expiry timer.
		var req *Request
		g.mu.RLock()
		if r, ok := g.pending[id]; ok {
			req = r
		}
		g.mu.RUnlock()

		if req == nil {
			return
		}

		timer := time.NewTimer(time.Until(req.ExpiresAt))
		defer timer.Stop()

		for {
			g.mu.RLock()
			r, ok := g.pending[id]
			g.mu.RUnlock()

			if !ok {
				return
			}

			switch r.Status {
			case StatusApproved:
				ch <- r
				return
			case StatusDenied:
				ch <- r
				return
			case StatusExpired:
				ch <- r
				return
			}

			select {
			case <-timer.C:
				g.mu.Lock()
				if r, ok := g.pending[id]; ok {
					r.Status = StatusExpired
					delete(g.pending, id)
					g.history = append(g.history, r)
				}
				g.mu.Unlock()

				if g.onExpired != nil {
					g.onExpired(r)
				}

				g.log.Info("approval expired", "id", id)
				return
			case <-time.After(100 * time.Millisecond):
				// Poll for status change.
			}
		}
	}()

	return ch
}

// Approve approves a pending request.
func (g *Gate) Approve(id string, approvedBy string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	req, ok := g.pending[id]
	if !ok {
		return fmt.Errorf("approval request not found: %s", id)
	}

	req.Status = StatusApproved
	req.ApprovedBy = approvedBy
	delete(g.pending, id)
	g.history = append(g.history, req)

	g.log.Info("approval granted", "id", id, "approved_by", approvedBy)

	if g.onApproved != nil {
		g.onApproved(req)
	}

	return nil
}

// Deny denies a pending request.
func (g *Gate) Deny(id string, approvedBy string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	req, ok := g.pending[id]
	if !ok {
		return fmt.Errorf("approval request not found: %s", id)
	}

	req.Status = StatusDenied
	req.ApprovedBy = approvedBy
	delete(g.pending, id)
	g.history = append(g.history, req)

	g.log.Info("approval denied", "id", id, "approved_by", approvedBy)

	if g.onDenied != nil {
		g.onDenied(req)
	}

	return nil
}

// PendingRequests returns all pending approval requests for a group.
func (g *Gate) PendingRequests(groupJID string) []*Request {
	g.mu.RLock()
	defer g.mu.RUnlock()

	var requests []*Request
	for _, req := range g.pending {
		if req.GroupJID == groupJID {
			requests = append(requests, req)
		}
	}

	return requests
}

// GetRequest returns a request by ID.
func (g *Gate) GetRequest(id string) *Request {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if req, ok := g.pending[id]; ok {
		return req
	}

	for _, req := range g.history {
		if req.ID == id {
			return req
		}
	}

	return nil
}

// shouldAutoApprove checks if a request should be auto-approved based on patterns.
func (g *Gate) shouldAutoApprove(req *Request) bool {
	for _, pattern := range g.cfg.AutoApprovePatterns {
		if strings.Contains(req.Description, pattern) || strings.Contains(string(req.Action), pattern) {
			return true
		}
	}

	return false
}

// SetOnApproved sets the callback for approved requests.
func (g *Gate) SetOnApproved(fn func(*Request)) {
	g.onApproved = fn
}

// SetOnDenied sets the callback for denied requests.
func (g *Gate) SetOnDenied(fn func(*Request)) {
	g.onDenied = fn
}

// SetOnExpired sets the callback for expired requests.
func (g *Gate) SetOnExpired(fn func(*Request)) {
	g.onExpired = fn
}

// IPCMessage represents an IPC message for approval.
type IPCMessage struct {
	Type    string          `json:"type"`
	Request json.RawMessage `json:"request,omitempty"`
	ID      string          `json:"id,omitempty"`
	By      string          `json:"by,omitempty"`
}

// HandleIPCMessage processes an IPC message for approval.
func (g *Gate) HandleIPCMessage(msg *IPCMessage) error {
	switch msg.Type {
	case "approval_request":
		// Forward the request to the user for approval.
		g.log.Info("approval ipc request", "id", msg.ID)
		return nil

	case "approval_approve":
		return g.Approve(msg.ID, msg.By)

	case "approval_deny":
		return g.Deny(msg.ID, msg.By)

	default:
		return fmt.Errorf("unknown approval ipc message type: %s", msg.Type)
	}
}

// FormatApprovalPrompt formats the approval request for display to the user.
func FormatApprovalPrompt(req *Request) string {
	return fmt.Sprintf("⚠️ Approval Required\n\nAction: %s\nDescription: %s\nExpires in: %s\n\nReply with 'approve' or 'deny' to respond.",
		req.Action, req.Description, time.Until(req.ExpiresAt).Round(time.Second))
}
