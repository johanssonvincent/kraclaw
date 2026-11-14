package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

// mockTransport is a mock transport for testing.
type mockTransport struct {
	mu       sync.Mutex
	messages []json.RawMessage
}

func (m *mockTransport) Start(ctx context.Context) error {
	return nil
}

func (m *mockTransport) Close() error {
	return nil
}

func (m *mockTransport) Send(ctx context.Context, msg json.RawMessage) error {
	// Echo back a response for each request.
	var req struct {
		ID     *int64 `json:"id"`
		Method string `json:"method"`
	}
	if err := json.Unmarshal(msg, &req); err != nil {
		return err
	}

	if req.ID == nil {
		return nil
	}

	var resp json.RawMessage
	switch req.Method {
	case "initialize":
		resp = json.RawMessage(`{"jsonrpc":"2.0","id":` + fmt.Sprint(*req.ID) + `,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"mock-server","version":"1.0.0"}}}`)
	case "tools/list":
		resp = json.RawMessage(`{"jsonrpc":"2.0","id":` + fmt.Sprint(*req.ID) + `,"result":{"tools":[{"name":"test-tool","description":"A test tool","inputSchema":{"type":"object"}}]}}`)
	case "tools/call":
		resp = json.RawMessage(`{"jsonrpc":"2.0","id":` + fmt.Sprint(*req.ID) + `,"result":{"content":[{"type":"text","text":"test result"}],"isError":false}}`)
	default:
		resp = json.RawMessage(`{"jsonrpc":"2.0","id":` + fmt.Sprint(*req.ID) + `,"result":{}}`)
	}

	// Queue the response for Receive.
	m.mu.Lock()
	m.messages = append(m.messages, resp)
	m.mu.Unlock()

	return nil
}

func (m *mockTransport) Receive(ctx context.Context) (json.RawMessage, error) {
	// Wait for a message or context cancellation.
	for {
		m.mu.Lock()
		if len(m.messages) > 0 {
			msg := m.messages[0]
			m.messages = m.messages[1:]
			m.mu.Unlock()
			return msg, nil
		}
		m.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Millisecond):
			// Continue waiting.
		}
	}
}

// TestMockTransport tests the MCP client with a mock transport.
func TestMockTransport(t *testing.T) {
	transport := &mockTransport{}
	client := New(transport, &testLogger{})

	ctx := context.Background()

	// Connect.
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	if err := client.Disconnect(); err != nil {
		t.Logf("Failed to disconnect: %v", err)
	}

	name, version := client.ServerInfo()
	if name != "mock-server" {
		t.Errorf("Expected server name 'mock-server', got '%s'", name)
	}
	if version != "1.0.0" {
		t.Errorf("Expected server version '1.0.0', got '%s'", version)
	}

	// List tools.
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("Failed to list tools: %v", err)
	}
	if len(tools) != 1 {
		t.Errorf("Expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name != "test-tool" {
		t.Errorf("Expected tool name 'test-tool', got '%s'", tools[0].Name)
	}

	// Call tool.
	result, err := client.CallTool(ctx, "test-tool", map[string]any{})
	if err != nil {
		t.Fatalf("Failed to call tool: %v", err)
	}
	if len(result.Content) != 1 {
		t.Errorf("Expected 1 content block, got %d", len(result.Content))
	}
	if result.Content[0].Text != "test result" {
		t.Errorf("Expected content 'test result', got '%s'", result.Content[0].Text)
	}

	t.Log("✓ Mock transport test passed")
}
