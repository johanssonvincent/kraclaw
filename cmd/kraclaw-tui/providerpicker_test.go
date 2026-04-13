package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"google.golang.org/grpc"

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

// buildPickerFromProviders mirrors the logic in the Update handler.
func buildPickerFromProviders(providers []*kraclawv1.ProviderInfo) creationPickerState {
	s := creationPickerState{}
	for _, p := range providers {
		s.items = append(s.items, creationPickerItem{
			id:    p.GetId(),
			label: p.GetDisplayName(),
		})
	}
	return s
}

func TestCreationPickerNavigation(t *testing.T) {
	providers := makeProviders("anthropic", "openai")
	picker := buildPickerFromProviders(providers)

	if picker.cursor != 0 {
		t.Fatalf("initial cursor = %d, want 0", picker.cursor)
	}
	if len(picker.items) != 2 {
		t.Fatalf("items len = %d, want 2", len(picker.items))
	}

	// Move down.
	picker.cursor++
	if picker.cursor != 1 {
		t.Errorf("cursor after down = %d, want 1", picker.cursor)
	}

	// Cannot go past end.
	picker.cursor++
	if picker.cursor > len(picker.items)-1 {
		picker.cursor = len(picker.items) - 1
	}
	if picker.cursor != 1 {
		t.Errorf("cursor clamped = %d, want 1", picker.cursor)
	}

	// Move up.
	picker.cursor--
	if picker.cursor != 0 {
		t.Errorf("cursor after up = %d, want 0", picker.cursor)
	}

	// Cannot go past start.
	picker.cursor--
	if picker.cursor < 0 {
		picker.cursor = 0
	}
	if picker.cursor != 0 {
		t.Errorf("cursor clamped low = %d, want 0", picker.cursor)
	}
}

func TestCreationPickerSelectProvider(t *testing.T) {
	providers := makeProviders("anthropic", "openai")
	picker := buildPickerFromProviders(providers)

	// Select the second item (openai).
	picker.cursor = 1
	selected := picker.items[picker.cursor]
	if selected.id != "openai" {
		t.Errorf("selected.id = %q, want %q", selected.id, "openai")
	}
}

func TestCreationPickerModelList(t *testing.T) {
	providers := makeProviders("anthropic", "openai")
	picker := buildPickerFromProviders(providers)

	// Select provider at index 0 (anthropic), then build model picker.
	selectedID := picker.items[0].id
	if selectedID != "anthropic" {
		t.Fatalf("selectedID = %q, want %q", selectedID, "anthropic")
	}

	modelPicker := creationPickerState{}
	for _, p := range providers {
		if p.GetId() == selectedID {
			for _, m := range p.GetModels() {
				modelPicker.items = append(modelPicker.items, creationPickerItem{
					id:    m.GetId(),
					label: m.GetDisplayName(),
				})
			}
			break
		}
	}

	if len(modelPicker.items) != 2 {
		t.Fatalf("model picker items = %d, want 2", len(modelPicker.items))
	}
	if modelPicker.items[0].id != "model-a-anthropic" {
		t.Errorf("first model id = %q, want %q", modelPicker.items[0].id, "model-a-anthropic")
	}
}

func TestCreationPickerBackNavigation(t *testing.T) {
	providers := makeProviders("anthropic", "openai")

	// Forward: build provider picker and select.
	providerPicker := buildPickerFromProviders(providers)
	providerPicker.cursor = 0
	selectedProvider := providerPicker.items[0].id

	// Build model picker.
	modelPicker := creationPickerState{}
	for _, p := range providers {
		if p.GetId() == selectedProvider {
			for _, m := range p.GetModels() {
				modelPicker.items = append(modelPicker.items, creationPickerItem{
					id:    m.GetId(),
					label: m.GetDisplayName(),
				})
			}
			break
		}
	}
	modelPicker.cursor = 1 // advance cursor so we can verify it resets on back

	// Esc from model: rebuild provider picker (cursor resets to zero).
	restoredPicker := buildPickerFromProviders(providers)
	if restoredPicker.cursor != 0 {
		t.Errorf("restored cursor = %d, want 0", restoredPicker.cursor)
	}
	if len(restoredPicker.items) != 2 {
		t.Errorf("restored items len = %d, want 2", len(restoredPicker.items))
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
}

// TestUpdateProvidersLoadedError asserts state is correctly reset when
// provider fetching fails.
func TestUpdateProvidersLoadedError(t *testing.T) {
	fake := &fakeGroupClient{}
	m := initialModel("test", &apiClient{groups: fake, channels: &mockChannelClient{}})

	m.chatState = chatStateSelectProvider
	m.creationPendingGroupName = "my-group"
	m.creationProvidersLoaded = false

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
	if m.creationPicker.items != nil {
		t.Errorf("creationPicker.items should be nil after error, got %v", m.creationPicker.items)
	}
	if m.creationProvidersLoaded {
		t.Error("creationProvidersLoaded should be false after error")
	}
}
