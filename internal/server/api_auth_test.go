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
//	"approve": usercode -> immediate authorization_code -> token bundle
//	"deny":    usercode OK, then poll endpoint returns HTTP 500 (errBadStatus)
type issuerMode string

const (
	issuerModeApprove issuerMode = "approve"
	issuerModeDeny    issuerMode = "deny"
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
			pollCalls.Add(1)
			if mode == issuerModeDeny {
				http.Error(w, "denied", http.StatusInternalServerError)
				return
			}
			// approve: first poll returns the authorization_code immediately.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"authorization_code":"ac_1","code_verifier":"cv_1"}`))
		case "/oauth/token":
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
		wantErrCode   string
		wantAccountID string
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
			wantErrCode: "INVALID_ARGUMENT",
		},
		"non-chatgpt provider yields INVALID_ARGUMENT error": {
			issuerMode: issuerModeApprove,
			req: &kraclawv1.StartChatGPTDeviceAuthRequest{
				GroupJid: "discord:42",
				Provider: provider.ProviderAnthropic,
			},
			wantSeq:     []string{"error"},
			wantErrCode: "INVALID_ARGUMENT",
		},
		"poll endpoint denial surfaces as INTERNAL after device_code emitted": {
			issuerMode: issuerModeDeny,
			req: &kraclawv1.StartChatGPTDeviceAuthRequest{
				GroupJid: "discord:42",
				Provider: provider.ProviderOpenAI,
			},
			wantSeq:     []string{"device_code", "error"},
			wantErrCode: "INTERNAL",
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

			if !equalSeqIgnoringTickCount(seq, tt.wantSeq) {
				t.Errorf("event sequence for %+v = %v, want %v", tt.req, seq, tt.wantSeq)
			}

			if tt.wantErrCode != "" {
				if terminal == nil {
					t.Fatalf("terminal event = nil, want error %q", tt.wantErrCode)
				}
				e := terminal.GetError()
				if e == nil {
					t.Fatalf("terminal event = %v, want *DeviceAuthEvent_Error_", terminal)
				}
				if e.GetCode() != tt.wantErrCode {
					t.Errorf("error code = %q, want %q", e.GetCode(), tt.wantErrCode)
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
		wantCode string
		wantSub  string
	}{
		"empty group_jid":      {req: &kraclawv1.StartChatGPTDeviceAuthRequest{Provider: "openai"}, wantCode: "INVALID_ARGUMENT", wantSub: "group_jid"},
		"unknown provider":     {req: &kraclawv1.StartChatGPTDeviceAuthRequest{GroupJid: "tui:g", Provider: "nope"}, wantCode: "INVALID_ARGUMENT", wantSub: "unknown provider"},
		"non-chatgpt provider": {req: &kraclawv1.StartChatGPTDeviceAuthRequest{GroupJid: "tui:g", Provider: "anthropic"}, wantCode: "INVALID_ARGUMENT", wantSub: "does not use chatgpt"},
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
				t.Errorf("code = %q, want %q", e.GetCode(), tt.wantCode)
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
		want string
	}{
		"timeout":           {err: chatgpt.ErrDeviceAuthTimeout, want: "TIMEOUT"},
		"deadline exceeded": {err: context.DeadlineExceeded, want: "TIMEOUT"},
		"access denied":     {err: chatgpt.ErrAccessDenied, want: "ACCESS_DENIED"},
		"context canceled":  {err: context.Canceled, want: "CANCELLED"},
		"wrapped denied":    {err: fmt.Errorf("wrap: %w", chatgpt.ErrAccessDenied), want: "ACCESS_DENIED"},
		"wrapped deadline":  {err: fmt.Errorf("wrap: %w", context.DeadlineExceeded), want: "TIMEOUT"},
		"wrapped canceled":  {err: fmt.Errorf("wrap: %w", context.Canceled), want: "CANCELLED"},
		"unknown":           {err: errors.New("boom"), want: "INTERNAL"},
		"nil":               {err: nil, want: "INTERNAL"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := errCodeFor(tt.err)
			if got != tt.want {
				t.Errorf("errCodeFor(%v) = %q, want %q", tt.err, got, tt.want)
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
	defer db.Close()

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
	if strings.Contains(logs, "AccessToken") || strings.Contains(strings.ToLower(logs), "access_token") {
		t.Errorf("logs leak access_token field name: %s", logs)
	}
}
