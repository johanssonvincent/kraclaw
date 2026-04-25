package provider

import "testing"

func TestNewRegistry_ContainsAnthropic(t *testing.T) {
	r := NewRegistry()
	p, ok := r.Get("anthropic")
	if !ok {
		t.Fatal("expected anthropic provider")
	}
	if p.DefaultModel == "" {
		t.Fatal("expected default model")
	}
}

func TestNewRegistry_ContainsOpenAI(t *testing.T) {
	r := NewRegistry()
	p, ok := r.Get("openai")
	if !ok {
		t.Fatal("expected openai provider")
	}
	if p.DefaultModel == "" {
		t.Fatal("expected default model")
	}
}

func TestRegistry_ValidateModel_Valid(t *testing.T) {
	r := NewRegistry()
	if err := r.ValidateModel("anthropic", "claude-sonnet-4-6"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRegistry_ValidateModel_InvalidModel(t *testing.T) {
	r := NewRegistry()
	if err := r.ValidateModel("anthropic", "gpt-5.4"); err == nil {
		t.Fatal("expected error for wrong provider model")
	}
}

func TestRegistry_ValidateModel_UnknownProvider(t *testing.T) {
	r := NewRegistry()
	if err := r.ValidateModel("gemini", "gemini-pro"); err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestRegistry_ValidateModel_EmptyModelAllowed(t *testing.T) {
	r := NewRegistry()
	if err := r.ValidateModel("openai", ""); err != nil {
		t.Fatalf("empty model should be allowed (uses default): %v", err)
	}
}

func TestRegistry_DefaultProvider(t *testing.T) {
	r := NewRegistry()
	if r.DefaultProvider() != "anthropic" {
		t.Fatalf("expected default provider anthropic, got %s", r.DefaultProvider())
	}
}

func TestRegistry_Models_ReturnsCorrectProvider(t *testing.T) {
	r := NewRegistry()
	models := r.Models("openai")
	if len(models) == 0 {
		t.Fatal("expected openai models")
	}
	for _, m := range models {
		if m.ID == "claude-sonnet-4-6" {
			t.Fatal("openai models should not contain claude models")
		}
	}
}

func TestRegistry_AuthMode(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		provider string
		want     AuthMode
	}{
		"anthropic uses api_key": {provider: ProviderAnthropic, want: AuthModeAPIKey},
		"openai uses chatgpt":    {provider: ProviderOpenAI, want: AuthModeChatGPT},
	}
	r := NewRegistry()
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p, ok := r.Get(tt.provider)
			if !ok {
				t.Fatalf("provider %q not registered", tt.provider)
			}
			if p.AuthMode != tt.want {
				t.Errorf("AuthMode for %q = %q, want %q", tt.provider, p.AuthMode, tt.want)
			}
		})
	}
}

func TestAuthMode_Valid(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		m    AuthMode
		want bool
	}{
		"api_key": {m: AuthModeAPIKey, want: true},
		"chatgpt": {m: AuthModeChatGPT, want: true},
		"empty":   {m: AuthMode(""), want: false},
		"unknown": {m: AuthMode("shipt"), want: false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := tt.m.Valid(); got != tt.want {
				t.Errorf("AuthMode(%q).Valid() = %v, want %v", string(tt.m), got, tt.want)
			}
		})
	}
}

func TestNewRegistryForTest_RejectsInvalidAuthMode(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic for invalid AuthMode in NewRegistryForTest")
		}
	}()
	NewRegistryForTest(map[string]ProviderInfo{
		"x": {ID: "x", AuthMode: AuthMode("shipt")},
	})
}
