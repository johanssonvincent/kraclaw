package chatgpt

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRefresh_Success(t *testing.T) {
	tests := map[string]struct {
		// respBody is the raw /oauth/token response body. Exactly one of
		// respBody or respBodyFn must be set; respBodyFn lets a case mint a
		// JWT at test time (it runs once per subtest).
		respBody   string
		respBodyFn func() string
		// reqCheck, when non-nil, runs extra request-side assertions.
		// It receives the parsed JSON body (nil if parse failed).
		reqCheck func(t *testing.T, body map[string]string)
		// refreshToken is the token passed to Refresh.
		refreshToken string
		// now pins the client clock when non-zero (needed for expires_in math).
		now time.Time
		// check asserts on the returned tokens.
		check func(t *testing.T, tokens *Tokens)
	}{
		"rotates tokens and populates IDClaims": {
			refreshToken: "rt_old",
			respBodyFn: func() string {
				idTok := mintJWTNow(map[string]any{
					"exp": time.Now().Add(time.Hour).Unix(),
					"https://api.openai.com/auth": map[string]any{
						"chatgpt_account_id": "acct_42",
						"chatgpt_plan_type":  "plus",
					},
				})
				return `{"id_token":"` + idTok + `","access_token":"acc_new","refresh_token":"rt_new"}`
			},
			reqCheck: func(t *testing.T, body map[string]string) {
				if body["client_id"] != ClientID {
					t.Errorf("client_id = %q, want %q", body["client_id"], ClientID)
				}
				if body["grant_type"] != "refresh_token" {
					t.Errorf("grant_type = %q, want refresh_token", body["grant_type"])
				}
				if body["refresh_token"] != "rt_old" {
					t.Errorf("refresh_token = %q, want rt_old", body["refresh_token"])
				}
			},
			check: func(t *testing.T, tokens *Tokens) {
				if tokens.AccessToken != "acc_new" || tokens.RefreshToken != "rt_new" {
					t.Fatalf("tokens = %+v", tokens)
				}
				if tokens.IDClaims.AccountID != "acct_42" {
					t.Fatalf("IDClaims.AccountID = %q, want acct_42", tokens.IDClaims.AccountID)
				}
			},
		},
		"keeps refresh_token when server omits it": {
			refreshToken: "rt_keep",
			respBodyFn: func() string {
				idTok := mintJWTNow(map[string]any{
					"exp":                         time.Now().Add(time.Hour).Unix(),
					"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct"},
				})
				return `{"id_token":"` + idTok + `","access_token":"acc_new"}`
			},
			check: func(t *testing.T, tokens *Tokens) {
				if tokens.RefreshToken != "rt_keep" {
					t.Fatalf("RefreshToken = %q, want rt_keep preserved", tokens.RefreshToken)
				}
			},
		},
		"expires_in fallback populates ExpiresAt": {
			refreshToken: "rt",
			now:          time.Unix(1_700_000_000, 0).UTC(),
			respBody:     `{"access_token":"a","refresh_token":"r","expires_in":600}`,
			check: func(t *testing.T, tokens *Tokens) {
				want := time.Unix(1_700_000_000, 0).UTC().Add(600 * time.Second)
				if !tokens.ExpiresAt.Equal(want) {
					t.Fatalf("ExpiresAt = %v, want %v", tokens.ExpiresAt, want)
				}
			},
		},
		"no expiry is observable via HasExpiry=false": {
			refreshToken: "rt",
			respBody:     `{"access_token":"a","refresh_token":"r"}`,
			check: func(t *testing.T, tokens *Tokens) {
				if tokens.HasExpiry {
					t.Fatalf("HasExpiry = true, want false when neither id_token.exp nor expires_in present; tokens=%+v", tokens)
				}
				if !tokens.ExpiresAt.IsZero() {
					t.Fatalf("ExpiresAt = %v, want zero", tokens.ExpiresAt)
				}
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/oauth/token" {
					t.Errorf("path = %s, want /oauth/token", r.URL.Path)
				}
				if got := r.Header.Get("Content-Type"); got != "application/json" {
					t.Errorf("Content-Type = %q, want application/json", got)
				}
				if tc.reqCheck != nil {
					raw, _ := io.ReadAll(r.Body)
					var parsed map[string]string
					_ = json.Unmarshal(raw, &parsed)
					tc.reqCheck(t, parsed)
				}
				body := tc.respBody
				if tc.respBodyFn != nil {
					body = tc.respBodyFn()
				}
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()

			var opts []func(*Config)
			if !tc.now.IsZero() {
				now := tc.now
				opts = append(opts, func(cfg *Config) { cfg.Now = func() time.Time { return now } })
			}
			c := newTestClient(t, srv, opts...)

			tokens, err := c.Refresh(context.Background(), tc.refreshToken)
			if err != nil {
				t.Fatalf("Refresh: %v", err)
			}
			tc.check(t, tokens)
		})
	}
}

// mintJWTNow is a test-time JWT builder usable from handler closures and case
// builders where no *testing.T is in scope. It panics on marshal errors, which
// cannot happen for the fixed payloads used here.
func mintJWTNow(payload map[string]any) string {
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		panic(err)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(headerJSON) + "." + enc.EncodeToString(payloadJSON) + ".sig"
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
	c, err := NewClient(Config{Issuer: srv.URL, HTTPClient: &http.Client{Timeout: 500 * time.Millisecond}})
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
