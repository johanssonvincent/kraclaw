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
