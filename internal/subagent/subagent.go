package subagent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Status represents the status of a subagent.
type Status string

const (
	// StatusPending means the subagent is waiting to start.
	StatusPending Status = "pending"

	// StatusRunning means the subagent is executing.
	StatusRunning Status = "running"

	// StatusCompleted means the subagent completed successfully.
	StatusCompleted Status = "completed"

	// StatusFailed means the subagent failed.
	StatusFailed Status = "failed"

	// StatusCancelled means the subagent was cancelled.
	StatusCancelled Status = "cancelled"
)

// Subagent represents a spawned subagent for parallel work.
type Subagent struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Prompt      string        `json:"prompt"`
	Context     string        `json:"context,omitempty"`
	ParentID    string        `json:"parent_id,omitempty"`
	GroupJID    string        `json:"group_jid"`
	Status      Status        `json:"status"`
	Result      *string       `json:"result,omitempty"`
	Error       *string       `json:"error,omitempty"`
	Group       string        `json:"group,omitempty"` // For grouped completions.
	Timeout     time.Duration `json:"timeout,omitempty"`
	CreatedAt   time.Time     `json:"created_at"`
	StartedAt   *time.Time    `json:"started_at,omitempty"`
	CompletedAt *time.Time    `json:"completed_at,omitempty"`
}

// AgentClient is the interface for spawning and communicating with agents.
type AgentClient interface {
	// Spawn creates a new agent sandbox and returns its ID.
	Spawn(ctx context.Context, groupJID string, prompt string) (string, error)

	// Send sends a message to an agent and returns the response.
	Send(ctx context.Context, agentID string, message string) (string, error)

	// SendWithTimeout sends a message with a timeout.
	SendWithTimeout(ctx context.Context, agentID string, message string, timeout time.Duration) (string, error)

	// Stop stops an agent sandbox.
	Stop(ctx context.Context, agentID string) error
}

// Config holds subagent configuration.
type Config struct {
	// MaxConcurrent is the maximum number of concurrent subagents.
	MaxConcurrent int `envconfig:"SUBAGENT_MAX_CONCURRENT" default:"5"`

	// DefaultTimeout is the default timeout for subagent execution.
	DefaultTimeout time.Duration `envconfig:"SUBAGENT_DEFAULT_TIMEOUT" default:"10m"`

	// EnableLogging controls whether subagent events are logged.
	EnableLogging bool `envconfig:"SUBAGENT_LOGGING" default:"true"`
}

// Manager manages subagent lifecycle.
type Manager struct {
	cfg    Config
	client AgentClient
	mu     sync.RWMutex
	agents map[string]*Subagent
	log    *slog.Logger
}

// New creates a new subagent manager.
func New(cfg Config, client AgentClient) *Manager {
	return &Manager{
		cfg:    cfg,
		client: client,
		agents: make(map[string]*Subagent),
		log:    slog.With("component", "subagent"),
	}
}

// Delegate spawns a subagent to handle a task.
func (m *Manager) Delegate(ctx context.Context, task *DelegationTask) (*Subagent, error) {
	m.mu.Lock()

	// Check concurrency limit.
	running := 0

	for _, a := range m.agents {
		if a.Status == StatusRunning || a.Status == StatusPending {
			running++
		}
	}

	if running >= m.cfg.MaxConcurrent {
		m.mu.Unlock()

		return nil, fmt.Errorf("subagent concurrency limit reached (%d/%d)", running, m.cfg.MaxConcurrent)
	}

	id := uuid.New().String()
	subagent := &Subagent{
		ID:        id,
		Name:      task.Name,
		Prompt:    task.Prompt,
		Context:   task.Context,
		ParentID:  task.ParentID,
		GroupJID:  task.GroupJID,
		Status:    StatusPending,
		Group:     task.Group,
		Timeout:   task.Timeout,
		CreatedAt: time.Now(),
	}

	if subagent.Timeout == 0 {
		subagent.Timeout = m.cfg.DefaultTimeout
	}

	m.agents[id] = subagent
	m.mu.Unlock()

	m.log.Info("subagent created", "id", id, "name", task.Name, "group", task.Group)

	// Spawn and run asynchronously.
	go m.runSubagent(ctx, subagent)

	return subagent, nil
}

// runSubagent spawns and runs a subagent.
func (m *Manager) runSubagent(ctx context.Context, subagent *Subagent) {
	m.mu.Lock()
	subagent.Status = StatusRunning
	started := time.Now()
	subagent.StartedAt = &started
	m.mu.Unlock()

	m.log.Info("subagent started", "id", subagent.ID, "name", subagent.Name)

	// Create context with timeout.
	taskCtx, cancel := context.WithTimeout(ctx, subagent.Timeout)
	defer cancel()

	// Build full prompt with context.
	fullPrompt := subagent.Prompt
	if subagent.Context != "" {
		fullPrompt = fmt.Sprintf("Context:\n%s\n\nYour task:\n%s", subagent.Context, fullPrompt)
	}

	// Spawn agent sandbox.
	agentID, err := m.client.Spawn(taskCtx, subagent.GroupJID, fullPrompt)
	if err != nil {
		m.completeSubagent(subagent, StatusFailed, nil, fmt.Errorf("spawn agent: %w", err))

		return
	}

	defer func() {
		// Clean up agent sandbox.
		if stopErr := m.client.Stop(context.Background(), agentID); stopErr != nil {
			m.log.Warn("failed to stop subagent sandbox", "id", subagent.ID, "error", stopErr)
		}
	}()

	// Wait for result.
	result, err := m.waitForResult(taskCtx, subagent, agentID)
	if err != nil {
		m.completeSubagent(subagent, StatusFailed, nil, err)

		return
	}

	m.completeSubagent(subagent, StatusCompleted, &result, nil)
}

// waitForResult waits for the subagent to produce a result.
func (m *Manager) waitForResult(ctx context.Context, subagent *Subagent, agentID string) (string, error) {
	// Send the task prompt to the agent.
	result, err := m.client.SendWithTimeout(ctx, agentID, subagent.Prompt, subagent.Timeout)
	if err != nil {
		return "", fmt.Errorf("send to agent: %w", err)
	}

	return result, nil
}

// completeSubagent marks a subagent as completed.
func (m *Manager) completeSubagent(subagent *Subagent, status Status, result *string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	subagent.Status = status
	subagent.Result = result

	if err != nil {
		subagent.Error = stringPtr(err.Error())
	}

	completed := time.Now()
	subagent.CompletedAt = &completed

	switch status {
	case StatusCompleted:
		m.log.Info("subagent completed", "id", subagent.ID, "duration", time.Since(*subagent.StartedAt).Round(time.Second))
	case StatusFailed:
		m.log.Error("subagent failed", "id", subagent.ID, "error", err)
	}
}

// DelegateBatch spawns multiple subagents in parallel and collects results.
func (m *Manager) DelegateBatch(ctx context.Context, tasks []*DelegationTask) []*SubagentResult {
	results := make([]*SubagentResult, len(tasks))

	var wg sync.WaitGroup

	var mu sync.Mutex

	sem := make(chan struct{}, m.cfg.MaxConcurrent)

	for i, task := range tasks {
		wg.Add(1)

		go func(idx int, t *DelegationTask) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			subagent, err := m.Delegate(ctx, t)
			if err != nil {
				mu.Lock()
				results[idx] = &SubagentResult{
					Error: err,
				}
				mu.Unlock()

				return
			}

			// Wait for completion.
			result := m.waitForSubagent(ctx, subagent.ID)

			mu.Lock()
			results[idx] = result
			mu.Unlock()
		}(i, task)
	}

	wg.Wait()

	return results
}

// waitForSubagent waits for a subagent to complete.
func (m *Manager) waitForSubagent(ctx context.Context, id string) *SubagentResult {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.mu.RLock()
			agent := m.agents[id]
			m.mu.RUnlock()

			return &SubagentResult{
				Subagent: agent,
				Error:    ctx.Err(),
			}

		case <-ticker.C:
			m.mu.RLock()
			agent, ok := m.agents[id]
			m.mu.RUnlock()

			if !ok {
				return &SubagentResult{
					Error: fmt.Errorf("subagent not found: %s", id),
				}
			}

			switch agent.Status {
			case StatusCompleted:
				return &SubagentResult{
					Subagent: agent,
					Result:   agent.Result,
				}
			case StatusFailed:
				return &SubagentResult{
					Subagent: agent,
					Error:    errorFromString(agent.Error),
				}
			case StatusCancelled:
				return &SubagentResult{
					Subagent: agent,
					Error:    fmt.Errorf("subagent cancelled"),
				}
			}
		}
	}
}

// Get returns a subagent by ID.
func (m *Manager) Get(ctx context.Context, id string) (*Subagent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	a, ok := m.agents[id]
	if !ok {
		return nil, fmt.Errorf("subagent not found: %s", id)
	}

	return a, nil
}

// List returns all subagents.
func (m *Manager) List(ctx context.Context) []*Subagent {
	m.mu.RLock()
	defer m.mu.RUnlock()

	agents := make([]*Subagent, 0, len(m.agents))
	for _, a := range m.agents {
		agents = append(agents, a)
	}

	return agents
}

// ListByGroup returns subagents for a group.
func (m *Manager) ListByGroup(ctx context.Context, group string) []*Subagent {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var agents []*Subagent
	for _, a := range m.agents {
		if a.Group == group {
			agents = append(agents, a)
		}
	}

	return agents
}

// Cancel cancels a running subagent.
func (m *Manager) Cancel(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	a, ok := m.agents[id]
	if !ok {
		return fmt.Errorf("subagent not found: %s", id)
	}

	if a.Status != StatusRunning && a.Status != StatusPending {
		return fmt.Errorf("subagent %s is not running (status: %s)", id, a.Status)
	}

	a.Status = StatusCancelled
	now := time.Now()
	a.CompletedAt = &now

	m.log.Info("subagent cancelled", "id", id)

	return nil
}

// Count returns the number of subagents by status.
func (m *Manager) Count() map[Status]int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	counts := make(map[Status]int)
	for _, a := range m.agents {
		counts[a.Status]++
	}

	return counts
}

// DelegationTask represents a task to delegate to a subagent.
type DelegationTask struct {
	Name     string
	Prompt   string
	Context  string
	GroupJID string
	ParentID string
	Group    string // For grouped completions.
	Timeout  time.Duration
}

// SubagentResult represents the result of a subagent execution.
type SubagentResult struct {
	Subagent *Subagent
	Result   *string
	Error    error
}

// GetResult returns the result text or empty string.
func (r *SubagentResult) GetResult() string {
	if r.Result != nil {
		return *r.Result
	}

	return ""
}

// FormatResults formats multiple subagent results as a readable summary.
func FormatResults(results []*SubagentResult) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "## Subagent Results (%d total)\n\n", len(results))

	success := 0
	failed := 0

	for i, r := range results {
		if r.Error != nil {
			failed++

			fmt.Fprintf(&sb, "%d. ❌ **%s**: %v\n\n", i+1, r.Subagent.Name, r.Error)
		} else {
			success++

			fmt.Fprintf(&sb, "%d. ✅ **%s**\n", i+1, r.Subagent.Name)
			fmt.Fprintf(&sb, "   %s\n\n", r.GetResult())
		}
	}

	fmt.Fprintf(&sb, "\n**Summary**: %d succeeded, %d failed\n", success, failed)

	return sb.String()
}

// stringPtr returns a pointer to a string.
func stringPtr(s string) *string {
	return &s
}

// errorFromString returns an error from a string pointer.
func errorFromString(s *string) error {
	if s == nil {
		return nil
	}

	return fmt.Errorf("%s", *s)
}
