package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Transport defines the interface for MCP communication transports.
type Transport interface {
	// Start establishes the connection and begins message handling.
	Start(ctx context.Context) error
	// Close terminates the connection.
	Close() error
	// Send writes a JSON-RPC message.
	Send(ctx context.Context, msg json.RawMessage) error
	// Receive returns the next incoming JSON-RPC message.
	Receive(ctx context.Context) (json.RawMessage, error)
}

// ToolInfo describes a callable tool exposed by an MCP server.
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// ToolResult is the response from calling a tool.
type ToolResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ContentBlock represents a piece of tool output.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ResourceInfo describes a resource exposed by an MCP server.
type ResourceInfo struct {
	URI         string `json:"uri"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// Client is an MCP client that connects to a server and can call tools.
type Client struct {
	transport Transport
	log       Logger

	mu            sync.RWMutex
	initialized   bool
	serverName    string
	serverVersion string

	// Pending requests: id -> chan response
	pendingMu sync.Mutex
	pending   map[int64]chan json.RawMessage
	nextID    int64
}

// Logger is the logging interface used by the MCP client.
type Logger interface {
	Info(msg string, fields ...any)
	Error(msg string, fields ...any)
	Debug(msg string, fields ...any)
}

// New creates a new MCP client with the given transport.
func New(transport Transport, log Logger) *Client {
	return &Client{
		transport: transport,
		log:       log,
		pending:   make(map[int64]chan json.RawMessage),
	}
}

// Connect initializes the MCP session by performing the initialize handshake.
func (c *Client) Connect(ctx context.Context) error {
	if err := c.transport.Start(ctx); err != nil {
		return fmt.Errorf("mcp: start transport: %w", err)
	}

	// Start receiving loop in background.
	go c.receiveLoop(ctx)

	// Send initialize request.
	params, err := marshal(map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "kraclaw-agent",
			"version": "1.0.0",
		},
	})
	if err != nil {
		return fmt.Errorf("mcp: marshal initialize params: %w", err)
	}

	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      c.nextRequestID(),
		Method:  "initialize",
		Params:  params,
	}

	resp, err := c.call(ctx, req)
	if err != nil {
		return fmt.Errorf("mcp: initialize: %w", err)
	}

	var initResult struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    any    `json:"capabilities"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}

	if err := json.Unmarshal(resp, &initResult); err != nil {
		return fmt.Errorf("mcp: parse initialize response: %w", err)
	}

	c.mu.Lock()
	c.initialized = true
	c.serverName = initResult.ServerInfo.Name
	c.serverVersion = initResult.ServerInfo.Version
	c.mu.Unlock()

	c.log.Info("mcp connected",
		"server", initResult.ServerInfo.Name,
		"version", initResult.ServerInfo.Version,
		"protocol", initResult.ProtocolVersion)

	// Send initialized notification.
	notif := jsonrpcNotification{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}

	notifBytes, err := marshal(notif)
	if err != nil {
		return fmt.Errorf("mcp: marshal initialized notification: %w", err)
	}

	if err := c.transport.Send(ctx, notifBytes); err != nil {
		c.log.Error("mcp: failed to send initialized notification", "error", err)
	}

	return nil
}

// Disconnect closes the MCP session and transport.
func (c *Client) Disconnect() error {
	c.mu.RLock()
	initialized := c.initialized
	c.mu.RUnlock()

	if initialized {
		// Send close notification (best-effort).
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		notif := jsonrpcNotification{
			JSONRPC: "2.0",
			Method:  "notifications/closed",
		}

		notifBytes, err := marshal(notif)
		if err != nil {
			c.log.Error("mcp: failed to marshal close notification", "error", err)
		} else if err := c.transport.Send(ctx, notifBytes); err != nil {
			c.log.Error("mcp: failed to send close notification", "error", err)
		}
	}

	return c.transport.Close()
}

// ListTools returns all tools exposed by the server.
func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	c.mu.RLock()

	if !c.initialized {
		c.mu.RUnlock()

		return nil, fmt.Errorf("mcp: not initialized")
	}

	c.mu.RUnlock()

	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      c.nextRequestID(),
		Method:  "tools/list",
	}

	resp, err := c.call(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("mcp: list tools: %w", err)
	}

	var result struct {
		Tools []ToolInfo `json:"tools"`
	}

	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("mcp: parse tools list: %w", err)
	}

	return result.Tools, nil
}

// CallTool invokes a tool on the server.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	c.mu.RLock()

	if !c.initialized {
		c.mu.RUnlock()

		return nil, fmt.Errorf("mcp: not initialized")
	}

	c.mu.RUnlock()

	params, err := marshal(map[string]any{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return nil, fmt.Errorf("mcp: marshal tool call params: %w", err)
	}

	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      c.nextRequestID(),
		Method:  "tools/call",
		Params:  params,
	}

	resp, err := c.call(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("mcp: call tool %s: %w", name, err)
	}

	var result ToolResult

	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("mcp: parse tool result: %w", err)
	}

	return &result, nil
}

// ListResources returns all resources exposed by the server.
func (c *Client) ListResources(ctx context.Context) ([]ResourceInfo, error) {
	c.mu.RLock()

	if !c.initialized {
		c.mu.RUnlock()

		return nil, fmt.Errorf("mcp: not initialized")
	}

	c.mu.RUnlock()

	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      c.nextRequestID(),
		Method:  "resources/list",
	}

	resp, err := c.call(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("mcp: list resources: %w", err)
	}

	var result struct {
		Resources []ResourceInfo `json:"resources"`
	}

	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("mcp: parse resources list: %w", err)
	}

	return result.Resources, nil
}

// ServerInfo returns information about the connected server.
func (c *Client) ServerInfo() (name, version string) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.serverName, c.serverVersion
}

// call sends a request and waits for the response.
func (c *Client) call(ctx context.Context, req jsonrpcRequest) (json.RawMessage, error) {
	id := req.ID

	respCh := make(chan json.RawMessage, 1)

	c.pendingMu.Lock()
	c.pending[id] = respCh
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	reqBytes, err := marshal(req)
	if err != nil {
		return nil, fmt.Errorf("mcp: marshal request: %w", err)
	}

	if err := c.transport.Send(ctx, reqBytes); err != nil {
		return nil, fmt.Errorf("mcp: send request: %w", err)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-respCh:
		return resp, nil
	}
}

// receiveLoop handles incoming messages from the transport.
func (c *Client) receiveLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		msg, err := c.transport.Receive(ctx)
		if err != nil {
			c.log.Error("mcp: receive error", "error", err)

			return
		}

		c.handleMessage(msg)
	}
}

// handleMessage routes an incoming JSON-RPC message.
func (c *Client) handleMessage(msg json.RawMessage) {
	// Check if it's a response (has "id").
	var envelope struct {
		ID *int64 `json:"id"`
	}

	if err := json.Unmarshal(msg, &envelope); err != nil {
		c.log.Debug("mcp: failed to parse message envelope", "error", err)

		return
	}

	if envelope.ID != nil {
		// It's a response.
		c.pendingMu.Lock()
		ch, ok := c.pending[*envelope.ID]
		c.pendingMu.Unlock()

		if ok {
			select {
			case ch <- msg:
			default:
				c.log.Debug("mcp: response channel full, dropping", "id", *envelope.ID)
			}
		} else {
			c.log.Debug("mcp: no pending request for response", "id", *envelope.ID)
		}
	}
	// Notifications are ignored for now.
}

// nextRequestID generates the next request ID.
func (c *Client) nextRequestID() int64 {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()

	c.nextID++

	return c.nextID
}

// JSON-RPC types.
type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonrpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

func marshal(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("mcp: marshal: %w", err)
	}

	return b, nil
}
