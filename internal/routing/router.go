package routing

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/johanssonvincent/kraclaw/internal/provider"
)

// Strategy defines how to select a provider.
type Strategy string

const (
	// StrategyCostOptimized selects the cheapest available provider.
	StrategyCostOptimized Strategy = "cost_optimized"

	// StrategyQualityOptimized selects the highest quality provider.
	StrategyQualityOptimized Strategy = "quality_optimized"

	// StrategyLatencyOptimized selects the provider with lowest latency.
	StrategyLatencyOptimized Strategy = "latency_optimized"

	// StrategyRoundRobin distributes requests evenly.
	StrategyRoundRobin Strategy = "round_robin"

	// StrategyFailoverOnly uses primary provider with failover on error.
	StrategyFailoverOnly Strategy = "failover_only"
)

// Config holds routing configuration.
type Config struct {
	// Strategy defines the provider selection strategy.
	Strategy Strategy `envconfig:"ROUTING_STRATEGY" default:"failover_only"`

	// PrimaryProvider is the preferred provider.
	PrimaryProvider string `envconfig:"ROUTING_PRIMARY_PROVIDER" default:"anthropic"`

	// FailoverProviders is a comma-separated list of failover providers.
	FailoverProviders string `envconfig:"ROUTING_FAILOVER_PROVIDERS" default:"openai"`

	// MaxRetries is the maximum number of retries across providers.
	MaxRetries int `envconfig:"ROUTING_MAX_RETRIES" default:"2"`

	// HealthCheckInterval is the interval between health checks.
	HealthCheckInterval time.Duration `envconfig:"ROUTING_HEALTH_CHECK_INTERVAL" default:"60s"`

	// HealthCheckTimeout is the timeout for health check requests.
	HealthCheckTimeout time.Duration `envconfig:"ROUTING_HEALTH_CHECK_TIMEOUT" default:"5s"`

	// UnhealthyThreshold is the number of consecutive failures before marking a provider unhealthy.
	UnhealthyThreshold int `envconfig:"ROUTING_UNHEALTHY_THRESHOLD" default:"3"`

	// HealthyThreshold is the number of consecutive successes before marking a provider healthy.
	HealthyThreshold int `envconfig:"ROUTING_HEALTHY_THRESHOLD" default:"2"`

	// EnableCostOptimization enables cost-based routing.
	EnableCostOptimization bool `envconfig:"ROUTING_COST_OPTIMIZED" default:"false"`
}

// ProviderCost holds cost information for a provider/model.
type ProviderCost struct {
	Provider    string
	Model       string
	InputCost   float64 // Cost per 1M input tokens
	OutputCost  float64 // Cost per 1M output tokens
	MinTokens   int
	MaxTokens   int
}

// ProviderHealth tracks health status of a provider.
type ProviderHealth struct {
	Name            string
	Status          HealthStatus
	LastCheck       time.Time
	LastResponseMs  float64
	LastError       error
	ConsecutiveFail int
	ConsecutiveOK   int
	TotalRequests   int64
	TotalErrors     int64
	AvgLatencyMs    float64
}

// HealthStatus represents the health status of a provider.
type HealthStatus string

const (
	// StatusHealthy means the provider is healthy.
	StatusHealthy HealthStatus = "healthy"

	// StatusUnhealthy means the provider is unhealthy.
	StatusUnhealthy HealthStatus = "unhealthy"

	// StatusUnknown means the provider health is unknown.
	StatusUnknown HealthStatus = "unknown"
)

// Router handles provider selection and failover.
type Router struct {
	cfg           Config
	registry      *provider.Registry
	health        map[string]*ProviderHealth
	costs         []ProviderCost
	mu            sync.RWMutex
	rrIndex       int
	log           *slog.Logger
	healthTicker  *time.Ticker
	ctx           context.Context
	cancel        context.CancelFunc
}

// New creates a new provider router.
func New(cfg Config, registry *provider.Registry) *Router {
	r := &Router{
		cfg:      cfg,
		registry: registry,
		health:   make(map[string]*ProviderHealth),
		log:      slog.With("component", "routing"),
	}

	// Initialize health status for known providers.
	for _, name := range registry.Providers() {
		r.health[name] = &ProviderHealth{
			Name:   name,
			Status: StatusUnknown,
		}
	}

	// Load default costs.
	r.costs = loadDefaultCosts()

	return r
}

// Start begins the health check loop.
func (r *Router) Start(ctx context.Context) {
	r.ctx, r.cancel = context.WithCancel(ctx)

	if r.cfg.HealthCheckInterval > 0 {
		r.healthTicker = time.NewTicker(r.cfg.HealthCheckInterval)
		go r.healthCheckLoop()
	}
}

// Stop stops the health check loop.
func (r *Router) Stop() {
	if r.cancel != nil {
		r.cancel()
	}

	if r.healthTicker != nil {
		r.healthTicker.Stop()
	}
}

// SelectProvider chooses a provider based on the configured strategy.
func (r *Router) SelectProvider(ctx context.Context, groupJID string) (string, error) {
	if !r.cfg.StrategyEnabled() {
		return r.cfg.PrimaryProvider, nil
	}

	switch r.cfg.Strategy {
	case StrategyFailoverOnly:
		return r.selectFailover(ctx)

	case StrategyCostOptimized:
		return r.selectCostOptimized(ctx)

	case StrategyQualityOptimized:
		return r.selectQualityOptimized(ctx)

	case StrategyLatencyOptimized:
		return r.selectLatencyOptimized(ctx)

	case StrategyRoundRobin:
		return r.selectRoundRobin(ctx)

	default:
		return r.cfg.PrimaryProvider, nil
	}
}

// selectFailover returns the primary provider or first healthy failover.
func (r *Router) selectFailover(_ context.Context) (string, error) {
	providers := []string{r.cfg.PrimaryProvider}

	// Add failover providers.
	if r.cfg.FailoverProviders != "" {
		for _, p := range strings.Split(r.cfg.FailoverProviders, ",") {
			p = strings.TrimSpace(p)
			if p != "" && p != r.cfg.PrimaryProvider {
				providers = append(providers, p)
			}
		}
	}

	// Return first healthy provider.
	for _, name := range providers {
		if r.isHealthy(name) {
			return name, nil
		}
	}

	// If no healthy providers, return primary anyway (let it fail).
	return r.cfg.PrimaryProvider, nil
}

// selectCostOptimized returns the cheapest healthy provider.
func (r *Router) selectCostOptimized(_ context.Context) (string, error) {
	minCost := math.MaxFloat64
	selected := ""

	for _, cost := range r.costs {
		if !r.isHealthy(cost.Provider) {
			continue
		}

		// Use average of input and output cost.
		avgCost := (cost.InputCost + cost.OutputCost) / 2

		if avgCost < minCost {
			minCost = avgCost
			selected = cost.Provider
		}
	}

	if selected == "" {
		return r.cfg.PrimaryProvider, nil
	}

	return selected, nil
}

// selectQualityOptimized returns the highest quality healthy provider.
func (r *Router) selectQualityOptimized(_ context.Context) (string, error) {
	// Priority order: opus > sonnet > haiku for Anthropic, gpt-5.5 > gpt-5 > gpt-4 for OpenAI.
	qualityOrder := map[string]int{
		"anthropic": 10,
		"openai":    5,
	}

	bestScore := -1
	selected := ""

	for _, name := range r.registry.Providers() {
		if !r.isHealthy(name) {
			continue
		}

		score := qualityOrder[name]
		if score > bestScore {
			bestScore = score
			selected = name
		}
	}

	if selected == "" {
		return r.cfg.PrimaryProvider, nil
	}

	return selected, nil
}

// selectLatencyOptimized returns the provider with lowest average latency.
func (r *Router) selectLatencyOptimized(_ context.Context) (string, error) {
	minLatency := math.MaxFloat64
	selected := ""

	r.mu.RLock()
	defer r.mu.RUnlock()

	for name, health := range r.health {
		if health.Status != StatusHealthy {
			continue
		}

		if health.AvgLatencyMs > 0 && health.AvgLatencyMs < minLatency {
			minLatency = health.AvgLatencyMs
			selected = name
		}
	}

	if selected == "" {
		return r.cfg.PrimaryProvider, nil
	}

	return selected, nil
}

// selectRoundRobin returns providers in round-robin order.
func (r *Router) selectRoundRobin(_ context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	providers := r.registry.Providers()

	for i := 0; i < len(providers); i++ {
		idx := r.rrIndex % len(providers)
		r.rrIndex++

		name := providers[idx]
		if r.isHealthy(name) {
			return name, nil
		}
	}

	// If no healthy providers, return first one.
	return providers[0], nil
}

// RecordSuccess records a successful request to a provider.
func (r *Router) RecordSuccess(provider string, latencyMs float64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	health, ok := r.health[provider]
	if !ok {
		health = &ProviderHealth{Name: provider}
		r.health[provider] = health
	}

	health.ConsecutiveFail = 0
	health.ConsecutiveOK++
	health.TotalRequests++
	health.LastResponseMs = latencyMs
	health.LastCheck = time.Now()

	// Update average latency (exponential moving average).
	if health.AvgLatencyMs == 0 {
		health.AvgLatencyMs = latencyMs
	} else {
		health.AvgLatencyMs = health.AvgLatencyMs*0.7 + latencyMs*0.3
	}

	// Check if provider should be marked healthy.
	if health.Status == StatusUnhealthy && health.ConsecutiveOK >= r.cfg.HealthyThreshold {
		health.Status = StatusHealthy
		r.log.Info("provider recovered", "provider", provider)
	} else if health.Status == StatusUnknown && health.ConsecutiveOK >= r.cfg.HealthyThreshold {
		health.Status = StatusHealthy
		r.log.Info("provider marked healthy", "provider", provider)
	}
}

// RecordFailure records a failed request to a provider.
func (r *Router) RecordFailure(provider string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	health, ok := r.health[provider]
	if !ok {
		health = &ProviderHealth{Name: provider}
		r.health[provider] = health
	}

	health.ConsecutiveOK = 0
	health.ConsecutiveFail++
	health.TotalRequests++
	health.TotalErrors++
	health.LastError = err
	health.LastCheck = time.Now()

	// Check if provider should be marked unhealthy.
	if health.Status != StatusUnhealthy && health.ConsecutiveFail >= r.cfg.UnhealthyThreshold {
		health.Status = StatusUnhealthy
		r.log.Warn("provider marked unhealthy", "provider", provider, "error", err, "consecutive_failures", health.ConsecutiveFail)
	}
}

// isHealthy checks if a provider is healthy.
func (r *Router) isHealthy(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	health, ok := r.health[name]
	if !ok {
		return true // Unknown providers are assumed healthy.
	}

	return health.Status != StatusUnhealthy
}

// GetHealth returns the health status of all providers.
func (r *Router) GetHealth() map[string]*ProviderHealth {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[string]*ProviderHealth)
	for name, health := range r.health {
		result[name] = &ProviderHealth{
			Name:            health.Name,
			Status:          health.Status,
			LastCheck:       health.LastCheck,
			LastResponseMs:  health.LastResponseMs,
			LastError:       health.LastError,
			ConsecutiveFail: health.ConsecutiveFail,
			ConsecutiveOK:   health.ConsecutiveOK,
			TotalRequests:   health.TotalRequests,
			TotalErrors:     health.TotalErrors,
			AvgLatencyMs:    health.AvgLatencyMs,
		}
	}

	return result
}

// healthCheckLoop periodically checks provider health.
func (r *Router) healthCheckLoop() {
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-r.healthTicker.C:
			r.checkAllProviders()
		}
	}
}

// checkAllProviders performs health checks on all providers.
func (r *Router) checkAllProviders() {
	for _, name := range r.registry.Providers() {
		go r.checkProvider(name)
	}
}

// checkProvider performs a health check on a single provider.
func (r *Router) checkProvider(name string) {
	ctx, cancel := context.WithTimeout(r.ctx, r.cfg.HealthCheckTimeout)
	defer cancel()

	start := time.Now()

	// Perform a lightweight health check (e.g., check if the API is reachable).
	err := r.doHealthCheck(ctx, name)
	latencyMs := float64(time.Since(start).Milliseconds())

	if err != nil {
		r.RecordFailure(name, err)
	} else {
		r.RecordSuccess(name, latencyMs)
	}
}

// doHealthCheck performs an actual health check request.
func (r *Router) doHealthCheck(ctx context.Context, name string) error {
	// For now, just check if the provider is registered.
	// In production, this would make a lightweight API call.
	if _, ok := r.registry.Get(name); !ok {
		return fmt.Errorf("provider %q not registered", name)
	}

	return nil
}

// StrategyEnabled returns whether routing strategy is active.
func (c *Config) StrategyEnabled() bool {
	return c.Strategy != "" && c.Strategy != "none"
}

// RetryHandler wraps an HTTP handler with automatic retries across providers.
type RetryHandler struct {
	router  *Router
	handler http.HandlerFunc
}

// NewRetryHandler creates a new retry handler.
func NewRetryHandler(router *Router, handler http.HandlerFunc) *RetryHandler {
	return &RetryHandler{router: router, handler: handler}
}

// ServeHTTP handles requests with automatic retries.
func (h *RetryHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	groupJID := r.Header.Get("X-Kraclaw-Group")

	// Get the original provider header.
	originalProvider := r.Header.Get("X-Kraclaw-Provider")

	maxRetries := h.router.cfg.MaxRetries
	lastErr := ""

	for attempt := 0; attempt <= maxRetries; attempt++ {
		// Select provider.
		provider := originalProvider
		if provider == "" {
			selected, err := h.router.SelectProvider(r.Context(), groupJID)
			if err != nil {
				http.Error(w, fmt.Sprintf("failed to select provider: %v", err), http.StatusBadGateway)
				return
			}

			provider = selected
		}

		// Set provider header.
		r.Header.Set("X-Kraclaw-Provider", provider)

		// Create a response recorder.
		rec := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}

		// Start timer.
		start := time.Now()

		// Execute handler.
		h.handler(rec, r)

		latencyMs := float64(time.Since(start).Milliseconds())

		// Record result.
		if rec.statusCode < 500 {
			h.router.RecordSuccess(provider, latencyMs)
			return
		}

		// Server error — record failure and retry.
		lastErr = fmt.Sprintf("provider %q returned %d", provider, rec.statusCode)
		h.router.RecordFailure(provider, fmt.Errorf("provider %q returned %d", provider, rec.statusCode))

		h.router.log.Debug("provider error, retrying",
			"provider", provider,
			"status", rec.statusCode,
			"attempt", attempt+1,
			"max_retries", maxRetries,
		)

		// Clear original provider to allow selection of different one.
		originalProvider = ""
	}

	// All retries exhausted.
	h.router.log.Error("all provider retries exhausted", "error", lastErr)
	http.Error(w, fmt.Sprintf("all providers failed: %s", lastErr), http.StatusBadGateway)
}

type responseRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *responseRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

// loadDefaultCosts returns default cost information for known providers.
func loadDefaultCosts() []ProviderCost {
	return []ProviderCost{
		// Anthropic models (per 1M tokens)
		{Provider: provider.ProviderAnthropic, Model: "claude-opus-4-6", InputCost: 15.00, OutputCost: 75.00},
		{Provider: provider.ProviderAnthropic, Model: "claude-sonnet-4-6", InputCost: 3.00, OutputCost: 15.00},
		{Provider: provider.ProviderAnthropic, Model: "claude-haiku-4-5", InputCost: 0.80, OutputCost: 4.00},
		// OpenAI models (per 1M tokens)
		{Provider: provider.ProviderOpenAI, Model: "gpt-5.5", InputCost: 12.00, OutputCost: 48.00},
		{Provider: provider.ProviderOpenAI, Model: "gpt-5.4", InputCost: 7.50, OutputCost: 30.00},
		{Provider: provider.ProviderOpenAI, Model: "gpt-5.2", InputCost: 2.50, OutputCost: 10.00},
	}
}
