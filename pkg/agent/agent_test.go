package agent

import "testing"

func TestLoadConfig_RequiresGroupFolder(t *testing.T) {
	t.Setenv("KRACLAW_GROUP_FOLDER", "")
	t.Setenv("GROUP_FOLDER", "")
	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected error when no group folder set")
	}
}

func TestLoadConfig_PrefersKraclawGroupFolder(t *testing.T) {
	t.Setenv("KRACLAW_GROUP_FOLDER", "kraclaw-group")
	t.Setenv("GROUP_FOLDER", "legacy-group")
	t.Setenv("REDIS_URL", "")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Group != "kraclaw-group" {
		t.Fatalf("expected kraclaw-group, got %q", cfg.Group)
	}
}

func TestLoadConfig_FallsBackToGroupFolder(t *testing.T) {
	t.Setenv("KRACLAW_GROUP_FOLDER", "")
	t.Setenv("GROUP_FOLDER", "legacy-group")
	t.Setenv("REDIS_URL", "")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Group != "legacy-group" {
		t.Fatalf("expected legacy-group, got %q", cfg.Group)
	}
}

func TestLoadConfig_DefaultRedisURL(t *testing.T) {
	t.Setenv("KRACLAW_GROUP_FOLDER", "test")
	t.Setenv("REDIS_URL", "")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RedisURL != "redis://localhost:6379" {
		t.Fatalf("expected default redis URL, got %q", cfg.RedisURL)
	}
}

func TestLoadConfig_AllFields(t *testing.T) {
	t.Setenv("KRACLAW_GROUP_FOLDER", "mygroup")
	t.Setenv("REDIS_URL", "redis://custom:6380")
	t.Setenv("KRACLAW_GROUP", "discord:123")
	t.Setenv("KRACLAW_PROXY_URL", "http://proxy:3001")
	t.Setenv("KRACLAW_PROVIDER", "openai")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RedisURL != "redis://custom:6380" {
		t.Fatalf("expected custom redis URL, got %q", cfg.RedisURL)
	}
	if cfg.GroupJID != "discord:123" {
		t.Fatalf("expected discord:123, got %q", cfg.GroupJID)
	}
	if cfg.ProxyURL != "http://proxy:3001" {
		t.Fatalf("expected proxy URL, got %q", cfg.ProxyURL)
	}
	if cfg.Provider != "openai" {
		t.Fatalf("expected openai, got %q", cfg.Provider)
	}
}
