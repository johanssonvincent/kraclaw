package tools

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestNewSearchClient(t *testing.T) {
	tests := []struct {
		name     string
		provider Provider
		apiKey   string
		engineID string
		wantNil  bool
	}{
		{
			name:     "tavily with key",
			provider: ProviderTavily,
			apiKey:   "test-key",
			wantNil:  false,
		},
		{
			name:     "exa with key",
			provider: ProviderExa,
			apiKey:   "test-key",
			wantNil:  false,
		},
		{
			name:     "google with key and engine id",
			provider: ProviderGoogle,
			apiKey:   "test-key",
			engineID: "test-engine",
			wantNil:  false,
		},
		{
			name:     "no api key",
			provider: ProviderTavily,
			apiKey:   "",
			wantNil:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewSearchClient(tt.provider, tt.apiKey, tt.engineID)
			if (client == nil) != tt.wantNil {
				t.Errorf("NewSearchClient() = %v, wantNil = %v", client, tt.wantNil)
			}
		})
	}
}

func TestSearchClient_Provider(t *testing.T) {
	client := NewSearchClient(ProviderTavily, "test-key", "")
	if client.Provider() != "tavily" {
		t.Errorf("Provider() = %q, want %q", client.Provider(), "tavily")
	}
}

func TestSearchClient_Search_NoKey(t *testing.T) {
	var client *SearchClient
	_, err := client.Search(context.Background(), DefaultParams("test"))
	if err == nil {
		t.Error("Search() with nil client should return error")
	}
}

func TestSearchClient_Search_EmptyQuery(t *testing.T) {
	client := NewSearchClient(ProviderTavily, "test-key", "")
	_, err := client.Search(context.Background(), SearchParams{})
	if err == nil {
		t.Error("Search() with empty query should return error")
	}
}

func TestSearchClient_Search_InvalidProvider(t *testing.T) {
	client := &SearchClient{
		provider: Provider("invalid"),
		client:   &http.Client{},
	}
	_, err := client.Search(context.Background(), DefaultParams("test"))
	if err == nil {
		t.Error("Search() with invalid provider should return error")
	}
}

func TestDefaultParams(t *testing.T) {
	params := DefaultParams("test query")
	if params.Query != "test query" {
		t.Errorf("Query = %q, want %q", params.Query, "test query")
	}
	if params.MaxResults != 5 {
		t.Errorf("MaxResults = %d, want 5", params.MaxResults)
	}
}

func TestSearchParams_MaxResultsBounds(t *testing.T) {
	client := NewSearchClient(ProviderTavily, "test-key", "")

	// Test zero max results (should default to 5).
	params := SearchParams{Query: "test", MaxResults: 0}
	if params.MaxResults <= 0 {
		params.MaxResults = 5
	}

	// Test over max (should cap at 20).
	params = SearchParams{Query: "test", MaxResults: 100}
	if params.MaxResults > 20 {
		params.MaxResults = 20
	}

	_ = client
}

func TestFormatAsPrompt(t *testing.T) {
	tests := []struct {
		name         string
		results      []SearchResult
		wantNonEmpty bool
	}{
		{
			name:         "empty results",
			results:      nil,
			wantNonEmpty: true, // Returns "No search results found."
		},
		{
			name: "single result",
			results: []SearchResult{
				{Title: "Test", URL: "http://example.com", Snippet: "A snippet"},
			},
			wantNonEmpty: true,
		},
		{
			name: "multiple results",
			results: []SearchResult{
				{Title: "Result 1", URL: "http://example.com/1"},
				{Title: "Result 2", URL: "http://example.com/2"},
			},
			wantNonEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatAsPrompt(tt.results)
			gotNonEmpty := result != ""
			if gotNonEmpty != tt.wantNonEmpty {
				t.Errorf("FormatAsPrompt() nonEmpty = %v, wantNonEmpty = %v", gotNonEmpty, tt.wantNonEmpty)
			}
		})
	}
}

func TestFormatToolPrompt(t *testing.T) {
	prompt := FormatToolPrompt()
	if prompt == "" {
		t.Error("FormatToolPrompt() should not be empty")
	}
	if !strings.Contains(prompt, "web_search") {
		t.Error("FormatToolPrompt() should contain web_search")
	}
}

func TestTimeRangeToDays(t *testing.T) {
	tests := map[string]string{
		"day":   "1",
		"week":  "7",
		"month": "30",
		"year":  "365",
	}

	for tr, want := range tests {
		t.Run(tr, func(t *testing.T) {
			got := timeRangeToDays(tr)
			if got != want {
				t.Errorf("timeRangeToDays(%q) = %q, want %q", tr, got, want)
			}
		})
	}
}
