package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Embedder produces embeddings for text (Tier 5: knowledge
// base / semantic search). Implementations: OllamaEmbedder (local,
// no keys) now; API-based embedders later (gated on keys).
type Embedder interface {
	// Embed returns the embedding vector for one text input.
	Embed(ctx context.Context, text string) ([]float64, error)
	// Dimensions is the vector size this embedder produces.
	Dimensions() int
	// Name identifies the embedder (model + host).
	Name() string
}

// DefaultEmbedModel is the default local embedding model (768-dim
// nomic-embed-text class, ).
const DefaultEmbedModel = "nomic-embed-text"

// OllamaEmbedder implements Embedder via Ollama's /api/embeddings
// endpoint — fully local, no API keys.
type OllamaEmbedder struct {
	BaseURL string // e.g. "http://localhost:11434"
	Model   string // default DefaultEmbedModel
	client  *http.Client
}

// NewOllamaEmbedder builds an embedder. The base URL comes from
// $OLLAMA_HOST (with or without scheme), defaulting to
// http://localhost:11434.
func NewOllamaEmbedder() *OllamaEmbedder {
	return &OllamaEmbedder{
		BaseURL: OllamaHostFromEnv(),
		Model:   DefaultEmbedModel,
		client:  &http.Client{Timeout: 60 * time.Second},
	}
}

// NewOllamaEmbedderAt builds an embedder at an explicit URL.
func NewOllamaEmbedderAt(baseURL, model string) *OllamaEmbedder {
	return &OllamaEmbedder{
		BaseURL: strings.TrimSuffix(baseURL, "/"),
		Model:   model,
		client:  &http.Client{Timeout: 60 * time.Second},
	}
}

// Name implements Embedder.
func (e *OllamaEmbedder) Name() string { return "ollama/" + e.Model }

// Dimensions implements Embedder: nomic-embed-text produces 768-dim
// vectors.
func (e *OllamaEmbedder) Dimensions() int { return 768 }

type ollamaEmbedRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type ollamaEmbedResponse struct {
	Embedding []float64 `json:"embedding"`
	Error     string    `json:"error"`
}

// Embed implements Embedder.
func (e *OllamaEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	payload, err := json.Marshal(ollamaEmbedRequest{Model: e.Model, Prompt: text})
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
		return nil, fmt.Errorf("embed: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("embed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var out ollamaEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("embed: decode: %w", err)
	}
	if out.Error != "" {
		return nil, fmt.Errorf("embed: %s", out.Error)
	}
	if len(out.Embedding) == 0 {
		return nil, fmt.Errorf("embed: empty embedding")
	}
	return out.Embedding, nil
}

// compile-time check.
var _ Embedder = (*OllamaEmbedder)(nil)
