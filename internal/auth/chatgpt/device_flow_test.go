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

func TestRequestDeviceCode_Errors(t *testing.T) {
	tests := map[string]struct {
		handler http.HandlerFunc
		// wantErrCheck asserts on the returned error. Required (non-nil).
		wantErrCheck func(t *testing.T, err error)
	}{
		"404 is surfaced as 'not enabled'": {
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "not found", http.StatusNotFound)
			},
			wantErrCheck: func(t *testing.T, err error) {
				if err == nil || !strings.Contains(err.Error(), "not enabled") {
					t.Fatalf("err = %v, want substring 'not enabled'", err)
				}
			},
		},
		"5xx wraps as errBadStatus": {
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "boom", http.StatusInternalServerError)
			},
			wantErrCheck: func(t *testing.T, err error) {
				var bad *errBadStatus
				if !errors.As(err, &bad) {
					t.Fatalf("err = %v, want *errBadStatus", err)
				}
				if bad.Status != http.StatusInternalServerError {
					t.Fatalf("Status = %d, want 500", bad.Status)
				}
			},
		},
		"missing device_auth_id errors": {
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"user_code":"UC","interval":"5"}`))
			},
			wantErrCheck: func(t *testing.T, err error) {
				if err == nil {
					t.Fatal("err = nil, want error")
				}
			},
		},
		"missing user_code errors": {
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"device_auth_id":"d","interval":"5"}`))
			},
			wantErrCheck: func(t *testing.T, err error) {
				if err == nil {
					t.Fatal("err = nil, want error")
				}
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			c := newTestClient(t, srv)
			_, err := c.RequestDeviceCode(context.Background())
			tc.wantErrCheck(t, err)
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

func TestPollOnce_Pending(t *testing.T) {
	const pendingBody = `{"error":"authorization_pending"}`
	const slowDownBody = `{"error":"slow_down"}`

	tests := map[string]struct {
		status int
		body   string
	}{
		// Status-driven: any of these statuses paired with a pending body
		// must classify as pending.
		"400 authorization_pending": {status: http.StatusBadRequest, body: pendingBody},
		"403 authorization_pending": {status: http.StatusForbidden, body: pendingBody},
		"404 authorization_pending": {status: http.StatusNotFound, body: pendingBody},
		// Body-code-driven: RFC 8628 slow_down is also pending, regardless of
		// the status the server picked.
		"400 slow_down": {status: http.StatusBadRequest, body: slowDownBody},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := newTestClient(t, srv)
			dc := deviceCodeForTest("dev", "USER", "", 5*time.Millisecond)
			if _, err := c.PollOnce(context.Background(), dc); !errors.Is(err, ErrAuthorizationPending) {
				t.Fatalf("status=%d body=%s: err = %v, want ErrAuthorizationPending", tc.status, tc.body, err)
			}
		})
	}
}

func TestPollOnce_Errors(t *testing.T) {
	tests := map[string]struct {
		handler http.HandlerFunc
		check   func(t *testing.T, err error)
	}{
		"404 with non-pending body is errBadStatus not pending": {
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"device_code_expired"}`))
			},
			check: func(t *testing.T, err error) {
				if errors.Is(err, ErrAuthorizationPending) {
					t.Fatalf("err = %v, must not be ErrAuthorizationPending when body is device_code_expired", err)
				}
				var bad *errBadStatus
				if !errors.As(err, &bad) {
					t.Fatalf("err = %v, want *errBadStatus", err)
				}
			},
		},
		"5xx is errBadStatus": {
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "oops", http.StatusInternalServerError)
			},
			check: func(t *testing.T, err error) {
				var bad *errBadStatus
				if !errors.As(err, &bad) || bad.Status != http.StatusInternalServerError {
					t.Fatalf("err = %v, want *errBadStatus with Status=500", err)
				}
			},
		},
		"missing code_verifier errors": {
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"authorization_code":"a"}`))
			},
			check: func(t *testing.T, err error) {
				if err == nil {
					t.Fatal("err = nil, want error")
				}
			},
		},
		"missing authorization_code errors": {
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"code_verifier":"v"}`))
			},
			check: func(t *testing.T, err error) {
				if err == nil {
					t.Fatal("err = nil, want error")
				}
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			c := newTestClient(t, srv)
			dc := deviceCodeForTest("dev", "USER", "", 5*time.Millisecond)
			_, err := c.PollOnce(context.Background(), dc)
			tc.check(t, err)
		})
	}
}

func TestPollUntilCode(t *testing.T) {
	tests := map[string]struct {
		handlerFactory func(calls *atomic.Int32) http.HandlerFunc
		configOpts     []func(*Config)
		// ctxFactory returns the ctx used for PollUntilCode and a cancel to
		// defer. If nil, context.Background() is used.
		ctxFactory func(t *testing.T) (context.Context, context.CancelFunc)
		// wantTicks >= 0 asserts onTick was invoked exactly this many times.
		// -1 means "do not check ticks".
		wantTicks int32
		// wantCalls >= 1 asserts handler was invoked exactly this many times.
		// 0 means "do not check call count".
		wantCalls int32
		// wantCode is the expected AuthorizationCode.Code ("" = don't check).
		wantCode string
		// check runs last; receives err verbatim from PollUntilCode.
		check func(t *testing.T, err error)
	}{
		"succeeds after two pending polls": {
			handlerFactory: func(calls *atomic.Int32) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					n := calls.Add(1)
					if n < 3 {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
						return
					}
					_, _ = w.Write([]byte(`{"authorization_code":"a","code_challenge":"c","code_verifier":"v"}`))
				}
			},
			wantTicks: 2,
			wantCalls: 3,
			wantCode:  "a",
			check: func(t *testing.T, err error) {
				if err != nil {
					t.Fatalf("PollUntilCode: %v", err)
				}
			},
		},
		"respects pre-cancelled context": {
			handlerFactory: func(calls *atomic.Int32) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
				}
			},
			ctxFactory: func(t *testing.T) (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			},
			wantTicks: -1,
			check: func(t *testing.T, err error) {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("err = %v, want context.Canceled", err)
				}
			},
		},
		"times out via PollTimeout when parent has no deadline": {
			handlerFactory: func(calls *atomic.Int32) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
				}
			},
			configOpts: []func(*Config){
				func(cfg *Config) {
					cfg.PollTimeout = 200 * time.Millisecond
					cfg.PollInterval = 20 * time.Millisecond
				},
			},
			wantTicks: -1,
			check: func(t *testing.T, err error) {
				if !errors.Is(err, ErrDeviceAuthTimeout) {
					t.Fatalf("err = %v, want ErrDeviceAuthTimeout", err)
				}
			},
		},
		"non-pending error returns immediately on first call": {
			handlerFactory: func(calls *atomic.Int32) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					http.Error(w, "boom", http.StatusInternalServerError)
				}
			},
			wantTicks: -1,
			wantCalls: 1,
			check: func(t *testing.T, err error) {
				var bad *errBadStatus
				if !errors.As(err, &bad) {
					t.Fatalf("err = %v, want *errBadStatus", err)
				}
			},
		},
		"parent deadline wins over PollTimeout": {
			handlerFactory: func(calls *atomic.Int32) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
				}
			},
			configOpts: []func(*Config){
				func(cfg *Config) {
					cfg.PollTimeout = time.Second
					cfg.PollInterval = 5 * time.Millisecond
				},
			},
			ctxFactory: func(t *testing.T) (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 20*time.Millisecond)
			},
			wantTicks: -1,
			check: func(t *testing.T, err error) {
				if errors.Is(err, ErrDeviceAuthTimeout) {
					t.Fatalf("err = %v, parent deadline must surface as ctx.Err(), not ErrDeviceAuthTimeout", err)
				}
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("err = %v, want context.DeadlineExceeded", err)
				}
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(tc.handlerFactory(&calls))
			defer srv.Close()

			c := newTestClient(t, srv, tc.configOpts...)
			dc := deviceCodeForTest("dev", "USER", c.VerificationURL(), time.Millisecond)

			ctx := context.Background()
			if tc.ctxFactory != nil {
				var cancel context.CancelFunc
				ctx, cancel = tc.ctxFactory(t)
				defer cancel()
			}

			var ticks atomic.Int32
			ac, err := c.PollUntilCode(ctx, dc, func() { ticks.Add(1) })
			tc.check(t, err)

			if tc.wantCode != "" {
				if ac == nil {
					t.Fatalf("ac = nil, want code %q", tc.wantCode)
				}
				if ac.Code != tc.wantCode {
					t.Fatalf("Code = %q, want %q", ac.Code, tc.wantCode)
				}
			}
			if tc.wantTicks >= 0 {
				if got := ticks.Load(); got != tc.wantTicks {
					t.Errorf("onTick calls = %d, want %d", got, tc.wantTicks)
				}
			}
			if tc.wantCalls > 0 {
				if got := calls.Load(); got != tc.wantCalls {
					t.Errorf("handler calls = %d, want %d", got, tc.wantCalls)
				}
			}
		})
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
