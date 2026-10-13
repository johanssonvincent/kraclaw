package orchestration

import (
	"context"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	cfg := Config{DefaultTimeout: 5 * time.Minute}
	m := New(cfg, nil)
	if m == nil {
		t.Fatal("New() returned nil")
	}
}

func TestCreateWorkflow(t *testing.T) {
	cfg := Config{DefaultTimeout: 5 * time.Minute}
	m := New(cfg, nil)
	ctx := context.Background()

	wf, err := m.CreateWorkflow(ctx, "test-workflow", PatternFanOutFanIn, "test-group", []*Task{
		{Name: "Task 1", Prompt: "Do something"},
		{Name: "Task 2", Prompt: "Do another thing"},
	})
	if err != nil {
		t.Fatalf("CreateWorkflow() error = %v", err)
	}
	if wf.ID == "" {
		t.Error("CreateWorkflow() should generate ID")
	}
	if len(wf.Tasks) != 2 {
		t.Errorf("Tasks = %d, want 2", len(wf.Tasks))
	}
	if wf.Status != WorkflowStatusPending {
		t.Errorf("Status = %v, want %v", wf.Status, WorkflowStatusPending)
	}
}

func TestCreateWorkflow_NoTasks(t *testing.T) {
	cfg := Config{DefaultTimeout: 5 * time.Minute}
	m := New(cfg, nil)
	ctx := context.Background()

	wf, err := m.CreateWorkflow(ctx, "empty", PatternSequentialPipeline, "test-group", nil)
	if err != nil {
		t.Fatalf("CreateWorkflow() error = %v", err)
	}
	if wf == nil {
		t.Error("CreateWorkflow() should succeed with no tasks")
	}
}

func TestGetWorkflow(t *testing.T) {
	cfg := Config{DefaultTimeout: 5 * time.Minute}
	m := New(cfg, nil)
	ctx := context.Background()

	wf, _ := m.CreateWorkflow(ctx, "test", PatternFanOutFanIn, "test-group", nil)

	result, err := m.GetWorkflow(ctx, wf.ID)
	if err != nil {
		t.Fatalf("GetWorkflow() error = %v", err)
	}
	if result.ID != wf.ID {
		t.Error("GetWorkflow() should return correct workflow")
	}
}

func TestGetWorkflow_NotFound(t *testing.T) {
	m := New(Config{}, nil)
	_, err := m.GetWorkflow(context.Background(), "nonexistent")
	if err == nil {
		t.Error("GetWorkflow() should return error for nonexistent workflow")
	}
}

func TestListWorkflows(t *testing.T) {
	cfg := Config{DefaultTimeout: 5 * time.Minute}
	m := New(cfg, nil)
	ctx := context.Background()

	if _, err := m.CreateWorkflow(ctx, "wf1", PatternFanOutFanIn, "group", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CreateWorkflow(ctx, "wf2", PatternSequentialPipeline, "group", nil); err != nil {
		t.Fatal(err)
	}

	workflows := m.ListWorkflows(ctx)
	if len(workflows) != 2 {
		t.Errorf("ListWorkflows() = %d, want 2", len(workflows))
	}
}

func TestCancelWorkflow(t *testing.T) {
	cfg := Config{DefaultTimeout: 5 * time.Minute}
	m := New(cfg, nil)
	ctx := context.Background()

	wf, _ := m.CreateWorkflow(ctx, "test", PatternFanOutFanIn, "group", nil)

	// Set to running.
	m.mu.Lock()
	wf.Status = WorkflowStatusRunning
	m.mu.Unlock()

	err := m.CancelWorkflow(ctx, wf.ID)
	if err != nil {
		t.Fatalf("CancelWorkflow() error = %v", err)
	}

	result, _ := m.GetWorkflow(ctx, wf.ID)
	if result.Status != WorkflowStatusCancelled {
		t.Errorf("Status = %v, want %v", result.Status, WorkflowStatusCancelled)
	}
}

func TestCancelWorkflow_NotFound(t *testing.T) {
	m := New(Config{}, nil)
	err := m.CancelWorkflow(context.Background(), "nonexistent")
	if err == nil {
		t.Error("CancelWorkflow() should return error for nonexistent workflow")
	}
}

func TestRunFanOutFanIn_NoTasks(t *testing.T) {
	cfg := Config{DefaultTimeout: 5 * time.Minute}
	m := New(cfg, nil)
	ctx := context.Background()

	wf, _ := m.CreateWorkflow(ctx, "empty", PatternFanOutFanIn, "group", nil)

	_, err := m.Run(ctx, wf.ID)
	if err == nil {
		t.Error("Run() should return error for no tasks")
	}
}

func TestRunSequentialPipeline_NoTasks(t *testing.T) {
	cfg := Config{DefaultTimeout: 5 * time.Minute}
	m := New(cfg, nil)
	ctx := context.Background()

	wf, _ := m.CreateWorkflow(ctx, "empty", PatternSequentialPipeline, "group", nil)

	_, err := m.Run(ctx, wf.ID)
	if err == nil {
		t.Error("Run() should return error for no tasks")
	}
}

func TestRunSupervisorWorker_NoTasks(t *testing.T) {
	cfg := Config{DefaultTimeout: 5 * time.Minute}
	m := New(cfg, nil)
	ctx := context.Background()

	wf, _ := m.CreateWorkflow(ctx, "empty", PatternSupervisorWorker, "group", nil)

	_, err := m.Run(ctx, wf.ID)
	if err == nil {
		t.Error("Run() should return error for no tasks")
	}
}

func TestRun_NotFound(t *testing.T) {
	m := New(Config{}, nil)
	_, err := m.Run(context.Background(), "nonexistent")
	if err == nil {
		t.Error("Run() should return error for nonexistent workflow")
	}
}

func TestRun_NotPending(t *testing.T) {
	cfg := Config{DefaultTimeout: 5 * time.Minute}
	m := New(cfg, nil)
	ctx := context.Background()

	wf, _ := m.CreateWorkflow(ctx, "test", PatternFanOutFanIn, "group", nil)
	m.mu.Lock()
	wf.Status = WorkflowStatusCompleted
	m.mu.Unlock()

	_, err := m.Run(ctx, wf.ID)
	if err == nil {
		t.Error("Run() should return error for non-pending workflow")
	}
}

func TestFormatWorkflowPrompt(t *testing.T) {
	wf := &Workflow{
		Name: "Test Workflow",
		Tasks: []*Task{
			{Name: "Task 1", Description: "Do something", AgentRole: RoleWorker},
		},
	}

	prompt := FormatWorkflowPrompt(wf)
	if prompt == "" {
		t.Error("FormatWorkflowPrompt() should not be empty")
	}
}

func TestParseTaskAssignments(t *testing.T) {
	jsonInput := `[{"name":"task1","description":"desc","prompt":"do it"}]`

	assignments, err := parseTaskAssignments(jsonInput)
	if err != nil {
		t.Fatalf("parseTaskAssignments() error = %v", err)
	}
	if len(assignments) != 1 {
		t.Errorf("assignments = %d, want 1", len(assignments))
	}
}

func TestParseTaskAssignments_Invalid(t *testing.T) {
	_, err := parseTaskAssignments("not json")
	if err == nil {
		t.Error("parseTaskAssignments() should return error for invalid JSON")
	}
}

func TestStringPtr(t *testing.T) {
	s := "test"
	p := stringPtr(s)
	if *p != s {
		t.Errorf("stringPtr() = %q, want %q", *p, s)
	}
}

func TestWorkflowIndexOfTask(t *testing.T) {
	wf := &Workflow{
		Tasks: []*Task{
			{ID: "task-1"},
			{ID: "task-2"},
		},
	}

	if wf.indexOfTask(&Task{ID: "task-1"}) != 0 {
		t.Error("indexOfTask() should return correct index")
	}
	if wf.indexOfTask(&Task{ID: "task-2"}) != 1 {
		t.Error("indexOfTask() should return correct index")
	}
	if wf.indexOfTask(&Task{ID: "task-3"}) != -1 {
		t.Error("indexOfTask() should return -1 for nonexistent task")
	}
}

func TestTaskStatus_Constants(t *testing.T) {
	if StatusPending != "pending" {
		t.Error("StatusPending should be 'pending'")
	}
	if StatusRunning != "running" {
		t.Error("StatusRunning should be 'running'")
	}
	if StatusCompleted != "completed" {
		t.Error("StatusCompleted should be 'completed'")
	}
	if StatusFailed != "failed" {
		t.Error("StatusFailed should be 'failed'")
	}
	if StatusSkipped != "skipped" {
		t.Error("StatusSkipped should be 'skipped'")
	}
}

func TestWorkflowStatus_Constants(t *testing.T) {
	if WorkflowStatusPending != "pending" {
		t.Error("WorkflowStatusPending should be 'pending'")
	}
	if WorkflowStatusRunning != "running" {
		t.Error("WorkflowStatusRunning should be 'running'")
	}
	if WorkflowStatusCompleted != "completed" {
		t.Error("WorkflowStatusCompleted should be 'completed'")
	}
	if WorkflowStatusFailed != "failed" {
		t.Error("WorkflowStatusFailed should be 'failed'")
	}
	if WorkflowStatusCancelled != "cancelled" {
		t.Error("WorkflowStatusCancelled should be 'cancelled'")
	}
}

func TestPattern_Constants(t *testing.T) {
	if PatternSupervisorWorker != "supervisor_worker" {
		t.Error("PatternSupervisorWorker should be 'supervisor_worker'")
	}
	if PatternFanOutFanIn != "fan_out_fan_in" {
		t.Error("PatternFanOutFanIn should be 'fan_out_fan_in'")
	}
	if PatternSequentialPipeline != "sequential_pipeline" {
		t.Error("PatternSequentialPipeline should be 'sequential_pipeline'")
	}
}

func TestAgentRole_Constants(t *testing.T) {
	if RoleSupervisor != "supervisor" {
		t.Error("RoleSupervisor should be 'supervisor'")
	}
	if RoleWorker != "worker" {
		t.Error("RoleWorker should be 'worker'")
	}
	if RoleSpecialist != "specialist" {
		t.Error("RoleSpecialist should be 'specialist'")
	}
}
