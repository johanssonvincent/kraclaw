package credproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/johanssonvincent/kraclaw/internal/provider"
)

// ModelLister fetches provider model metadata using the same credentials and
// upstream routing as proxied agent requests.
type ModelLister struct {
	resolver CredentialResolver
	client   *http.Client
}

// NewModelLister creates a credential-aware model lister. resolver is required.
func NewModelLister(resolver CredentialResolver) *ModelLister {
	return &ModelLister{
		resolver: resolver,
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// ListModels returns dynamically fetched models for providerID.
func (l *ModelLister) ListModels(ctx context.Context, groupJID string, providerID string) ([]provider.ModelInfo, error) {
	if l == nil || l.resolver == nil {
		return nil, fmt.Errorf("model lister not configured")
	}
	switch providerID {
	case provider.ProviderOpenAI:
		return l.listOpenAIModels(ctx, groupJID)
	default:
		return nil, fmt.Errorf("dynamic model listing unsupported for provider %q", providerID)
	}
}

func (l *ModelLister) listOpenAIModels(ctx context.Context, groupJID string) ([]provider.ModelInfo, error) {
	cred, err := l.resolver.Resolve(ctx, groupJID, provider.ProviderOpenAI)
	if err != nil {
		return nil, err
	}
	if cred.Provider != provider.ProviderOpenAI {
		return nil, fmt.Errorf("resolved provider %q, want %q", cred.Provider, provider.ProviderOpenAI)
	}

	upstream := strings.TrimRight(cred.UpstreamURL, "/")
	if upstream == "" {
		upstream = "https://api.openai.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cred.APIKey)

	client := l.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openai list models returned %s", resp.Status)
	}

	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}

	modelsByID := make(map[string]provider.ModelInfo, len(body.Data))
	for _, item := range body.Data {
		id := strings.TrimSpace(item.ID)
		if !isUserSelectableOpenAIModel(id) {
			continue
		}
		modelsByID[id] = provider.ModelInfo{ID: id, DisplayName: displayNameFromModelID(id)}
	}
	models := make([]provider.ModelInfo, 0, len(modelsByID))
	for _, model := range modelsByID {
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})
	return models, nil
}

func isUserSelectableOpenAIModel(id string) bool {
	if id == "" {
		return false
	}
	if strings.HasPrefix(id, "gpt-") {
		return true
	}
	return id == "o1" || strings.HasPrefix(id, "o1-") ||
		id == "o3" || strings.HasPrefix(id, "o3-") ||
		id == "o4" || strings.HasPrefix(id, "o4-")
}

func displayNameFromModelID(id string) string {
	parts := strings.FieldsFunc(id, func(r rune) bool { return r == '-' || r == '_' })
	for i, part := range parts {
		if part == "" {
			continue
		}
		lower := strings.ToLower(part)
		if strings.HasPrefix(lower, "gpt") || strings.HasPrefix(lower, "o") {
			parts[i] = strings.ToUpper(part)
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}
