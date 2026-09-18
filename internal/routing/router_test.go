package routing

import (
	"context"
	"testing"

	"github.com/johanssonvincent/kraclaw/internal/provider"
)

func TestNew(t *testing.T) {
	cfg := Config{Strategy: StrategyFailoverOnly}
	reg := provider.NewRegistry()
	r := New(cfg, reg)
	if r == nil {
		t.Fatal("New() returned nil")
	}
}

func TestSelectProvider_FailoverOnly(t *testing.T) {
	cfg := Config{
		Strategy:        StrategyFailoverOnly,
		PrimaryProvider: "anthropic",
		FailoverProviders: "openai",
	}
	reg := provider.NewRegistry()
	r := New(cfg, reg)

	provider, err := r.SelectProvider(context.Background(), "test-group")
	if err != nil {
		t.Errorf("SelectProvider() error = %v", err)
	}
	if provider != "anthropic" {
		t.Errorf("SelectProvider() = %q, want %q", provider, "anthropic")
	}
}

func TestSelectProvider_RoundRobin(t *testing.T) {
	cfg := Config{
		Strategy: StrategyRoundRobin,
	}
	reg := provider.NewRegistry()
	r := New(cfg, reg)

	// First call should return one provider.
	p1, err := r.SelectProvider(context.Background(), "test-group")
	if err != nil {
		t.Fatalf("SelectProvider() error = %v", err)
	}

	// Second call might return the same or different (depends on health).
	p2, err := r.SelectProvider(context.Background(), "test-group")
	if err != nil {
		t.Fatalf("SelectProvider() error = %v", err)
	}

	// Both should be valid providers.
	valid := map[string]bool{"anthropic": true, "openai": true}
	if !valid[p1] {
		t.Errorf("p1 = %q is not a valid provider", p1)
	}
	if !valid[p2] {
		t.Errorf("p2 = %q is not a valid provider", p2)
	}
}

func TestRecordSuccess(t *testing.T) {
	cfg := Config{
		Strategy: StrategyFailoverOnly,
		HealthyThreshold: 2,
	}
	reg := provider.NewRegistry()
	r := New(cfg, reg)

	r.RecordSuccess("anthropic", 100.0)
	r.RecordSuccess("anthropic", 120.0)

	health := r.GetHealth()
	h := health["anthropic"]
	if h.Status != StatusHealthy {
		t.Errorf("Status = %v, want %v after %d successes", h.Status, StatusHealthy, cfg.HealthyThreshold)
	}
}

func TestRecordFailure(t *testing.T) {
	cfg := Config{
		Strategy:         StrategyFailoverOnly,
		UnhealthyThreshold: 3,
	}
	reg := provider.NewRegistry()
	r := New(cfg, reg)

	// Mark as healthy first.
	r.RecordSuccess("anthropic", 100.0)
	r.RecordSuccess("anthropic", 100.0)

	// Then fail.
	r.RecordFailure("anthropic", testErr{})
	r.RecordFailure("anthropic", testErr{})
	r.RecordFailure("anthropic", testErr{})

	health := r.GetHealth()
	h := health["anthropic"]
	if h.Status != StatusUnhealthy {
		t.Errorf("Status = %v, want %v after %d failures", h.Status, StatusUnhealthy, cfg.UnhealthyThreshold)
	}
}

func TestIsHealthy(t *testing.T) {
	cfg := Config{
		Strategy: StrategyFailoverOnly,
	}
	reg := provider.NewRegistry()
	r := New(cfg, reg)

	// Unknown provider should be considered healthy.
	if !r.isHealthy("unknown-provider") {
		t.Error("isHealthy() should return true for unknown provider")
	}

	// Mark a provider as unhealthy.
	r.mu.Lock()
	r.health["anthropic"] = &ProviderHealth{
		Name:   "anthropic",
		Status: StatusUnhealthy,
	}
	r.mu.Unlock()

	if r.isHealthy("anthropic") {
		t.Error("isHealthy() should return false for unhealthy provider")
	}
}

func TestGetHealth(t *testing.T) {
	cfg := Config{Strategy: StrategyFailoverOnly}
	reg := provider.NewRegistry()
	r := New(cfg, reg)

	health := r.GetHealth()
	if len(health) == 0 {
		t.Error("GetHealth() should return health for registered providers")
	}
}

func TestConfig_StrategyEnabled(t *testing.T) {
	tests := []struct {
		strategy Strategy
		want     bool
	}{
		{StrategyFailoverOnly, true},
		{StrategyCostOptimized, true},
		{"", false},
		{"none", false},
	}

	for _, tt := range tests {
		t.Run(string(tt.strategy), func(t *testing.T) {
			cfg := Config{Strategy: tt.strategy}
			if got := cfg.StrategyEnabled(); got != tt.want {
				t.Errorf("StrategyEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoadDefaultCosts(t *testing.T) {
	costs := loadDefaultCosts()
	if len(costs) == 0 {
		t.Error("loadDefaultCosts() should return costs")
	}

	// Check that we have costs for both providers.
	hasAnthropic := false
	hasOpenAI := false
	for _, c := range costs {
		if c.Provider == provider.ProviderAnthropic {
			hasAnthropic = true
		}
		if c.Provider == provider.ProviderOpenAI {
			hasOpenAI = true
		}
	}

	if !hasAnthropic {
		t.Error("Should have Anthropic costs")
	}
	if !hasOpenAI {
		t.Error("Should have OpenAI costs")
	}
}

type testErr struct{}

func (testErr) Error() string { return "test error" }
