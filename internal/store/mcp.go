package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// MCPServerConfig holds configuration for a single MCP server connection.
type MCPServerConfig struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Transport  string            `json:"transport"` // "stdio" or "http"
	Command    string            `json:"command,omitempty"`
	Args       []string          `json:"args,omitempty"`
	URL        string            `json:"url,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	ToolFilter []string          `json:"tool_filter,omitempty"` // allowed tools (empty = all)
	Enabled    bool              `json:"enabled"`
}

// Validate checks that the config has required fields for its transport type.
func (c *MCPServerConfig) Validate() error {
	if c.ID == "" {
		return fmt.Errorf("mcp server: ID is required")
	}

	if c.Name == "" {
		return fmt.Errorf("mcp server %s: name is required", c.ID)
	}

	switch c.Transport {
	case "stdio":
		if c.Command == "" {
			return fmt.Errorf("mcp server %s: command is required for stdio transport", c.ID)
		}
	case "http":
		if c.URL == "" {
			return fmt.Errorf("mcp server %s: url is required for http transport", c.ID)
		}
	default:
		return fmt.Errorf("mcp server %s: unknown transport %q", c.ID, c.Transport)
	}

	return nil
}

// MarshalJSONForEnv encodes a list of MCP server configs as a JSON string
// suitable for passing as an environment variable.
func MarshalJSONForEnv(servers []MCPServerConfig) (string, error) {
	b, err := json.Marshal(servers)
	if err != nil {
		return "", fmt.Errorf("marshal mcp servers: %w", err)
	}

	return string(b), nil
}

// UnmarshalJSONFromEnv parses MCP server configs from a JSON string.
func UnmarshalJSONFromEnv(data string) ([]MCPServerConfig, error) {
	if data == "" {
		return nil, nil
	}

	var servers []MCPServerConfig

	if err := json.Unmarshal([]byte(data), &servers); err != nil {
		return nil, fmt.Errorf("unmarshal mcp servers: %w", err)
	}

	return servers, nil
}

// MCPStore defines the interface for persisting MCP server configurations.
type MCPStore interface {
	// ListServers returns all MCP servers for a group.
	ListServers(ctx context.Context, groupFolder string) ([]MCPServerConfig, error)
	// CreateServer adds a new MCP server config.
	CreateServer(ctx context.Context, groupFolder string, server MCPServerConfig) error
	// UpdateServer modifies an existing MCP server config.
	UpdateServer(ctx context.Context, groupFolder string, server MCPServerConfig) error
	// DeleteServer removes an MCP server config.
	DeleteServer(ctx context.Context, groupFolder, serverID string) error
}
