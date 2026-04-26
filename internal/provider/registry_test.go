package provider

import "testing"

func TestRegistry_Get(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		provider        string
		wantOK          bool
		wantDefaultNonE bool
	}{
		"anthropic registered with default model": {provider: "anthropic", wantOK: true, wantDefaultNonE: true},
		"openai registered with default model":    {provider: "openai", wantOK: true, wantDefaultNonE: true},
		"unknown provider returns ok=false":       {provider: "gemini", wantOK: false, wantDefaultNonE: false},
	}
	r := NewRegistry()
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p, ok := r.Get(tt.provider)
			if ok != tt.wantOK {
				t.Errorf("r.Get(%q) ok = %v, want %v", tt.provider, ok, tt.wantOK)
			}
			if tt.wantOK && tt.wantDefaultNonE && p.DefaultModel == "" {
				t.Errorf("r.Get(%q).DefaultModel = %q, want non-empty", tt.provider, p.DefaultModel)
			}
		})
	}
}

func TestRegistry_ValidateModel(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		provider string
		model    string
		wantErr  bool
	}{
		"anthropic + valid model":           {provider: "anthropic", model: "claude-sonnet-4-6", wantErr: false},
		"anthropic + openai model errors":   {provider: "anthropic", model: "gpt-5.4", wantErr: true},
		"unknown provider errors":           {provider: "gemini", model: "gemini-pro", wantErr: true},
		"openai + empty model uses default": {provider: "openai", model: "", wantErr: false},
	}
	r := NewRegistry()
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := r.ValidateModel(tt.provider, tt.model)
			gotErr := err != nil
			if gotErr != tt.wantErr {
				t.Errorf("r.ValidateModel(%q, %q) err = %v, wantErr = %v", tt.provider, tt.model, err, tt.wantErr)
			}
		})
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
