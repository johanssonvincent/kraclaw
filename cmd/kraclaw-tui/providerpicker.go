package main

import (
	"context"
	"fmt"
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

// providersLoadedMsg carries the ListProviders response back to the model.
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
			return providersLoadedMsg{flowID: flowID, err: translateListProvidersErr(err)}
		}
		return providersLoadedMsg{flowID: flowID, providers: resp.GetProviders()}
	}
}

// translateListProvidersErr converts gRPC errors into user-readable messages.
func translateListProvidersErr(err error) error {
	switch status.Code(err) {
	case codes.DeadlineExceeded:
		return fmt.Errorf("timed out contacting server — check your connection")
	case codes.Unavailable:
		return fmt.Errorf("server unavailable — is kraclaw running?")
	case codes.Unimplemented:
		return fmt.Errorf("server does not support provider listing; upgrade kraclaw")
	default:
		return fmt.Errorf("failed to load providers: %w", err)
	}
}

// buildProviderItems converts ProviderInfo slices to picker items, dropping zero-model providers.
// If selectedID is non-empty, returns the cursor index of that provider (or 0 if absent).
func buildProviderItems(providers []*kraclawv1.ProviderInfo, selectedID string) ([]creationPickerItem, int) {
	items := make([]creationPickerItem, 0, len(providers))
	cursor := 0
	for _, p := range providers {
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
