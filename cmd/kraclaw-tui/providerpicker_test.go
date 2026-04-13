package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	kraclawv1 "github.com/johanssonvincent/kraclaw/pkg/pb/kraclawv1"
)

// makeProviders builds a minimal []*kraclawv1.ProviderInfo slice for tests.
func makeProviders(ids ...string) []*kraclawv1.ProviderInfo {
	providers := make([]*kraclawv1.ProviderInfo, 0, len(ids))
	for _, id := range ids {
		providers = append(providers, &kraclawv1.ProviderInfo{
			Id:           id,
			DisplayName:  id + " Display",
			DefaultModel: "default-model-" + id,
			Models: []*kraclawv1.ModelInfo{
				{Id: "model-a-" + id, DisplayName: "Model A " + id},
				{Id: "model-b-" + id, DisplayName: "Model B " + id},
			},
		})
	}
	return providers
}


func TestCreationPickerNavigation(t *testing.T) {
	fake := &fakeGroupClient{}
	m := initialModel("test", &apiClient{groups: fake, channels: &mockChannelClient{}})
	m.chatState = chatStateSelectProvider

	next, _ := m.Update(providersLoadedMsg{providers: makeProviders("anthropic", "openai")})
	m = next.(model)

	if m.creationPicker.cursor != 0 {
		t.Fatalf("initial cursor = %d, want 0", m.creationPicker.cursor)
	}
	if len(m.creationPicker.items) != 2 {
		t.Fatalf("items len = %d, want 2", len(m.creationPicker.items))
	}

	// Move down.
	next, _ = m.Update(keyPress("j"))
	m = next.(model)
	if m.creationPicker.cursor != 1 {
		t.Errorf("cursor after j = %d, want 1", m.creationPicker.cursor)
	}

	// Cannot go past end.
	next, _ = m.Update(keyPress("j"))
	m = next.(model)
	if m.creationPicker.cursor != 1 {
		t.Errorf("cursor clamped at end = %d, want 1", m.creationPicker.cursor)
	}

	// Move up.
	next, _ = m.Update(keyPress("k"))
	m = next.(model)
	if m.creationPicker.cursor != 0 {
		t.Errorf("cursor after k = %d, want 0", m.creationPicker.cursor)
	}

	// Cannot go past start.
	next, _ = m.Update(keyPress("k"))
	m = next.(model)
	if m.creationPicker.cursor != 0 {
		t.Errorf("cursor clamped at start = %d, want 0", m.creationPicker.cursor)
	}
}

func TestCreationPickerSelectProvider(t *testing.T) {
	fake := &fakeGroupClient{}
	m := initialModel("test", &apiClient{groups: fake, channels: &mockChannelClient{}})
	m.chatState = chatStateSelectProvider

	next, _ := m.Update(providersLoadedMsg{providers: makeProviders("anthropic", "openai")})
	m = next.(model)

	// Navigate to second item (openai) and Enter to select.
	next, _ = m.Update(keyPress("j"))
	m = next.(model)
	next, _ = m.Update(keyPress("enter"))
	m = next.(model)

	if m.chatState != chatStateSelectModel {
		t.Fatalf("chatState = %v, want chatStateSelectModel", m.chatState)
	}
	if m.creationSelectedProvider != "openai" {
		t.Errorf("creationSelectedProvider = %q, want %q", m.creationSelectedProvider, "openai")
	}
}

func TestCreationPickerModelList(t *testing.T) {
	fake := &fakeGroupClient{}
	m := initialModel("test", &apiClient{groups: fake, channels: &mockChannelClient{}})
	m.chatState = chatStateSelectProvider

	next, _ := m.Update(providersLoadedMsg{providers: makeProviders("anthropic")})
	m = next.(model)

	// Enter to select the only provider (anthropic) → transitions to model picker.
	next, _ = m.Update(keyPress("enter"))
	m = next.(model)

	if m.chatState != chatStateSelectModel {
		t.Fatalf("chatState = %v, want chatStateSelectModel", m.chatState)
	}
	if len(m.creationPicker.items) != 2 {
		t.Fatalf("model picker items = %d, want 2", len(m.creationPicker.items))
	}
	if m.creationPicker.items[0].id != "model-a-anthropic" {
		t.Errorf("first model id = %q, want %q", m.creationPicker.items[0].id, "model-a-anthropic")
	}
}

func TestCreationPickerBackNavigation(t *testing.T) {
	fake := &fakeGroupClient{}
	m := initialModel("test", &apiClient{groups: fake, channels: &mockChannelClient{}})
	m.chatState = chatStateSelectProvider

	// Load two providers; select the second one (openai, index 1).
	next, _ := m.Update(providersLoadedMsg{providers: makeProviders("anthropic", "openai")})
	m = next.(model)

	next, _ = m.Update(keyPress("j")) // cursor → 1
	m = next.(model)
	next, _ = m.Update(keyPress("enter")) // select openai → model picker
	m = next.(model)

	if m.chatState != chatStateSelectModel {
		t.Fatalf("chatState = %v after select, want chatStateSelectModel", m.chatState)
	}
	// Advance model cursor to prove it resets on back-nav.
	next, _ = m.Update(keyPress("j"))
	m = next.(model)
	if m.creationPicker.cursor != 1 {
		t.Fatalf("model cursor after j = %d, want 1", m.creationPicker.cursor)
	}

	// Esc back to provider picker; cursor must land on previously selected provider (openai, index 1).
	next, _ = m.Update(keyPress("esc"))
	m = next.(model)

	if m.chatState != chatStateSelectProvider {
		t.Fatalf("chatState = %v after esc, want chatStateSelectProvider", m.chatState)
	}
	if m.creationPicker.cursor != 1 {
		t.Errorf("restored provider cursor = %d, want 1 (openai)", m.creationPicker.cursor)
	}
	if len(m.creationPicker.items) != 2 {
		t.Errorf("restored items len = %d, want 2", len(m.creationPicker.items))
	}
}

// TestProvidersLoadedMsgIgnoredAfterEsc asserts that a late-arriving
// providersLoadedMsg is dropped when the user has already pressed Esc.
func TestProvidersLoadedMsgIgnoredAfterEsc(t *testing.T) {
	fake := &fakeGroupClient{}
	m := initialModel("test", &apiClient{groups: fake, channels: &mockChannelClient{}})
	m.chatState = chatStateSelectProvider
	m.creationPendingGroupName = "my-group"

	// User presses Esc → state rewinds to chatStateSelectGroup.
	next, _ := m.Update(keyPress("esc"))
	m = next.(model)
	if m.chatState != chatStateSelectGroup {
		t.Fatalf("chatState = %v after esc, want chatStateSelectGroup", m.chatState)
	}

	// Late-arriving success response must be ignored.
	next, _ = m.Update(providersLoadedMsg{providers: makeProviders("anthropic")})
	m = next.(model)

	if m.chatState != chatStateSelectGroup {
		t.Errorf("chatState = %v, want chatStateSelectGroup (stale msg must be ignored)", m.chatState)
	}
	if m.creationProvidersLoaded {
		t.Error("creationProvidersLoaded should remain false")
	}
	if len(m.creationPicker.items) != 0 {
		t.Errorf("creationPicker.items should be empty, got %d", len(m.creationPicker.items))
	}
}

// TestProviderPickerEscClearsChatErr asserts that Esc from the provider picker
// clears any stale error so it doesn't bleed into the name-input state.
func TestProviderPickerEscClearsChatErr(t *testing.T) {
	fake := &fakeGroupClient{}
	m := initialModel("test", &apiClient{groups: fake, channels: &mockChannelClient{}})
	m.chatState = chatStateSelectProvider
	m.chatErr = errors.New("stale error")

	next, _ := m.Update(keyPress("esc"))
	m = next.(model)

	if m.chatErr != nil {
		t.Errorf("chatErr = %v, want nil after Esc from provider picker", m.chatErr)
	}
}

// fakeGroupClient is a minimal GroupServiceClient that records the last
// RegisterGroup request and returns a stub group.
type fakeGroupClient struct {
	lastRegisterReq *kraclawv1.RegisterGroupRequest
}

func (f *fakeGroupClient) RegisterGroup(_ context.Context, in *kraclawv1.RegisterGroupRequest, _ ...grpc.CallOption) (*kraclawv1.Group, error) {
	f.lastRegisterReq = in
	return &kraclawv1.Group{Jid: in.Jid, Name: in.Name, Folder: in.Folder}, nil
}

func (f *fakeGroupClient) ListGroups(_ context.Context, _ *kraclawv1.ListGroupsRequest, _ ...grpc.CallOption) (*kraclawv1.ListGroupsResponse, error) {
	return &kraclawv1.ListGroupsResponse{}, nil
}

func (f *fakeGroupClient) GetGroup(_ context.Context, _ *kraclawv1.GetGroupRequest, _ ...grpc.CallOption) (*kraclawv1.Group, error) {
	return nil, nil
}

func (f *fakeGroupClient) UnregisterGroup(_ context.Context, _ *kraclawv1.UnregisterGroupRequest, _ ...grpc.CallOption) (*kraclawv1.UnregisterGroupResponse, error) {
	return &kraclawv1.UnregisterGroupResponse{}, nil
}

func (f *fakeGroupClient) GetSenderAllowlist(_ context.Context, _ *kraclawv1.GetSenderAllowlistRequest, _ ...grpc.CallOption) (*kraclawv1.SenderAllowlist, error) {
	return nil, nil
}

func (f *fakeGroupClient) UpdateSenderAllowlist(_ context.Context, _ *kraclawv1.UpdateSenderAllowlistRequest, _ ...grpc.CallOption) (*kraclawv1.SenderAllowlist, error) {
	return nil, nil
}

func (f *fakeGroupClient) ListProviders(_ context.Context, _ *kraclawv1.ListProvidersRequest, _ ...grpc.CallOption) (*kraclawv1.ListProvidersResponse, error) {
	return &kraclawv1.ListProvidersResponse{}, nil
}

// TestUpdateContainerConfigJson drives the full provider→model→register flow
// through Update and asserts that container_config_json is set correctly.
func TestUpdateContainerConfigJson(t *testing.T) {
	fake := &fakeGroupClient{}
	m := initialModel("test", &apiClient{groups: fake, channels: &mockChannelClient{}})

	providers := makeProviders("anthropic")

	// Simulate: providers loaded successfully.
	m.chatState = chatStateSelectProvider
	m.creationPendingGroupName = "my-group"

	next, _ := m.Update(providersLoadedMsg{providers: providers})
	m = next.(model)

	if !m.creationProvidersLoaded {
		t.Fatal("creationProvidersLoaded should be true after successful load")
	}
	if m.chatState != chatStateSelectProvider {
		t.Fatalf("chatState = %v, want chatStateSelectProvider", m.chatState)
	}
	if len(m.creationPicker.items) != 1 {
		t.Fatalf("picker items = %d, want 1", len(m.creationPicker.items))
	}

	// Press Enter to select the provider.
	next, _ = m.Update(keyPress("enter"))
	m = next.(model)

	if m.chatState != chatStateSelectModel {
		t.Fatalf("chatState = %v, want chatStateSelectModel after provider select", m.chatState)
	}
	if len(m.creationPicker.items) != 2 {
		t.Fatalf("model picker items = %d, want 2", len(m.creationPicker.items))
	}

	// Press Enter to select the first model.
	next, cmd := m.Update(keyPress("enter"))
	m = next.(model)

	// Execute the registerGroupCmd and capture the resulting message.
	if cmd == nil {
		t.Fatal("expected a command after model selection, got nil")
	}
	result := cmd()
	regMsg, ok := result.(groupRegisteredMsg)
	if !ok {
		t.Fatalf("expected groupRegisteredMsg, got %T", result)
	}
	if regMsg.err != nil {
		t.Fatalf("registerGroupCmd returned error: %v", regMsg.err)
	}

	req := fake.lastRegisterReq
	if req == nil {
		t.Fatal("RegisterGroup was never called")
	}
	if req.ContainerConfigJson == "" {
		t.Fatal("ContainerConfigJson is empty; expected JSON with provider/model")
	}

	var cc struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if err := json.Unmarshal([]byte(req.ContainerConfigJson), &cc); err != nil {
		t.Fatalf("ContainerConfigJson is not valid JSON: %v", err)
	}
	if cc.Provider != "anthropic" {
		t.Errorf("provider = %q, want %q", cc.Provider, "anthropic")
	}
	if cc.Model != "model-a-anthropic" {
		t.Errorf("model = %q, want %q", cc.Model, "model-a-anthropic")
	}

	// T3: all creation fields must be zeroed after the model-select Enter.
	if m.creationPendingGroupName != "" {
		t.Errorf("creationPendingGroupName = %q, want empty after group creation", m.creationPendingGroupName)
	}
	if m.creationSelectedProvider != "" {
		t.Errorf("creationSelectedProvider = %q, want empty after group creation", m.creationSelectedProvider)
	}
	if m.creationProviders != nil {
		t.Errorf("creationProviders = %v, want nil after group creation", m.creationProviders)
	}
	if len(m.creationPicker.items) != 0 {
		t.Errorf("creationPicker.items = %v, want empty after group creation", m.creationPicker.items)
	}
	if m.creationProvidersLoaded {
		t.Error("creationProvidersLoaded should be false after group creation")
	}
	if m.chatState != chatStateConnecting {
		t.Errorf("chatState = %v, want chatStateConnecting", m.chatState)
	}
}

// makeProvider builds a single ProviderInfo with a configurable number of models.
func makeProvider(id string, modelCount int) *kraclawv1.ProviderInfo {
	models := make([]*kraclawv1.ModelInfo, modelCount)
	for i := range modelCount {
		models[i] = &kraclawv1.ModelInfo{
			Id:          fmt.Sprintf("model-%d-%s", i, id),
			DisplayName: fmt.Sprintf("Model %d %s", i, id),
		}
	}
	p := &kraclawv1.ProviderInfo{
		Id:          id,
		DisplayName: id + " Display",
	}
	if modelCount > 0 {
		p.DefaultModel = models[0].Id
		p.Models = models
	}
	return p
}

// TestCreationPickerFiltersZeroModelProviders asserts that providers with no
// models are excluded from the picker after providers are loaded.
func TestCreationPickerFiltersZeroModelProviders(t *testing.T) {
	fake := &fakeGroupClient{}
	m := initialModel("test", &apiClient{groups: fake, channels: &mockChannelClient{}})
	m.chatState = chatStateSelectProvider
	m.creationFlowID = 1

	noModels := makeProvider("empty-provider", 0)
	withModels := makeProvider("real-provider", 2)

	next, _ := m.Update(providersLoadedMsg{flowID: 1, providers: []*kraclawv1.ProviderInfo{noModels, withModels}})
	m = next.(model)

	if !m.creationProvidersLoaded {
		t.Fatal("creationProvidersLoaded should be true")
	}
	if len(m.creationPicker.items) != 1 {
		t.Fatalf("picker items = %d, want 1 (zero-model provider filtered)", len(m.creationPicker.items))
	}
	if m.creationPicker.items[0].id != "real-provider" {
		t.Errorf("item[0].id = %q, want %q", m.creationPicker.items[0].id, "real-provider")
	}
}

// TestCreationPickerEnterOnEmpty asserts that pressing Enter on an empty picker
// or before providers have loaded is a no-op.
func TestCreationPickerEnterOnEmpty(t *testing.T) {
	t.Run("provider picker empty items", func(t *testing.T) {
		fake := &fakeGroupClient{}
		m := initialModel("test", &apiClient{groups: fake, channels: &mockChannelClient{}})
		m.chatState = chatStateSelectProvider
		m.creationProvidersLoaded = true
		// No items in picker.

		next, cmd := m.Update(keyPress("enter"))
		m = next.(model)

		if m.chatState != chatStateSelectProvider {
			t.Errorf("chatState = %v, want chatStateSelectProvider", m.chatState)
		}
		if cmd != nil {
			t.Error("expected no command on Enter with empty provider picker")
		}
	})

	t.Run("provider picker not loaded", func(t *testing.T) {
		fake := &fakeGroupClient{}
		m := initialModel("test", &apiClient{groups: fake, channels: &mockChannelClient{}})
		m.chatState = chatStateSelectProvider
		m.creationProvidersLoaded = false
		// Add items to prove the loaded guard fires before the items check.
		m.creationPicker.items = []creationPickerItem{{id: "p", label: "P"}}

		next, cmd := m.Update(keyPress("enter"))
		m = next.(model)

		if m.chatState != chatStateSelectProvider {
			t.Errorf("chatState = %v, want chatStateSelectProvider", m.chatState)
		}
		if cmd != nil {
			t.Error("expected no command on Enter before providers loaded")
		}
	})

	t.Run("model picker empty items", func(t *testing.T) {
		fake := &fakeGroupClient{}
		m := initialModel("test", &apiClient{groups: fake, channels: &mockChannelClient{}})
		m.chatState = chatStateSelectModel
		// No items in picker.

		next, cmd := m.Update(keyPress("enter"))
		m = next.(model)

		if m.chatState != chatStateSelectModel {
			t.Errorf("chatState = %v, want chatStateSelectModel", m.chatState)
		}
		if cmd != nil {
			t.Error("expected no command on Enter with empty model picker")
		}
	})
}

// TestCreationPickerNoProvidersRendersEmpty asserts that a nil providers list
// results in the "No providers are configured" message being rendered.
func TestCreationPickerNoProvidersRendersEmpty(t *testing.T) {
	fake := &fakeGroupClient{}
	m := initialModel("test", &apiClient{groups: fake, channels: &mockChannelClient{}})
	m.chatState = chatStateSelectProvider
	m.creationFlowID = 1

	next, _ := m.Update(providersLoadedMsg{flowID: 1, providers: nil})
	m = next.(model)

	if !m.creationProvidersLoaded {
		t.Fatal("creationProvidersLoaded should be true even with nil providers")
	}
	if len(m.creationPicker.items) != 0 {
		t.Fatalf("picker items = %d, want 0", len(m.creationPicker.items))
	}

	rendered := m.renderChat()
	if !strings.Contains(rendered, "No providers are configured") {
		t.Errorf("expected 'No providers are configured' in render, got:\n%s", rendered)
	}
}

// TestProvidersLoadedMsgStaleFlowIgnored asserts that a providersLoadedMsg
// carrying a stale flowID is dropped even while still in chatStateSelectProvider.
func TestProvidersLoadedMsgStaleFlowIgnored(t *testing.T) {
	fake := &fakeGroupClient{}
	m := initialModel("test", &apiClient{groups: fake, channels: &mockChannelClient{}})
	m.chatState = chatStateSelectProvider
	m.creationFlowID = 1 // current flow

	// Stale message from the previous flow (flowID=0).
	next, _ := m.Update(providersLoadedMsg{flowID: 0, providers: makeProviders("anthropic")})
	m = next.(model)

	if m.creationProvidersLoaded {
		t.Error("creationProvidersLoaded should remain false for stale flowID")
	}
	if len(m.creationPicker.items) != 0 {
		t.Errorf("picker items = %d, want 0 for stale flowID", len(m.creationPicker.items))
	}
	if m.chatState != chatStateSelectProvider {
		t.Errorf("chatState = %v, want chatStateSelectProvider", m.chatState)
	}
}

// TestUpdateProvidersLoadedError asserts state is correctly reset when
// provider fetching fails.
func TestUpdateProvidersLoadedError(t *testing.T) {
	fake := &fakeGroupClient{}
	m := initialModel("test", &apiClient{groups: fake, channels: &mockChannelClient{}})

	m.chatState = chatStateSelectProvider
	m.creationPendingGroupName = "my-group"
	m.creationSelectedProvider = "openai"
	m.creationProviders = makeProviders("openai")
	m.creationPicker = creationPickerState{items: []creationPickerItem{{id: "openai", label: "OpenAI"}}}
	m.creationProvidersLoaded = true

	someErr := errors.New("connection refused")
	next, _ := m.Update(providersLoadedMsg{err: someErr})
	m = next.(model)

	if m.chatState != chatStateSelectGroup {
		t.Errorf("chatState = %v, want chatStateSelectGroup", m.chatState)
	}
	if m.chatErr == nil {
		t.Fatal("chatErr should be set on provider load failure")
	}
	if !errors.Is(m.chatErr, someErr) {
		t.Errorf("chatErr does not wrap original error: %v", m.chatErr)
	}
	if m.creationPendingGroupName != "" {
		t.Errorf("creationPendingGroupName = %q, want empty", m.creationPendingGroupName)
	}
	if m.creationSelectedProvider != "" {
		t.Errorf("creationSelectedProvider = %q, want empty", m.creationSelectedProvider)
	}
	if m.creationProviders != nil {
		t.Errorf("creationProviders = %v, want nil after error", m.creationProviders)
	}
	if m.creationPicker.items != nil {
		t.Errorf("creationPicker.items should be nil after error, got %v", m.creationPicker.items)
	}
	if m.creationProvidersLoaded {
		t.Error("creationProvidersLoaded should be false after error")
	}
}

// TestTranslateListProvidersErr verifies the gRPC-to-user-message mapping for
// ListProviders errors.
func TestTranslateListProvidersErr(t *testing.T) {
	tests := []struct {
		name string
		in   error
		want string
	}{
		{"deadline", status.Error(codes.DeadlineExceeded, ""), "timed out contacting server"},
		{"unavailable", status.Error(codes.Unavailable, ""), "server unavailable"},
		{"unimplemented", status.Error(codes.Unimplemented, ""), "server does not support provider listing"},
		{"default", errors.New("boom"), "failed to load providers: boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := translateListProvidersErr(tt.in)
			if !strings.Contains(got.Error(), tt.want) {
				t.Errorf("translateListProvidersErr(%v).Error() = %q, want substring %q", tt.in, got.Error(), tt.want)
			}
		})
	}
}

// TestTranslateRegisterGroupErr verifies the gRPC-to-user-message mapping for
// RegisterGroup errors.
func TestTranslateRegisterGroupErr(t *testing.T) {
	tests := []struct {
		name string
		in   error
		want string
	}{
		{"already_exists", status.Error(codes.AlreadyExists, ""), "a group with that name already exists"},
		{"deadline", status.Error(codes.DeadlineExceeded, ""), "could not reach the server"},
		{"unavailable", status.Error(codes.Unavailable, ""), "could not reach the server"},
		{"invalid_argument", status.Error(codes.InvalidArgument, "bad input"), "invalid group configuration"},
		{"default", errors.New("boom"), "failed to create group: boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := translateRegisterGroupErr(tt.in)
			if !strings.Contains(got.Error(), tt.want) {
				t.Errorf("translateRegisterGroupErr(%v).Error() = %q, want substring %q", tt.in, got.Error(), tt.want)
			}
		})
	}
}

// TestUpdateGroupRegisteredError asserts that a groupRegisteredMsg with an
// error resets all creation fields and sets chatErr to the translated message.
func TestUpdateGroupRegisteredError(t *testing.T) {
	fake := &fakeGroupClient{}
	m := initialModel("test", &apiClient{groups: fake, channels: &mockChannelClient{}})

	m.chatState = chatStateSelectProvider
	m.creationPendingGroupName = "mygroup"
	m.creationSelectedProvider = "openai"
	m.creationProviders = makeProviders("openai")
	m.creationPicker = creationPickerState{items: []creationPickerItem{{id: "openai", label: "OpenAI"}}}
	m.creationProvidersLoaded = true

	// registerGroupCmd translates errors before packaging them; deliver a
	// pre-translated error to mirror what the real command produces.
	translatedErr := translateRegisterGroupErr(status.Error(codes.AlreadyExists, "already exists"))
	next, _ := m.Update(groupRegisteredMsg{err: translatedErr})
	m = next.(model)

	if m.chatState != chatStateSelectGroup {
		t.Errorf("chatState = %v, want chatStateSelectGroup", m.chatState)
	}
	if m.chatErr == nil {
		t.Fatal("chatErr should be set after registration error")
	}
	if !strings.Contains(m.chatErr.Error(), "a group with that name already exists") {
		t.Errorf("chatErr = %q, want translated message", m.chatErr.Error())
	}
	if m.creationPendingGroupName != "" {
		t.Errorf("creationPendingGroupName = %q, want empty", m.creationPendingGroupName)
	}
	if m.creationSelectedProvider != "" {
		t.Errorf("creationSelectedProvider = %q, want empty", m.creationSelectedProvider)
	}
	if m.creationProviders != nil {
		t.Errorf("creationProviders = %v, want nil", m.creationProviders)
	}
	if len(m.creationPicker.items) != 0 {
		t.Errorf("creationPicker.items = %v, want empty", m.creationPicker.items)
	}
	if m.creationProvidersLoaded {
		t.Error("creationProvidersLoaded should be false after error")
	}
}

// TestCreationPickerSelectProviderClearsChatErr asserts that selecting a
// provider (Enter) clears any stale chatErr before entering the model step.
func TestCreationPickerSelectProviderClearsChatErr(t *testing.T) {
	fake := &fakeGroupClient{}
	m := initialModel("test", &apiClient{groups: fake, channels: &mockChannelClient{}})
	m.chatState = chatStateSelectProvider
	m.creationFlowID = 1

	next, _ := m.Update(providersLoadedMsg{flowID: 1, providers: makeProviders("anthropic")})
	m = next.(model)

	m.chatErr = errors.New("stale error")

	next, _ = m.Update(keyPress("enter"))
	m = next.(model)

	if m.chatState != chatStateSelectModel {
		t.Fatalf("chatState = %v, want chatStateSelectModel", m.chatState)
	}
	if m.chatErr != nil {
		t.Errorf("chatErr = %v, want nil after Enter from provider picker", m.chatErr)
	}
}

// TestCreationPickerModelEscClearsChatErr asserts that pressing Esc from the
// model picker clears any stale chatErr before returning to provider selection.
func TestCreationPickerModelEscClearsChatErr(t *testing.T) {
	fake := &fakeGroupClient{}
	m := initialModel("test", &apiClient{groups: fake, channels: &mockChannelClient{}})
	m.chatState = chatStateSelectModel
	m.creationProviders = makeProviders("anthropic")
	m.creationSelectedProvider = "anthropic"
	m.creationPicker = creationPickerState{items: []creationPickerItem{{id: "model-a-anthropic", label: "Model A"}}}
	m.chatErr = errors.New("stale error")

	next, _ := m.Update(keyPress("esc"))
	m = next.(model)

	if m.chatState != chatStateSelectProvider {
		t.Fatalf("chatState = %v, want chatStateSelectProvider", m.chatState)
	}
	if m.chatErr != nil {
		t.Errorf("chatErr = %v, want nil after Esc from model picker", m.chatErr)
	}
}

// TestBuildProviderItemsDropsEmptyID asserts that providers with an empty ID
// are excluded from the picker, protecting downstream against empty registrations.
func TestBuildProviderItemsDropsEmptyID(t *testing.T) {
	empty := &kraclawv1.ProviderInfo{
		Id:     "",
		Models: []*kraclawv1.ModelInfo{{Id: "m1", DisplayName: "M1"}},
	}
	real := makeProvider("real", 1)

	items, _ := buildProviderItems([]*kraclawv1.ProviderInfo{empty, real}, "")
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1 (empty-ID provider dropped)", len(items))
	}
	if items[0].id != "real" {
		t.Errorf("item[0].id = %q, want %q", items[0].id, "real")
	}
}

// TestCreationPickerAllProvidersFilteredRenders asserts that when providers
// exist but all have zero models, the "no models" message is rendered (not the
// "no providers configured" message).
func TestCreationPickerAllProvidersFilteredRenders(t *testing.T) {
	fake := &fakeGroupClient{}
	m := initialModel("test", &apiClient{groups: fake, channels: &mockChannelClient{}})
	m.chatState = chatStateSelectProvider
	m.creationFlowID = 1

	noModels := makeProvider("empty-provider", 0)
	next, _ := m.Update(providersLoadedMsg{flowID: 1, providers: []*kraclawv1.ProviderInfo{noModels}})
	m = next.(model)

	rendered := m.renderChat()
	if strings.Contains(rendered, "No providers are configured on this server") {
		t.Errorf("got 'No providers are configured' message, expected the 'none have any models' message")
	}
	if !strings.Contains(rendered, "none have any models") {
		t.Errorf("expected 'none have any models' in render, got:\n%s", rendered)
	}
}
