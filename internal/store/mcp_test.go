package store

import (
	"testing"
)

func TestMCPServerConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  MCPServerConfig
		wantErr bool
	}{
		{
			name: "valid stdio",
			config: MCPServerConfig{
				ID:        "test",
				Name:      "Test Server",
				Transport: "stdio",
				Command:   "/usr/bin/mcp-server",
			},
		},
		{
			name: "valid http",
			config: MCPServerConfig{
				ID:        "test",
				Name:      "Test Server",
				Transport: "http",
				URL:       "http://localhost:8080/mcp",
			},
		},
		{
			name: "missing id",
			config: MCPServerConfig{
				Name:      "Test Server",
				Transport: "stdio",
				Command:   "/usr/bin/mcp-server",
			},
			wantErr: true,
		},
		{
			name: "missing name",
			config: MCPServerConfig{
				ID:        "test",
				Transport: "stdio",
				Command:   "/usr/bin/mcp-server",
			},
			wantErr: true,
		},
		{
			name: "stdio missing command",
			config: MCPServerConfig{
				ID:        "test",
				Name:      "Test Server",
				Transport: "stdio",
			},
			wantErr: true,
		},
		{
			name: "http missing url",
			config: MCPServerConfig{
				ID:        "test",
				Name:      "Test Server",
				Transport: "http",
			},
			wantErr: true,
		},
		{
			name: "unknown transport",
			config: MCPServerConfig{
				ID:        "test",
				Name:      "Test Server",
				Transport: "websocket",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.config.Validate()
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}

			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
