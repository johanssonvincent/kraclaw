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
	t.Parallel()
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
					t.Errorf("Interval = %v, want 3s", dc.Interval)
				}
			},
		},
		"usercode alias (no underscore)": {
			body: `{"device_auth_id":"d","usercode":"ABCD","interval":"5"}`,
			check: func(t *testing.T, dc *DeviceCode) {
				if dc.UserCode != "ABCD" {
					t.Errorf("UserCode = %q, want ABCD via alias", dc.UserCode)
				}
			},
		},
		"interval null falls back to default": {
			body: `{"device_auth_id":"d","user_code":"UC","interval":null}`,
			check: func(t *testing.T, dc *DeviceCode) {
				if dc.Interval != DefaultPollInterval {
					t.Errorf("Interval = %v, want DefaultPollInterval %v", dc.Interval, DefaultPollInterval)
				}
			},
		},
		"interval empty string falls back to default": {
			body: `{"device_auth_id":"d","user_code":"UC","interval":""}`,
			check: func(t *testing.T, dc *DeviceCode) {
				if dc.Interval != DefaultPollInterval {
					t.Errorf("Interval = %v, want DefaultPollInterval %v", dc.Interval, DefaultPollInterval)
				}
			},
		},
		"interval field missing falls back to default": {
			body: `{"device_auth_id":"d","user_code":"UC"}`,
			check: func(t *testing.T, dc *DeviceCode) {
				if dc.Interval != DefaultPollInterval {
					t.Errorf("Interval = %v, want DefaultPollInterval %v", dc.Interval, DefaultPollInterval)
				}
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
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
	t.Parallel()
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
					t.Errorf("err = %v, want substring 'not enabled'", err)
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
					t.Errorf("Status = %d, want 500", bad.Status)
				}
			},
		},
		"missing device_auth_id errors": {
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"user_code":"UC","interval":"5"}`))
			},
			wantErrCheck: func(t *testing.T, err error) {
				if err == nil {
					t.Error("err = nil, want error")
				}
			},
		},
		"missing user_code errors": {
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"device_auth_id":"d","interval":"5"}`))
			},
			wantErrCheck: func(t *testing.T, err error) {
				if err == nil {
					t.Error("err = nil, want error")
				}
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			c := newTestClient(t, srv)
			_, err := c.RequestDeviceCode(context.Background())
			tc.wantErrCheck(t, err)
		})
	}
}

func TestPollOnce_Success(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		respBody     string
		wantCode     string
		wantVerifier string
	}{
		"full triple": {
			respBody:     `{"authorization_code":"ac_1","code_challenge":"cc_1","code_verifier":"cv_1"}`,
			wantCode:     "ac_1",
			wantVerifier: "cv_1",
		},
		"no code_challenge": {
			respBody:     `{"authorization_code":"ac_2","code_verifier":"cv_2"}`,
			wantCode:     "ac_2",
			wantVerifier: "cv_2",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/accounts/deviceauth/token" {
					t.Errorf("path = %q, want /api/accounts/deviceauth/token", r.URL.Path)
				}
				body, _ := io.ReadAll(r.Body)
				var got map[string]string
				_ = json.Unmarshal(body, &got)
				if got["device_auth_id"] != "dev" || got["user_code"] != "USER" {
					t.Errorf("body = %q, want device_auth_id=dev & user_code=USER", body)
				}
				_, _ = w.Write([]byte(tc.respBody))
			}))
			defer srv.Close()

			c := newTestClient(t, srv)
			dc := deviceCodeForTest("dev", "USER", c.VerificationURL(), 5*time.Millisecond)

			ac, err := c.PollOnce(context.Background(), dc)
			if err != nil {
				t.Fatalf("PollOnce(%q) err = %v, want nil", tc.respBody, err)
			}
			if ac.Code != tc.wantCode {
				t.Errorf("PollOnce(%q) Code = %q, want %q", tc.respBody, ac.Code, tc.wantCode)
			}
			if ac.CodeVerifier != tc.wantVerifier {
				t.Errorf("PollOnce(%q) CodeVerifier = %q, want %q", tc.respBody, ac.CodeVerifier, tc.wantVerifier)
			}
		})
	}
}

func TestPollOnce_Pending(t *testing.T) {
	t.Parallel()
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
		"403 nested deviceauth_authorization_unknown": {
			status: http.StatusForbidden,
			body:   `{"error":{"message":"Device authorization is unknown. Please try again.","type":"invalid_request_error","param":null,"code":"deviceauth_authorization_unknown"}}`,
		},
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
			dc := deviceCodeForTest("dev", "USER", "", 5*time.Millisecond)
			if _, err := c.PollOnce(context.Background(), dc); !errors.Is(err, ErrAuthorizationPending) {
				t.Errorf("status=%d body=%s: err = %v, want ErrAuthorizationPending", tc.status, tc.body, err)
			}
		})
	}
}

func TestPollOnce_Errors(t *testing.T) {
	t.Parallel()
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
					t.Errorf("err = %v, must not be ErrAuthorizationPending when body is device_code_expired", err)
				}
				var bad *errBadStatus
				if !errors.As(err, &bad) {
					t.Errorf("err = %v, want *errBadStatus", err)
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
					t.Errorf("err = %v, want *errBadStatus with Status=500", err)
				}
			},
		},
		"missing code_verifier errors": {
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"authorization_code":"a"}`))
			},
			check: func(t *testing.T, err error) {
				if err == nil {
					t.Error("err = nil, want error")
				}
			},
		},
		"missing authorization_code errors": {
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"code_verifier":"v"}`))
			},
			check: func(t *testing.T, err error) {
				if err == nil {
					t.Error("err = nil, want error")
				}
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
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
	t.Parallel()
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
					t.Errorf("err = %v, want context.Canceled", err)
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
					t.Errorf("err = %v, want ErrDeviceAuthTimeout", err)
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
					t.Errorf("err = %v, want *errBadStatus", err)
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
					t.Errorf("err = %v, parent deadline must surface as ctx.Err(), not ErrDeviceAuthTimeout", err)
				}
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("err = %v, want context.DeadlineExceeded", err)
				}
			},
		},
		// RFC 8628 §3.5: on slow_down the client MUST widen the poll interval
		// by 5 seconds. Handler returns pending, then slow_down, then success;
		// the gap between the slow_down response and the next poll must be at
		// least slowDownBackoff.
		"slow_down widens the poll interval per RFC 8628 §3.5": {
			handlerFactory: func(calls *atomic.Int32) http.HandlerFunc {
				var slowDownAt atomic.Int64
				return func(w http.ResponseWriter, r *http.Request) {
					n := calls.Add(1)
					switch n {
					case 1:
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
					case 2:
						slowDownAt.Store(time.Now().UnixNano())
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte(`{"error":"slow_down"}`))
					default:
						gap := time.Duration(time.Now().UnixNano() - slowDownAt.Load())
						if gap < 4500*time.Millisecond {
							t.Errorf("gap between slow_down response and next poll = %v, want >= 4.5s per RFC 8628 §3.5", gap)
						}
						_, _ = w.Write([]byte(`{"authorization_code":"a","code_challenge":"c","code_verifier":"v"}`))
					}
				}
			},
			configOpts: []func(*Config){
				func(cfg *Config) {
					cfg.PollTimeout = 10 * time.Second
					cfg.PollInterval = 5 * time.Millisecond
				},
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
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
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
					t.Errorf("Code = %q, want %q", ac.Code, tc.wantCode)
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
	t.Parallel()
	tests := map[string]struct {
		input    *AuthorizationCode
		respBody func(t *testing.T) string
		check    func(t *testing.T, tokens *Tokens)
	}{
		"id_token exp populates ExpiresAt": {
			input: &AuthorizationCode{Code: "ac_1", CodeVerifier: "cv_1"},
			respBody: func(t *testing.T) string {
				idTok := mintJWT(t, map[string]any{
					"exp": time.Now().Add(time.Hour).Unix(),
					"https://api.openai.com/auth": map[string]any{
						"chatgpt_account_id": "acct_42",
						"chatgpt_plan_type":  "plus",
					},
				})
				return `{"id_token":"` + idTok + `","access_token":"acc_1","refresh_token":"ref_1"}`
			},
			check: func(t *testing.T, tokens *Tokens) {
				if tokens.AccessToken != "acc_1" {
					t.Errorf("AccessToken = %q, want acc_1", tokens.AccessToken)
				}
				if tokens.RefreshToken != "ref_1" {
					t.Errorf("RefreshToken = %q, want ref_1", tokens.RefreshToken)
				}
				if tokens.IDClaims.AccountID != "acct_42" {
					t.Errorf("IDClaims.AccountID = %q, want acct_42", tokens.IDClaims.AccountID)
				}
				if tokens.IDClaims.PlanType != "plus" {
					t.Errorf("IDClaims.PlanType = %q, want plus", tokens.IDClaims.PlanType)
				}
				if tokens.ExpiresAt.IsZero() {
					t.Errorf("ExpiresAt = zero, want populated from id_token exp")
				}
				if !tokens.HasExpiry() {
					t.Errorf("HasExpiry() = false, want true when id_token exp is present")
				}
			},
		},
		"expires_in fallback when id_token has no exp": {
			input: &AuthorizationCode{Code: "ac_2", CodeVerifier: "cv_2"},
			respBody: func(t *testing.T) string {
				idTok := mintJWT(t, map[string]any{
					"https://api.openai.com/auth": map[string]any{
						"chatgpt_account_id": "acct_7",
					},
				})
				return `{"id_token":"` + idTok + `","access_token":"acc_2","refresh_token":"ref_2","expires_in":600}`
			},
			check: func(t *testing.T, tokens *Tokens) {
				if tokens.ExpiresAt.IsZero() {
					t.Errorf("ExpiresAt = zero, want populated from expires_in")
				}
				if !tokens.HasExpiry() {
					t.Errorf("HasExpiry() = false, want true when expires_in is present")
				}
			},
		},
		"no exp and no expires_in yields HasExpiry=false": {
			input: &AuthorizationCode{Code: "ac_3", CodeVerifier: "cv_3"},
			respBody: func(t *testing.T) string {
				idTok := mintJWT(t, map[string]any{
					"https://api.openai.com/auth": map[string]any{
						"chatgpt_account_id": "acct_9",
					},
				})
				return `{"id_token":"` + idTok + `","access_token":"acc_3","refresh_token":"ref_3"}`
			},
			check: func(t *testing.T, tokens *Tokens) {
				if tokens.HasExpiry() {
					t.Errorf("HasExpiry() = true, want false when id_token has no exp and response has no expires_in")
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
					t.Errorf("path = %q, want /oauth/token", r.URL.Path)
				}
				if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
					t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", got)
				}
				body, _ := io.ReadAll(r.Body)
				form, err := url.ParseQuery(string(body))
				if err != nil {
					t.Fatalf("parse form %q: %v", body, err)
				}
				if got := form.Get("grant_type"); got != "authorization_code" {
					t.Errorf("grant_type = %q, want authorization_code", got)
				}
				if got := form.Get("code"); got != tc.input.Code {
					t.Errorf("code = %q, want %q", got, tc.input.Code)
				}
				if got := form.Get("code_verifier"); got != tc.input.CodeVerifier {
					t.Errorf("code_verifier = %q, want %q", got, tc.input.CodeVerifier)
				}
				if got := form.Get("client_id"); got != ClientID {
					t.Errorf("client_id = %q, want %q", got, ClientID)
				}
				if got := form.Get("redirect_uri"); !strings.HasSuffix(got, "/deviceauth/callback") {
					t.Errorf("redirect_uri = %q, want suffix /deviceauth/callback", got)
				}
				_, _ = w.Write([]byte(tc.respBody(t)))
			}))
			defer srv.Close()

			c := newTestClient(t, srv)
			tokens, err := c.ExchangeCode(context.Background(), tc.input)
			if err != nil {
				t.Fatalf("ExchangeCode: %v", err)
			}
			tc.check(t, tokens)
		})
	}
}

func TestExchangeCode_Errors(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		handler   http.HandlerFunc
		input     *AuthorizationCode
		errSubstr string
	}{
		"empty code": {
			input: &AuthorizationCode{},
		},
		"non-2xx": {
			input:   &AuthorizationCode{Code: "a", CodeVerifier: "v"},
			handler: func(w http.ResponseWriter, r *http.Request) { http.Error(w, "bad", http.StatusBadRequest) },
		},
		"missing access_token": {
			input: &AuthorizationCode{Code: "a", CodeVerifier: "v"},
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"id_token":"x.y.z","refresh_token":"r"}`))
			},
			errSubstr: "missing access_token",
		},
		"missing refresh_token": {
			input: &AuthorizationCode{Code: "a", CodeVerifier: "v"},
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"access_token":"a","id_token":"x.y.z"}`))
			},
			errSubstr: "missing refresh_token",
		},
		"missing id_token": {
			input: &AuthorizationCode{Code: "a", CodeVerifier: "v"},
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r"}`))
			},
			errSubstr: "missing id_token",
		},
		"malformed id_token": {
			input: &AuthorizationCode{Code: "a", CodeVerifier: "v"},
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r","id_token":"nope"}`))
			},
			errSubstr: "parse id_token",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var srv *httptest.Server
			if tc.handler != nil {
				srv = httptest.NewServer(tc.handler)
			} else {
				srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Error("handler must not be called; ExchangeCode must short-circuit on invalid input")
				}))
			}
			defer srv.Close()
			c := newTestClient(t, srv)
			_, err := c.ExchangeCode(context.Background(), tc.input)
			if err == nil {
				t.Fatal("err = nil, want error")
			}
			if tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
				t.Errorf("err = %q, want substring %q", err, tc.errSubstr)
			}
		})
	}
}

func TestNewClient(t *testing.T) {
	t.Parallel()
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
			t.Parallel()
			c, err := NewClient(tc.config)
			if tc.wantErr {
				if err == nil {
					t.Errorf("NewClient(%+v) = nil error, want error", tc.config)
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

func TestPollUntilCode_CancelDuringSleep_ReturnsPromptly(t *testing.T) {
	t.Parallel()

	// Handler always returns authorization_pending.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(Config{
		Issuer:       srv.URL,
		PollInterval: 5 * time.Second, // long enough that the test fails if we wait it out
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel ~20ms after PollUntilCode starts — the first PollOnce will
	// return authorization_pending, then the loop enters its 5s sleep, and
	// our cancel must interrupt it.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	dc := &DeviceCode{
		deviceAuthID: "did",
		UserCode:     "uc",
		Interval:     5 * time.Second,
	}
	start := time.Now()
	_, err = c.PollUntilCode(ctx, dc, nil)
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("PollUntilCode took %v, want < 500ms after ctx cancel mid-sleep", elapsed)
	}
}

func TestPollOnce_AccessDeniedReturnsSentinel(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		status int
		body   string
		want   error
	}{
		"400 access_denied": {status: 400, body: `{"error":"access_denied"}`, want: ErrAccessDenied},
		"403 access_denied": {status: 403, body: `{"error":"access_denied"}`, want: ErrAccessDenied},
		"400 expired_token": {status: 400, body: `{"error":"expired_token"}`, want: ErrAccessDenied},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			c, err := NewClient(Config{Issuer: srv.URL, HTTPClient: srv.Client()})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			dc := deviceCodeForTest("dc", "UC", srv.URL, 5*time.Millisecond)
			_, got := c.PollOnce(context.Background(), dc)
			if !errors.Is(got, tt.want) {
				t.Errorf("PollOnce(%s) error = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}

func TestExchangeCode_AccessDeniedReturnsSentinel(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		status int
		body   string
	}{
		"400 access_denied": {status: 400, body: `{"error":"access_denied"}`},
		"400 expired_token": {status: 400, body: `{"error":"expired_token"}`},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			c, err := NewClient(Config{Issuer: srv.URL, HTTPClient: srv.Client()})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			_, got := c.ExchangeCode(context.Background(), &AuthorizationCode{Code: "x", CodeVerifier: "v"})
			if !errors.Is(got, ErrAccessDenied) {
				t.Errorf("ExchangeCode body=%q error = %v, want ErrAccessDenied", tt.body, got)
			}
		})
	}
}

func TestPollTerminalCode(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		status          int
		body            []byte
		wantTerminal    error
		wantParseErrSet bool
	}{
		"in-range access_denied returns sentinel": {
			status:          http.StatusBadRequest,
			body:            []byte(`{"error":"access_denied"}`),
			wantTerminal:    ErrAccessDenied,
			wantParseErrSet: false,
		},
		"in-range expired_token returns sentinel": {
			status:          http.StatusForbidden,
			body:            []byte(`{"error":"expired_token"}`),
			wantTerminal:    ErrAccessDenied,
			wantParseErrSet: false,
		},
		"nested access_denied returns sentinel": {
			status:          http.StatusForbidden,
			body:            []byte(`{"error":{"code":"access_denied"}}`),
			wantTerminal:    ErrAccessDenied,
			wantParseErrSet: false,
		},
		"in-range pending code is not terminal": {
			status:          http.StatusBadRequest,
			body:            []byte(`{"error":"authorization_pending"}`),
			wantTerminal:    nil,
			wantParseErrSet: false,
		},
		"in-range garbage body returns parse error": {
			status:          http.StatusBadRequest,
			body:            []byte("<html>nginx 502</html>"),
			wantTerminal:    nil,
			wantParseErrSet: true,
		},
		"out-of-range status returns nil/nil even on garbage": {
			status:          http.StatusInternalServerError,
			body:            []byte("<html>nginx 502</html>"),
			wantTerminal:    nil,
			wantParseErrSet: false,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			gotTerminal, gotParseErr := pollTerminalCode(tt.status, tt.body)
			if !errors.Is(gotTerminal, tt.wantTerminal) {
				t.Errorf("pollTerminalCode(%d, %q) terminal = %v, want %v",
					tt.status, string(tt.body), gotTerminal, tt.wantTerminal)
			}
			if (gotParseErr != nil) != tt.wantParseErrSet {
				t.Errorf("pollTerminalCode(%d, %q) parseErr = %v, want parseErrSet=%v",
					tt.status, string(tt.body), gotParseErr, tt.wantParseErrSet)
			}
		})
	}
}

func TestPollPendingCode(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		status          int
		body            []byte
		wantCode        string
		wantParseErrSet bool
	}{
		"in-range authorization_pending": {
			status:          http.StatusBadRequest,
			body:            []byte(`{"error":"authorization_pending"}`),
			wantCode:        "authorization_pending",
			wantParseErrSet: false,
		},
		"in-range slow_down": {
			status:          http.StatusBadRequest,
			body:            []byte(`{"error":"slow_down"}`),
			wantCode:        "slow_down",
			wantParseErrSet: false,
		},
		"nested deviceauth_authorization_unknown": {
			status:          http.StatusForbidden,
			body:            []byte(`{"error":{"message":"Device authorization is unknown. Please try again.","type":"invalid_request_error","param":null,"code":"deviceauth_authorization_unknown"}}`),
			wantCode:        "deviceauth_authorization_unknown",
			wantParseErrSet: false,
		},
		"in-range terminal code is not pending": {
			status:          http.StatusBadRequest,
			body:            []byte(`{"error":"access_denied"}`),
			wantCode:        "",
			wantParseErrSet: false,
		},
		"in-range garbage body returns parse error": {
			status:          http.StatusBadRequest,
			body:            []byte("<html>nginx 502</html>"),
			wantCode:        "",
			wantParseErrSet: true,
		},
		"out-of-range status returns empty/nil even on garbage": {
			status:          http.StatusInternalServerError,
			body:            []byte("<html>nginx 502</html>"),
			wantCode:        "",
			wantParseErrSet: false,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			gotCode, gotParseErr := pollPendingCode(tt.status, tt.body)
			if gotCode != tt.wantCode {
				t.Errorf("pollPendingCode(%d, %q) code = %q, want %q",
					tt.status, string(tt.body), gotCode, tt.wantCode)
			}
			if (gotParseErr != nil) != tt.wantParseErrSet {
				t.Errorf("pollPendingCode(%d, %q) parseErr = %v, want parseErrSet=%v",
					tt.status, string(tt.body), gotParseErr, tt.wantParseErrSet)
			}
		})
	}
}
