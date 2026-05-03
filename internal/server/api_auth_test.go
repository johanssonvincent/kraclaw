package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/johanssonvincent/kraclaw/internal/auth/chatgpt"
	"github.com/johanssonvincent/kraclaw/internal/credproxy"
	"github.com/johanssonvincent/kraclaw/internal/provider"
	kraclawv1 "github.com/johanssonvincent/kraclaw/pkg/pb/kraclawv1"
)

// issuerMode selects the behaviour of the test fake issuer:
//
//	"approve":          usercode -> immediate authorization_code -> token bundle
//	"deny":             usercode OK, then poll/exchange endpoint returns 400 access_denied (ErrAccessDenied)
//	"slow_approve":     authorization_pending twice, then approves on the third poll
//	"unknown_pending":  OpenAI nested deviceauth_authorization_unknown, then approves
//	"5xx_during_poll":  usercode OK, then poll endpoint returns 502 (errBadStatus → INTERNAL)
type issuerMode string

const (
	issuerModeApprove        issuerMode = "approve"
	issuerModeDeny           issuerMode = "deny"
	issuerModeSlowApprove    issuerMode = "slow_approve"
	issuerModeUnknownPending issuerMode = "unknown_pending"
	issuerMode5xxDuringPoll  issuerMode = "5xx_during_poll"
)

func mintTestJWT(t *testing.T, payload map[string]any) string {
	t.Helper()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal jwt header: %v", err)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal jwt payload: %v", err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(headerJSON) + "." + enc.EncodeToString(payloadJSON) + ".sig"
}

func newFakeIssuer(t *testing.T, mode issuerMode, idTokenJWT string, expiresIn int64) *httptest.Server {
	t.Helper()
	var pollCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"device_auth_id":"dev_xyz","user_code":"USER-1234","interval":"0.005"}`))
		case "/api/accounts/deviceauth/token":
			n := int(pollCalls.Add(1))
			switch mode {
			case issuerModeDeny:
				// Return 400 access_denied so pollTerminalCode catches it as ErrAccessDenied.
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"access_denied"}`))
				return
			case issuerModeSlowApprove:
				// First two calls return authorization_pending; third approves.
				if n < 3 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
					return
				}
			case issuerModeUnknownPending:
				if n < 2 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"error":{"message":"Device authorization is unknown. Please try again.","type":"invalid_request_error","param":null,"code":"deviceauth_authorization_unknown"}}`))
					return
				}
			case issuerMode5xxDuringPoll:
				// Subsequent /api/accounts/deviceauth/token poll fails with
				// 502; pollOnce wraps this as *errBadStatus, which falls
				// through errCodeFor's default → INTERNAL.
				http.Error(w, "bad gateway", http.StatusBadGateway)
				return
			}
			// approve / slow_approve after 2 pending: return the authorization_code.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"authorization_code":"ac_1","code_verifier":"cv_1"}`))
		case "/oauth/token":
			if mode == issuerModeDeny {
				// ExchangeCode also uses pollTerminalCode on 400 responses.
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"access_denied"}`))
				return
			}
			resp := map[string]any{
				"id_token":      idTokenJWT,
				"access_token":  "access_42",
				"refresh_token": "refresh_42",
				"expires_in":    expiresIn,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		default:
			t.Errorf("unexpected fake issuer path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// startAuthGRPC wires the authService onto a bufconn-backed gRPC server and
// returns a connected client plus a cleanup func.
func startAuthGRPC(t *testing.T, svc *authService) (kraclawv1.AuthServiceClient, func()) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	kraclawv1.RegisterAuthServiceServer(srv, svc)

	go func() {
		if err := srv.Serve(lis); err != nil {
			t.Logf("grpc serve: %v", err)
		}
	}()

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	cleanup := func() {
		_ = conn.Close()
		srv.Stop()
		_ = lis.Close()
	}
	return kraclawv1.NewAuthServiceClient(conn), cleanup
}

// collectEvents drains the stream until io.EOF or the client deadline fires
// and returns the names of the events it saw (deviceCode/tick/success/error)
// plus the terminal event so the caller can introspect codes.
func collectEvents(t *testing.T, stream grpc.ServerStreamingClient[kraclawv1.DeviceAuthEvent]) (eventNames []string, terminal *kraclawv1.DeviceAuthEvent) {
	t.Helper()
	for {
		evt, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return eventNames, terminal
		}
		if err != nil {
			// Surface unexpected transport errors but stop the loop.
			t.Logf("stream.Recv: %v", err)
			return eventNames, terminal
		}
		switch evt.GetEvent().(type) {
		case *kraclawv1.DeviceAuthEvent_DeviceCode_:
			eventNames = append(eventNames, "device_code")
		case *kraclawv1.DeviceAuthEvent_Tick_:
			eventNames = append(eventNames, "tick")
		case *kraclawv1.DeviceAuthEvent_Success_:
			eventNames = append(eventNames, "success")
			terminal = evt
		case *kraclawv1.DeviceAuthEvent_Error_:
			eventNames = append(eventNames, "error")
			terminal = evt
		}
	}
}

// equalSeqIgnoringTickCount collapses runs of consecutive "tick" events to a
// single "tick" entry before comparing the slices.
func equalSeqIgnoringTickCount(got, want []string) bool {
	collapse := func(in []string) []string {
		out := make([]string, 0, len(in))
		for _, name := range in {
			if name == "tick" && len(out) > 0 && out[len(out)-1] == "tick" {
				continue
			}
			out = append(out, name)
		}
		return out
	}
	a := collapse(got)
	b := collapse(want)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// equalSeqAtLeastOneTick verifies that got matches want with the constraint
// that every "tick" entry in want must match >= 1 consecutive "tick" entries
// in got. All other entries must match exactly.
func equalSeqAtLeastOneTick(got, want []string) bool {
	si, wi := 0, 0
	for si < len(got) && wi < len(want) {
		if want[wi] == "tick" {
			matched := 0
			for si < len(got) && got[si] == "tick" {
				si++
				matched++
			}
			if matched < 1 {
				return false
			}
			wi++
		} else {
			if got[si] != want[wi] {
				return false
			}
			si++
			wi++
		}
	}
	return si == len(got) && wi == len(want)
}

func TestAuthService_StartChatGPTDeviceAuth_Streaming(t *testing.T) {
	t.Parallel()

	expFuture := time.Now().Add(45 * time.Minute).UTC().Truncate(time.Second).Unix()
	idTok := mintTestJWT(t, map[string]any{
		"email": "user@example.com",
		"exp":   expFuture,
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id":         "acct_99",
			"chatgpt_user_id":            "user_42",
			"chatgpt_account_is_fedramp": false,
		},
	})

	tests := map[string]struct {
		issuerMode    issuerMode
		req           *kraclawv1.StartChatGPTDeviceAuthRequest
		expectUpsert  bool
		wantSeq       []string
		wantErrCode   kraclawv1.DeviceAuthEvent_ErrorCode
		wantAccountID string
		matchSeq      func([]string, []string) bool
	}{
		"success path: approves immediately, persists tokens": {
			issuerMode: issuerModeApprove,
			req: &kraclawv1.StartChatGPTDeviceAuthRequest{
				GroupJid: "discord:42",
				Provider: provider.ProviderOpenAI,
			},
			expectUpsert:  true,
			wantSeq:       []string{"device_code", "success"},
			wantAccountID: "acct_99",
		},
		"unknown provider yields INVALID_ARGUMENT error before issuer is contacted": {
			issuerMode: issuerModeApprove,
			req: &kraclawv1.StartChatGPTDeviceAuthRequest{
				GroupJid: "discord:42",
				Provider: "no-such",
			},
			wantSeq:     []string{"error"},
			wantErrCode: kraclawv1.DeviceAuthEvent_INVALID_ARGUMENT,
		},
		"non-chatgpt provider yields INVALID_ARGUMENT error": {
			issuerMode: issuerModeApprove,
			req: &kraclawv1.StartChatGPTDeviceAuthRequest{
				GroupJid: "discord:42",
				Provider: provider.ProviderAnthropic,
			},
			wantSeq:     []string{"error"},
			wantErrCode: kraclawv1.DeviceAuthEvent_INVALID_ARGUMENT,
		},
		"deny path: poll 400 access_denied yields ACCESS_DENIED": {
			issuerMode: issuerModeDeny,
			req: &kraclawv1.StartChatGPTDeviceAuthRequest{
				GroupJid: "tui:g",
				Provider: provider.ProviderOpenAI,
			},
			wantSeq:     []string{"device_code", "error"},
			wantErrCode: kraclawv1.DeviceAuthEvent_ACCESS_DENIED,
		},
		"slow approval emits at least one tick before success": {
			issuerMode: issuerModeSlowApprove,
			req: &kraclawv1.StartChatGPTDeviceAuthRequest{
				GroupJid: "tui:g",
				Provider: provider.ProviderOpenAI,
			},
			wantSeq:       []string{"device_code", "tick", "success"},
			wantAccountID: "acct_99",
			expectUpsert:  true,
			matchSeq:      equalSeqAtLeastOneTick,
		},
		"OpenAI unknown device auth poll waits before success": {
			issuerMode: issuerModeUnknownPending,
			req: &kraclawv1.StartChatGPTDeviceAuthRequest{
				GroupJid: "tui:g",
				Provider: provider.ProviderOpenAI,
			},
			wantSeq:       []string{"device_code", "tick", "success"},
			wantAccountID: "acct_99",
			expectUpsert:  true,
			matchSeq:      equalSeqAtLeastOneTick,
		},
		"issuer returns 5xx mid-poll → INTERNAL": {
			issuerMode: issuerMode5xxDuringPoll,
			req: &kraclawv1.StartChatGPTDeviceAuthRequest{
				GroupJid: "tui:g",
				Provider: provider.ProviderOpenAI,
			},
			wantSeq:     []string{"device_code", "error"},
			wantErrCode: kraclawv1.DeviceAuthEvent_INTERNAL,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			issuer := newFakeIssuer(t, tt.issuerMode, idTok, 2700)

			client, err := chatgpt.NewClient(chatgpt.Config{
				Issuer:       issuer.URL,
				HTTPClient:   issuer.Client(),
				PollInterval: 5 * time.Millisecond,
				PollTimeout:  500 * time.Millisecond,
				Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
			})
			if err != nil {
				t.Fatalf("chatgpt.NewClient: %v", err)
			}

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })

			enc, err := credproxy.NewEncryptor(strings.Repeat("ab", 32))
			if err != nil {
				t.Fatalf("NewEncryptor: %v", err)
			}
			// Timezone probe runs once on NewCredentialStore.
			mock.ExpectQuery("SELECT TIMESTAMP").WillReturnRows(
				sqlmock.NewRows([]string{"t"}).AddRow(time.Now().UTC().Truncate(time.Second)),
			)
			store, err := credproxy.NewCredentialStore(db, enc)
			if err != nil {
				t.Fatalf("NewCredentialStore: %v", err)
			}

			if tt.expectUpsert {
				mock.ExpectExec("REPLACE INTO credentials").
					WithArgs(
						tt.req.GroupJid, tt.req.Provider, string(credproxy.AuthModeChatGPT),
						sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
						tt.wantAccountID, sqlmock.AnyArg(), false,
					).
					WillReturnResult(sqlmock.NewResult(0, 1))
			}

			svc := newAuthService(client, store, provider.NewRegistry(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			gclient, cleanup := startAuthGRPC(t, svc)
			defer cleanup()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			stream, err := gclient.StartChatGPTDeviceAuth(ctx, tt.req)
			if err != nil {
				t.Fatalf("StartChatGPTDeviceAuth(%+v) err = %v, want nil", tt.req, err)
			}
			seq, terminal := collectEvents(t, stream)

			matchSeq := tt.matchSeq
			if matchSeq == nil {
				matchSeq = equalSeqIgnoringTickCount
			}
			if !matchSeq(seq, tt.wantSeq) {
				t.Errorf("event sequence for %+v = %v, want %v", tt.req, seq, tt.wantSeq)
			}

			if tt.wantErrCode != kraclawv1.DeviceAuthEvent_ERROR_CODE_UNSPECIFIED {
				if terminal == nil {
					t.Fatalf("terminal event = nil, want error %v", tt.wantErrCode)
				}
				e := terminal.GetError()
				if e == nil {
					t.Fatalf("terminal event = %v, want *DeviceAuthEvent_Error_", terminal)
				}
				if e.GetCode() != tt.wantErrCode {
					t.Errorf("error code = %v, want %v", e.GetCode(), tt.wantErrCode)
				}
			} else {
				if terminal == nil {
					t.Fatalf("terminal event = nil, want success")
				}
				s := terminal.GetSuccess()
				if s == nil {
					t.Fatalf("terminal event = %v, want *DeviceAuthEvent_Success_", terminal)
				}
				if s.GetAccountId() != tt.wantAccountID {
					t.Errorf("AccountId = %q, want %q", s.GetAccountId(), tt.wantAccountID)
				}
			}

			if tt.expectUpsert {
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Errorf("sqlmock expectations not met: %v", err)
				}
			}
		})
	}
}

func TestStartChatGPTDeviceAuth_ValidationErrorsUseInvalidArgument(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		req      *kraclawv1.StartChatGPTDeviceAuthRequest
		wantCode kraclawv1.DeviceAuthEvent_ErrorCode
		wantSub  string
	}{
		"empty group_jid":      {req: &kraclawv1.StartChatGPTDeviceAuthRequest{Provider: "openai"}, wantCode: kraclawv1.DeviceAuthEvent_INVALID_ARGUMENT, wantSub: "group_jid"},
		"unknown provider":     {req: &kraclawv1.StartChatGPTDeviceAuthRequest{GroupJid: "tui:g", Provider: "nope"}, wantCode: kraclawv1.DeviceAuthEvent_INVALID_ARGUMENT, wantSub: "unknown provider"},
		"non-chatgpt provider": {req: &kraclawv1.StartChatGPTDeviceAuthRequest{GroupJid: "tui:g", Provider: "anthropic"}, wantCode: kraclawv1.DeviceAuthEvent_INVALID_ARGUMENT, wantSub: "does not use chatgpt"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			svc := newAuthService(nil, nil, provider.NewRegistry(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			gclient, cleanup := startAuthGRPC(t, svc)
			defer cleanup()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stream, err := gclient.StartChatGPTDeviceAuth(ctx, tt.req)
			if err != nil {
				t.Fatalf("StartChatGPTDeviceAuth: %v", err)
			}
			ev, err := stream.Recv()
			if err != nil {
				t.Fatalf("Recv: %v", err)
			}
			e := ev.GetError()
			if e == nil {
				t.Fatalf("expected Error event, got %T", ev.GetEvent())
			}
			if e.GetCode() != tt.wantCode {
				t.Errorf("code = %v, want %v", e.GetCode(), tt.wantCode)
			}
			if !strings.Contains(e.GetMessage(), tt.wantSub) {
				t.Errorf("message = %q, want substring %q", e.GetMessage(), tt.wantSub)
			}
		})
	}
}

func TestErrCodeFor(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		err  error
		want kraclawv1.DeviceAuthEvent_ErrorCode
	}{
		"timeout":           {err: chatgpt.ErrDeviceAuthTimeout, want: kraclawv1.DeviceAuthEvent_TIMEOUT},
		"deadline exceeded": {err: context.DeadlineExceeded, want: kraclawv1.DeviceAuthEvent_TIMEOUT},
		"access denied":     {err: chatgpt.ErrAccessDenied, want: kraclawv1.DeviceAuthEvent_ACCESS_DENIED},
		"context canceled":  {err: context.Canceled, want: kraclawv1.DeviceAuthEvent_CANCELLED},
		"wrapped denied":    {err: fmt.Errorf("wrap: %w", chatgpt.ErrAccessDenied), want: kraclawv1.DeviceAuthEvent_ACCESS_DENIED},
		"wrapped deadline":  {err: fmt.Errorf("wrap: %w", context.DeadlineExceeded), want: kraclawv1.DeviceAuthEvent_TIMEOUT},
		"wrapped canceled":  {err: fmt.Errorf("wrap: %w", context.Canceled), want: kraclawv1.DeviceAuthEvent_CANCELLED},
		"unknown":           {err: errors.New("boom"), want: kraclawv1.DeviceAuthEvent_INTERNAL},
		"nil":               {err: nil, want: kraclawv1.DeviceAuthEvent_INTERNAL},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := errCodeFor(tt.err)
			if got != tt.want {
				t.Errorf("errCodeFor(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestStartChatGPTDeviceAuth_UpsertFailureLogsRedactedMetadata(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(handler)

	idTok := mintTestJWT(t, map[string]any{
		"email": "u@example.com",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id":         "acct_77",
			"chatgpt_user_id":            "user_42",
			"chatgpt_account_is_fedramp": false,
		},
	})
	// expiresIn must be positive; 3600 = 1 hour
	issuer := newFakeIssuer(t, issuerModeApprove, idTok, 3600)

	client, err := chatgpt.NewClient(chatgpt.Config{
		Issuer:       issuer.URL,
		HTTPClient:   issuer.Client(),
		PollInterval: 5 * time.Millisecond,
		PollTimeout:  500 * time.Millisecond,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	// NewCredentialStore issues a timezone probe query on construction.
	mock.ExpectQuery("SELECT TIMESTAMP").WillReturnRows(
		sqlmock.NewRows([]string{"t"}).AddRow(time.Now().UTC().Truncate(time.Second)),
	)

	enc, err := credproxy.NewEncryptor(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	store, err := credproxy.NewCredentialStore(db, enc)
	if err != nil {
		t.Fatalf("NewCredentialStore: %v", err)
	}

	mock.ExpectExec("REPLACE INTO credentials").WillReturnError(errors.New("disk full"))

	svc := newAuthService(client, store, provider.NewRegistry(), logger)
	gclient, cleanup := startAuthGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := gclient.StartChatGPTDeviceAuth(ctx, &kraclawv1.StartChatGPTDeviceAuthRequest{
		GroupJid: "tui:g", Provider: "openai",
	})
	if err != nil {
		t.Fatalf("StartChatGPTDeviceAuth: %v", err)
	}
	_, terminal := collectEvents(t, stream)
	if terminal == nil || terminal.GetError() == nil {
		t.Fatalf("expected terminal Error event")
	}

	logs := buf.String()
	if !strings.Contains(logs, "store_credentials_failed") {
		t.Errorf("expected log error_id=store_credentials_failed in logs, got: %s", logs)
	}
	if !strings.Contains(logs, "acct_77") {
		t.Errorf("expected log to include account_id acct_77, got: %s", logs)
	}
	for _, forbidden := range []string{"accesstoken", "access_token", "refreshtoken", "refresh_token", "idtoken", "id_token"} {
		if strings.Contains(strings.ToLower(logs), forbidden) {
			t.Errorf("logs leak token field %q: %s", forbidden, logs)
		}
	}
}

// TestStartChatGPTDeviceAuth_CredStoreErrorScrubbed verifies that the wire
// Error.Message returned to the client when the credential store fails
// contains a fixed, generic string and never echoes the underlying error
// text. This is defense-in-depth: a future contributor wrapping the err
// with token material in fmt.Errorf must not be able to leak it through
// the gRPC stream.
func TestStartChatGPTDeviceAuth_CredStoreErrorScrubbed(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		credErr        error
		wantWireSubstr string
		wantNotInWire  string
	}{
		"raw err with token-shaped text is scrubbed": {
			credErr:        errors.New("mysql: insert tokens sk-secret-AAAA failed: connection refused"),
			wantWireSubstr: "failed to persist credentials",
			wantNotInWire:  "sk-secret-AAAA",
		},
		"generic err is scrubbed": {
			credErr:        errors.New("disk full"),
			wantWireSubstr: "failed to persist credentials",
			wantNotInWire:  "disk full",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			idTok := mintTestJWT(t, map[string]any{
				"email": "u@example.com",
				"exp":   time.Now().Add(time.Hour).Unix(),
				"https://api.openai.com/auth": map[string]any{
					"chatgpt_account_id":         "acct_77",
					"chatgpt_user_id":            "user_42",
					"chatgpt_account_is_fedramp": false,
				},
			})
			issuer := newFakeIssuer(t, issuerModeApprove, idTok, 3600)

			client, err := chatgpt.NewClient(chatgpt.Config{
				Issuer:       issuer.URL,
				HTTPClient:   issuer.Client(),
				PollInterval: 5 * time.Millisecond,
				PollTimeout:  500 * time.Millisecond,
				Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
			})
			if err != nil {
				t.Fatalf("chatgpt.NewClient: %v", err)
			}

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer func() { _ = db.Close() }()

			mock.ExpectQuery("SELECT TIMESTAMP").WillReturnRows(
				sqlmock.NewRows([]string{"t"}).AddRow(time.Now().UTC().Truncate(time.Second)),
			)

			enc, err := credproxy.NewEncryptor(strings.Repeat("ab", 32))
			if err != nil {
				t.Fatalf("NewEncryptor: %v", err)
			}
			store, err := credproxy.NewCredentialStore(db, enc)
			if err != nil {
				t.Fatalf("NewCredentialStore: %v", err)
			}

			mock.ExpectExec("REPLACE INTO credentials").WillReturnError(tt.credErr)

			svc := newAuthService(client, store, provider.NewRegistry(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			gclient, cleanup := startAuthGRPC(t, svc)
			defer cleanup()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			stream, err := gclient.StartChatGPTDeviceAuth(ctx, &kraclawv1.StartChatGPTDeviceAuthRequest{
				GroupJid: "tui:g", Provider: "openai",
			})
			if err != nil {
				t.Fatalf("StartChatGPTDeviceAuth: %v", err)
			}
			_, terminal := collectEvents(t, stream)
			if terminal == nil {
				t.Fatalf("credErr=%v: terminal event = nil, want Error event", tt.credErr)
			}
			e := terminal.GetError()
			if e == nil {
				t.Fatalf("credErr=%v: terminal event = %v, want *DeviceAuthEvent_Error_", tt.credErr, terminal)
			}
			if e.GetCode() != kraclawv1.DeviceAuthEvent_INTERNAL {
				t.Errorf("credErr=%v: error code = %v, want %v",
					tt.credErr, e.GetCode(), kraclawv1.DeviceAuthEvent_INTERNAL)
			}
			msg := e.GetMessage()
			if !strings.Contains(msg, tt.wantWireSubstr) {
				t.Errorf("credErr=%v: wire message = %q, want substring %q",
					tt.credErr, msg, tt.wantWireSubstr)
			}
			if strings.Contains(msg, tt.wantNotInWire) {
				t.Errorf("credErr=%v: wire message = %q, must NOT contain %q",
					tt.credErr, msg, tt.wantNotInWire)
			}
		})
	}
}
