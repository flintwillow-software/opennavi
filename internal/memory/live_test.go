package memory

import (
	"context"
	"math"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/llm"
)

// liveEmbedder returns an Ollama embedder against $OLLAMA_HOST (or
// navi-inference) and skips the test when Ollama is unreachable —
// fully local, no keys involved.
func liveEmbedder(t *testing.T) *llm.OllamaEmbedder {
	t.Helper()
	host := os.Getenv("OLLAMA_HOST")
	if host == "" {
		host = "http://10.0.0.50:11434" // navi-inference
	}
	e := llm.NewOllamaEmbedderAt(host, "nomic-embed-text")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, perr := e.Embed(ctx, "ping"); perr != nil {
		t.Skipf("Ollama unreachable at %s: %v", host, perr)
	}
	return e
}

func TestLiveOllamaEmbedder(t *testing.T) {
	e := liveEmbedder(t)
	vec, err := e.Embed(context.Background(), "the dog barked all night")
	if err != nil {
		t.Fatal(err)
	}
	if len(vec) != e.Dimensions() {
		t.Fatalf("dims = %d, want %d", len(vec), e.Dimensions())
	}
	norm := 0.0
	for _, v := range vec {
		norm += v * v
	}
	if math.IsNaN(norm) || norm == 0 {
		t.Fatalf("degenerate embedding: norm %v", norm)
	}
}

func TestLiveSemanticSearchEndToEnd(t *testing.T) {
	e := liveEmbedder(t)
	s := testStore(t)
	ix, err := NewIndex(s, e)
	if err != nil {
		t.Fatal(err)
	}

	docs := []struct{ id, text string }{
		{"kb-deploy", "Deploying the service: build with make, push the container, roll out with kubectl rollout status."},
		{"kb-ssh", "The staging server needs SSH port 2222, not the default 22."},
		{"kb-db", "Database connection pool exhausted during the P0 incident; restore pool config to fix."},
	}
	for _, d := range docs {
		if _, err := s.AddEntry(Entry{Scope: ScopeMemories, Subject: d.id, Content: d.text}); err != nil {
			t.Fatal(err)
		}
		if err := ix.Upsert(context.Background(), d.id, d.text); err != nil {
			t.Fatal(err)
		}
	}

	// Semantic query with NO keyword overlap ("deploy" appears in
	// kb-deploy, but "roll out the new release" shares intent).
	results, err := ix.SemanticSearch(context.Background(),
		"how do we ship a new release of the service", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("semantic search returned nothing")
	}
	if results[0].Entry.Subject != "kb-deploy" {
		t.Fatalf("top hit = %s (want kb-deploy), scores: %v", results[0].Entry.Subject, results)
	}

	// Prune-with-index consistency: delete an entry, reconcile, search
	// again — the orphan must not surface.
	if err := s.Delete(ScopeMemories, "kb-ssh"); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Reconcile(); err != nil {
		t.Fatal(err)
	}
	results, err = ix.SemanticSearch(context.Background(), "2222 ssh port staging", 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Entry.Subject == "kb-ssh" {
			t.Fatal("orphaned vector surfaced after reconcile")
		}
	}
}

// Compile-time: the pool of live tests uses the same llm.Embedder
// contract as the rest of the provider layer.
var _ llm.Embedder = (*llm.OllamaEmbedder)(nil)

func TestLiveHTTPHealthy(t *testing.T) {
	// Cheap connectivity sanity for the host the embedder targets.
	host := os.Getenv("OLLAMA_HOST")
	if host == "" {
		host = "http://10.0.0.50:11434"
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(host + "/api/tags")
	if err != nil {
		t.Skipf("ollama unreachable: %v", err)
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Status, "2") {
		t.Fatalf("tags status = %q", resp.Status)
	}
}
