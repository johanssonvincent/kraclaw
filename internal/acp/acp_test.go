package acp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	cfg := Config{Enabled: true}
	s := New(cfg)
	if s == nil {
		t.Fatal("New() returned nil")
	}
}

func TestConfig_Disabled(t *testing.T) {
	cfg := Config{Enabled: false}
	s := New(cfg)
	ctx := context.Background()
	err := s.Start(ctx)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := s.Stop(ctx); err != nil {
		t.Logf("failed to stop server: %v", err)
	}
}

func TestHandleRoot(t *testing.T) {
	s := New(Config{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	s.handleRoot(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp["status"] != "running" {
		t.Errorf("status = %v, want running", resp["status"])
	}
}

func TestHandleHealth(t *testing.T) {
	s := New(Config{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	s.handleHealth(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}
	if w.Body.String() != "ok" {
		t.Errorf("Body = %q, want ok", w.Body.String())
	}
}

func TestHandleSessions_Get(t *testing.T) {
	s := New(Config{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/sessions", nil)
	s.handleSessions(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHandleSessions_Post(t *testing.T) {
	s := New(Config{})
	body := `{"client_name":"test-client"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	s.handleSessions(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp["session_id"] == "" {
		t.Error("session_id should not be empty")
	}
}

func TestHandleFilesRead(t *testing.T) {
	s := New(Config{EnableFileOperations: true})
	body := `{"path":"/tmp/test.txt"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/files/read", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	s.handleFilesRead(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("Status = %d, want 500 for nonexistent file", w.Code)
	}
}

func TestHandleFilesWrite(t *testing.T) {
	s := New(Config{EnableFileOperations: true})
	body := `{"path":"/tmp/test.txt","content":"test"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/files/write", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	s.handleFilesWrite(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHandleFilesList(t *testing.T) {
	s := New(Config{})
	body := `{"path":"/tmp"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/files/list", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	s.handleFilesList(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHandleFilesSearch(t *testing.T) {
	s := New(Config{})
	body := `{"path":"/tmp","pattern":"test"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/files/search", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	s.handleFilesSearch(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHandleTerminal(t *testing.T) {
	s := New(Config{EnableTerminal: true})
	body := `{"command":"echo hello"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/terminal", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	s.handleTerminal(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHandleChat(t *testing.T) {
	s := New(Config{})
	body := `{"message":"hello"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	s.handleChat(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHandleRoot_NotGet(t *testing.T) {
	s := New(Config{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	s.handleRoot(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleHealth_NotGet(t *testing.T) {
	s := New(Config{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/health", nil)
	s.handleHealth(w, r)
	// Health endpoint doesn't check method, always returns OK
	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHandleFilesRead_BadJSON(t *testing.T) {
	s := New(Config{EnableFileOperations: true})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/files/read", strings.NewReader("not json"))
	s.handleFilesRead(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleFilesRead_MissingPath(t *testing.T) {
	s := New(Config{EnableFileOperations: true})
	body := `{"content":"test"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/files/read", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	s.handleFilesRead(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleFilesWrite_BadJSON(t *testing.T) {
	s := New(Config{EnableFileOperations: true})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/files/write", strings.NewReader("not json"))
	s.handleFilesWrite(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleFilesWrite_MissingPath(t *testing.T) {
	s := New(Config{EnableFileOperations: true})
	body := `{"content":"test"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/files/write", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	s.handleFilesWrite(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleFilesWrite_MissingContent(t *testing.T) {
	s := New(Config{EnableFileOperations: true})
	body := `{"path":"/tmp/test.txt"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/files/write", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	s.handleFilesWrite(w, r)
	// Content can be empty string, so this should succeed
	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHandleFilesList_BadJSON(t *testing.T) {
	s := New(Config{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/files/list", strings.NewReader("not json"))
	s.handleFilesList(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleFilesList_MissingPath(t *testing.T) {
	s := New(Config{})
	body := `{}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/files/list", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	s.handleFilesList(w, r)
	// Path defaults to "." if missing
	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHandleFilesSearch_BadJSON(t *testing.T) {
	s := New(Config{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/files/search", strings.NewReader("not json"))
	s.handleFilesSearch(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleFilesSearch_MissingPath(t *testing.T) {
	s := New(Config{})
	body := `{"pattern":"test"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/files/search", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	s.handleFilesSearch(w, r)
	// Path defaults to "." if missing
	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHandleTerminal_BadJSON(t *testing.T) {
	s := New(Config{EnableTerminal: true})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/terminal", strings.NewReader("not json"))
	s.handleTerminal(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleTerminal_MissingCommand(t *testing.T) {
	s := New(Config{EnableTerminal: true})
	body := `{}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/terminal", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	s.handleTerminal(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleChat_BadJSON(t *testing.T) {
	s := New(Config{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader("not json"))
	s.handleChat(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleChat_MissingMessage(t *testing.T) {
	s := New(Config{})
	body := `{}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	s.handleChat(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestCORS(t *testing.T) {
	s := New(Config{})
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := s.corsMiddleware(mux)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodOptions, "/", nil)
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("CORS header should be set")
	}
}

func TestReadFile(t *testing.T) {
	_, err := readFile("/nonexistent/file.txt", 1024)
	if err == nil {
		t.Error("readFile() should return error for nonexistent file")
	}
}

func TestWriteFile(t *testing.T) {
	err := writeFile("/tmp/test_acp_write.txt", "test content")
	if err != nil {
		t.Fatalf("writeFile() error = %v", err)
	}
}

func TestListDirectory(t *testing.T) {
	entries, err := listDirectory("/tmp")
	if err != nil {
		t.Fatalf("listDirectory() error = %v", err)
	}
	if len(entries) == 0 {
		t.Error("listDirectory() should return entries for /tmp")
	}
}

func TestSearchFiles(t *testing.T) {
	results, err := searchFiles("/tmp", "test", 10)
	if err != nil {
		t.Fatalf("searchFiles() error = %v", err)
	}
	_ = results
}

func TestRunCommand(t *testing.T) {
	output, err := runCommand("echo hello", "", 5)
	if err != nil {
		t.Fatalf("runCommand() error = %v", err)
	}
	if !strings.Contains(output, "hello") {
		t.Errorf("runCommand() output = %q, want to contain 'hello'", output)
	}
}

func TestRunCommandImpl(t *testing.T) {
	ctx := context.Background()
	output, err := runCommandImpl(ctx, "echo hello", "")
	if err != nil {
		t.Fatalf("runCommandImpl() error = %v", err)
	}
	if !strings.Contains(output, "hello") {
		t.Errorf("runCommandImpl() output = %q, want to contain 'hello'", output)
	}
}

func TestRunCommandFallback(t *testing.T) {
	ctx := context.Background()
	output, err := runCommandFallback(ctx, "echo hello", "")
	if err != nil {
		t.Fatalf("runCommandFallback() error = %v", err)
	}
	if !strings.Contains(output, "hello") {
		t.Errorf("runCommandFallback() output = %q, want to contain 'hello'", output)
	}
}

func TestRunCommandFallback_Error(t *testing.T) {
	ctx := context.Background()
	_, err := runCommandFallback(ctx, "exit 1", "")
	if err == nil {
		t.Error("runCommandFallback() should return error for failed command")
	}
}

func TestRunCommandFallback_Workdir(t *testing.T) {
	ctx := context.Background()
	output, err := runCommandFallback(ctx, "pwd", "/tmp")
	if err != nil {
		t.Fatalf("runCommandFallback() error = %v", err)
	}
	if !strings.Contains(output, "/tmp") {
		t.Errorf("runCommandFallback() output = %q, want to contain '/tmp'", output)
	}
}

func TestConfig_Defaults(t *testing.T) {
	cfg := Config{}
	if cfg.Enabled {
		t.Error("Enabled should default to false")
	}
}

func TestServer_CreateSession(t *testing.T) {
	s := New(Config{})
	body := `{"client_name":"test-client"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	s.createSession(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestServer_ListSessions(t *testing.T) {
	s := New(Config{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/sessions", nil)
	s.listSessions(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}
}
