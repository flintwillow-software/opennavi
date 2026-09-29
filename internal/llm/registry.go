package llm

import (
	"fmt"
	"sort"
	"sync"
)

// ProviderRegistry is the LLM provider registry: named
// providers registered at startup, looked up for selection and
// fallback. This is distinct from the #141 interface registry (which
// selects delivery modes, not LLM backends).
type ProviderRegistry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

// NewProviderRegistry creates an empty provider registry.
func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{providers: make(map[string]Provider)}
}

// Register adds a provider under its Name(). Registering a name twice
// replaces the previous provider.
func (r *ProviderRegistry) Register(p Provider) error {
	if p == nil {
		return fmt.Errorf("llm: register nil provider")
	}
	if p.Name() == "" {
		return fmt.Errorf("llm: register provider with empty name")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.Name()] = p
	return nil
}

// Get returns the provider with the given name.
func (r *ProviderRegistry) Get(name string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("llm: provider %q: %w", name, ErrProviderNotFound)
	}
	return p, nil
}

// List returns the registered provider names, sorted.
func (r *ProviderRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
