package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Config holds ACP configuration.
type Config struct {
	// Enabled controls whether ACP is active.
	Enabled bool `envconfig:"ACP_ENABLED" default:"false"`

	// ListenAddr is the address to listen on.
	ListenAddr string `envconfig:"ACP_LISTEN_ADDR" default:"127.0.0.1:6118"`

	// WorkspacePath is the workspace directory.
	WorkspacePath string `envconfig:"ACP_WORKSPACE_PATH"`

	// EnableFileOperations controls whether file operations are allowed.
	EnableFileOperations bool `envconfig:"ACP_FILE_OPS" default:"true"`

	// EnableTerminal controls whether terminal operations are allowed.
	EnableTerminal bool `envconfig:"ACP_TERMINAL" default:"true"`

	// MaxFileSize is the maximum file size for read operations.
	MaxFileSize int64 `envconfig:"ACP_MAX_FILE_SIZE" default:"1048576"` // 1MB
}

// Server is the ACP server.
type Server struct {
	cfg      Config
	httpSrv  *http.Server
	listener net.Listener
	mu       sync.RWMutex
	sessions map[string]*Session
	log      *slog.Logger
}

// Session represents an ACP client session.
type Session struct {
	ID            string    `json:"id"`
	ClientName    string    `json:"client_name"`
	ClientVersion string    `json:"client_version,omitempty"`
	Workspace     string    `json:"workspace,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	LastActive    time.Time `json:"last_active"`
}

// New creates a new ACP server.
func New(cfg Config) *Server {
	return &Server{
		cfg:      cfg,
		sessions: make(map[string]*Session),
		log:      slog.With("component", "acp"),
	}
}

// Start starts the ACP server.
func (s *Server) Start(ctx context.Context) error {
	if !s.cfg.Enabled {
		s.log.Info("ACP server disabled")

		return nil
	}

	listener, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("acp: listen on %s: %w", s.cfg.ListenAddr, err)
	}

	s.listener = listener

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/sessions", s.handleSessions)
	mux.HandleFunc("/files/read", s.handleFilesRead)
	mux.HandleFunc("/files/write", s.handleFilesWrite)
	mux.HandleFunc("/files/list", s.handleFilesList)
	mux.HandleFunc("/files/search", s.handleFilesSearch)
	mux.HandleFunc("/terminal", s.handleTerminal)
	mux.HandleFunc("/chat", s.handleChat)

	s.httpSrv = &http.Server{
		Handler:           s.corsMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	s.log.Info("ACP server starting", "addr", s.cfg.ListenAddr)

	go func() {
		<-ctx.Done()

		if err := s.Stop(context.Background()); err != nil {
			s.log.Error("ACP server shutdown error", "error", err)
		}
	}()

	if err := s.httpSrv.Serve(listener); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("acp: serve error: %w", err)
	}

	return nil
}

// Stop stops the ACP server.
func (s *Server) Stop(ctx context.Context) error {
	if s.httpSrv == nil {
		return nil
	}

	s.log.Info("ACP server stopping")

	return s.httpSrv.Shutdown(ctx)
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	resp := map[string]any{
		"name":    "Kraclaw ACP Server",
		"version": "1.0.0",
		"status":  "running",
	}

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.log.Error("failed to encode response", "error", err)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.createSession(w, r)
	case http.MethodGet:
		s.listSessions(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClientName    string `json:"client_name"`
		ClientVersion string `json:"client_version"`
		Workspace     string `json:"workspace"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)

		return
	}

	if req.ClientName == "" {
		req.ClientName = "unknown"
	}

	session := &Session{
		ID:            uuid.New().String(),
		ClientName:    req.ClientName,
		ClientVersion: req.ClientVersion,
		Workspace:     req.Workspace,
		CreatedAt:     time.Now(),
		LastActive:    time.Now(),
	}

	s.mu.Lock()
	s.sessions[session.ID] = session
	s.mu.Unlock()

	s.log.Info("ACP session created", "id", session.ID, "client", req.ClientName)

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(map[string]any{
		"session_id": session.ID,
	}); err != nil {
		s.log.Error("failed to encode response", "error", err)
	}
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	sessions := make([]*Session, 0, len(s.sessions))

	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}

	s.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(map[string]any{
		"sessions": sessions,
	}); err != nil {
		s.log.Error("failed to encode response", "error", err)
	}
}

func (s *Server) handleFilesRead(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.EnableFileOperations {
		http.Error(w, "File operations disabled", http.StatusForbidden)

		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	var req struct {
		Path string `json:"path"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)

		return
	}

	if req.Path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)

		return
	}

	// Read file.
	data, err := readFile(req.Path, s.cfg.MaxFileSize)
	if err != nil {
		http.Error(w, fmt.Sprintf("read file: %v", err), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(map[string]any{
		"path":    req.Path,
		"content": string(data),
		"size":    len(data),
	}); err != nil {
		s.log.Error("failed to encode response", "error", err)
	}
}

func (s *Server) handleFilesWrite(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.EnableFileOperations {
		http.Error(w, "File operations disabled", http.StatusForbidden)

		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)

		return
	}

	if req.Path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)

		return
	}

	// Write file.
	if err := writeFile(req.Path, req.Content); err != nil {
		http.Error(w, fmt.Sprintf("write file: %v", err), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(map[string]any{
		"path":   req.Path,
		"status": "ok",
	}); err != nil {
		s.log.Error("failed to encode response", "error", err)
	}
}

func (s *Server) handleFilesList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	var req struct {
		Path string `json:"path"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)

		return
	}

	if req.Path == "" {
		req.Path = "."
	}

	entries, err := listDirectory(req.Path)
	if err != nil {
		http.Error(w, fmt.Sprintf("list directory: %v", err), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(map[string]any{
		"path":    req.Path,
		"entries": entries,
	}); err != nil {
		s.log.Error("failed to encode response", "error", err)
	}
}

func (s *Server) handleFilesSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	var req struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Limit   int    `json:"limit"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)

		return
	}

	if req.Pattern == "" {
		http.Error(w, "pattern is required", http.StatusBadRequest)

		return
	}

	if req.Path == "" {
		req.Path = "."
	}

	if req.Limit <= 0 {
		req.Limit = 50
	}

	results, err := searchFiles(req.Path, req.Pattern, req.Limit)
	if err != nil {
		http.Error(w, fmt.Sprintf("search files: %v", err), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(map[string]any{
		"pattern": req.Pattern,
		"results": results,
	}); err != nil {
		s.log.Error("failed to encode response", "error", err)
	}
}

func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.EnableTerminal {
		http.Error(w, "Terminal operations disabled", http.StatusForbidden)

		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	var req struct {
		Command string `json:"command"`
		Workdir string `json:"workdir"`
		Timeout int    `json:"timeout"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)

		return
	}

	if req.Command == "" {
		http.Error(w, "command is required", http.StatusBadRequest)

		return
	}

	if req.Timeout <= 0 {
		req.Timeout = 30
	}

	output, err := runCommand(req.Command, req.Workdir, req.Timeout)
	if err != nil {
		http.Error(w, fmt.Sprintf("run command: %v", err), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(map[string]any{
		"command": req.Command,
		"output":  output,
	}); err != nil {
		s.log.Error("failed to encode response", "error", err)
	}
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	var req struct {
		Message   string `json:"message"`
		Workspace string `json:"workspace"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)

		return
	}

	if req.Message == "" {
		http.Error(w, "message is required", http.StatusBadRequest)

		return
	}

	// Forward to Kraclaw agent via IPC or direct call.
	// For now, return a placeholder response.
	response := fmt.Sprintf("Received: %s\n\n(ACP chat integration pending — wire to Kraclaw agent IPC)", req.Message)

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(map[string]any{
		"response": response,
	}); err != nil {
		s.log.Error("failed to encode response", "error", err)
	}
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)

			return
		}

		next.ServeHTTP(w, r)
	})
}

// readFile reads a file with size limit.
func readFile(path string, maxSize int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	if info.Size() > maxSize {
		return nil, fmt.Errorf("file too large (%d > %d bytes)", info.Size(), maxSize)
	}

	return os.ReadFile(path)
}

// writeFile writes content to a file.
func writeFile(path string, content string) error {
	dir := strings.TrimSuffix(path, path[strings.LastIndex(path, "/"):])
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create directory: %w", err)
		}
	}

	return os.WriteFile(path, []byte(content), 0o644)
}

// listDirectory lists directory entries.
func listDirectory(path string) ([]map[string]any, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	result := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		result = append(result, map[string]any{
			"name":     entry.Name(),
			"is_dir":   entry.IsDir(),
			"size":     info.Size(),
			"modified": info.ModTime().Format(time.RFC3339),
		})
	}

	return result, nil
}

// searchFiles searches for files matching a pattern.
func searchFiles(path string, pattern string, limit int) ([]string, error) {
	var results []string

	err := filepathWalk(path, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors.
		}

		if len(results) >= limit {
			return fmt.Errorf("limit reached")
		}

		if !info.IsDir() && strings.Contains(strings.ToLower(filePath), strings.ToLower(pattern)) {
			results = append(results, filePath)
		}

		return nil
	})

	if err != nil && err.Error() != "limit reached" {
		return nil, err
	}

	return results, nil
}

// filepathWalk is a simple filepath walker.
func filepathWalk(root string, fn func(path string, info os.FileInfo, err error) error) error {
	info, err := os.Stat(root)
	if err != nil {
		return fn(root, nil, err)
	}

	if err := fn(root, info, nil); err != nil {
		return err
	}

	if !info.IsDir() {
		return nil
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return fn(root, info, err)
	}

	for _, entry := range entries {
		path := root + "/" + entry.Name()
		if err := filepathWalk(path, fn); err != nil {
			if err.Error() == "limit reached" {
				return err
			}
		}
	}

	return nil
}

// runCommand runs a shell command.
func runCommand(cmd string, workdir string, timeout int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	// Use exec to run the command.
	// For simplicity, we'll use a basic approach.
	output, err := runCommandImpl(ctx, cmd, workdir)
	if err != nil {
		return "", err
	}

	return output, nil
}

// runCommandImpl is the actual command runner.
func runCommandImpl(ctx context.Context, cmd string, workdir string) (string, error) {
	// Import exec at package level instead.
	return runCommandFallback(ctx, cmd, workdir)
}

// runCommandFallback runs a command using exec.
func runCommandFallback(ctx context.Context, cmd string, workdir string) (string, error) {
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	if workdir != "" {
		c.Dir = workdir
	}

	var stdout, stderr bytes.Buffer

	c.Stdout = &stdout
	c.Stderr = &stderr

	if err := c.Run(); err != nil {
		output := stdout.String()
		if stderr.Len() > 0 {
			output += "\n" + stderr.String()
		}

		return "", fmt.Errorf("exit %d: %s", c.ProcessState.ExitCode(), output)
	}

	return stdout.String(), nil
}
