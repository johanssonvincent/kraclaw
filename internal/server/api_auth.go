package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/johanssonvincent/kraclaw/internal/auth/chatgpt"
	"github.com/johanssonvincent/kraclaw/internal/credproxy"
	"github.com/johanssonvincent/kraclaw/internal/provider"
	kraclawv1 "github.com/johanssonvincent/kraclaw/pkg/pb/kraclawv1"
)

// authService orchestrates the ChatGPT OAuth device-code flow:
//
//  1. RequestDeviceCode at the issuer — emit DeviceCode to the client.
//  2. PollUntilCode while the user approves — emit a Tick on each heartbeat.
//  3. ExchangeCode for the token bundle.
//  4. Persist via CredentialStore.UpsertChatGPTCredential.
//  5. Emit Success with the parsed account_id and absolute expiry.
//
// On any failure between steps 1 and 4 the service emits a single terminal
// Error event and returns nil — the stream itself is not used to surface
// errors so a TUI never has to translate gRPC status codes into UX.
type authService struct {
	kraclawv1.UnimplementedAuthServiceServer

	chatgpt   *chatgpt.Client
	creds     *credproxy.CredentialStore
	providers *provider.Registry
	log       *slog.Logger
}

// newAuthService constructs an authService. log == nil falls back to slog.Default.
func newAuthService(c *chatgpt.Client, s *credproxy.CredentialStore, r *provider.Registry, log *slog.Logger) *authService {
	if log == nil {
		log = slog.Default()
	}
	if r == nil {
		r = provider.NewRegistry()
	}
	return &authService{chatgpt: c, creds: s, providers: r, log: log}
}

// StartChatGPTDeviceAuth implements kraclawv1.AuthServiceServer.
func (s *authService) StartChatGPTDeviceAuth(req *kraclawv1.StartChatGPTDeviceAuthRequest, stream kraclawv1.AuthService_StartChatGPTDeviceAuthServer) error {
	ctx := stream.Context()

	if req.GetGroupJid() == "" {
		return s.sendError(stream, "INTERNAL", "group_jid is required")
	}
	prov, ok := s.providers.Get(req.GetProvider())
	if !ok {
		return s.sendError(stream, "INTERNAL", fmt.Sprintf("unknown provider %q", req.GetProvider()))
	}
	if prov.AuthMode != provider.AuthModeChatGPT {
		return s.sendError(stream, "INTERNAL", fmt.Sprintf("provider %q does not use chatgpt auth", prov.ID))
	}

	dc, err := s.chatgpt.RequestDeviceCode(ctx)
	if err != nil {
		return s.sendError(stream, errCodeFor(err), fmt.Sprintf("request device code: %v", err))
	}

	if err := stream.Send(&kraclawv1.DeviceAuthEvent{
		Event: &kraclawv1.DeviceAuthEvent_DeviceCode_{
			DeviceCode: &kraclawv1.DeviceAuthEvent_DeviceCode{
				UserCode:            dc.UserCode,
				VerificationUrl:     dc.VerificationURL,
				PollIntervalSeconds: int32(dc.Interval / time.Second),
			},
		},
	}); err != nil {
		return fmt.Errorf("send device_code event: %w", err)
	}

	start := time.Now()
	tickFn := func() {
		// Tick send errors are best-effort: the stream may already be torn
		// down by the time a heartbeat fires. The real failure surfaces from
		// PollUntilCode/ExchangeCode below.
		_ = stream.Send(&kraclawv1.DeviceAuthEvent{
			Event: &kraclawv1.DeviceAuthEvent_Tick_{
				Tick: &kraclawv1.DeviceAuthEvent_Tick{
					ElapsedSeconds: int32(time.Since(start) / time.Second),
				},
			},
		})
	}

	authCode, err := s.chatgpt.PollUntilCode(ctx, dc, tickFn)
	if err != nil {
		return s.sendError(stream, errCodeFor(err), fmt.Sprintf("poll: %v", err))
	}

	tokens, err := s.chatgpt.ExchangeCode(ctx, authCode)
	if err != nil {
		return s.sendError(stream, errCodeFor(err), fmt.Sprintf("exchange: %v", err))
	}

	if err := s.creds.UpsertChatGPTCredential(ctx, req.GetGroupJid(), req.GetProvider(), &credproxy.ChatGPTTokens{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		IDToken:      tokens.IDToken,
		AccountID:    tokens.IDClaims.AccountID,
		ExpiresAt:    tokens.ExpiresAt,
		IsFedRAMP:    tokens.IDClaims.IsFedRAMP,
	}); err != nil {
		return s.sendError(stream, "INTERNAL", fmt.Sprintf("store credentials: %v", err))
	}

	if err := stream.Send(&kraclawv1.DeviceAuthEvent{
		Event: &kraclawv1.DeviceAuthEvent_Success_{
			Success: &kraclawv1.DeviceAuthEvent_Success{
				AccountId: tokens.IDClaims.AccountID,
				ExpiresAt: tokens.ExpiresAt.Format(time.RFC3339),
			},
		},
	}); err != nil {
		return fmt.Errorf("send success event: %w", err)
	}
	return nil
}

// sendError emits an Error event terminating the stream and returns nil so
// the gRPC layer reports the stream as cleanly closed (the error envelope is
// in-band so the client can render it without translating status codes).
func (s *authService) sendError(stream kraclawv1.AuthService_StartChatGPTDeviceAuthServer, code, msg string) error {
	s.log.Warn("StartChatGPTDeviceAuth error", "code", code, "msg", msg)
	if err := stream.Send(&kraclawv1.DeviceAuthEvent{
		Event: &kraclawv1.DeviceAuthEvent_Error_{
			Error: &kraclawv1.DeviceAuthEvent_Error{Code: code, Message: msg},
		},
	}); err != nil {
		return fmt.Errorf("send error event: %w", err)
	}
	return nil
}

// errCodeFor maps a low-level error to the proto error code (TIMEOUT,
// ACCESS_DENIED, INTERNAL). ACCESS_DENIED covers explicit context.Canceled —
// the operator either cancelled the TUI or hit the OAuth deny button.
func errCodeFor(err error) string {
	switch {
	case errors.Is(err, chatgpt.ErrDeviceAuthTimeout):
		return "TIMEOUT"
	case errors.Is(err, context.Canceled):
		return "ACCESS_DENIED"
	default:
		return "INTERNAL"
	}
}
