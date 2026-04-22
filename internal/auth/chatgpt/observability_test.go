package chatgpt

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRefresh_LogsOnUnparseable401(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "<html>blocked</html>")
	}))
	defer srv.Close()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c, err := NewClient(Config{Issuer: srv.URL, HTTPClient: srv.Client(), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Refresh(context.Background(), "rt")
	if !strings.Contains(buf.String(), "refresh response body unparseable") {
		t.Errorf("expected warn log, got: %s", buf.String())
	}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Errorf("log line not JSON: %s", line)
		}
	}
}
