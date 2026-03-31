package provider

import "fmt"

// ModelInfo describes a single model offered by a provider.
type ModelInfo struct {
	ID          string
	DisplayName string
}

// ProviderInfo describes a supported AI provider.
type ProviderInfo struct {
	ID           string
	DisplayName  string
	Models       []ModelInfo
	DefaultModel string
}

// Registry holds all known providers and their models.
type Registry struct {
	providers map[string]ProviderInfo
}

// NewRegistry creates a registry pre-populated with Anthropic and OpenAI.
func NewRegistry() *Registry {
	r := &Registry{providers: make(map[string]ProviderInfo)}

	r.providers["anthropic"] = ProviderInfo{
		ID:           "anthropic",
		DisplayName:  "Anthropic",
		DefaultModel: "claude-sonnet-4-6",
		Models: []ModelInfo{
			{ID: "claude-opus-4-6", DisplayName: "Claude Opus 4.6"},
			{ID: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6"},
			{ID: "claude-haiku-4-5", DisplayName: "Claude Haiku 4.5"},
			{ID: "claude-sonnet-4-5", DisplayName: "Claude Sonnet 4.5"},
			{ID: "claude-opus-4-5", DisplayName: "Claude Opus 4.5"},
			{ID: "claude-opus-4-1", DisplayName: "Claude Opus 4.1"},
			{ID: "claude-sonnet-4-0", DisplayName: "Claude Sonnet 4"},
			{ID: "claude-opus-4-0", DisplayName: "Claude Opus 4"},
		},
	}

	r.providers["openai"] = ProviderInfo{
		ID:           "openai",
		DisplayName:  "OpenAI",
		DefaultModel: "gpt-5.4",
		Models: []ModelInfo{
			{ID: "gpt-5.4", DisplayName: "GPT-5.4"},
			{ID: "gpt-5.4-mini", DisplayName: "GPT-5.4 Mini"},
			{ID: "gpt-5.4-nano", DisplayName: "GPT-5.4 Nano"},
			{ID: "gpt-5.4-pro", DisplayName: "GPT-5.4 Pro"},
			{ID: "gpt-5.3-codex", DisplayName: "GPT-5.3 Codex"},
			{ID: "o3-mini", DisplayName: "o3-mini"},
		},
	}

	return r
}

// Get returns a provider by ID.
func (r *Registry) Get(id string) (ProviderInfo, bool) {
	p, ok := r.providers[id]
	return p, ok
}

// DefaultProvider returns "anthropic" for backwards compatibility.
func (r *Registry) DefaultProvider() string {
	return "anthropic"
}

// ValidateModel checks that model belongs to provider. Empty model is valid (uses default).
func (r *Registry) ValidateModel(providerID, model string) error {
	p, ok := r.providers[providerID]
	if !ok {
		return fmt.Errorf("unknown provider %q", providerID)
	}
	if model == "" {
		return nil
	}
	for _, m := range p.Models {
		if m.ID == model {
			return nil
		}
	}
	return fmt.Errorf("model %q is not valid for provider %q", model, providerID)
}

// Models returns the model list for a provider. Returns nil for unknown providers.
func (r *Registry) Models(providerID string) []ModelInfo {
	p, ok := r.providers[providerID]
	if !ok {
		return nil
	}
	out := make([]ModelInfo, len(p.Models))
	copy(out, p.Models)
	return out
}

// Providers returns all registered provider IDs.
func (r *Registry) Providers() []string {
	ids := make([]string, 0, len(r.providers))
	for id := range r.providers {
		ids = append(ids, id)
	}
	return ids
}
