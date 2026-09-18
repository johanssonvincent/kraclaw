package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Provider is a search provider type.
type Provider string

const (
	// ProviderTavily is the Tavily search provider.
	ProviderTavily Provider = "tavily"

	// ProviderExa is the Exa search provider.
	ProviderExa Provider = "exa"

	// ProviderGoogle is the Google Custom Search provider.
	ProviderGoogle Provider = "google"
)

// SearchClient is a web search client.
type SearchClient struct {
	provider Provider
	apiKey   string
	client   *http.Client
	engineID string // For Google Custom Search
}

// Provider returns the provider name.
func (c *SearchClient) Provider() string {
	return string(c.provider)
}

// SearchResult is a single search result.
type SearchResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Snippet     string `json:"snippet"`
	Content     string `json:"content,omitempty"`
	Score       float64 `json:"score,omitempty"`
	PublishedDate string `json:"published_date,omitempty"`
}

// SearchParams holds search parameters.
type SearchParams struct {
	Query       string
	MaxResults  int
	IncludeContent bool
	TimeRange   string // "day", "week", "month", "year"
}

// DefaultParams returns default search parameters.
func DefaultParams(query string) SearchParams {
	return SearchParams{
		Query:       query,
		MaxResults:  5,
		IncludeContent: false,
	}
}

// NewSearchClient creates a new search client.
func NewSearchClient(provider Provider, apiKey string, engineID string) *SearchClient {
	if apiKey == "" {
		return nil
	}

	return &SearchClient{
		provider: provider,
		apiKey:   apiKey,
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
		engineID: engineID,
	}
}

// Search performs a web search.
func (c *SearchClient) Search(ctx context.Context, params SearchParams) ([]SearchResult, error) {
	if c == nil {
		return nil, fmt.Errorf("search client not configured")
	}

	if params.Query == "" {
		return nil, fmt.Errorf("search query is required")
	}

	if params.MaxResults <= 0 {
		params.MaxResults = 5
	}

	if params.MaxResults > 20 {
		params.MaxResults = 20
	}

	switch c.provider {
	case ProviderTavily:
		return c.searchTavily(ctx, params)
	case ProviderExa:
		return c.searchExa(ctx, params)
	case ProviderGoogle:
		return c.searchGoogle(ctx, params)
	default:
		return nil, fmt.Errorf("unsupported search provider: %s", c.provider)
	}
}

// searchTavily performs a search using Tavily API.
func (c *SearchClient) searchTavily(ctx context.Context, params SearchParams) ([]SearchResult, error) {
	reqBody := map[string]any{
		"query":    params.Query,
		"max_results": params.MaxResults,
		"include_answer": false,
	}

	if params.IncludeContent {
		reqBody["include_raw_content"] = true
		reqBody["include_images"] = false
	}

	if params.TimeRange != "" {
		reqBody["time_range"] = params.TimeRange
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal search request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/search", strings.NewReader(string(bodyBytes)))
	if err != nil {
		return nil, fmt.Errorf("create search request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute search request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("search API returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Results []struct {
			Title         string `json:"title"`
			URL           string `json:"url"`
			Snippet       string `json:"content"`
			Score         float64 `json:"score"`
			PublishedDate string `json:"published_date"`
		} `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode search response: %w", err)
	}

	searchResults := make([]SearchResult, len(result.Results))
	for i, r := range result.Results {
		searchResults[i] = SearchResult{
			Title:         r.Title,
			URL:           r.URL,
			Snippet:       r.Snippet,
			Score:         r.Score,
			PublishedDate: r.PublishedDate,
		}
	}

	return searchResults, nil
}

// searchExa performs a search using Exa API.
func (c *SearchClient) searchExa(ctx context.Context, params SearchParams) ([]SearchResult, error) {
	reqBody := map[string]any{
		"query": params.Query,
		"numResults": params.MaxResults,
	}

	if params.IncludeContent {
		reqBody["contents"] = map[string]any{
			"text": true,
		}
	}

	if params.TimeRange != "" {
		reqBody["startPublishedDate"], reqBody["endPublishedDate"] = timeRangeToDates(params.TimeRange)
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal search request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.exa.ai/search", strings.NewReader(string(bodyBytes)))
	if err != nil {
		return nil, fmt.Errorf("create search request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute search request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("search API returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Results []struct {
			Title         string `json:"title"`
			URL           string `json:"url"`
			Snippet       string `json:"snippet"`
			Text          string `json:"text"`
			Score         float64 `json:"score"`
			PublishedDate string `json:"publishedDate"`
		} `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode search response: %w", err)
	}

	searchResults := make([]SearchResult, len(result.Results))
	for i, r := range result.Results {
		searchResults[i] = SearchResult{
			Title:         r.Title,
			URL:           r.URL,
			Snippet:       r.Snippet,
			Content:       r.Text,
			Score:         r.Score,
			PublishedDate: r.PublishedDate,
		}
	}

	return searchResults, nil
}

// searchGoogle performs a search using Google Custom Search API.
func (c *SearchClient) searchGoogle(ctx context.Context, params SearchParams) ([]SearchResult, error) {
	if c.engineID == "" {
		return nil, fmt.Errorf("google search requires engine ID")
	}

	endpoint := "https://www.googleapis.com/customsearch/v1"
	paramsMap := url.Values{
		"key":       {c.apiKey},
		"cx":        {c.engineID},
		"q":         {params.Query},
		"num":       {fmt.Sprintf("%d", params.MaxResults)},
	}

	if params.TimeRange != "" {
		paramsMap.Set("dateRestrict", "d"+timeRangeToDays(params.TimeRange))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+paramsMap.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("create search request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute search request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("search API returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Items []struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Snippet string `json:"snippet"`
		} `json:"items"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode search response: %w", err)
	}

	searchResults := make([]SearchResult, len(result.Items))
	for i, item := range result.Items {
		searchResults[i] = SearchResult{
			Title:   item.Title,
			URL:     item.Link,
			Snippet: item.Snippet,
		}
	}

	return searchResults, nil
}

// timeRangeToDates converts a time range string to start/end dates for Exa.
func timeRangeToDates(timeRange string) (string, string) {
	now := time.Now()
	var start time.Time

	switch timeRange {
	case "day":
		start = now.AddDate(0, 0, -1)
	case "week":
		start = now.AddDate(0, 0, -7)
	case "month":
		start = now.AddDate(0, -1, 0)
	case "year":
		start = now.AddDate(-1, 0, 0)
	default:
		start = now.AddDate(-1, 0, 0)
	}

	return start.Format("2006-01-02"), now.Format("2006-01-02")
}

// timeRangeToDays converts a time range string to days for Google.
func timeRangeToDays(timeRange string) string {
	switch timeRange {
	case "day":
		return "1"
	case "week":
		return "7"
	case "month":
		return "30"
	case "year":
		return "365"
	default:
		return "365"
	}
}

// FormatAsPrompt formats search results as a readable text block.
func FormatAsPrompt(results []SearchResult) string {
	if len(results) == 0 {
		return "No search results found."
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d results:\n\n", len(results)))

	for i, r := range results {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, r.Title))
		if r.URL != "" {
			sb.WriteString(fmt.Sprintf("   URL: %s\n", r.URL))
		}
		if r.PublishedDate != "" {
			sb.WriteString(fmt.Sprintf("   Published: %s\n", r.PublishedDate))
		}
		if r.Content != "" {
			content := r.Content
			if len(content) > 500 {
				content = content[:500] + "..."
			}
			sb.WriteString(fmt.Sprintf("   Content: %s\n", content))
		} else if r.Snippet != "" {
			sb.WriteString(fmt.Sprintf("   %s\n", r.Snippet))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// FormatToolPrompt returns the tool description for the system prompt.
func FormatToolPrompt() string {
	return `### web_search

Search the web for current information. Use this when you need to find:
- Current events, news, or recent developments
- Factual information that may have changed
- Specific websites, documentation, or resources
- Answers to questions requiring up-to-date knowledge

Input schema:
{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "The search query. Be specific and descriptive."
    },
    "max_results": {
      "type": "integer",
      "description": "Maximum number of results to return (1-20). Default: 5."
    },
    "include_content": {
      "type": "boolean",
      "description": "Whether to include page content in results. Default: false."
    },
    "time_range": {
      "type": "string",
      "description": "Time range filter: day, week, month, year. Optional."
    }
  },
  "required": ["query"]
}`
}
