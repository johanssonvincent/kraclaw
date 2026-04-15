package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	kraclawv1 "github.com/johanssonvincent/kraclaw/pkg/pb/kraclawv1"
)

// creationPickerItem is a single selectable row in the provider/model creation picker.
type creationPickerItem struct {
	id    string
	label string
}

// creationPickerState backs both the provider-select and model-select steps
// in the new-group creation flow.
type creationPickerState struct {
	items  []creationPickerItem
	cursor int
}

// providersLoadedMsg is the result of a ListProviders call. The message is
// discarded if the model is not in chatStateSelectProvider or if flowID does
// not match creationFlowID (stale response from a prior flow); err is
// non-nil when the call failed. The handler treats a non-nil, non-stale error
// as a retryable failure that leaves the user on chatStateSelectProvider with
// chatErr set.
type providersLoadedMsg struct {
	flowID    int
	providers []*kraclawv1.ProviderInfo
	err       error
}

// fetchProvidersCmd calls ListProviders and returns a providersLoadedMsg tagged with flowID.
func (m model) fetchProvidersCmd(flowID int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := m.api.groups.ListProviders(ctx, &kraclawv1.ListProvidersRequest{})
		if err != nil {
			slog.Error("ListProviders RPC failed", "grpc_code", status.Code(err).String(), "err", err)
			return providersLoadedMsg{flowID: flowID, err: translateListProvidersErr(err)}
		}
		return providersLoadedMsg{flowID: flowID, providers: resp.GetProviders()}
	}
}

// translateListProvidersErr maps known gRPC status codes to human-readable errors.
// Named branches (DeadlineExceeded, Unavailable, Unimplemented, PermissionDenied,
// Unauthenticated) return fresh errors that do NOT wrap the original; the default
// branch wraps with %w.
func translateListProvidersErr(err error) error {
	switch status.Code(err) {
	case codes.DeadlineExceeded:
		return fmt.Errorf("timed out contacting server — check your connection")
	case codes.Unavailable:
		return fmt.Errorf("server unavailable — is kraclaw running?")
	case codes.Unimplemented:
		return fmt.Errorf("server does not support provider listing; upgrade kraclaw")
	case codes.PermissionDenied, codes.Unauthenticated:
		return fmt.Errorf("access denied — check your TLS certificate configuration")
	default:
		return fmt.Errorf("failed to load providers: %w", err)
	}
}

// translateRegisterGroupErr maps known gRPC status codes from RegisterGroup to
// human-readable errors. AlreadyExists, DeadlineExceeded, Unavailable, and
// Internal return fresh errors that do NOT wrap the original.
// InvalidArgument and the default branch preserve the original error via %w.
func translateRegisterGroupErr(err error) error {
	switch status.Code(err) {
	case codes.AlreadyExists:
		return fmt.Errorf("a group with that name already exists")
	case codes.DeadlineExceeded, codes.Unavailable:
		return fmt.Errorf("could not reach the server — check your connection")
	case codes.Internal:
		return fmt.Errorf("the server encountered an internal error — check server logs or retry")
	case codes.InvalidArgument:
		return fmt.Errorf("invalid group configuration: %w", err)
	case codes.Unimplemented:
		return fmt.Errorf("server does not support group registration; upgrade kraclaw")
	default:
		return fmt.Errorf("failed to create group: %w", err)
	}
}

// translateStreamInboundErr maps known gRPC status codes from StreamInbound to
// human-readable errors. Named branches return fresh errors that do NOT wrap
// the original; the default branch wraps with %w.
func translateStreamInboundErr(err error) error {
	switch status.Code(err) {
	case codes.DeadlineExceeded, codes.Unavailable:
		return fmt.Errorf("could not reach the server — check your connection")
	case codes.PermissionDenied, codes.Unauthenticated:
		return fmt.Errorf("access denied — check your TLS certificate configuration")
	case codes.NotFound:
		return fmt.Errorf("group not found on the server")
	default:
		return fmt.Errorf("failed to open stream: %w", err)
	}
}

// resetCreationFlow clears all transient state for the new-group creation flow.
// Call from every exit path (Esc, error, success) to prevent stale state leaking
// into the next flow invocation.
func (m *model) resetCreationFlow() {
	m.creationPendingGroupName = ""
	m.creationSelectedProvider = ""
	m.creationProviders = nil
	m.creationPicker = creationPickerState{}
	m.creationProvidersLoaded = false
}

// buildProviderItems converts ProviderInfo slices to picker items, dropping
// providers with an empty ID or zero models.
// If selectedID is non-empty, returns the cursor index of that provider in the
// filtered list; if selectedID is non-empty but not found in the filtered list,
// the cursor defaults to 0 (first item). If selectedID is empty, cursor is
// always 0.
func buildProviderItems(providers []*kraclawv1.ProviderInfo, selectedID string) ([]creationPickerItem, int) {
	items := make([]creationPickerItem, 0, len(providers))
	cursor := 0
	for _, p := range providers {
		if p.GetId() == "" {
			continue
		}
		if len(p.GetModels()) == 0 {
			continue
		}
		items = append(items, creationPickerItem{id: p.GetId(), label: p.GetDisplayName()})
		if selectedID != "" && p.GetId() == selectedID {
			cursor = len(items) - 1
		}
	}
	return items, cursor
}
