package subagent

import (
	"context"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	cfg := Config{MaxConcurrent: 5, DefaultTimeout: 5 * time.Minute}
	m := New(cfg, nil)
	if m == nil {
		t.Fatal("New() returned nil")
	}
}

func TestGet_NotFound(t *testing.T) {
	m := New(Config{}, nil)
	_, err := m.Get(context.Background(), "nonexistent")
	if err == nil {
		t.Error("Get() should return error for nonexistent subagent")
	}
}

func TestList(t *testing.T) {
	m := New(Config{}, nil)
	m.agents["test"] = &Subagent{ID: "test", Name: "Test Agent", Status: StatusPending}

	agents := m.List(context.Background())
	if len(agents) != 1 {
		t.Errorf("List() = %d, want 1", len(agents))
	}
}

func TestListByGroup(t *testing.T) {
	m := New(Config{}, nil)
	m.agents["a"] = &Subagent{ID: "a", Name: "A", Group: "group-1", Status: StatusPending}
	m.agents["b"] = &Subagent{ID: "b", Name: "B", Group: "group-2", Status: StatusPending}

	agents := m.ListByGroup(context.Background(), "group-1")
	if len(agents) != 1 {
		t.Errorf("ListByGroup() = %d, want 1", len(agents))
	}
}

func TestCancel_NotFound(t *testing.T) {
	m := New(Config{}, nil)
	err := m.Cancel(context.Background(), "nonexistent")
	if err == nil {
		t.Error("Cancel() should return error for nonexistent subagent")
	}
}

func TestCancel_NotRunning(t *testing.T) {
	m := New(Config{}, nil)
	m.agents["test"] = &Subagent{ID: "test", Status: StatusCompleted}

	err := m.Cancel(context.Background(), "test")
	if err == nil {
		t.Error("Cancel() should return error for non-running subagent")
	}
}

func TestCount(t *testing.T) {
	m := New(Config{}, nil)
	m.agents["a"] = &Subagent{ID: "a", Status: StatusPending}
	m.agents["b"] = &Subagent{ID: "b", Status: StatusRunning}
	m.agents["c"] = &Subagent{ID: "c", Status: StatusCompleted}

	counts := m.Count()
	if counts[StatusPending] != 1 {
		t.Errorf("Count(Pending) = %d, want 1", counts[StatusPending])
	}
	if counts[StatusRunning] != 1 {
		t.Errorf("Count(Running) = %d, want 1", counts[StatusRunning])
	}
	if counts[StatusCompleted] != 1 {
		t.Errorf("Count(Completed) = %d, want 1", counts[StatusCompleted])
	}
}

func TestGetResult(t *testing.T) {
	result := "test result"
	r := &SubagentResult{
		Subagent: &Subagent{ID: "test"},
		Result:   &result,
	}
	if r.GetResult() != "test result" {
		t.Errorf("GetResult() = %q, want %q", r.GetResult(), "test result")
	}

	r2 := &SubagentResult{Subagent: &Subagent{ID: "test"}}
	if r2.GetResult() != "" {
		t.Errorf("GetResult() = %q, want empty", r2.GetResult())
	}
}

func TestFormatResults(t *testing.T) {
	result := "Success"
	results := []*SubagentResult{
		{Subagent: &Subagent{Name: "Agent 1"}, Result: &result},
		{Subagent: &Subagent{Name: "Agent 2"}, Error: testErr{}},
	}

	output := FormatResults(results)
	if output == "" {
		t.Error("FormatResults() should not be empty")
	}
}

func TestStatus_Constants(t *testing.T) {
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
	if StatusCancelled != "cancelled" {
		t.Error("StatusCancelled should be 'cancelled'")
	}
}

type testErr struct{}

func (testErr) Error() string { return "test error" }
