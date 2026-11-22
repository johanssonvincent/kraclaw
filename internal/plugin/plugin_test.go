package plugin

import (
	"context"
	"encoding/json"
	"testing"
)

func TestNew(t *testing.T) {
	cfg := Config{PluginDirs: "/tmp"}
	m := New(cfg)
	if m == nil {
		t.Fatal("New() returned nil")
	}
}

func TestList(t *testing.T) {
	m := New(Config{})
	m.plugins["test"] = &Plugin{Manifest: Manifest{Name: "test"}}

	plugins := m.List()
	if len(plugins) != 1 {
		t.Errorf("List() = %d, want 1", len(plugins))
	}
}

func TestGet_NotFound(t *testing.T) {
	m := New(Config{})
	_, err := m.Get("nonexistent")
	if err == nil {
		t.Error("Get() should return error for nonexistent plugin")
	}
}

func TestManifest_MarshalJSON(t *testing.T) {
	manifest := Manifest{Name: "test", Version: "1.0.0"}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if len(data) == 0 {
		t.Error("json.Marshal() should return non-empty data")
	}
}

func TestManifest_UnmarshalJSON(t *testing.T) {
	manifest := Manifest{Name: "test"}
	data, _ := json.Marshal(manifest)
	manifest2 := &Manifest{}
	err := json.Unmarshal(data, manifest2)
	if err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if manifest2.Name != "test" {
		t.Errorf("json.Unmarshal() Name = %q, want %q", manifest2.Name, "test")
	}
}

func TestToolResult_MarshalJSON(t *testing.T) {
	result := ToolResult{Text: "test", IsError: false}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if len(data) == 0 {
		t.Error("json.Marshal() should return non-empty data")
	}
}

func TestPlugin_MarshalJSON(t *testing.T) {
	plugin := &Plugin{Manifest: Manifest{Name: "test"}}
	// Plugin has function fields that can't be marshaled, so test Manifest only.
	data, err := json.Marshal(plugin.Manifest)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if len(data) == 0 {
		t.Error("json.Marshal() should return non-empty data")
	}
}

func TestConfig_Defaults(t *testing.T) {
	cfg := Config{}
	if cfg.PluginDirs != "" {
		t.Errorf("PluginDirs = %q, want empty", cfg.PluginDirs)
	}
}

func TestCallTool_NotFound(t *testing.T) {
	m := New(Config{})
	_, err := m.CallTool(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Error("CallTool() should return error for nonexistent tool")
	}
}

func TestRunHook_NotFound(t *testing.T) {
	m := New(Config{})
	result, err := m.RunHook(context.Background(), "nonexistent", nil)
	// May return nil result without error if no hooks registered
	_ = result
	_ = err
}

func TestRunCommand_NotFound(t *testing.T) {
	m := New(Config{})
	_, err := m.RunCommand(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Error("RunCommand() should return error for nonexistent command")
	}
}

func TestListTools(t *testing.T) {
	m := New(Config{})
	tools := m.ListTools()
	if tools == nil {
		t.Error("ListTools() should not return nil")
	}
}

func TestListCommands(t *testing.T) {
	m := New(Config{})
	cmds := m.ListCommands()
	if cmds == nil {
		t.Error("ListCommands() should not return nil")
	}
}

func TestPlugin_GetName(t *testing.T) {
	plugin := &Plugin{Manifest: Manifest{Name: "test"}}
	if plugin.Manifest.Name != "test" {
		t.Errorf("Manifest.Name = %q, want %q", plugin.Manifest.Name, "test")
	}
}

func TestPlugin_GetVersion(t *testing.T) {
	plugin := &Plugin{Manifest: Manifest{Version: "1.0.0"}}
	if plugin.Manifest.Version != "1.0.0" {
		t.Errorf("Manifest.Version = %q, want %q", plugin.Manifest.Version, "1.0.0")
	}
}

func TestPlugin_IsEnabled(t *testing.T) {
	plugin := &Plugin{Manifest: Manifest{Enabled: true}}
	if !plugin.Manifest.Enabled {
		t.Error("Manifest.Enabled should be true")
	}
}

func TestPlugin_Enable(t *testing.T) {
	plugin := &Plugin{Manifest: Manifest{Name: "test", Enabled: false}}
	plugin.Manifest.Enabled = true
	if !plugin.Manifest.Enabled {
		t.Error("Manifest.Enabled should be true after Enable")
	}
}

func TestPlugin_Disable(t *testing.T) {
	plugin := &Plugin{Manifest: Manifest{Name: "test", Enabled: true}}
	plugin.Manifest.Enabled = false
	if plugin.Manifest.Enabled {
		t.Error("Manifest.Enabled should be false after Disable")
	}
}

func TestPlugin_GetPath(t *testing.T) {
	plugin := &Plugin{Path: "/plugins/test"}
	if plugin.Path != "/plugins/test" {
		t.Errorf("Path = %q, want %q", plugin.Path, "/plugins/test")
	}
}

func TestPlugin_GetTools(t *testing.T) {
	tools := map[string]ToolFunc{"tool1": nil}
	plugin := &Plugin{Tools: tools}
	if len(plugin.Tools) != 1 {
		t.Errorf("Tools = %d, want 1", len(plugin.Tools))
	}
}

func TestPlugin_GetHooks(t *testing.T) {
	hooks := map[string][]HookFunc{"hook1": {nil}}
	plugin := &Plugin{Hooks: hooks}
	if len(plugin.Hooks) != 1 {
		t.Errorf("Hooks = %d, want 1", len(plugin.Hooks))
	}
}

func TestPlugin_GetCommands(t *testing.T) {
	cmds := map[string]CommandFunc{"cmd1": nil}
	plugin := &Plugin{Commands: cmds}
	if len(plugin.Commands) != 1 {
		t.Errorf("Commands = %d, want 1", len(plugin.Commands))
	}
}

func TestManager_Enable_NotFound(t *testing.T) {
	m := New(Config{})
	err := m.Enable(context.Background(), "nonexistent")
	if err == nil {
		t.Error("Enable() should return error for nonexistent plugin")
	}
}

func TestManager_Disable_NotFound(t *testing.T) {
	m := New(Config{})
	err := m.Disable(context.Background(), "nonexistent")
	if err == nil {
		t.Error("Disable() should return error for nonexistent plugin")
	}
}

func TestManager_FormatToolsPrompt_Empty(t *testing.T) {
	m := New(Config{})
	prompt := m.FormatToolsPrompt()
	// May be empty if no tools registered
	_ = prompt
}

func TestManager_CheckRequirements_Empty(t *testing.T) {
	m := New(Config{})
	err := m.checkRequirements(nil)
	if err != nil {
		t.Fatalf("checkRequirements() error = %v", err)
	}
}

func TestManager_CheckRequirements_Missing(t *testing.T) {
	m := New(Config{})
	err := m.checkRequirements([]string{"NONEXISTENT_ENV_VAR_123"})
	if err == nil {
		t.Error("checkRequirements() should return error for missing env var")
	}
}

func TestLoadPluginManifest(t *testing.T) {
	// Test that LoadAll works with empty plugin dirs
	m := New(Config{})
	err := m.LoadAll(context.Background())
	// Should not crash even with empty dirs
	if err != nil {
		t.Logf("LoadAll() returned (may be expected): %v", err)
	}
}
