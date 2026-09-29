// Package provider binds declared model descriptors (model.Provider) to
// live llm adapters. It is the seam ADR 0009 implies but the interface
// packages cannot hold: model/ stays stdlib-only and secret-free, so the
// code that turns a CREDENTIAL REFERENCE into a value, and a descriptor
// into an adapter, lives here as an implementation.
//
// Credential resolution is fail-closed (ADR 0008): a descriptor whose
// credential is not set is an error, never a silent unauthenticated
// request, and the error names the variable rather than its value.
package provider

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/model"
)

// Set is one navi's declared provider set, binding each descriptor to a
// live adapter on demand and caching the result.
type Set struct {
	descriptors []model.Provider
	env         func(string) string

	mu    sync.Mutex
	cache map[string]llm.Provider
}

// New builds a set over the declared descriptors. env resolves credential
// references; nil means os.Getenv.
func New(descriptors []model.Provider, env func(string) string) *Set {
	if env == nil {
		env = os.Getenv
	}
	return &Set{
		descriptors: append([]model.Provider(nil), descriptors...),
		env:         env,
		cache:       map[string]llm.Provider{},
	}
}

// Descriptors returns the declared set in declaration order.
func (s *Set) Descriptors() []model.Provider {
	return append([]model.Provider(nil), s.descriptors...)
}

// Len returns how many providers are declared.
func (s *Set) Len() int { return len(s.descriptors) }

// Find returns the descriptor matching a provider name or a model name.
// Provider name wins when both match.
func (s *Set) Find(name string) (model.Provider, bool) {
	for _, p := range s.descriptors {
		if strings.EqualFold(p.Name, name) {
			return p, true
		}
	}
	for _, p := range s.descriptors {
		if p.Model == name {
			return p, true
		}
	}
	return model.Provider{}, false
}

// Default returns the descriptor marked default (ADR 0009).
func (s *Set) Default() (model.Provider, bool) {
	for _, p := range s.descriptors {
		if p.Default {
			return p, true
		}
	}
	return model.Provider{}, false
}

// For resolves a provider or model name to a live adapter. An unknown
// name is an error naming the declared set — ADR 0009 forbids a silent
// fallback, because quietly routing work to a different model than the
// caller asked for is worse than failing.
func (s *Set) For(name string) (llm.Provider, error) {
	p, ok := s.Find(name)
	if !ok {
		return nil, fmt.Errorf("model %q: %w (declared: %s)",
			name, model.ErrUnknownProvider, strings.Join(s.names(), ", "))
	}
	return s.Resolve(p)
}

// Resolve binds one descriptor to a live adapter. Per-provider edge cases
// live here rather than in a switch the caller must remember to extend:
// adding a provider means adding a case and nothing else.
func (s *Set) Resolve(p model.Provider) (llm.Provider, error) {
	key := strings.ToLower(p.Name) + "|" + p.Model + "|" + p.Endpoint
	s.mu.Lock()
	if got, ok := s.cache[key]; ok {
		s.mu.Unlock()
		return got, nil
	}
	s.mu.Unlock()

	built, err := s.build(p)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cache[key] = built
	s.mu.Unlock()
	return built, nil
}

func (s *Set) build(p model.Provider) (llm.Provider, error) {
	switch strings.ToLower(p.Name) {
	case "ollama":
		// OLLAMA_HOST wins over the compiled default, matching the
		// runtime's long-standing behaviour; llm owns the normalisation
		// so the two cannot drift.
		endpoint := p.Endpoint
		if endpoint == "" || endpoint == model.DefaultOllamaEndpoint {
			endpoint = llm.OllamaHostFromEnv()
		}
		op := llm.NewOllamaProviderAt(endpoint)
		op.Model = p.Model
		return op, nil

	case "openrouter":
		secret, err := s.credential(p)
		if err != nil {
			return nil, err
		}
		endpoint := p.Endpoint
		if endpoint == "" {
			endpoint = llm.DefaultOpenRouterURL
		}
		return llm.NewOpenRouterProviderWithClient(endpoint, secret, http.DefaultClient), nil

	case "mock":
		// The mock provider exists for tests and for CI runs that must
		// not touch the network; a navi may declare it deliberately.
		return llm.NewMockProvider(), nil

	default:
		return nil, fmt.Errorf("provider %q: %w", p.Name, model.ErrUnknownProvider)
	}
}

// credential resolves a descriptor's credential REFERENCE, fail-closed.
// The error never carries the value — only the reference name.
func (s *Set) credential(p model.Provider) (string, error) {
	if p.Credential == "" {
		return "", fmt.Errorf(
			"provider %q requires a credential but its descriptor names none", p.Name)
	}
	if v := s.env(string(p.Credential)); v != "" {
		return v, nil
	}
	return "", fmt.Errorf(
		"provider %q: credential %s is not set (fail-closed — ADR 0008)",
		p.Name, p.Credential)
}

func (s *Set) names() []string {
	out := make([]string, 0, len(s.descriptors))
	for _, p := range s.descriptors {
		out = append(out, p.Name)
	}
	return out
}
