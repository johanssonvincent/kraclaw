package credproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestModelLister_ListOpenAIModels(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q, want /v1/models", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": "gpt-5.5"},
				{"id": "whisper-1"},
				{"id": "omni-moderation-latest"},
				{"id": "o3-mini"},
				{"id": "gpt-5.5"}
			]
		}`))
	}))
	defer upstream.Close()

	lister := NewModelLister(&staticCredentialResolver{cred: &resolvedCredential{
		Provider:    "openai",
		APIKey:      "sk-test",
		UpstreamURL: upstream.URL,
	}})
	models, err := lister.ListModels(context.Background(), "tui:g1", "openai")
	if err != nil {
		t.Fatalf("ListModels() err = %v, want nil", err)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q, want Bearer sk-test", gotAuth)
	}
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2: %#v", len(models), models)
	}
	if models[0].ID != "gpt-5.5" || models[1].ID != "o3-mini" {
		t.Fatalf("models = %#v, want sorted filtered OpenAI chat models", models)
	}
}

func TestModelLister_ListOpenAIModelsResolverError(t *testing.T) {
	lister := NewModelLister(&staticCredentialResolver{err: context.Canceled})
	_, err := lister.ListModels(context.Background(), "tui:g1", "openai")
	if err == nil {
		t.Fatal("ListModels() err = nil, want resolver error")
	}
}
