// Package ollama provides an Ollama-backed model.Embedder (ADR 0040
// decision 1): local, no API keys, over Ollama's /api/embeddings.
//
// It lives in its own package rather than in model, mirroring how a datastore
// backend lives in datastore/sqlite rather than in datastore: model is the
// stdlib-only contract an out-of-repo component may depend on, and an
// implementation that performs network I/O does not belong in a component's
// dependency closure unless the component asked for it.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/model"
)

// Embedder implements model.Embedder via Ollama's /api/embeddings endpoint.
type Embedder struct {
	// BaseURL is the Ollama host, e.g. "http://localhost:11434".
	BaseURL string
	// Model is the embedding model name; empty selects
	// model.DefaultEmbedModel.
	Model string
	// Dims is the vector length this model produces; zero selects
	// model.DefaultEmbedDimensions. It is declared rather than discovered
	// because the endpoint reports only the vector itself, and a wrong
	// dimension is worse than none: ADR 0040 decision 8 compares stored
	// vectors by dimension, so a mis-declared one would let two different
	// embedding spaces be compared.
	Dims int

	client *http.Client
}

// New builds an embedder at the default host and model. The host comes from
// $OLLAMA_HOST (with or without a scheme), defaulting to
// model.DefaultOllamaEndpoint.
func New() *Embedder {
	return &Embedder{
		BaseURL: hostFromEnv(),
		Model:   model.DefaultEmbedModel,
		Dims:    model.DefaultEmbedDimensions,
		client:  &http.Client{Timeout: 60 * time.Second},
	}
}

// NewAt builds an embedder at an explicit host and model. An empty model or a
// zero dims selects the default, so a caller may name only the host.
func NewAt(baseURL, embedModel string, dims int) *Embedder {
	e := New()
	if strings.TrimSpace(baseURL) != "" {
		e.BaseURL = strings.TrimSuffix(baseURL, "/")
	}
	if strings.TrimSpace(embedModel) != "" {
		e.Model = embedModel
	}
	if dims > 0 {
		e.Dims = dims
	}
	return e
}

// Name implements model.Embedder.
func (e *Embedder) Name() string { return "ollama/" + e.Model }

// Dimensions implements model.Embedder.
func (e *Embedder) Dimensions() int {
	if e.Dims > 0 {
		return e.Dims
	}
	return model.DefaultEmbedDimensions
}

// Embed implements model.Embedder.
//
// The response length is checked against Dimensions: Ollama returns whatever
// the named model produces, so a model that does not match the declared
// dimension is a configuration error surfaced here rather than a vector that
// silently cannot be compared with the ones already stored.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float64, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("embed: text is empty")
	}
	payload, err := json.Marshal(embedRequest{Model: e.Model, Prompt: text})
	if err != nil {
		return nil, fmt.Errorf("embed: encode: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.BaseURL+"/api/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("embed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: %s: %w", e.Name(), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("embed: %s: status %d: %s", e.Name(), resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var out embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("embed: %s: decode: %w", e.Name(), err)
	}
	if out.Error != "" {
		return nil, fmt.Errorf("embed: %s: %s", e.Name(), out.Error)
	}
	if len(out.Embedding) == 0 {
		return nil, fmt.Errorf("embed: %s: empty embedding", e.Name())
	}
	if want := e.Dimensions(); len(out.Embedding) != want {
		return nil, fmt.Errorf(
			"embed: %s returned %d dimensions, but %d was declared — the model and the declared dimension disagree",
			e.Name(), len(out.Embedding), want)
	}
	return out.Embedding, nil
}

type embedRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type embedResponse struct {
	Embedding []float64 `json:"embedding"`
	Error     string    `json:"error"`
}

// hostFromEnv resolves the Ollama base URL from $OLLAMA_HOST, tolerating a
// bare host:port and a trailing slash. It is a local copy of the same rule the
// chat provider applies, because internal/llm is not importable from a public
// package and the rule is three lines.
func hostFromEnv() string {
	host := os.Getenv("OLLAMA_HOST")
	if host == "" {
		return model.DefaultOllamaEndpoint
	}
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	return strings.TrimSuffix(host, "/")
}

// compile-time check.
var _ model.Embedder = (*Embedder)(nil)
