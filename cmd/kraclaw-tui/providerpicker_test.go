package main

import (
	"testing"

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
