// Package model is the public model contract (ADR 0004, ADR 0009):
// model selection is provider-set with per-thought selection, and the
// contract carries DESCRIPTORS, never secrets (ADR 0008 — secrets are
// interpolated at runtime, never literal in descriptor/definition
// text). Stdlib-only.
package model

import (
	"errors"
	"fmt"
)

// Provider is the provider descriptor: how to reach a provider and
// which model it serves by default. NO credentials in the descriptor —
// keys live in the runtime secret surface (ADR 0008).
type Provider struct {
	// Name is the provider identifier ("ollama", "openrouter").
	Name string
	// Model is the default model the provider serves.
	Model string
	// Endpoint is the provider base endpoint (URL or host:port).
	Endpoint string
}

// Model is the model descriptor exposed to components: the model name
// plus the provider it routes to.
type Model struct {
	// Name is the model identifier (the name per-thought overrides
	// use — ADR 0009).
	Name string
	// ProviderName names the provider this model routes through.
	ProviderName string
	// Description is a human-readable summary of the model's
	// characteristics/tradeoffs.
	Description string
}

// ErrUnknownProvider is returned when a registry lookup fails.
var ErrUnknownProvider = errors.New("model: unknown provider")

// Registry is the model registry type: components (connectors,
// portals) resolve providers by name through it; the navi satisfies
// it structurally at composition time (ADR 0004 rule 2).
type Registry interface {
	// Register adds a provider to the registry (replacing any
	// provider of the same name).
	Register(p Provider) error
	// Find returns the provider under name, or ok=false when unknown.
	Find(name string) (*Provider, bool)
}

// registry is a stdlib-only structural Registry implementation for
// extension authors who need one and for tests — the navi provides
// its own at composition time.
type registry struct {
	providers map[string]Provider
}

// NewRegistry builds an empty registry.
func NewRegistry() *registry {
	return &registry{providers: make(map[string]Provider)}
}

func (r *registry) Register(p Provider) error {
	if p.Name == "" {
		return fmt.Errorf("model: provider requires a name")
	}
	r.providers[p.Name] = p
	return nil
}

func (r *registry) Find(name string) (*Provider, bool) {
	p, ok := r.providers[name]
	if !ok {
		return nil, false
	}
	return &p, true
}
