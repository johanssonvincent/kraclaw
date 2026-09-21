package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Manifest describes a plugin.
type Manifest struct {
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Description string            `json:"description,omitempty"`
	Author      string            `json:"author,omitempty"`
	Enabled     bool              `json:"enabled,omitempty"`
	Tools       []ToolManifest    `json:"tools,omitempty"`
	Hooks       []HookManifest    `json:"hooks,omitempty"`
	Commands    []CommandManifest `json:"commands,omitempty"`
	Requires    []string          `json:"requires,omitempty"` // Required env vars.
	Labels      []string          `json:"labels,omitempty"`
}

// ToolManifest describes a tool provided by the plugin.
type ToolManifest struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
}

// HookManifest describes a hook provided by the plugin.
type HookManifest struct {
	Name  string `json:"name"`            // e.g., "on_message", "on_response", "on_tool_call"
	Order int    `json:"order,omitempty"` // Lower runs first.
}

// CommandManifest describes a command provided by the plugin.
type CommandManifest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Plugin represents a loaded plugin.
type Plugin struct {
	Manifest Manifest
	Path     string
	Enabled  bool
	Tools    map[string]ToolFunc
	Hooks    map[string][]HookFunc
	Commands map[string]CommandFunc
	Instance any // Plugin instance for stateful plugins.
}

// ToolFunc is a function that implements a tool.
type ToolFunc func(ctx context.Context, args map[string]any) (*ToolResult, error)

// ToolResult is the result of a tool call.
type ToolResult struct {
	Text    string `json:"text,omitempty"`
	Content any    `json:"content,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
}

// HookFunc is a function that implements a hook.
type HookFunc func(ctx context.Context, data any) (any, error)

// CommandFunc is a function that implements a command.
type CommandFunc func(ctx context.Context, args []string) (string, error)

// Config holds plugin system configuration.
type Config struct {
	// PluginDirs is a comma-separated list of directories to scan for plugins.
	PluginDirs string `envconfig:"PLUGIN_DIRS" default:"/data/plugins"`

	// EnableDynamicLoading controls whether plugins can be loaded at runtime.
	EnableDynamicLoading bool `envconfig:"PLUGIN_DYNAMIC_LOAD" default:"false"`

	// EnableRemotePlugins controls whether plugins can be loaded from remote URLs.
	EnableRemotePlugins bool `envconfig:"PLUGIN_REMOTE" default:"false"`
}

// Manager manages plugin lifecycle.
type Manager struct {
	cfg      Config
	mu       sync.RWMutex
	plugins  map[string]*Plugin
	tools    map[string]*PluginTool    // tool name -> (plugin, func)
	hooks    map[string][]*PluginHook  // hook name -> list
	commands map[string]*PluginCommand // command name -> (plugin, func)
	log      *slog.Logger
}

// PluginTool holds a tool reference.
type PluginTool struct {
	Plugin *Plugin
	Func   ToolFunc
}

// PluginHook holds a hook reference.
type PluginHook struct {
	Plugin *Plugin
	Func   HookFunc
	Order  int
}

// PluginCommand holds a command reference.
type PluginCommand struct {
	Plugin *Plugin
	Func   CommandFunc
}

// New creates a new plugin manager.
func New(cfg Config) *Manager {
	return &Manager{
		cfg:      cfg,
		plugins:  make(map[string]*Plugin),
		tools:    make(map[string]*PluginTool),
		hooks:    make(map[string][]*PluginHook),
		commands: make(map[string]*PluginCommand),
		log:      slog.With("component", "plugin"),
	}
}

// LoadAll loads all plugins from configured directories.
func (m *Manager) LoadAll(ctx context.Context) error {
	dirs := strings.Split(m.cfg.PluginDirs, ",")

	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}

		if err := m.loadDirectory(ctx, dir); err != nil {
			m.log.Error("failed to load plugin directory", "dir", dir, "error", err)
		}
	}

	m.log.Info("plugins loaded", "count", len(m.plugins))

	return nil
}

// loadDirectory loads plugins from a directory.
func (m *Manager) loadDirectory(ctx context.Context, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Directory doesn't exist, that's fine.
		}

		return fmt.Errorf("read plugin directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		pluginPath := filepath.Join(dir, entry.Name())
		if err := m.loadPlugin(ctx, pluginPath); err != nil {
			m.log.Warn("failed to load plugin", "path", pluginPath, "error", err)
		}
	}

	return nil
}

// loadPlugin loads a single plugin from its directory.
func (m *Manager) loadPlugin(ctx context.Context, path string) error {
	manifestPath := filepath.Join(path, "plugin.json")

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read plugin manifest: %w", err)
	}

	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("parse plugin manifest: %w", err)
	}

	if manifest.Name == "" {
		return fmt.Errorf("plugin name is required")
	}

	// Check if already loaded.
	m.mu.Lock()
	if _, exists := m.plugins[manifest.Name]; exists {
		m.mu.Unlock()

		return fmt.Errorf("plugin %q already loaded", manifest.Name)
	}
	m.mu.Unlock()

	// Check required env vars.
	if err := m.checkRequirements(manifest.Requires); err != nil {
		m.log.Warn("plugin requirements not met, skipping", "name", manifest.Name, "error", err)

		return nil
	}

	// Create plugin.
	plugin := &Plugin{
		Manifest: manifest,
		Path:     path,
		Enabled:  true, // Default to enabled.
		Tools:    make(map[string]ToolFunc),
		Hooks:    make(map[string][]HookFunc),
		Commands: make(map[string]CommandFunc),
	}

	// Load plugin code if it exists.
	if err := m.loadPluginCode(plugin); err != nil {
		m.log.Warn("failed to load plugin code", "name", manifest.Name, "error", err)
		// Continue with manifest-only plugin.
	}

	// Register plugin.
	m.mu.Lock()
	m.plugins[manifest.Name] = plugin

	// Register tools.
	for name, fn := range plugin.Tools {
		m.tools[name] = &PluginTool{Plugin: plugin, Func: fn}
	}

	// Register hooks.
	for name, fns := range plugin.Hooks {
		for _, fn := range fns {
			m.hooks[name] = append(m.hooks[name], &PluginHook{
				Plugin: plugin,
				Func:   fn,
				Order:  0,
			})
		}
	}

	// Register commands.
	for name, fn := range plugin.Commands {
		m.commands[name] = &PluginCommand{Plugin: plugin, Func: fn}
	}

	m.mu.Unlock()

	m.log.Info("plugin loaded", "name", manifest.Name, "version", manifest.Version, "tools", len(plugin.Tools), "hooks", len(plugin.Hooks), "commands", len(plugin.Commands))

	return nil
}

// loadPluginCode loads plugin implementation code.
// For now, this is a placeholder — real implementation would use Go plugins or a sandbox.
func (m *Manager) loadPluginCode(plugin *Plugin) error {
	// Check for plugin.go
	codePath := filepath.Join(plugin.Path, "plugin.go")
	if _, err := os.Stat(codePath); os.IsNotExist(err) {
		return nil // No code to load.
	}

	// For now, just log that we found code but can't load it.
	// Real implementation would use:
	// - Go plugins (plugin.Open)
	// - WASM sandbox
	// - Separate process with IPC
	m.log.Debug("plugin code found but not loaded (requires Go plugins or WASM)", "path", codePath)

	return nil
}

// checkRequirements checks if required env vars are set.
func (m *Manager) checkRequirements(requires []string) error {
	for _, env := range requires {
		if os.Getenv(env) == "" {
			return fmt.Errorf("required env var %q not set", env)
		}
	}

	return nil
}

// Get returns a plugin by name.
func (m *Manager) Get(name string) (*Plugin, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	p, ok := m.plugins[name]
	if !ok {
		return nil, fmt.Errorf("plugin not found: %s", name)
	}

	return p, nil
}

// List returns all plugins.
func (m *Manager) List() []*Plugin {
	m.mu.RLock()
	defer m.mu.RUnlock()

	plugins := make([]*Plugin, 0, len(m.plugins))
	for _, p := range m.plugins {
		plugins = append(plugins, p)
	}

	sort.Slice(plugins, func(i, j int) bool {
		return plugins[i].Manifest.Name < plugins[j].Manifest.Name
	})

	return plugins
}

// CallTool calls a tool by name.
func (m *Manager) CallTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	m.mu.RLock()
	pt, ok := m.tools[name]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("tool not found: %s", name)
	}

	if !pt.Plugin.Enabled {
		return nil, fmt.Errorf("plugin %q is disabled", pt.Plugin.Manifest.Name)
	}

	return pt.Func(ctx, args)
}

// RunHook runs all hooks for a given hook name.
func (m *Manager) RunHook(ctx context.Context, name string, data any) (any, error) {
	m.mu.RLock()
	hooks := m.hooks[name]
	m.mu.RUnlock()

	if len(hooks) == 0 {
		return data, nil
	}

	// Sort by order.
	sort.Slice(hooks, func(i, j int) bool {
		return hooks[i].Order < hooks[j].Order
	})

	result := data

	for _, hook := range hooks {
		if !hook.Plugin.Enabled {
			continue
		}

		var err error

		result, err = hook.Func(ctx, result)
		if err != nil {
			m.log.Error("hook failed", "name", name, "plugin", hook.Plugin.Manifest.Name, "error", err)
			// Continue with other hooks.
		}
	}

	return result, nil
}

// RunCommand runs a command by name.
func (m *Manager) RunCommand(ctx context.Context, name string, args []string) (string, error) {
	m.mu.RLock()
	pc, ok := m.commands[name]
	m.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("command not found: %s", name)
	}

	if !pc.Plugin.Enabled {
		return "", fmt.Errorf("plugin %q is disabled", pc.Plugin.Manifest.Name)
	}

	return pc.Func(ctx, args)
}

// ListTools returns all registered tools.
func (m *Manager) ListTools() map[string]*PluginTool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	tools := make(map[string]*PluginTool)
	for name, pt := range m.tools {
		if pt.Plugin.Enabled {
			tools[name] = pt
		}
	}

	return tools
}

// ListCommands returns all registered commands.
func (m *Manager) ListCommands() map[string]*PluginCommand {
	m.mu.RLock()
	defer m.mu.RUnlock()

	commands := make(map[string]*PluginCommand)
	for name, pc := range m.commands {
		if pc.Plugin.Enabled {
			commands[name] = pc
		}
	}

	return commands
}

// Enable enables a plugin.
func (m *Manager) Enable(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	p, ok := m.plugins[name]
	if !ok {
		return fmt.Errorf("plugin not found: %s", name)
	}

	p.Enabled = true
	p.Manifest.Enabled = true

	m.log.Info("plugin enabled", "name", name)

	return nil
}

// Disable disables a plugin.
func (m *Manager) Disable(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	p, ok := m.plugins[name]
	if !ok {
		return fmt.Errorf("plugin not found: %s", name)
	}

	p.Enabled = false
	p.Manifest.Enabled = false

	m.log.Info("plugin disabled", "name", name)

	return nil
}

// FormatToolsPrompt formats all tools for inclusion in a system prompt.
func (m *Manager) FormatToolsPrompt() string {
	tools := m.ListTools()
	if len(tools) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Plugin Tools\n\n")
	sb.WriteString("You have access to the following tools from plugins:\n\n")

	for name, pt := range tools {
		fmt.Fprintf(&sb, "### %s (%s)\n", name, pt.Plugin.Manifest.Name)

		if pt.Plugin.Manifest.Description != "" {
			fmt.Fprintf(&sb, "%s\n\n", pt.Plugin.Manifest.Description)
		}
	}

	return sb.String()
}
