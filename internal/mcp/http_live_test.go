package mcp

import (
	"context"
	"fmt"
	"testing"
)

// TestLiveHttpTransport tests the MCP client against a real HTTP MCP server.
// Requires a running MCP server at http://127.0.0.1:8766/.
func TestLiveHttpTransport(t *testing.T) {
	// Create HTTP client.
	mcpClient := NewHTTPClient("http://127.0.0.1:8766/", nil, &testLogger{})

	ctx := context.Background()

	// Connect.
	fmt.Println("Connecting to HTTP MCP server...")
	if err := mcpClient.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer func() {
		if err := mcpClient.Disconnect(); err != nil {
			t.Logf("Failed to disconnect: %v", err)
		}
	}()

	name, version := mcpClient.ServerInfo()
	fmt.Printf("Connected to server: %s (version: %s)\n", name, version)

	// List tools.
	fmt.Println("\nListing tools...")
	tools, err := mcpClient.ListTools(ctx)
	if err != nil {
		t.Fatalf("Failed to list tools: %v", err)
	}
	fmt.Printf("Found %d tools:\n", len(tools))
	for _, tool := range tools {
		fmt.Printf("  - %s: %s\n", tool.Name, tool.Description)
	}

	// Call echo tool.
	fmt.Println("\nCalling echo tool...")
	result, err := mcpClient.CallTool(ctx, "echo", map[string]any{
		"text": "Hello from HTTP!",
	})
	if err != nil {
		t.Fatalf("Failed to call echo tool: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("Echo result has no content")
	}
	echoText := result.Content[0].Text
	fmt.Printf("Echo result: %s\n", echoText)
	if echoText != "Echo: Hello from HTTP!" {
		t.Errorf("Unexpected echo result: %s", echoText)
	}

	// Call add tool.
	fmt.Println("\nCalling add tool...")
	result, err = mcpClient.CallTool(ctx, "add", map[string]any{
		"a": 10,
		"b": 20,
	})
	if err != nil {
		t.Fatalf("Failed to call add tool: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("Add result has no content")
	}
	addText := result.Content[0].Text
	fmt.Printf("Add result: %s\n", addText)
	if addText != "Result: 30" {
		t.Errorf("Unexpected add result: %s", addText)
	}

	fmt.Println("\n✓ All MCP HTTP live tests passed!")
}
