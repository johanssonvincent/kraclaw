package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// HTTPTransport connects to an MCP server via HTTP POST.
type HTTPTransport struct {
	url       string
	headers   map[string]string
	client    *http.Client
	log       Logger
	closeOnce sync.Once
	closed    bool
}

// NewHTTPTransport creates a transport that communicates with an MCP server via HTTP.
func NewHTTPTransport(url string, headers map[string]string, log Logger) *HTTPTransport {
	return &HTTPTransport{
		url:     url,
		headers: headers,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
		log: log,
	}
}

// Start validates the connection to the server.
func (t *HTTPTransport) Start(ctx context.Context) error {
	// Test connectivity with a simple HEAD request.
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, t.url, nil)
	if err != nil {
		return fmt.Errorf("http: create request: %w", err)
	}

	for k, v := range t.headers {
		req.Header.Set(k, v)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("http: connect to %s: %w", t.url, err)
	}

	resp.Body.Close()

	t.log.Debug("http transport started", "url", t.url)

	return nil
}

// Close cleans up the transport.
func (t *HTTPTransport) Close() error {
	t.closeOnce.Do(func() {
		t.closed = true
	})

	return nil
}

// Send is not used for HTTP transport (requests are synchronous).
func (t *HTTPTransport) Send(ctx context.Context, msg json.RawMessage) error {
	return fmt.Errorf("http: send not supported (use call directly)")
}

// Receive is not used for HTTP transport (responses are synchronous).
func (t *HTTPTransport) Receive(ctx context.Context) (json.RawMessage, error) {
	return nil, fmt.Errorf("http: receive not supported")
}

// CallHTTP performs a synchronous HTTP request to the MCP server.
// This is used instead of Send/Receive for HTTP transport.
func (t *HTTPTransport) CallHTTP(ctx context.Context, req jsonrpcRequest) (json.RawMessage, error) {
	if t.closed {
		return nil, fmt.Errorf("http: transport closed")
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("http: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("http: create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	for k, v := range t.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http: do request: %w", err)
	}

	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("http: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http: status %d: %s", resp.StatusCode, string(respBody))
	}

	if !json.Valid(respBody) {
		return nil, fmt.Errorf("http: invalid JSON response: %s", string(respBody))
	}

	return json.RawMessage(respBody), nil
}

// HTTPClient is a specialized MCP client for HTTP transport.
type HTTPClient struct {
	transport *HTTPTransport
	log       Logger

	mu          sync.RWMutex
	initialized bool
	serverName  string
	serverVersion string

	nextID int64
}

// NewHTTPClient creates an MCP client that uses HTTP transport.
func NewHTTPClient(url string, headers map[string]string, log Logger) *HTTPClient {
	return &HTTPClient{
		transport: NewHTTPTransport(url, headers, log),
		log:       log,
	}
}

// Connect initializes the MCP session via HTTP.
func (c *HTTPClient) Connect(ctx context.Context) error {
	if err := c.transport.Start(ctx); err != nil {
		return fmt.Errorf("mcp:http: start transport: %w", err)
	}

	// Send initialize request.
	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      c.nextRequestID(),
		Method:  "initialize",
		Params: mustMarshal(map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "kraclaw-agent",
				"version": "1.0.0",
			},
		}),
	}

	resp, err := c.transport.CallHTTP(ctx, req)
	if err != nil {
		return fmt.Errorf("mcp:http: initialize: %w", err)
	}

	var initResult struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}

	if err := json.Unmarshal(resp, &initResult); err != nil {
		return fmt.Errorf("mcp:http: parse initialize: %w", err)
	}

	c.mu.Lock()
	c.initialized = true
	c.serverName = initResult.ServerInfo.Name
	c.serverVersion = initResult.ServerInfo.Version
	c.mu.Unlock()

	c.log.Info("mcp:http connected",
		"server", initResult.ServerInfo.Name,
		"url", c.transport.url)

	// Send initialized notification.
	notif := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      c.nextRequestID(),
		Method:  "notifications/initialized",
	}

	if _, err := c.transport.CallHTTP(ctx, notif); err != nil {
		c.log.Error("mcp:http: failed to send initialized", "error", err)
	}

	return nil
}

// Disconnect closes the HTTP client.
func (c *HTTPClient) Disconnect() error {
	return c.transport.Close()
}

// ListTools returns all tools exposed by the server.
func (c *HTTPClient) ListTools(ctx context.Context) ([]ToolInfo, error) {
	c.mu.RLock()
	if !c.initialized {
		c.mu.RUnlock()
		return nil, fmt.Errorf("mcp:http: not initialized")
	}
	c.mu.RUnlock()

	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      c.nextRequestID(),
		Method:  "tools/list",
	}

	resp, err := c.transport.CallHTTP(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("mcp:http: list tools: %w", err)
	}

	var result struct {
		Tools []ToolInfo `json:"tools"`
	}

	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("mcp:http: parse tools: %w", err)
	}

	return result.Tools, nil
}

// CallTool invokes a tool on the server.
func (c *HTTPClient) CallTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	c.mu.RLock()
	if !c.initialized {
		c.mu.RUnlock()
		return nil, fmt.Errorf("mcp:http: not initialized")
	}
	c.mu.RUnlock()

	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      c.nextRequestID(),
		Method:  "tools/call",
		Params: mustMarshal(map[string]any{
			"name":      name,
			"arguments": args,
		}),
	}

	resp, err := c.transport.CallHTTP(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("mcp:http: call tool %s: %w", name, err)
	}

	var result ToolResult

	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("mcp:http: parse tool result: %w", err)
	}

	return &result, nil
}

// ListResources returns all resources exposed by the server.
func (c *HTTPClient) ListResources(ctx context.Context) ([]ResourceInfo, error) {
	c.mu.RLock()
	if !c.initialized {
		c.mu.RUnlock()
		return nil, fmt.Errorf("mcp:http: not initialized")
	}
	c.mu.RUnlock()

	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      c.nextRequestID(),
		Method:  "resources/list",
	}

	resp, err := c.transport.CallHTTP(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("mcp:http: list resources: %w", err)
	}

	var result struct {
		Resources []ResourceInfo `json:"resources"`
	}

	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("mcp:http: parse resources: %w", err)
	}

	return result.Resources, nil
}

// ServerInfo returns information about the connected server.
func (c *HTTPClient) ServerInfo() (name, version string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.serverName, c.serverVersion
}

// nextRequestID generates the next request ID.
func (c *HTTPClient) nextRequestID() int64 {
	c.nextID++
	return c.nextID
}
