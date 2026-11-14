package mcp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
)

// testLogger is a simple logger for tests.
type testLogger struct{}

func (l *testLogger) Debug(msg string, args ...any) {}
func (l *testLogger) Info(msg string, args ...any)  {}
func (l *testLogger) Warn(msg string, args ...any)  {}
func (l *testLogger) Error(msg string, args ...any) {}

// TestLiveStdioTransport tests the MCP client against a real Python MCP server
// using stdio transport. Requires python3 with the mcp package installed.
func TestLiveStdioTransport(t *testing.T) {
	// Check if python3 is available.
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	// Create a temporary test server script.
	serverScript := `
import sys
import json
import asyncio

async def main():
    while True:
        line = sys.stdin.readline()
        if not line:
            break
        try:
            msg = json.loads(line)
            if msg.get("method") == "initialize":
                response = {
                    "jsonrpc": "2.0",
                    "id": msg["id"],
                    "result": {
                        "protocolVersion": "2024-11-05",
                        "capabilities": {},
                        "serverInfo": {
                            "name": "test-server",
                            "version": "1.0.0"
                        }
                    }
                }
            elif msg.get("method") == "tools/list":
                response = {
                    "jsonrpc": "2.0",
                    "id": msg["id"],
                    "result": {
                        "tools": [
                            {
                                "name": "echo",
                                "description": "Echo back input",
                                "inputSchema": {
                                    "type": "object",
                                    "properties": {
                                        "text": {"type": "string"}
                                    },
                                    "required": ["text"]
                                }
                            },
                            {
                                "name": "add",
                                "description": "Add two numbers",
                                "inputSchema": {
                                    "type": "object",
                                    "properties": {
                                        "a": {"type": "number"},
                                        "b": {"type": "number"}
                                    },
                                    "required": ["a", "b"]
                                }
                            }
                        ]
                    }
                }
            elif msg.get("method") == "tools/call":
                params = msg.get("params", {})
                tool_name = params.get("name", "")
                args = params.get("arguments", {})
                if tool_name == "echo":
                    result_text = f"Echo: {args.get('text', '')}"
                elif tool_name == "add":
                    result = args.get('a', 0) + args.get('b', 0)
                    result_text = f"Result: {result}"
                else:
                    result_text = f"Unknown tool: {tool_name}"
                response = {
                    "jsonrpc": "2.0",
                    "id": msg["id"],
                    "result": {
                        "content": [
                            {"type": "text", "text": result_text}
                        ],
                        "isError": False
                    }
                }
            else:
                response = {
                    "jsonrpc": "2.0",
                    "id": msg["id"],
                    "result": {}
                }
            sys.stdout.write(json.dumps(response) + "\n")
            sys.stdout.flush()
        except Exception as e:
            sys.stderr.write(f"Error: {e}\n")

if __name__ == "__main__":
    asyncio.run(main())
`

	tmpfile, err := os.CreateTemp("", "mcp_server_*.py")
	if err != nil {
		t.Fatalf("Failed to create temp server script: %v", err)
	}
	defer func() {
		if err := os.Remove(tmpfile.Name()); err != nil {
			t.Logf("Failed to remove temp file: %v", err)
		}
	}()

	if _, err := tmpfile.WriteString(serverScript); err != nil {
		t.Fatalf("Failed to write server script: %v", err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatalf("Failed to close temp file: %v", err)
	}

	ctx := context.Background()

	// Create stdio transport.
	transport := NewStdioTransport("/usr/bin/python3", []string{tmpfile.Name()}, nil, &testLogger{})
	defer func() {
		if err := transport.Close(); err != nil {
			t.Logf("Failed to close transport: %v", err)
		}
	}()

	// Create client.
	client := New(transport, &testLogger{})

	// Connect.
	fmt.Println("Connecting to MCP server...")
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer func() {
		if err := client.Disconnect(); err != nil {
			t.Logf("Failed to disconnect: %v", err)
		}
	}()

	name, version := client.ServerInfo()
	fmt.Printf("Connected to server: '%s' (version: '%s')\n", name, version)

	// List tools.
	fmt.Println("\nListing tools...")
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("Failed to list tools: %v", err)
	}
	fmt.Printf("Found %d tools:\n", len(tools))
	for i, tool := range tools {
		fmt.Printf("  [%d] %s: %s\n", i, tool.Name, tool.Description)
	}
	if len(tools) != 2 {
		t.Errorf("Expected 2 tools, got %d", len(tools))
	}

	// Call echo tool.
	fmt.Println("\nCalling echo tool...")
	result, err := client.CallTool(ctx, "echo", map[string]any{
		"text": "Hello from Kraclaw!",
	})
	if err != nil {
		t.Fatalf("Failed to call echo tool: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("Echo result has no content")
	}
	echoText := result.Content[0].Text
	fmt.Printf("Echo result: %s\n", echoText)
	if echoText != "Echo: Hello from Kraclaw!" {
		t.Errorf("Unexpected echo result: %s", echoText)
	}

	// Call add tool.
	fmt.Println("\nCalling add tool...")
	result, err = client.CallTool(ctx, "add", map[string]any{
		"a": 5,
		"b": 3,
	})
	if err != nil {
		t.Fatalf("Failed to call add tool: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("Add result has no content")
	}
	addText := result.Content[0].Text
	fmt.Printf("Add result: %s\n", addText)
	if addText != "Result: 8" {
		t.Errorf("Unexpected add result: %s", addText)
	}

	fmt.Println("\n✓ All MCP live tests passed!")
}
