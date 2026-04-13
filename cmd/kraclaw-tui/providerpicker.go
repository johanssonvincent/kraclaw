package main

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

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
	providers []*kraclawv1.ProviderInfo
	err       error
}

// fetchProvidersCmd calls ListProviders and returns a providersLoadedMsg.
func (m model) fetchProvidersCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := m.api.groups.ListProviders(ctx, &kraclawv1.ListProvidersRequest{})
		if err != nil {
			return providersLoadedMsg{err: err}
		}
		return providersLoadedMsg{providers: resp.GetProviders()}
	}
}
