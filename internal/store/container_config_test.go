package store

import "testing"

func TestContainerConfig_ProviderJSONRoundTrip(t *testing.T) {
	cc := &ContainerConfig{
		Model:    "gpt-5.4",
		Provider: "openai",
		Timeout:  30000,
	}
	data, err := ContainerConfigJSON(cc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, err := ParseContainerConfig(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Provider != "openai" {
		t.Fatalf("expected provider openai, got %q", parsed.Provider)
	}
}

func TestContainerConfig_ProviderDefaultsEmpty(t *testing.T) {
	data := []byte(`{"model":"claude-sonnet-4-6"}`)
	parsed, err := ParseContainerConfig(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Provider != "" {
		t.Fatalf("expected empty provider for legacy config, got %q", parsed.Provider)
	}
}
