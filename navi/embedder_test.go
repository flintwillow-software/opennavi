package navi

import (
	"context"
	"errors"
	"testing"

	"github.com/flintwillow-software/opennavi/datastore/inmem"
	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/model"
)

// stubEmbedder is a model.Embedder that returns a fixed vector, so the wiring
// can be tested without a model server.
type stubEmbedder struct {
	dims int
	err  error
}

func (s stubEmbedder) Embed(_ context.Context, text string) ([]float64, error) {
	if s.err != nil {
		return nil, s.err
	}
	v := make([]float64, s.dims)
	for i := range v {
		v[i] = float64(len(text)) / float64(i+1)
	}
	return v, nil
}
func (s stubEmbedder) Dimensions() int { return s.dims }
func (s stubEmbedder) Name() string    { return "stub" }

// buildMemoryNavi assembles a navi with memory declared, returning it after
// assembly so the tier can be inspected.
func buildMemoryNavi(t *testing.T, opts ...Option) *Navi {
	t.Helper()
	base := []Option{
		WithProvider(llm.NewMockProvider(streamEvents("ok"))),
		WithWorkers(1),
		WithShortTermMemory(),
	}
	n := New("embed-navi", append(base, opts...)...)
	if err := n.Datastores.Add(memoryCandidate(inmem.New())); err != nil {
		t.Fatal(err)
	}
	if err := n.assemble(); err != nil {
		t.Fatalf("assemble: %v", err)
	}
	return n
}

// TestWithEmbedderReachesTheArchive is ADR 0040 decision 1's wiring: the
// declared embedder is what the long-term tier searches with.
func TestWithEmbedderReachesTheArchive(t *testing.T) {
	emb := stubEmbedder{dims: 768}
	n := buildMemoryNavi(t, WithEmbedder(emb))

	if n.longMemory == nil {
		t.Fatal("long-term tier was not built")
	}
	got := n.longMemory.Embedder()
	if got == nil {
		t.Fatal("the declared embedder did not reach the archive")
	}
	if got.Name() != "stub" {
		t.Fatalf("archive embedder = %q, want stub", got.Name())
	}
	if !n.longMemory.Semantic() {
		t.Error("Semantic() must be true when an embedder is declared")
	}
}

// TestNoEmbedderIsKeywordOnly is ADR 0040 decision 2: a navi with no embedder
// keeps its archive, and search is keyword-only rather than unavailable.
func TestNoEmbedderIsKeywordOnly(t *testing.T) {
	n := buildMemoryNavi(t)

	if n.longMemory == nil {
		t.Fatal("the archive must exist without an embedder")
	}
	if n.longMemory.Embedder() != nil {
		t.Fatal("no embedder was declared, so the archive must report none")
	}
	if n.longMemory.Semantic() {
		t.Error("Semantic() must be false without an embedder")
	}
}

// TestWithEmbedderIgnoresNil: a nil embedder is \"not declared\", not a panic
// and not a half-wired tier.
func TestWithEmbedderIgnoresNil(t *testing.T) {
	n := buildMemoryNavi(t, WithEmbedder(nil))

	if n.embedder != nil {
		t.Fatal("a nil embedder must not be recorded as declared")
	}
	if n.longMemory.Semantic() {
		t.Error("Semantic() must be false after a nil declaration")
	}
}

// TestEmbedderIsOptionalAtAssembly: declaring memory without an embedder must
// not fail assembly — the tier's availability is decoupled from the deployment
// (ADR 0040 decision 2's whole point).
func TestEmbedderIsOptionalAtAssembly(t *testing.T) {
	n := New("no-embed-navi",
		WithProvider(llm.NewMockProvider(streamEvents("ok"))),
		WithWorkers(1),
		WithShortTermMemory(),
	)
	if err := n.Datastores.Add(memoryCandidate(inmem.New())); err != nil {
		t.Fatal(err)
	}
	if err := n.assemble(); err != nil {
		t.Fatalf("assembly must not require an embedder: %v", err)
	}
}

// TestEmbedderFailureIsVisibleThroughTheContract: a failing embedder surfaces
// its error rather than a zero vector, which is what ADR 0040 decision 4
// depends on to refuse an unembeddable archive write.
func TestEmbedderFailureIsVisibleThroughTheContract(t *testing.T) {
	sentinel := errors.New("model server is down")
	n := buildMemoryNavi(t, WithEmbedder(stubEmbedder{dims: 768, err: sentinel}))

	emb := n.longMemory.Embedder()
	if emb == nil {
		t.Fatal("embedder missing")
	}
	if _, err := emb.Embed(context.Background(), "text"); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the sentinel", err)
	}
}

// TestEmbedderContractIsPublic: model.Embedder is the contract an out-of-repo
// author implements, so a type from outside this module must satisfy it.
func TestEmbedderContractIsPublic(t *testing.T) {
	var _ model.Embedder = stubEmbedder{dims: 1}
}
