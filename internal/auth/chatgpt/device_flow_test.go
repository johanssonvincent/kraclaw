package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, server *httptest.Server, opts ...func(*Config)) *Client {
	t.Helper()
	cfg := Config{
		Issuer:       server.URL,
		HTTPClient:   server.Client(),
		Now:          func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		PollTimeout:  500 * time.Millisecond,
		PollInterval: 5 * time.Millisecond,
	}
	for _, o := range opts {
		o(&cfg)
	}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestRequestDeviceCode_ResponseParsing(t *testing.T) {
	tests := map[string]struct {
		body  string
		check func(t *testing.T, dc *DeviceCode)
	}{
		"full success response with string interval": {
			body: `{"device_auth_id":"dev_abc","user_code":"BCDF-1234","interval":"7"}`,
			check: func(t *testing.T, dc *DeviceCode) {
				if dc.UserCode != "BCDF-1234" {
					t.Errorf("UserCode = %q, want BCDF-1234", dc.UserCode)
				}
				if dc.deviceAuthID != "dev_abc" {
					t.Errorf("deviceAuthID = %q, want dev_abc", dc.deviceAuthID)
				}
				if dc.Interval != 7*time.Second {
					t.Errorf("Interval = %v, want 7s", dc.Interval)
				}
				if !strings.HasSuffix(dc.VerificationURL, "/codex/device") {
					t.Errorf("VerificationURL = %q, want suffix /codex/device", dc.VerificationURL)
				}
			},
		},
		"numeric interval": {
			body: `{"device_auth_id":"d","user_code":"C","interval":3}`,
			check: func(t *testing.T, dc *DeviceCode) {
				if dc.Interval != 3*time.Second {
					t.Fatalf("Interval = %v, want 3s", dc.Interval)
				}
			},
		},
		"usercode alias (no underscore)": {
			body: `{"device_auth_id":"d","usercode":"ABCD","interval":"5"}`,
			check: func(t *testing.T, dc *DeviceCode) {
				if dc.UserCode != "ABCD" {
					t.Fatalf("UserCode = %q, want ABCD via alias", dc.UserCode)
				}
			},
		},
		"interval null falls back to default": {
			body: `{"device_auth_id":"d","user_code":"UC","interval":null}`,
			check: func(t *testing.T, dc *DeviceCode) {
				if dc.Interval != DefaultPollInterval {
					t.Fatalf("Interval = %v, want DefaultPollInterval %v", dc.Interval, DefaultPollInterval)
				}
			},
		},
		"interval empty string falls back to default": {
			body: `{"device_auth_id":"d","user_code":"UC","interval":""}`,
			check: func(t *testing.T, dc *DeviceCode) {
				if dc.Interval != DefaultPollInterval {
					t.Fatalf("Interval = %v, want DefaultPollInterval %v", dc.Interval, DefaultPollInterval)
				}
			},
		},
		"interval field missing falls back to default": {
			body: `{"device_auth_id":"d","user_code":"UC"}`,
			check: func(t *testing.T, dc *DeviceCode) {
				if dc.Interval != DefaultPollInterval {
					t.Fatalf("Interval = %v, want DefaultPollInterval %v", dc.Interval, DefaultPollInterval)
				}
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/accounts/deviceauth/usercode" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				if r.Method != http.MethodPost {
					t.Errorf("method = %s, want POST", r.Method)
				}
				if r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("Content-Type = %q, want application/json", r.Header.Get("Content-Type"))
				}
				body, _ := io.ReadAll(r.Body)
				var got map[string]string
				_ = json.Unmarshal(body, &got)
				if got["client_id"] != ClientID {
					t.Errorf("client_id = %q, want %q", got["client_id"], ClientID)
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := newTestClient(t, srv)
			dc, err := c.RequestDeviceCode(context.Background())
			if err != nil {
				t.Fatalf("RequestDeviceCode: %v", err)
			}
			tc.check(t, dc)
		})
	}
}

func TestRequestDeviceCode_404IsDisabledError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.RequestDeviceCode(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("expected 'not enabled' error, got %v", err)
	}
}

func TestRequestDeviceCode_5xxIsBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.RequestDeviceCode(context.Background())
	var bad *errBadStatus
	if !errors.As(err, &bad) {
		t.Fatalf("expected errBadStatus, got %v", err)
	}
	if bad.Status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", bad.Status)
	}
}

func TestRequestDeviceCode_MissingFields(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"missing device_auth_id", `{"user_code":"UC","interval":"5"}`},
		{"missing user_code", `{"device_auth_id":"d","interval":"5"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := newTestClient(t, srv)
			if _, err := c.RequestDeviceCode(context.Background()); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestPollOnce_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/accounts/deviceauth/token" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var got map[string]string
		_ = json.Unmarshal(body, &got)
		if got["device_auth_id"] != "dev" || got["user_code"] != "USER" {
			t.Errorf("unexpected body %s", body)
		}
		_, _ = w.Write([]byte(`{"authorization_code":"ac_1","code_challenge":"cc_1","code_verifier":"cv_1"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	dc := deviceCodeForTest("dev", "USER", c.VerificationURL(), 5*time.Millisecond)

	ac, err := c.PollOnce(context.Background(), dc)
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if ac.Code != "ac_1" || ac.CodeVerifier != "cv_1" || ac.CodeChallenge != "cc_1" {
		t.Fatalf("AuthorizationCode = %+v", ac)
	}
}

func TestPollOnce_PendingStatuses(t *testing.T) {
	statuses := []int{http.StatusForbidden, http.StatusNotFound, http.StatusBadRequest}
	for _, status := range statuses {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
			}))
			defer srv.Close()
			c := newTestClient(t, srv)
			dc := deviceCodeForTest("dev", "USER", "", 5*time.Millisecond)
			if _, err := c.PollOnce(context.Background(), dc); !errors.Is(err, ErrAuthorizationPending) {
				t.Fatalf("status %d with pending body: expected ErrAuthorizationPending, got %v", status, err)
			}
		})
	}
}

func TestPollOnce_404WithoutPendingCodeIsBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"device_code_expired"}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	dc := deviceCodeForTest("daid", "UC", c.VerificationURL(), time.Second)
	_, err := c.PollOnce(context.Background(), dc)
	if errors.Is(err, ErrAuthorizationPending) {
		t.Fatalf("404 with non-pending body should not be pending; got %v", err)
	}
	var bad *errBadStatus
	if !errors.As(err, &bad) {
		t.Fatalf("expected errBadStatus, got %v", err)
	}
}

func TestPollOnce_PendingByCodeNotStatus(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"400 authorization_pending", http.StatusBadRequest, `{"error":"authorization_pending"}`},
		{"400 slow_down", http.StatusBadRequest, `{"error":"slow_down"}`},
		{"403 authorization_pending (legacy)", http.StatusForbidden, `{"error":"authorization_pending"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			c := newTestClient(t, srv)
			dc := deviceCodeForTest("daid", "UC", c.VerificationURL(), time.Second)
			_, err := c.PollOnce(context.Background(), dc)
			if !errors.Is(err, ErrAuthorizationPending) {
				t.Fatalf("expected ErrAuthorizationPending, got %v", err)
			}
		})
	}
}

func TestPollOnce_OtherErrorIsBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "oops", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	dc := deviceCodeForTest("dev", "USER", "", 5*time.Millisecond)
	_, err := c.PollOnce(context.Background(), dc)
	var bad *errBadStatus
	if !errors.As(err, &bad) || bad.Status != http.StatusInternalServerError {
		t.Fatalf("expected 500 errBadStatus, got %v", err)
	}
}

func TestPollOnce_MissingFields(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"missing code_verifier", `{"authorization_code":"a"}`},
		{"missing authorization_code", `{"code_verifier":"v"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := newTestClient(t, srv)
			dc := deviceCodeForTest("dev", "USER", "", 5*time.Millisecond)
			if _, err := c.PollOnce(context.Background(), dc); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestPollUntilCode_SucceedsAfterPending(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
			return
		}
		_, _ = w.Write([]byte(`{"authorization_code":"a","code_challenge":"c","code_verifier":"v"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	dc := deviceCodeForTest("dev", "USER", "", time.Millisecond)

	var ticks atomic.Int32
	ac, err := c.PollUntilCode(context.Background(), dc, func() { ticks.Add(1) })
	if err != nil {
		t.Fatalf("PollUntilCode: %v", err)
	}
	if ac.Code != "a" {
		t.Fatalf("code = %q", ac.Code)
	}
	// Handler returns pending on calls 1 and 2, then success on call 3.
	// onTick fires once per pending response → must equal 2, not >= 1
	// (which would mask a regression where onTick runs only once).
	if got := ticks.Load(); got != 2 {
		t.Errorf("onTick = %d, want 2 (one per pending poll)", got)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("poll calls = %d, want 3", got)
	}
}

func TestPollUntilCode_RespectsContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	dc := deviceCodeForTest("dev", "USER", "", time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.PollUntilCode(ctx, dc, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestPollUntilCode_TimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, func(cfg *Config) {
		cfg.PollTimeout = 200 * time.Millisecond
		cfg.PollInterval = 20 * time.Millisecond
	})
	dc := deviceCodeForTest("dev", "USER", "", time.Millisecond)

	_, err := c.PollUntilCode(context.Background(), dc, nil)
	if !errors.Is(err, ErrDeviceAuthTimeout) {
		t.Fatalf("expected ErrDeviceAuthTimeout, got %v", err)
	}
}

func TestPollUntilCode_NonPendingErrorReturnsImmediately(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	dc := deviceCodeForTest("daid", "UC", c.VerificationURL(), time.Millisecond)
	_, err := c.PollUntilCode(context.Background(), dc, nil)
	var bad *errBadStatus
	if !errors.As(err, &bad) {
		t.Fatalf("expected errBadStatus, got %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected immediate return, got %d calls", got)
	}
}

func TestPollUntilCode_ParentDeadlineWins(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, func(cfg *Config) {
		cfg.PollTimeout = time.Second
		cfg.PollInterval = 5 * time.Millisecond
	})
	dc := deviceCodeForTest("daid", "UC", c.VerificationURL(), 5*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.PollUntilCode(ctx, dc, nil)
	if errors.Is(err, ErrDeviceAuthTimeout) {
		t.Fatalf("parent deadline must surface as ctx.Err(), not ErrDeviceAuthTimeout; got %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
}

func TestExchangeCode_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		form, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if form.Get("grant_type") != "authorization_code" {
			t.Errorf("grant_type = %q", form.Get("grant_type"))
		}
		if form.Get("code") != "ac_1" {
			t.Errorf("code = %q", form.Get("code"))
		}
		if form.Get("code_verifier") != "cv_1" {
			t.Errorf("code_verifier = %q", form.Get("code_verifier"))
		}
		if form.Get("client_id") != ClientID {
			t.Errorf("client_id = %q", form.Get("client_id"))
		}
		if !strings.HasSuffix(form.Get("redirect_uri"), "/deviceauth/callback") {
			t.Errorf("redirect_uri = %q", form.Get("redirect_uri"))
		}
		idTok := mintJWT(t, map[string]any{
			"exp": time.Now().Add(time.Hour).Unix(),
			"https://api.openai.com/auth": map[string]any{
				"chatgpt_account_id": "acct_42",
				"chatgpt_plan_type":  "plus",
			},
		})
		_, _ = w.Write([]byte(`{"id_token":"` + idTok + `","access_token":"acc_1","refresh_token":"ref_1"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	tokens, err := c.ExchangeCode(context.Background(), &AuthorizationCode{
		Code: "ac_1", CodeVerifier: "cv_1",
	})
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if tokens.AccessToken != "acc_1" || tokens.RefreshToken != "ref_1" {
		t.Fatalf("tokens = %+v", tokens)
	}
	if tokens.IDClaims.AccountID != "acct_42" || tokens.IDClaims.PlanType != "plus" {
		t.Fatalf("IDClaims = %+v", tokens.IDClaims)
	}
	if tokens.ExpiresAt.IsZero() {
		t.Fatal("expected ExpiresAt populated from id_token exp")
	}
}

func TestExchangeCode_Errors(t *testing.T) {
	tests := []struct {
		name      string
		handler   http.HandlerFunc
		input     *AuthorizationCode
		errSubstr string
	}{
		{
			name:  "empty code",
			input: &AuthorizationCode{},
		},
		{
			name:    "non-2xx",
			input:   &AuthorizationCode{Code: "a", CodeVerifier: "v"},
			handler: func(w http.ResponseWriter, r *http.Request) { http.Error(w, "bad", http.StatusBadRequest) },
		},
		{
			name:  "missing access_token",
			input: &AuthorizationCode{Code: "a", CodeVerifier: "v"},
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"id_token":"x.y.z","refresh_token":"r"}`))
			},
			errSubstr: "missing access_token",
		},
		{
			name:  "missing refresh_token",
			input: &AuthorizationCode{Code: "a", CodeVerifier: "v"},
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"access_token":"a","id_token":"x.y.z"}`))
			},
			errSubstr: "missing refresh_token",
		},
		{
			name:  "missing id_token",
			input: &AuthorizationCode{Code: "a", CodeVerifier: "v"},
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r"}`))
			},
			errSubstr: "missing id_token",
		},
		{
			name:  "malformed id_token",
			input: &AuthorizationCode{Code: "a", CodeVerifier: "v"},
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r","id_token":"nope"}`))
			},
			errSubstr: "parse id_token",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var srv *httptest.Server
			if tt.handler != nil {
				srv = httptest.NewServer(tt.handler)
				defer srv.Close()
			} else {
				srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Error("handler should not be called; ExchangeCode must short-circuit on invalid input")
				}))
				defer srv.Close()
			}
			c := newTestClient(t, srv)
			_, err := c.ExchangeCode(context.Background(), tt.input)
			if err == nil {
				t.Fatal("expected error")
			}
			if tt.errSubstr != "" && !strings.Contains(err.Error(), tt.errSubstr) {
				t.Fatalf("error %q does not contain %q", err, tt.errSubstr)
			}
		})
	}
}

func TestNewClient(t *testing.T) {
	tests := map[string]struct {
		config  Config
		wantErr bool
		check   func(t *testing.T, c *Client)
	}{
		"defaults populate derived URLs": {
			config:  Config{},
			wantErr: false,
			check: func(t *testing.T, c *Client) {
				if c.Issuer() != DefaultIssuer {
					t.Errorf("Issuer() = %q, want %q", c.Issuer(), DefaultIssuer)
				}
				if !strings.HasSuffix(c.VerificationURL(), "/codex/device") {
					t.Errorf("VerificationURL() = %q, want suffix /codex/device", c.VerificationURL())
				}
				if !strings.HasSuffix(c.RedirectURI(), "/deviceauth/callback") {
					t.Errorf("RedirectURI() = %q, want suffix /deviceauth/callback", c.RedirectURI())
				}
			},
		},
		"rejects non-http issuer scheme": {
			config:  Config{Issuer: "ftp://nope"},
			wantErr: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			c, err := NewClient(tc.config)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NewClient(%+v) = nil error, want error", tc.config)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			if tc.check != nil {
				tc.check(t, c)
			}
		})
	}
}
