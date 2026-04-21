package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRefresh_Success_RotatesTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		var got map[string]string
		_ = json.Unmarshal(body, &got)
		if got["client_id"] != ClientID {
			t.Errorf("client_id = %q", got["client_id"])
		}
		if got["grant_type"] != "refresh_token" {
			t.Errorf("grant_type = %q", got["grant_type"])
		}
		if got["refresh_token"] != "rt_old" {
			t.Errorf("refresh_token = %q", got["refresh_token"])
		}
		idTok := mintJWT(t, map[string]any{
			"exp": time.Now().Add(time.Hour).Unix(),
			"https://api.openai.com/auth": map[string]any{
				"chatgpt_account_id": "acct_42",
				"chatgpt_plan_type":  "plus",
			},
		})
		_, _ = w.Write([]byte(`{"id_token":"` + idTok + `","access_token":"acc_new","refresh_token":"rt_new"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	tokens, err := c.Refresh(context.Background(), "rt_old")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tokens.AccessToken != "acc_new" || tokens.RefreshToken != "rt_new" {
		t.Fatalf("tokens = %+v", tokens)
	}
	if tokens.IDClaims.AccountID != "acct_42" {
		t.Fatalf("IDClaims.AccountID = %q", tokens.IDClaims.AccountID)
	}
}

func TestRefresh_KeepsRefreshTokenWhenServerOmits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idTok := mintJWT(t, map[string]any{
			"exp":                         time.Now().Add(time.Hour).Unix(),
			"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct"},
		})
		_, _ = w.Write([]byte(`{"id_token":"` + idTok + `","access_token":"acc_new"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	tokens, err := c.Refresh(context.Background(), "rt_keep")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tokens.RefreshToken != "rt_keep" {
		t.Fatalf("expected refresh_token to be preserved, got %q", tokens.RefreshToken)
	}
}

func TestRefresh_ExpiresInFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r","expires_in":600}`))
	}))
	defer srv.Close()

	now := time.Unix(1_700_000_000, 0).UTC()
	c := newTestClient(t, srv, func(cfg *Config) { cfg.Now = func() time.Time { return now } })
	tokens, err := c.Refresh(context.Background(), "rt")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	want := now.Add(600 * time.Second)
	if !tokens.ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt = %v, want %v", tokens.ExpiresAt, want)
	}
}

func TestRefresh_PermanentFailures(t *testing.T) {
	tests := []struct {
		name string
		body string
		want RefreshFailureReason
	}{
		{"expired", `{"error":"refresh_token_expired"}`, RefreshFailureExpired},
		{"reused-error-code", `{"error_code":"refresh_token_reused"}`, RefreshFailureReused},
		{"invalidated", `{"error":"refresh_token_invalidated"}`, RefreshFailureRevoked},
		{"unknown", `{"error":"something_else"}`, RefreshFailureUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			c := newTestClient(t, srv)
			_, err := c.Refresh(context.Background(), "rt")
			var re *RefreshError
			if !errors.As(err, &re) {
				t.Fatalf("expected RefreshError, got %v", err)
			}
			if !re.Permanent() {
				t.Fatalf("expected permanent error, got %+v", re)
			}
			if re.Reason != tt.want {
				t.Fatalf("Reason = %q, want %q", re.Reason, tt.want)
			}
		})
	}
}

func TestRefresh_Permanent400InvalidGrant(t *testing.T) {
	tests := []struct {
		name string
		body string
		want RefreshFailureReason
	}{
		{"400 invalid_grant + refresh_token_expired", `{"error":"invalid_grant","error_code":"refresh_token_expired"}`, RefreshFailureExpired},
		{"400 invalid_grant + refresh_token_reused", `{"error":"invalid_grant","error_code":"refresh_token_reused"}`, RefreshFailureReused},
		{"400 invalid_grant + refresh_token_invalidated", `{"error":"invalid_grant","error_code":"refresh_token_invalidated"}`, RefreshFailureRevoked},
		{"400 bare invalid_grant", `{"error":"invalid_grant"}`, RefreshFailureUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			c := newTestClient(t, srv)
			_, err := c.Refresh(context.Background(), "rt")
			var re *RefreshError
			if !errors.As(err, &re) {
				t.Fatalf("expected RefreshError, got %v", err)
			}
			if !re.Permanent() {
				t.Fatalf("400 invalid_grant must be permanent, got %+v", re)
			}
			if re.Reason != tt.want {
				t.Fatalf("Reason = %q, want %q", re.Reason, tt.want)
			}
			if re.Status != http.StatusBadRequest {
				t.Fatalf("Status = %d, want 400", re.Status)
			}
		})
	}
}

func TestRefresh_401WithHTMLBodyIsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`<html><body>Cloudflare challenge</body></html>`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	_, err := c.Refresh(context.Background(), "rt")
	var re *RefreshError
	if !errors.As(err, &re) {
		t.Fatal(err)
	}
	if re.Permanent() {
		t.Fatalf("401 with unparseable body must not force re-auth; got %+v", re)
	}
}

func TestRefresh_TransientFailures(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "5xx",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "boom", http.StatusInternalServerError)
			},
		},
		{
			name: "bad json",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{not-json`))
			},
		},
		{
			name: "missing access_token",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"refresh_token":"r"}`))
			},
		},
		{
			name: "401 with empty body",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()
			c := newTestClient(t, srv)
			_, err := c.Refresh(context.Background(), "rt")
			var re *RefreshError
			if !errors.As(err, &re) {
				t.Fatalf("expected RefreshError, got %v", err)
			}
			if re.Permanent() {
				t.Fatalf("expected transient error, got %+v", re)
			}
		})
	}
}

func TestRefresh_MalformedIDTokenIsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r","id_token":"not.a.jwt"}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	_, err := c.Refresh(context.Background(), "rt")
	var re *RefreshError
	if !errors.As(err, &re) {
		t.Fatal(err)
	}
	if re.Permanent() {
		t.Fatalf("malformed id_token in 2xx must be transient; got %+v", re)
	}
}

func TestRefresh_NoExpiryIsObservable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r"}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	tokens, err := c.Refresh(context.Background(), "rt")
	if err != nil {
		t.Fatal(err)
	}
	if tokens.HasExpiry {
		t.Fatalf("HasExpiry must be false when neither id_token.exp nor expires_in present; got %+v", tokens)
	}
	if !tokens.ExpiresAt.IsZero() {
		t.Fatalf("ExpiresAt should remain zero; got %v", tokens.ExpiresAt)
	}
}

func TestRefresh_RejectsEmptyToken(t *testing.T) {
	c, err := NewClient(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Refresh(context.Background(), "  "); err == nil {
		t.Fatal("expected error for blank refresh token")
	}
}

func TestRefresh_TransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // close immediately so requests fail
	c, err := NewClient(Config{Issuer: srv.URL, HTTPClient: &http.Client{Timeout: 50 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Refresh(context.Background(), "rt")
	var re *RefreshError
	if !errors.As(err, &re) {
		t.Fatalf("expected RefreshError, got %v", err)
	}
	if re.Permanent() {
		t.Fatalf("transport error should be transient, got %+v", re)
	}
}
