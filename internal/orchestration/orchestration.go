package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Pattern defines the orchestration pattern to use.
type Pattern string

const (
	// PatternSupervisorWorker uses one supervisor agent to delegate to worker agents.
	PatternSupervisorWorker Pattern = "supervisor_worker"

	// PatternFanOutFanIn dispatches tasks in parallel and merges results.
	PatternFanOutFanIn Pattern = "fan_out_fan_in"

	// PatternSequentialPipeline runs agents in a fixed order, each transforming output.
	PatternSequentialPipeline Pattern = "sequential_pipeline"
)

// AgentRole defines the role of an agent in an orchestration.
type AgentRole string

const (
	// RoleSupervisor is the coordinating agent.
	RoleSupervisor AgentRole = "supervisor"

	// RoleWorker is a task-executing agent.
	RoleWorker AgentRole = "worker"

	// RoleSpecialist is a domain-specific agent.
	RoleSpecialist AgentRole = "specialist"
)

// Task represents a unit of work for an agent.
type Task struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Prompt      string     `json:"prompt"`
	AgentID     string     `json:"agent_id,omitempty"`
	AgentRole   AgentRole  `json:"agent_role,omitempty"`
	DependsOn   []string   `json:"depends_on,omitempty"`
	Status      TaskStatus `json:"status"`
	Result      *string    `json:"result,omitempty"`
	Error       *string    `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// TaskStatus represents the status of a task.
type TaskStatus string

const (
	// StatusPending means the task is waiting to be executed.
	StatusPending TaskStatus = "pending"

	// StatusRunning means the task is being executed.
	StatusRunning TaskStatus = "running"

	// StatusCompleted means the task completed successfully.
	StatusCompleted TaskStatus = "completed"

	// StatusFailed means the task failed.
	StatusFailed TaskStatus = "failed"

	// StatusSkipped means the task was skipped (dependency failed).
	StatusSkipped TaskStatus = "skipped"
)

// Workflow represents a multi-agent workflow.
type Workflow struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Pattern     Pattern        `json:"pattern"`
	GroupJID    string         `json:"group_jid"`
	Tasks       []*Task        `json:"tasks"`
	Status      WorkflowStatus `json:"status"`
	CreatedAt   time.Time      `json:"created_at"`
	StartedAt   *time.Time     `json:"started_at,omitempty"`
	CompletedAt *time.Time     `json:"completed_at,omitempty"`
	Error       *string        `json:"error,omitempty"`
	MaxRetries  int            `json:"max_retries,omitempty"`
	Timeout     time.Duration  `json:"timeout,omitempty"`
}

// WorkflowStatus represents the status of a workflow.
type WorkflowStatus string

const (
	// WorkflowStatusPending means the workflow is waiting to start.
	WorkflowStatusPending WorkflowStatus = "pending"

	// WorkflowStatusRunning means the workflow is executing.
	WorkflowStatusRunning WorkflowStatus = "running"

	// WorkflowStatusCompleted means the workflow completed successfully.
	WorkflowStatusCompleted WorkflowStatus = "completed"

	// WorkflowStatusFailed means the workflow failed.
	WorkflowStatusFailed WorkflowStatus = "failed"

	// WorkflowStatusCancelled means the workflow was cancelled.
	WorkflowStatusCancelled WorkflowStatus = "cancelled"
)

// AgentClient is the interface for communicating with agents.
type AgentClient interface {
	// Send sends a message to an agent and returns the response.
	Send(ctx context.Context, agentID string, message string) (string, error)

	// SendWithTimeout sends a message with a timeout.
	SendWithTimeout(ctx context.Context, agentID string, message string, timeout time.Duration) (string, error)
}

// Config holds orchestration configuration.
type Config struct {
	// DefaultTimeout is the default timeout for task execution.
	DefaultTimeout time.Duration `envconfig:"ORCHESTRATION_TASK_TIMEOUT" default:"5m"`

	// MaxRetries is the maximum number of retries per task.
	MaxRetries int `envconfig:"ORCHESTRATION_MAX_RETRIES" default:"2"`

	// ParallelLimit is the maximum number of parallel tasks.
	ParallelLimit int `envconfig:"ORCHESTRATION_PARALLEL_LIMIT" default:"5"`

	// EnableLogging controls whether orchestration events are logged.
	EnableLogging bool `envconfig:"ORCHESTRATION_LOGGING" default:"true"`
}

// Orchestrator manages multi-agent workflows.
type Orchestrator struct {
	cfg       Config
	client    AgentClient
	mu        sync.RWMutex
	workflows map[string]*Workflow
	log       *slog.Logger
}

// New creates a new orchestrator.
func New(cfg Config, client AgentClient) *Orchestrator {
	return &Orchestrator{
		cfg:       cfg,
		client:    client,
		workflows: make(map[string]*Workflow),
		log:       slog.With("component", "orchestration"),
	}
}

// CreateWorkflow creates a new workflow with the specified pattern.
func (o *Orchestrator) CreateWorkflow(ctx context.Context, name string, pattern Pattern, groupJID string, tasks []*Task) (*Workflow, error) {
	w := &Workflow{
		ID:         uuid.New().String(),
		Name:       name,
		Pattern:    pattern,
		GroupJID:   groupJID,
		Tasks:      tasks,
		Status:     WorkflowStatusPending,
		CreatedAt:  time.Now(),
		MaxRetries: o.cfg.MaxRetries,
		Timeout:    o.cfg.DefaultTimeout,
	}

	// Assign IDs to tasks.
	for i, task := range tasks {
		if task.ID == "" {
			task.ID = fmt.Sprintf("%s-task-%d", w.ID, i+1)
		}

		task.Status = StatusPending
		task.CreatedAt = time.Now()
	}

	o.mu.Lock()
	o.workflows[w.ID] = w
	o.mu.Unlock()

	o.log.Info("workflow created", "id", w.ID, "name", name, "pattern", pattern, "tasks", len(tasks))

	return w, nil
}

// Run executes a workflow.
func (o *Orchestrator) Run(ctx context.Context, workflowID string) (*Workflow, error) {
	o.mu.RLock()
	workflow, ok := o.workflows[workflowID]
	o.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("workflow not found: %s", workflowID)
	}

	if workflow.Status != WorkflowStatusPending {
		return nil, fmt.Errorf("workflow %s is not pending (status: %s)", workflowID, workflow.Status)
	}

	// Update status.
	now := time.Now()
	workflow.Status = WorkflowStatusRunning
	workflow.StartedAt = &now

	o.mu.Lock()
	o.workflows[workflowID] = workflow
	o.mu.Unlock()

	o.log.Info("workflow started", "id", workflowID, "pattern", workflow.Pattern)

	// Execute based on pattern.
	var err error

	switch workflow.Pattern {
	case PatternSupervisorWorker:
		err = o.runSupervisorWorker(ctx, workflow)
	case PatternFanOutFanIn:
		err = o.runFanOutFanIn(ctx, workflow)
	case PatternSequentialPipeline:
		err = o.runSequentialPipeline(ctx, workflow)
	default:
		err = fmt.Errorf("unsupported pattern: %s", workflow.Pattern)
	}

	// Update final status.
	o.mu.Lock()
	defer o.mu.Unlock()

	if err != nil {
		workflow.Status = WorkflowStatusFailed
		workflow.Error = stringPtr(err.Error())

		now := time.Now()
		workflow.CompletedAt = &now

		o.log.Error("workflow failed", "id", workflowID, "error", err)
	} else {
		workflow.Status = WorkflowStatusCompleted

		now := time.Now()
		workflow.CompletedAt = &now

		o.log.Info("workflow completed", "id", workflowID)
	}

	return workflow, err
}

// runSupervisorWorker executes a supervisor/worker pattern.
func (o *Orchestrator) runSupervisorWorker(ctx context.Context, workflow *Workflow) error {
	if len(workflow.Tasks) == 0 {
		return fmt.Errorf("no tasks in workflow")
	}

	// First task is the supervisor.
	supervisor := workflow.Tasks[0]
	if supervisor.AgentRole != RoleSupervisor {
		supervisor.AgentRole = RoleSupervisor
	}

	// Execute supervisor to get task decomposition.
	result, err := o.executeTask(ctx, workflow, supervisor)
	if err != nil {
		return fmt.Errorf("supervisor task failed: %w", err)
	}

	// Parse supervisor's task assignments.
	assignments, err := parseTaskAssignments(result)
	if err != nil {
		o.log.Warn("could not parse task assignments, executing remaining tasks sequentially", "error", err)

		// Fall back to executing remaining tasks.
		for _, task := range workflow.Tasks[1:] {
			if _, err := o.executeTask(ctx, workflow, task); err != nil {
				return fmt.Errorf("task %s failed: %w", task.ID, err)
			}
		}

		return nil
	}

	// Execute worker tasks based on assignments.
	for _, assignment := range assignments {
		task := &Task{
			ID:          uuid.New().String(),
			Name:        assignment.Name,
			Description: assignment.Description,
			Prompt:      assignment.Prompt,
			AgentRole:   RoleWorker,
			Status:      StatusPending,
			CreatedAt:   time.Now(),
		}

		workflow.Tasks = append(workflow.Tasks, task)

		if _, err := o.executeTask(ctx, workflow, task); err != nil {
			o.log.Error("worker task failed", "task_id", task.ID, "error", err)
			task.Status = StatusFailed
			task.Error = stringPtr(err.Error())
		}
	}

	return nil
}

// runFanOutFanIn executes a fan-out/fan-in pattern.
func (o *Orchestrator) runFanOutFanIn(ctx context.Context, workflow *Workflow) error {
	if len(workflow.Tasks) == 0 {
		return fmt.Errorf("no tasks in workflow")
	}

	// Limit parallelism.
	sem := make(chan struct{}, o.cfg.ParallelLimit)

	var wg sync.WaitGroup

	var mu sync.Mutex

	var firstErr error

	// Fan out: execute all tasks in parallel.
	for _, task := range workflow.Tasks {
		wg.Add(1)

		go func(t *Task) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			if _, err := o.executeTask(ctx, workflow, t); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}

				t.Status = StatusFailed
				t.Error = stringPtr(err.Error())
				mu.Unlock()
			}
		}(task)
	}

	wg.Wait()

	return firstErr
}

// runSequentialPipeline executes a sequential pipeline pattern.
func (o *Orchestrator) runSequentialPipeline(ctx context.Context, workflow *Workflow) error {
	if len(workflow.Tasks) == 0 {
		return fmt.Errorf("no tasks in workflow")
	}

	var prevResult string

	for _, task := range workflow.Tasks {
		// Inject previous result into prompt.
		if prevResult != "" {
			task.Prompt = fmt.Sprintf("Previous step output:\n%s\n\nYour task:\n%s", prevResult, task.Prompt)
		}

		result, err := o.executeTask(ctx, workflow, task)
		if err != nil {
			// Mark remaining tasks as skipped.
			for _, remaining := range workflow.Tasks[workflow.indexOfTask(task)+1:] {
				remaining.Status = StatusSkipped
			}

			return fmt.Errorf("task %s failed: %w", task.ID, err)
		}

		prevResult = result
	}

	return nil
}

// executeTask executes a single task.
func (o *Orchestrator) executeTask(ctx context.Context, workflow *Workflow, task *Task) (string, error) {
	o.mu.Lock()
	task.Status = StatusRunning
	started := time.Now()
	task.StartedAt = &started
	o.mu.Unlock()

	o.log.Info("task started", "task_id", task.ID, "name", task.Name)

	// Create context with timeout.
	taskCtx, cancel := context.WithTimeout(ctx, workflow.Timeout)
	defer cancel()

	// Send to agent.
	var result string

	var err error

	if task.AgentID != "" {
		result, err = o.client.SendWithTimeout(taskCtx, task.AgentID, task.Prompt, workflow.Timeout)
	} else {
		// Use default agent (main).
		result, err = o.client.SendWithTimeout(taskCtx, "main", task.Prompt, workflow.Timeout)
	}

	if err != nil {
		o.mu.Lock()
		task.Status = StatusFailed
		task.Error = stringPtr(err.Error())
		completed := time.Now()
		task.CompletedAt = &completed
		o.mu.Unlock()

		o.log.Error("task failed", "task_id", task.ID, "error", err)

		return "", err
	}

	o.mu.Lock()
	task.Status = StatusCompleted
	task.Result = &result
	completed := time.Now()
	task.CompletedAt = &completed
	o.mu.Unlock()

	o.log.Info("task completed", "task_id", task.ID, "duration", time.Since(started))

	return result, nil
}

// GetWorkflow returns a workflow by ID.
func (o *Orchestrator) GetWorkflow(ctx context.Context, id string) (*Workflow, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()

	w, ok := o.workflows[id]
	if !ok {
		return nil, fmt.Errorf("workflow not found: %s", id)
	}

	return w, nil
}

// ListWorkflows returns all workflows.
func (o *Orchestrator) ListWorkflows(ctx context.Context) []*Workflow {
	o.mu.RLock()
	defer o.mu.RUnlock()

	workflows := make([]*Workflow, 0, len(o.workflows))
	for _, w := range o.workflows {
		workflows = append(workflows, w)
	}

	return workflows
}

// CancelWorkflow cancels a running workflow.
func (o *Orchestrator) CancelWorkflow(ctx context.Context, id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	w, ok := o.workflows[id]
	if !ok {
		return fmt.Errorf("workflow not found: %s", id)
	}

	if w.Status != WorkflowStatusRunning {
		return fmt.Errorf("workflow %s is not running (status: %s)", id, w.Status)
	}

	w.Status = WorkflowStatusCancelled
	now := time.Now()
	w.CompletedAt = &now

	return nil
}

// taskAssignment represents a task assignment from a supervisor.
type taskAssignment struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
}

// parseTaskAssignments parses task assignments from supervisor output.
func parseTaskAssignments(output string) ([]taskAssignment, error) {
	var assignments []taskAssignment

	// Try JSON first.
	if err := json.Unmarshal([]byte(output), &assignments); err == nil {
		return assignments, nil
	}

	// Try JSON array in markdown block.
	inBlock := false

	var block strings.Builder

	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "```json") || strings.Contains(line, "```") {
			inBlock = !inBlock

			continue
		}

		if inBlock {
			block.WriteString(line)
			block.WriteString("\n")
		}
	}

	if block.Len() > 0 {
		if err := json.Unmarshal([]byte(block.String()), &assignments); err == nil {
			return assignments, nil
		}
	}

	// Return nil to indicate fallback mode.
	return nil, fmt.Errorf("could not parse task assignments as JSON")
}

// stringPtr returns a pointer to a string.
func stringPtr(s string) *string {
	return &s
}

// indexOfTask returns the index of a task in the workflow.
func (w *Workflow) indexOfTask(task *Task) int {
	for i, t := range w.Tasks {
		if t.ID == task.ID {
			return i
		}
	}

	return -1
}

// FormatWorkflowPrompt formats a workflow's context for the supervisor agent.
func FormatWorkflowPrompt(workflow *Workflow) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "You are the supervisor for workflow: %s\n\n", workflow.Name)
	sb.WriteString("Your job is to analyze the task and decompose it into subtasks for worker agents.\n\n")

	sb.WriteString("## Available Tasks\n\n")

	for i, task := range workflow.Tasks {
		if task.AgentRole == RoleSupervisor {
			continue
		}

		fmt.Fprintf(&sb, "%d. %s: %s\n", i, task.Name, task.Description)
	}

	sb.WriteString("\n## Output Format\n\n")
	sb.WriteString("Return a JSON array of task assignments:\n")
	sb.WriteString(`[
  {
    "name": "task name",
    "description": "brief description",
    "prompt": "detailed instructions for the worker agent"
  }
]`)

	return sb.String()
}
