package model

import "context"

// Embedder produces a vector for a piece of text, so the long-term memory
// archive can be searched by meaning rather than only by word (ADR 0040
// decision 1, ADR 0037 open question 3).
//
// It is a **contract**, the way Provider and datastore.Backend are: a navi
// author supplies one at construction (`navi.WithEmbedder`), and an author
// cannot supply a type from an internal package — which is why this lives
// here rather than beside its Ollama implementation. The package stays
// stdlib-only (ADR 0004), so the interface drags nothing into a component's
// dependency closure; an implementation lives in its own package
// (model/ollama), exactly as a datastore backend lives in datastore/sqlite.
//
// **A navi without one still has an archive.** ADR 0040 decision 2 settles
// ADR 0037's open question: no embedder means keyword search, not no tier. The
// archive's value is parking a conclusion you cannot act on yet, and that does
// not depend on the search being semantic — so this is an addition to search
// quality, never a precondition for the tier existing.
type Embedder interface {
	// Embed returns the vector for one text input. An error is a real
	// failure, never a zero vector: ADR 0040 decision 4 writes embeddings on
	// the write path, so a navi with an embedder declared refuses an archive
	// write it cannot embed rather than storing an entry whose vector would
	// silently describe nothing.
	Embed(ctx context.Context, text string) ([]float64, error)
	// Dimensions is the vector length this embedder produces. It is part of
	// the interface rather than an afterthought because a vector is only
	// comparable to another of the same length: ADR 0040 decision 8 ignores
	// a stored vector whose dimensions do not match the active embedder,
	// since cosine between two different embedding spaces is meaningless.
	Dimensions() int
	// Name identifies the embedder (model and host), for diagnostics and for
	// the metadata a stored vector carries.
	Name() string
}

// DefaultEmbedModel is the conventional local embedding model: a 768-dimension
// model of the nomic-embed-text class, which is what a navi gets when it
// declares an embedder without naming one.
const DefaultEmbedModel = "nomic-embed-text"

// DefaultEmbedDimensions is the vector length DefaultEmbedModel produces.
//
// It is a compiled constant rather than a lookup because the dimension is a
// property of the model family, and a navi that names a different model
// should state its dimensions (the implementation's Dimensions method is the
// authority; this is only the default's).
const DefaultEmbedDimensions = 768
