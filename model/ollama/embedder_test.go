package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/model"
)

// fakeOllama serves /api/embeddings with a fixed-dimension vector.
func fakeOllama(t *testing.T, dims int, wantModel string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embeddings" {
			t.Errorf("path = %q, want /api/embeddings", r.URL.Path)
		}
		var req struct {
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if wantModel != "" && req.Model != wantModel {
			t.Errorf("model = %q, want %q", req.Model, wantModel)
		}
		if strings.TrimSpace(req.Prompt) == "" {
			t.Error("prompt was empty")
		}
		vec := make([]float64, dims)
		for i := range vec {
			vec[i] = float64(i) / float64(dims)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": vec})
	}))
}

// TestEmbedReturnsAVectorOfTheDeclaredSize.
func TestEmbedReturnsAVectorOfTheDeclaredSize(t *testing.T) {
	srv := fakeOllama(t, model.DefaultEmbedDimensions, model.DefaultEmbedModel)
	defer srv.Close()

	e := NewAt(srv.URL, "", 0)
	if e.Dimensions() != model.DefaultEmbedDimensions {
		t.Fatalf("Dimensions() = %d, want %d", e.Dimensions(), model.DefaultEmbedDimensions)
	}
	got, err := e.Embed(context.Background(), "a note about deploys")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != model.DefaultEmbedDimensions {
		t.Fatalf("vector length = %d, want %d", len(got), model.DefaultEmbedDimensions)
	}
}

// TestEmbedRejectsADimensionMismatch is ADR 0040 decision 8's precondition: a
// model that does not produce the declared dimension is a configuration error
// surfaced at the call, not a vector that silently cannot be compared with the
// ones already stored.
func TestEmbedRejectsADimensionMismatch(t *testing.T) {
	srv := fakeOllama(t, 128, "some-other-model")
	defer srv.Close()

	// Declares 768, but the server returns 128.
	e := NewAt(srv.URL, "some-other-model", 768)
	_, err := e.Embed(context.Background(), "text")
	if err == nil {
		t.Fatal("a dimension mismatch must be an error")
	}
	if !strings.Contains(err.Error(), "128") || !strings.Contains(err.Error(), "768") {
		t.Fatalf("error should name both dimensions: %v", err)
	}
}

// TestEmbedRejectsEmptyText: an empty embedding input is a caller error, not a
// request worth making.
func TestEmbedRejectsEmptyText(t *testing.T) {
	e := NewAt("http://127.0.0.1:1", "", 0)
	if _, err := e.Embed(context.Background(), "   "); err == nil {
		t.Fatal("empty text must be refused")
	}
}

// TestEmbedReportsHTTPFailure: an unreachable or erroring host is a loud
// failure, because ADR 0040 decision 4 puts embedding on the write path and a
// silent zero vector would be worse than a refused write.
func TestEmbedReportsHTTPFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()

	e := NewAt(srv.URL, "", 0)
	_, err := e.Embed(context.Background(), "text")
	if err == nil {
		t.Fatal("a non-200 response must be an error")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error should name the status: %v", err)
	}
}

// TestEmbedReportsOllamaError: Ollama reports model-level failures in the body
// with a 200 status, so the body's error field must be honoured.
func TestEmbedReportsOllamaError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "model 'nope' not found"})
	}))
	defer srv.Close()

	e := NewAt(srv.URL, "nope", 0)
	_, err := e.Embed(context.Background(), "text")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want the body's error reported", err)
	}
}

// TestEmbedRejectsEmptyVector: a 200 with no vector is a failure, not a
// zero-length embedding.
func TestEmbedRejectsEmptyVector(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": []float64{}})
	}))
	defer srv.Close()

	e := NewAt(srv.URL, "", 0)
	if _, err := e.Embed(context.Background(), "text"); err == nil {
		t.Fatal("an empty embedding must be an error")
	}
}

// TestNameIdentifiesModelAndHost: Name() is what diagnostics and stored-vector
// metadata report, so it must name the model.
func TestNameIdentifiesModelAndHost(t *testing.T) {
	e := NewAt("http://example.invalid:11434", "custom-embed", 512)
	if got := e.Name(); got != "ollama/custom-embed" {
		t.Fatalf("Name() = %q, want ollama/custom-embed", got)
	}
	if got := e.Dimensions(); got != 512 {
		t.Fatalf("Dimensions() = %d, want the declared 512", got)
	}
}

// TestHostFromEnv: $OLLAMA_HOST is honoured, tolerating a bare host:port and a
// trailing slash, and defaulting when unset.
func TestHostFromEnv(t *testing.T) {
	for _, tc := range []struct{ env, want string }{
		{"", model.DefaultOllamaEndpoint},
		{"http://localhost:11434", "http://localhost:11434"},
		{"localhost:11434", "http://localhost:11434"},
		{"http://host:1234/", "http://host:1234"},
	} {
		t.Setenv("OLLAMA_HOST", tc.env)
		if got := New().BaseURL; got != tc.want {
			t.Errorf("OLLAMA_HOST=%q -> %q, want %q", tc.env, got, tc.want)
		}
	}
}

// TestSatisfiesTheContract: the implementation is usable wherever the contract
// is expected — which is the point of moving the interface to model.
func TestSatisfiesTheContract(t *testing.T) {
	var _ model.Embedder = New()
}
