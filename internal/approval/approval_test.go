package approval

import (
	"context"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	cfg := Config{Enabled: true}
	g := New(cfg)
	if g == nil {
		t.Fatal("New() returned nil")
	}
	if !g.Enabled() {
		t.Error("Enabled() should be true")
	}
}

func TestConfig_Disabled(t *testing.T) {
	cfg := Config{Enabled: false}
	g := New(cfg)
	if g.Enabled() {
		t.Error("Enabled() should be false")
	}
}

func TestRequiresApproval(t *testing.T) {
	tests := []struct {
		name    string
		actions []Action
		action  Action
		want    bool
	}{
		{"file_write enabled", []Action{ActionFileWrite}, ActionFileWrite, true},
		{"code_execution enabled", []Action{ActionCodeExecution}, ActionCodeExecution, true},
		{"unknown action", []Action{ActionFileWrite}, Action("unknown"), false},
		{"empty actions", []Action{}, ActionFileWrite, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Enabled: true, Actions: tt.actions}
			g := New(cfg)
			if got := g.RequiresApproval(tt.action); got != tt.want {
				t.Errorf("RequiresApproval(%v) = %v, want %v", tt.action, got, tt.want)
			}
		})
	}
}

func TestRequiresApproval_Disabled(t *testing.T) {
	cfg := Config{Enabled: false, Actions: []Action{ActionFileWrite}}
	g := New(cfg)
	if g.RequiresApproval(ActionFileWrite) {
		t.Error("RequiresApproval should be false when disabled")
	}
}

func TestRequestApproval_NoApprovalNeeded(t *testing.T) {
	cfg := Config{Enabled: false}
	g := New(cfg)
	req := &Request{Action: ActionFileWrite}
	result, err := g.RequestApproval(context.Background(), req)
	if err != nil {
		t.Errorf("RequestApproval() error = %v", err)
	}
	if result == nil {
		t.Error("RequestApproval() should return request when no approval needed")
	}
}

func TestRequestApproval_ContextCancel(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Actions: []Action{ActionFileWrite},
		Timeout: 10 * time.Second,
	}
	g := New(cfg)
	req := &Request{
		Action:      ActionFileWrite,
		Description: "test write",
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err := g.RequestApproval(ctx, req)
	if err == nil {
		t.Error("RequestApproval() should return error on cancelled context")
	}
}

func TestRequestApproval_AutoApprove(t *testing.T) {
	cfg := Config{
		Enabled:             true,
		Actions:             []Action{ActionFileWrite},
		AutoApprovePatterns: []string{"test"},
	}
	g := New(cfg)
	req := &Request{
		Action:      ActionFileWrite,
		Description: "test file write",
	}

	result, err := g.RequestApproval(context.Background(), req)
	if err != nil {
		t.Errorf("RequestApproval() error = %v", err)
	}
	if result.Status != StatusApproved {
		t.Errorf("Status = %v, want %v", result.Status, StatusApproved)
	}
	if result.ApprovedBy != "auto-approval" {
		t.Errorf("ApprovedBy = %v, want 'auto-approval'", result.ApprovedBy)
	}
}

func TestApprove(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Actions: []Action{ActionFileWrite},
		Timeout: 10 * time.Second,
	}
	g := New(cfg)
	req := &Request{
		ID:          "test-1",
		Action:      ActionFileWrite,
		Description: "test",
	}

	// Store request manually for test.
	g.mu.Lock()
	g.pending[req.ID] = req
	g.mu.Unlock()

	err := g.Approve(req.ID, "tester")
	if err != nil {
		t.Errorf("Approve() error = %v", err)
	}

	result := g.GetRequest(req.ID)
	if result == nil {
		t.Fatal("GetRequest() returned nil after approve")
	}
	if result.Status != StatusApproved {
		t.Errorf("Status = %v, want %v", result.Status, StatusApproved)
	}
}

func TestDeny(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Actions: []Action{ActionFileWrite},
		Timeout: 10 * time.Second,
	}
	g := New(cfg)
	req := &Request{
		ID:          "test-2",
		Action:      ActionFileWrite,
		Description: "test",
	}

	g.mu.Lock()
	g.pending[req.ID] = req
	g.mu.Unlock()

	err := g.Deny(req.ID, "tester")
	if err != nil {
		t.Errorf("Deny() error = %v", err)
	}

	result := g.GetRequest(req.ID)
	if result == nil {
		t.Fatal("GetRequest() returned nil after deny")
	}
	if result.Status != StatusDenied {
		t.Errorf("Status = %v, want %v", result.Status, StatusDenied)
	}
}

func TestApprove_NotFound(t *testing.T) {
	g := New(Config{})
	err := g.Approve("nonexistent", "tester")
	if err == nil {
		t.Error("Approve() should return error for nonexistent request")
	}
}

func TestDeny_NotFound(t *testing.T) {
	g := New(Config{})
	err := g.Deny("nonexistent", "tester")
	if err == nil {
		t.Error("Deny() should return error for nonexistent request")
	}
}

func TestPendingRequests(t *testing.T) {
	cfg := Config{Enabled: true, Actions: []Action{ActionFileWrite}}
	g := New(cfg)

	g.mu.Lock()
	g.pending["req-1"] = &Request{ID: "req-1", GroupJID: "group-a", Action: ActionFileWrite}
	g.pending["req-2"] = &Request{ID: "req-2", GroupJID: "group-b", Action: ActionFileWrite}
	g.mu.Unlock()

	requests := g.PendingRequests("group-a")
	if len(requests) != 1 {
		t.Errorf("PendingRequests() = %d, want 1", len(requests))
	}
}

func TestGetRequest(t *testing.T) {
	g := New(Config{})

	// Test not found.
	result := g.GetRequest("nonexistent")
	if result != nil {
		t.Error("GetRequest() should return nil for nonexistent request")
	}

	// Test found in pending.
	g.mu.Lock()
	g.pending["test"] = &Request{ID: "test", Action: ActionFileWrite}
	g.mu.Unlock()

	result = g.GetRequest("test")
	if result == nil {
		t.Error("GetRequest() should return request from pending")
	}
}

func TestFormatApprovalPrompt(t *testing.T) {
	req := &Request{
		Action:      ActionFileWrite,
		Description: "Write to /etc/passwd",
		ExpiresAt:   time.Now().Add(5 * time.Minute),
	}

	prompt := FormatApprovalPrompt(req)
	if prompt == "" {
		t.Error("FormatApprovalPrompt() should not be empty")
	}
	if !contains(prompt, "file_write") {
		t.Error("Prompt should contain action")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && search(s, substr))
}

func search(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
