package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/johanssonvincent/kraclaw/internal/channel"
	"github.com/johanssonvincent/kraclaw/internal/ipc"
	"github.com/johanssonvincent/kraclaw/internal/store"
)

func TestHandleSlashCommand(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		sender     string
		groups     []store.Group
		allowlist  map[string][]store.SenderAllowlistEntry
		wantReturn bool
		wantSent   func([]sentMessage) bool
	}{
		{
			name:    "not a command",
			content: "hello",
			sender:  "alice",
			groups: []store.Group{
				{JID: "group1@g.us", Folder: "group1", Name: "Test"},
			},
			wantReturn: false,
			wantSent:   func(msgs []sentMessage) bool { return len(msgs) == 0 },
		},
		{
			name:    "empty slash",
			content: "/",
			sender:  "alice",
			groups: []store.Group{
				{JID: "group1@g.us", Folder: "group1", Name: "Test"},
			},
			// "/" is parsed as command with empty name, hits default case
			wantReturn: true,
			wantSent: func(msgs []sentMessage) bool {
				return len(msgs) > 0 && strings.Contains(msgs[0].text, "Unknown command")
			},
		},
		{
			name:    "models command",
			content: "/models",
			sender:  "alice",
			groups: []store.Group{
				{JID: "group1@g.us", Folder: "group1", Name: "Test"},
			},
			wantReturn: true,
			wantSent: func(msgs []sentMessage) bool {
				return len(msgs) > 0 && strings.Contains(msgs[0].text, "Models (Anthropic):")
			},
		},
		{
			name:    "model show current",
			content: "/model",
			sender:  "alice",
			groups: []store.Group{
				{JID: "group1@g.us", Folder: "group1", Name: "Test",
					ContainerConfig: &store.ContainerConfig{Model: "claude-sonnet-4-6"}},
			},
			wantReturn: true,
			wantSent: func(msgs []sentMessage) bool {
				return len(msgs) > 0 && strings.Contains(msgs[0].text, "Current model: claude-sonnet-4-6")
			},
		},
		{
			name:    "help command",
			content: "/help",
			sender:  "alice",
			groups: []store.Group{
				{JID: "group1@g.us", Folder: "group1", Name: "Test"},
			},
			wantReturn: true,
			wantSent: func(msgs []sentMessage) bool {
				return len(msgs) > 0 && strings.Contains(msgs[0].text, "Available commands")
			},
		},
		{
			name:    "unknown command",
			content: "/foobar",
			sender:  "alice",
			groups: []store.Group{
				{JID: "group1@g.us", Folder: "group1", Name: "Test"},
			},
			wantReturn: true,
			wantSent: func(msgs []sentMessage) bool {
				return len(msgs) > 0 && strings.Contains(msgs[0].text, "Unknown command: /foobar")
			},
		},
		{
			name:    "unauthorized sender",
			content: "/help",
			sender:  "eve",
			groups: []store.Group{
				{JID: "group1@g.us", Folder: "group1", Name: "Test"},
			},
			allowlist: map[string][]store.SenderAllowlistEntry{
				"group1@g.us": {
					{ChatJID: "group1@g.us", AllowPattern: "alice", Mode: store.ModeTrigger},
				},
			},
			wantReturn: true,
			wantSent: func(msgs []sentMessage) bool {
				return len(msgs) > 0 && strings.Contains(msgs[0].text, "not allowed")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newMockStore()
			s.groups = tt.groups
			if tt.allowlist != nil {
				s.allowlist = tt.allowlist
			}
			ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
			b := &mockIPCBroker{}
			o := newTestOrchestratorWithRouter(s, newMockQueue(), b, []channel.Channel{ch})

			got := o.handleSlashCommand(context.Background(), "group1@g.us", tt.content, tt.sender)
			if got != tt.wantReturn {
				t.Errorf("handleSlashCommand() = %v, want %v", got, tt.wantReturn)
			}
			if !tt.wantSent(ch.sent) {
				var texts []string
				for _, m := range ch.sent {
					texts = append(texts, m.text)
				}
				t.Errorf("sent messages = %v, did not match expectation", texts)
			}
		})
	}
}

func TestHandleModelCommand_SetModel(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		groups    []store.Group
		active    map[string]bool
		wantText  string
		wantIPC   bool
	}{
		{
			name:      "set valid model",
			requested: "claude-sonnet-4-6",
			groups: []store.Group{
				{JID: "group1@g.us", Folder: "group1", Name: "Test"},
			},
			wantText: "Model set to claude-sonnet-4-6",
		},
		{
			name:      "set unknown model",
			requested: "nonexistent",
			groups: []store.Group{
				{JID: "group1@g.us", Folder: "group1", Name: "Test"},
			},
			wantText: `Unknown model "nonexistent"`,
		},
		{
			name:      "set already-current model",
			requested: "claude-sonnet-4-6",
			groups: []store.Group{
				{JID: "group1@g.us", Folder: "group1", Name: "Test",
					ContainerConfig: &store.ContainerConfig{Model: "claude-sonnet-4-6"}},
			},
			wantText: "already set",
		},
		{
			name:      "set model with active sandbox",
			requested: "claude-sonnet-4-6",
			groups: []store.Group{
				{JID: "group1@g.us", Folder: "group1", Name: "Test"},
			},
			active:   map[string]bool{"group1@g.us": true},
			wantText: "Model set to claude-sonnet-4-6",
			wantIPC:  true,
		},
		{
			name:      "group not found",
			requested: "claude-sonnet-4-6",
			groups:    nil,
			wantText:  "Unable to fetch current model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newMockStore()
			s.groups = tt.groups
			q := newMockQueue()
			if tt.active != nil {
				q.active = tt.active
			}
			ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
			b := &mockIPCBroker{}
			o := newTestOrchestratorWithRouter(s, q, b, []channel.Channel{ch})

			// Populate registeredGroups for sendModelUpdateToActive
			for _, g := range tt.groups {
				o.registeredGroups[g.JID] = g
			}

			o.handleModelCommand(context.Background(), "group1@g.us", tt.requested)

			if len(ch.sent) == 0 {
				t.Fatal("expected at least one sent message")
			}
			if !strings.Contains(ch.sent[0].text, tt.wantText) {
				t.Errorf("sent text = %q, want substring %q", ch.sent[0].text, tt.wantText)
			}
			if tt.wantIPC {
				if len(b.inputSent) == 0 {
					t.Error("expected IPC message to active sandbox, got none")
				} else if b.inputSent[0].Type != ipc.IPCSetModel {
					t.Errorf("IPC message type = %q, want %q", b.inputSent[0].Type, ipc.IPCSetModel)
				}
			}
		})
	}
}

func TestHandleModelsCommand_Scenarios(t *testing.T) {
	t.Run("models with current marker", func(t *testing.T) {
		s := newMockStore()
		s.groups = []store.Group{
			{JID: "group1@g.us", Folder: "group1", Name: "Test",
				ContainerConfig: &store.ContainerConfig{Model: "claude-sonnet-4-6"}},
		}
		ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
		o := newTestOrchestratorWithRouter(s, newMockQueue(), &mockIPCBroker{}, []channel.Channel{ch})

		o.handleModelsCommand(context.Background(), "group1@g.us")

		if len(ch.sent) == 0 {
			t.Fatal("expected sent message")
		}
		if !strings.Contains(ch.sent[0].text, "(current)") {
			t.Errorf("sent text = %q, want substring %q", ch.sent[0].text, "(current)")
		}
	})

	t.Run("models with no current shows default", func(t *testing.T) {
		s := newMockStore()
		s.groups = []store.Group{
			{JID: "group1@g.us", Folder: "group1", Name: "Test"},
		}
		ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
		o := newTestOrchestratorWithRouter(s, newMockQueue(), &mockIPCBroker{}, []channel.Channel{ch})

		o.handleModelsCommand(context.Background(), "group1@g.us")

		if len(ch.sent) == 0 {
			t.Fatal("expected sent message")
		}
		if !strings.Contains(ch.sent[0].text, "(default)") {
			t.Errorf("sent text = %q, want substring %q for default indicator", ch.sent[0].text, "(default)")
		}
		if strings.Contains(ch.sent[0].text, "(current)") {
			t.Errorf("sent text = %q, should NOT contain %q when no model set", ch.sent[0].text, "(current)")
		}
	})

	t.Run("models for openai provider", func(t *testing.T) {
		s := newMockStore()
		s.groups = []store.Group{
			{JID: "group1@g.us", Folder: "group1", Name: "Test",
				ContainerConfig: &store.ContainerConfig{Provider: "openai", Model: "gpt-5.4"}},
		}
		ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
		o := newTestOrchestratorWithRouter(s, newMockQueue(), &mockIPCBroker{}, []channel.Channel{ch})

		o.handleModelsCommand(context.Background(), "group1@g.us")

		if len(ch.sent) == 0 {
			t.Fatal("expected sent message")
		}
		if !strings.Contains(ch.sent[0].text, "Models (OpenAI):") {
			t.Errorf("sent text = %q, want substring %q", ch.sent[0].text, "Models (OpenAI):")
		}
		if !strings.Contains(ch.sent[0].text, "gpt-5.4") {
			t.Errorf("sent text = %q, want substring %q", ch.sent[0].text, "gpt-5.4")
		}
	})
}

func TestHandleModelCommand_ProviderRegistryValidation(t *testing.T) {
	s := newMockStore()
	s.groups = []store.Group{{JID: "group1@g.us", Folder: "group1", Name: "Test"}}

	q := newMockQueue()
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	b := &mockIPCBroker{}
	o := newTestOrchestratorWithRouter(s, q, b, []channel.Channel{ch})

	// Provider registry validates against the local model list without
	// calling any upstream API.
	o.handleModelCommand(context.Background(), "group1@g.us", "claude-opus-4-6")

	if len(ch.sent) == 0 {
		t.Fatal("expected sent message")
	}
	if !strings.Contains(ch.sent[0].text, "Model set to claude-opus-4-6") {
		t.Fatalf("sent text = %q, want model set confirmation", ch.sent[0].text)
	}
}

func TestHandleModelCommand_SetModelClearsSessionState(t *testing.T) {
	s := newMockStore()
	s.groups = []store.Group{{
		JID:    "group1@g.us",
		Folder: "group1",
		Name:   "Test",
		ContainerConfig: &store.ContainerConfig{
			Model: "claude-sonnet-4-6",
		},
	}}
	s.sessions["group1"] = &store.Session{GroupFolder: "group1", SessionID: "sess-old"}

	q := newMockQueue()
	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	o := newTestOrchestratorWithRouter(s, q, &mockIPCBroker{}, []channel.Channel{ch})
	o.registeredGroups["group1@g.us"] = s.groups[0]
	o.sessions["group1"] = "sess-old"

	o.handleModelCommand(context.Background(), "group1@g.us", "claude-opus-4-1")

	if len(ch.sent) == 0 {
		t.Fatal("expected sent message")
	}
	if !strings.Contains(ch.sent[0].text, "Model set to claude-opus-4-1") {
		t.Fatalf("sent text = %q, want model set confirmation", ch.sent[0].text)
	}
	if _, ok := o.sessions["group1"]; ok {
		t.Fatal("expected in-memory session to be cleared after model switch")
	}
	if _, ok := s.sessions["group1"]; ok {
		t.Fatal("expected persisted session to be cleared after model switch")
	}
}

func TestHandleModelCommand_SetModelDeleteSessionError(t *testing.T) {
	s := newMockStore()
	s.groups = []store.Group{{
		JID:    "group1@g.us",
		Folder: "group1",
		Name:   "Test",
	}}
	s.sessions["group1"] = &store.Session{GroupFolder: "group1", SessionID: "sess-old"}
	s.deleteSessionErr = errors.New("delete failed")

	ch := &mockChannel{name: "test", connected: true, ownsJIDs: map[string]bool{"group1@g.us": true}}
	o := newTestOrchestratorWithRouter(s, newMockQueue(), &mockIPCBroker{}, []channel.Channel{ch})
	o.registeredGroups["group1@g.us"] = s.groups[0]
	o.sessions["group1"] = "sess-old"

	o.handleModelCommand(context.Background(), "group1@g.us", "claude-opus-4-1")

	if len(ch.sent) == 0 {
		t.Fatal("expected sent message")
	}
	if !strings.Contains(ch.sent[0].text, "Failed to update the model") {
		t.Fatalf("sent text = %q, want failure message", ch.sent[0].text)
	}
	if _, ok := o.sessions["group1"]; !ok {
		t.Fatal("expected in-memory session to remain when delete fails")
	}
	if _, ok := s.sessions["group1"]; !ok {
		t.Fatal("expected persisted session to remain when delete fails")
	}
}
