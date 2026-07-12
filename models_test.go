package sentinel

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetAvailableModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/models" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-5-5","title":"GPT-5.5"},{"model_slug":"gpt-5-5-mini","name":"Instant"}]}`))
	}))
	defer server.Close()

	client := NewClient(Config{BearerToken: "test"})
	client.HTTPClient().SetBaseURL(server.URL)
	models, err := client.GetAvailableModels()
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].Slug != "gpt-5-5" || models[1].Slug != "gpt-5-5-mini" {
		t.Fatalf("unexpected models: %#v", models)
	}
}

func TestNoteUpstreamModelKeepsFirstRoutingDecision(t *testing.T) {
	client := NewClient(Config{BearerToken: "test"})
	result := &ChatResult{}
	client.noteUpstreamModel(result, map[string]interface{}{
		"metadata": map[string]interface{}{"model_slug": "gpt-5-5-mini"},
	})
	client.noteUpstreamModel(result, map[string]interface{}{
		"metadata": map[string]interface{}{"model_slug": "gpt-5-5"},
	})
	if result.UpstreamModel != "gpt-5-5-mini" {
		t.Fatalf("unexpected upstream model: %q", result.UpstreamModel)
	}
}
