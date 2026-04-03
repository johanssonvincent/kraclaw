package agent

import "testing"

func TestLoadConfig_RequiresGroup(t *testing.T) {
	t.Setenv("KRACLAW_GROUP", "")
	t.Setenv("GROUP_FOLDER", "some-folder")
	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected error when KRACLAW_GROUP not set")
	}
}

func TestLoadConfig_RequiresGroupFolder(t *testing.T) {
	t.Setenv("KRACLAW_GROUP", "test@g.us")
	t.Setenv("GROUP_FOLDER", "")
	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected error when GROUP_FOLDER not set")
	}
}

func TestLoadConfig_DefaultNATSURL(t *testing.T) {
	t.Setenv("KRACLAW_GROUP", "test@g.us")
	t.Setenv("GROUP_FOLDER", "test-folder")
	t.Setenv("NATS_URL", "")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NATSURL != "nats://localhost:4222" {
		t.Fatalf("expected default NATS URL, got %q", cfg.NATSURL)
	}
}

func TestLoadConfig_DefaultAgentID(t *testing.T) {
	t.Setenv("KRACLAW_GROUP", "test@g.us")
	t.Setenv("GROUP_FOLDER", "test-folder")
	t.Setenv("KRACLAW_AGENT_ID", "")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentID != "main" {
		t.Fatalf("expected default agent ID 'main', got %q", cfg.AgentID)
	}
}

func TestLoadConfig_AllFields(t *testing.T) {
	t.Setenv("KRACLAW_GROUP", "discord:123")
	t.Setenv("GROUP_FOLDER", "mygroup")
	t.Setenv("NATS_URL", "nats://custom:4222")
	t.Setenv("KRACLAW_AGENT_ID", "worker-1")
	t.Setenv("KRACLAW_PROXY_URL", "http://proxy:3001")
	t.Setenv("KRACLAW_PROVIDER", "openai")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NATSURL != "nats://custom:4222" {
		t.Fatalf("expected custom NATS URL, got %q", cfg.NATSURL)
	}
	if cfg.GroupJID != "discord:123" {
		t.Fatalf("expected discord:123, got %q", cfg.GroupJID)
	}
	if cfg.AgentID != "worker-1" {
		t.Fatalf("expected worker-1, got %q", cfg.AgentID)
	}
	if cfg.Group != "mygroup" {
		t.Fatalf("expected mygroup, got %q", cfg.Group)
	}
	if cfg.ProxyURL != "http://proxy:3001" {
		t.Fatalf("expected proxy URL, got %q", cfg.ProxyURL)
	}
	if cfg.Provider != "openai" {
		t.Fatalf("expected openai, got %q", cfg.Provider)
	}
}
