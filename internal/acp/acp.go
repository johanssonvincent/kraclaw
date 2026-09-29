package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/johanssonvincent/kraclaw/internal/sandbox"
)

type Config struct {
	Enabled              bool   `envconfig:"ACP_ENABLED" default:"false"`
	ListenAddr           string `envconfig:"ACP_LISTEN_ADDR" default:"127.0.0.1:6118"`
	WorkspacePath        string `envconfig:"ACP_WORKSPACE_PATH"`
	EnableFileOperations bool   `envconfig:"ACP_FILE_OPS" default:"true"`
	EnableTerminal       bool   `envconfig:"ACP_TERMINAL" default:"true"`
	MaxFileSize          int64  `envconfig:"ACP_MAX_FILE_SIZE" default:"1048576"`
}

type Server struct {
	cfg      Config
	httpSrv  *http.Server
	listener net.Listener
	mu       sync.RWMutex
	sessions map[string]*Session
	sandbox  *sandbox.Controller
	log      *slog.Logger
}

type Session struct {
	ID            string    `json:"id"`
	ClientName    string    `json:"client_name"`
	ClientVersion string    `json:"client_version,omitempty"`
	Workspace     string    `json:"workspace,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	LastActive    time.Time `json:"last_active"`
}

func New(cfg Config, sb *sandbox.Controller) *Server {
	return &Server{
		cfg:      cfg,
		sessions: make(map[string]*Session),
		sandbox:  sb,
		log:      slog.With("component", "acp"),
	}
}

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

	absPath, err := s.validatePath(req.Path)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid path: %v", err), http.StatusBadRequest)

		return
	}

	data, err := readFile(absPath, s.cfg.MaxFileSize)
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

	absPath, err := s.validatePath(req.Path)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid path: %v", err), http.StatusBadRequest)

		return
	}

	if err := writeFile(absPath, req.Content); err != nil {
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

	absPath, err := s.validatePath(req.Path)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid path: %v", err), http.StatusBadRequest)

		return
	}

	entries, err := listDirectory(absPath)
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

	absPath, err := s.validatePath(req.Path)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid path: %v", err), http.StatusBadRequest)

		return
	}

	if req.Limit <= 0 {
		req.Limit = 50
	}

	results, err := searchFiles(absPath, req.Pattern, req.Limit)
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
		Sandbox string `json:"sandbox"`
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

	if req.Sandbox == "" {
		http.Error(w, "sandbox is required", http.StatusBadRequest)

		return
	}

	if s.sandbox == nil {
		http.Error(w, "sandbox controller not configured", http.StatusInternalServerError)

		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(req.Timeout)*time.Second)
	defer cancel()

	output, err := s.sandbox.ExecInPod(ctx, req.Sandbox, req.Command)
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

func (s *Server) validatePath(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("invalid path %q: %w", path, err)
	}

	workspace := s.cfg.WorkspacePath
	if workspace == "" {
		workspace = "."
	}

	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("invalid workspace path %q: %w", workspace, err)
	}

	rel, err := filepath.Rel(absWorkspace, absPath)
	if err != nil {
		return "", fmt.Errorf("path %q is outside workspace %q: %w", path, workspace, err)
	}

	if strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", fmt.Errorf("path %q is outside workspace %q", path, workspace)
	}

	return absPath, nil
}

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

func writeFile(path string, content string) error {
	dir := strings.TrimSuffix(path, path[strings.LastIndex(path, "/"):])
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create directory: %w", err)
		}
	}

	return os.WriteFile(path, []byte(content), 0o644)
}

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

func searchFiles(path string, pattern string, limit int) ([]string, error) {
	var results []string

	err := filepathWalk(path, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
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
