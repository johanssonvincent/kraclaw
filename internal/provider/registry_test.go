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
