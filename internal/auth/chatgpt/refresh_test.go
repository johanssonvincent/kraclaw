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
	t.Parallel()
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
				if tokens.AccessToken != "acc_new" {
					t.Errorf("AccessToken = %q, want acc_new", tokens.AccessToken)
				}
				if tokens.RefreshToken != "rt_new" {
					t.Errorf("RefreshToken = %q, want rt_new", tokens.RefreshToken)
				}
				if tokens.IDClaims.AccountID != "acct_42" {
					t.Errorf("IDClaims.AccountID = %q, want acct_42", tokens.IDClaims.AccountID)
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
					t.Errorf("RefreshToken = %q, want rt_keep preserved", tokens.RefreshToken)
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
					t.Errorf("ExpiresAt = %v, want %v", tokens.ExpiresAt, want)
				}
			},
		},
		"no expiry is observable via HasExpiry=false": {
			refreshToken: "rt",
			respBody:     `{"access_token":"a","refresh_token":"r"}`,
			check: func(t *testing.T, tokens *Tokens) {
				if tokens.HasExpiry {
					t.Errorf("HasExpiry = true, want false when neither id_token.exp nor expires_in present; tokens=%+v", tokens)
				}
				if !tokens.ExpiresAt.IsZero() {
					t.Errorf("ExpiresAt = %v, want zero", tokens.ExpiresAt)
				}
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
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
	t.Parallel()
	tests := map[string]struct {
		status     int
		body       string
		wantReason RefreshFailureReason
	}{
		// 401 with RFC 6749 error/error_code values.
		"401 refresh_token_expired":     {status: http.StatusUnauthorized, body: `{"error":"refresh_token_expired"}`, wantReason: RefreshFailureExpired},
		"401 reused (error_code)":       {status: http.StatusUnauthorized, body: `{"error_code":"refresh_token_reused"}`, wantReason: RefreshFailureReused},
		"401 refresh_token_invalidated": {status: http.StatusUnauthorized, body: `{"error":"refresh_token_invalidated"}`, wantReason: RefreshFailureRevoked},
		"401 unknown error body":        {status: http.StatusUnauthorized, body: `{"error":"something_else"}`, wantReason: RefreshFailureUnknown},
		// 400 invalid_grant variants (RFC 6749 §5.2).
		"400 invalid_grant + refresh_token_expired":     {status: http.StatusBadRequest, body: `{"error":"invalid_grant","error_code":"refresh_token_expired"}`, wantReason: RefreshFailureExpired},
		"400 invalid_grant + refresh_token_reused":      {status: http.StatusBadRequest, body: `{"error":"invalid_grant","error_code":"refresh_token_reused"}`, wantReason: RefreshFailureReused},
		"400 invalid_grant + refresh_token_invalidated": {status: http.StatusBadRequest, body: `{"error":"invalid_grant","error_code":"refresh_token_invalidated"}`, wantReason: RefreshFailureRevoked},
		"400 bare invalid_grant":                        {status: http.StatusBadRequest, body: `{"error":"invalid_grant"}`, wantReason: RefreshFailureUnknown},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := newTestClient(t, srv)
			_, err := c.Refresh(context.Background(), "rt")
			var re *RefreshError
			if !errors.As(err, &re) {
				t.Fatalf("err = %v, want *RefreshError", err)
			}
			if !re.Permanent() {
				t.Errorf("Permanent() = false, want true (status=%d body=%s err=%+v)", tc.status, tc.body, re)
			}
			if re.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q (status=%d body=%s)", re.Reason, tc.wantReason, tc.status, tc.body)
			}
			if re.Status != tc.status {
				t.Errorf("Status = %d, want %d", re.Status, tc.status)
			}
		})
	}
}

func TestRefresh_TransientFailures(t *testing.T) {
	t.Parallel()
	tests := map[string]http.HandlerFunc{
		"5xx server error": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		},
		"bad json": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{not-json`))
		},
		"missing access_token": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"refresh_token":"r"}`))
		},
		"401 empty body": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
		"401 with HTML body (Cloudflare challenge)": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`<html><body>Cloudflare challenge</body></html>`))
		},
		"2xx with malformed id_token": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r","id_token":"not.a.jwt"}`))
		},
	}
	for name, handler := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(handler)
			defer srv.Close()
			c := newTestClient(t, srv)
			_, err := c.Refresh(context.Background(), "rt")
			var re *RefreshError
			if !errors.As(err, &re) {
				t.Fatalf("err = %v, want *RefreshError", err)
			}
			if re.Permanent() {
				t.Errorf("Permanent() = true, want false (err=%+v)", re)
			}
		})
	}
}

func TestRefresh_PreflightAndTransport(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		// setup returns a ready-to-use Client and the refresh_token argument.
		setup func(t *testing.T) (*Client, string)
		// wantPermanent, when non-nil, asserts on RefreshError.Permanent().
		// Nil means the test only requires a non-nil error (no wrapping shape).
		wantPermanent *bool
	}{
		"rejects blank refresh_token before network": {
			setup: func(t *testing.T) (*Client, string) {
				c, err := NewClient(Config{})
				if err != nil {
					t.Fatalf("NewClient: %v", err)
				}
				return c, "  "
			},
		},
		"transport error is transient": {
			setup: func(t *testing.T) (*Client, string) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
				srv.Close() // close immediately so requests fail at dial
				c, err := NewClient(Config{Issuer: srv.URL, HTTPClient: &http.Client{Timeout: 500 * time.Millisecond}})
				if err != nil {
					t.Fatalf("NewClient: %v", err)
				}
				return c, "rt"
			},
			wantPermanent: boolPtr(false),
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c, token := tc.setup(t)
			_, err := c.Refresh(context.Background(), token)
			if err == nil {
				t.Fatal("Refresh returned nil error, want error")
			}
			if tc.wantPermanent != nil {
				var re *RefreshError
				if !errors.As(err, &re) {
					t.Fatalf("err = %v, want *RefreshError", err)
				}
				if re.Permanent() != *tc.wantPermanent {
					t.Errorf("Permanent() = %v, want %v (err=%+v)", re.Permanent(), *tc.wantPermanent, re)
				}
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }
