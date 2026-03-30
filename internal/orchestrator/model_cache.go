package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

type refreshResult struct {
	models    []modelInfo
	refreshed bool
	err       error
}

type modelInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type modelsResponse struct {
	Data []modelInfo `json:"data"`
}

type modelCache struct {
	baseURL string
	version string
	ttl     time.Duration
	client  *http.Client
	log     *slog.Logger

	useHardcodedModelList bool
	hardcodedModels       []modelInfo

	oauthExchangePath string
	oauthAPIKeyTTL    time.Duration

	mu      sync.RWMutex
	fetched time.Time
	models  []modelInfo
	lastErr error

	oauthAPIKey   string
	oauthAPIKeyAt time.Time

	sf singleflight.Group
}

func newModelCache(proxyAddr string, version string, ttl time.Duration, useHardcodedModelList bool, log *slog.Logger) *modelCache {
	if log == nil {
		log = slog.Default()
	}
	return &modelCache{
		baseURL:               normalizeProxyURL(proxyAddr),
		version:               strings.TrimSpace(version),
		ttl:                   ttl,
		client:                &http.Client{Timeout: 10 * time.Second},
		log:                   log.With("component", "model-cache"),
		useHardcodedModelList: useHardcodedModelList,
		hardcodedModels:       defaultOAuthAllowedModels(),
		oauthExchangePath:     "/api/oauth/claude_cli/create_api_key",
		oauthAPIKeyTTL:        15 * time.Minute,
	}
}

func (c *modelCache) List(ctx context.Context) ([]modelInfo, bool, error) {
	c.mu.RLock()
	models := append([]modelInfo(nil), c.models...)
	fetched := c.fetched
	lastErr := c.lastErr
	c.mu.RUnlock()

	if len(models) > 0 && time.Since(fetched) < c.ttl {
		return models, false, lastErr
	}

	v, err, _ := c.sf.Do("refresh", func() (interface{}, error) {
		models, refreshed, refreshErr := c.refresh(ctx)
		return &refreshResult{models: models, refreshed: refreshed, err: refreshErr}, nil
	})
	if err != nil {
		return nil, true, err
	}
	r := v.(*refreshResult)
	return r.models, r.refreshed, r.err
}

func (c *modelCache) refresh(ctx context.Context) ([]modelInfo, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.models) > 0 && time.Since(c.fetched) < c.ttl {
		return append([]modelInfo(nil), c.models...), false, c.lastErr
	}

	if c.useHardcodedModelList {
		c.models = append([]modelInfo(nil), c.hardcodedModels...)
		c.fetched = time.Now()
		c.lastErr = nil
		if len(c.models) == 0 {
			c.lastErr = fmt.Errorf("models response empty")
		}
		return append([]modelInfo(nil), c.models...), false, c.lastErr
	}

	apiKey := c.currentOAuthAPIKey()
	parsed, statusCode, err := c.fetchModels(ctx, apiKey)
	if err != nil {
		if statusCode == http.StatusUnauthorized {
			c.log.Debug("models request unauthorized; attempting oauth api-key exchange fallback")
			exchangedKey, exchangeErr := c.exchangeOAuthAPIKey(ctx)
			if exchangeErr != nil {
				c.log.Debug("oauth api-key exchange fallback failed", "error", exchangeErr)
				c.lastErr = fmt.Errorf("models request failed: %w", err)
				return append([]modelInfo(nil), c.models...), true, c.lastErr
			}

			parsed, _, err = c.fetchModels(ctx, exchangedKey)
			if err != nil {
				c.log.Debug("oauth api-key exchange fallback retry failed", "error", err)
				c.lastErr = fmt.Errorf("models request failed after oauth exchange: %w", err)
				return append([]modelInfo(nil), c.models...), true, c.lastErr
			}
			c.log.Debug("oauth api-key exchange fallback succeeded")
		} else {
			c.lastErr = fmt.Errorf("models request failed: %w", err)
			return append([]modelInfo(nil), c.models...), true, c.lastErr
		}
	}

	c.models = parsed.Data
	c.fetched = time.Now()
	c.lastErr = nil

	if len(c.models) == 0 {
		c.lastErr = fmt.Errorf("models response empty")
	}

	return append([]modelInfo(nil), c.models...), false, c.lastErr
}

func (c *modelCache) fetchModels(ctx context.Context, apiKey string) (modelsResponse, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/models", nil)
	if err != nil {
		return modelsResponse{}, 0, err
	}
	if c.version != "" {
		req.Header.Set("anthropic-version", c.version)
	}
	if apiKey != "" {
		req.Header.Set("X-Api-Key", apiKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return modelsResponse{}, 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return modelsResponse{}, resp.StatusCode, fmt.Errorf("%s", resp.Status)
	}

	var parsed modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return modelsResponse{}, resp.StatusCode, err
	}

	return parsed, resp.StatusCode, nil
}

func (c *modelCache) exchangeOAuthAPIKey(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+c.oauthExchangePath, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer placeholder")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("oauth exchange failed: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	key, err := extractAPIKey(body)
	if err != nil {
		return "", err
	}

	c.oauthAPIKey = key
	c.oauthAPIKeyAt = time.Now()
	return key, nil
}

func (c *modelCache) currentOAuthAPIKey() string {
	if c.oauthAPIKey == "" {
		return ""
	}
	if c.oauthAPIKeyTTL > 0 && time.Since(c.oauthAPIKeyAt) > c.oauthAPIKeyTTL {
		c.oauthAPIKey = ""
		c.oauthAPIKeyAt = time.Time{}
		return ""
	}
	return c.oauthAPIKey
}

func extractAPIKey(payload []byte) (string, error) {
	var parsed any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return "", fmt.Errorf("decode oauth exchange response: %w", err)
	}
	if key := findAPIKey(parsed); key != "" {
		return key, nil
	}
	return "", fmt.Errorf("oauth exchange response did not include API key")
}

func findAPIKey(v any) string {
	switch val := v.(type) {
	case map[string]any:
		for _, field := range []string{"api_key", "key", "access_token", "token"} {
			if raw, ok := val[field]; ok {
				if s, ok := raw.(string); ok && strings.HasPrefix(s, "sk-ant-") {
					return s
				}
			}
		}
		for _, raw := range val {
			if key := findAPIKey(raw); key != "" {
				return key
			}
		}
	case []any:
		for _, raw := range val {
			if key := findAPIKey(raw); key != "" {
				return key
			}
		}
	case string:
		if strings.HasPrefix(val, "sk-ant-") {
			return val
		}
	}
	return ""
}

func normalizeProxyURL(addr string) string {
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return strings.TrimRight(addr, "/")
	}
	if strings.HasPrefix(addr, ":") {
		return "http://127.0.0.1" + strings.TrimRight(addr, "/")
	}
	return "http://" + strings.TrimRight(addr, "/")
}
