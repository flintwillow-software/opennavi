// Package model is the public model component (ADR 0004, ADR 0009):
// model selection is provider-set with per-thought selection, and the
// descriptors carry no secrets (ADR 0008 — secrets are
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
	// Endpoint is the provider base endpoint (URL or host:port). Empty
	// means the compiled default for the named provider.
	Endpoint string
	// Credential names where this provider's secret lives — an
	// environment variable or a file path, NEVER the value (ADR 0008).
	// Descriptors get logged, printed in tool output, and committed, so
	// a literal key must never reach one. Empty = needs no credential.
	Credential CredentialRef
	// Mode is "local" or "cloud". Informational, but it tells a reader
	// whether the provider leaves the machine.
	Mode string
	// Thinking asks a reasoning-capable provider for its extended reasoning,
	// or its absence. Nil says nothing, leaving the model's own default;
	// false turns hidden reasoning off, so a reasoning model answers instead
	// of spending the budget thinking (the `think` parameter, where the
	// provider supports it).
	Thinking *bool
	// Default marks the provider a thought uses when it names none
	// (ADR 0009). A navi declares a set and exactly one default.
	Default bool
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

// CredentialRef names where a provider's secret lives: an environment
// variable (or file path) holding it. It is a REFERENCE, never the value.
type CredentialRef string

// Compiled endpoint defaults (ADR 0008 permits these in the binary; the
// operator overrides them at runtime).
const (
	DefaultOllamaEndpoint     = "http://localhost:11434"
	DefaultOpenRouterEndpoint = "https://openrouter.ai/api/v1"
)

// DefaultOpenRouterCredential is the conventional environment variable
// holding an OpenRouter key, used unless a constructor is told otherwise.
const DefaultOpenRouterCredential CredentialRef = "OPENROUTER_API_KEY"

// Provider modes.
const (
	ModeLocal = "local"
	ModeCloud = "cloud"
)

// Option adjusts a provider descriptor at construction.
type Option func(*Provider)

// WithEndpoint overrides the compiled endpoint default.
func WithEndpoint(endpoint string) Option {
	return func(p *Provider) { p.Endpoint = endpoint }
}

// WithThinking asks the provider for its extended reasoning, or its absence.
// Unset (the default) sends no reasoning parameter at all.
func WithThinking(enabled bool) Option {
	return func(p *Provider) { p.Thinking = &enabled }
}

// WithCredential names where the provider's secret lives (ADR 0008).
func WithCredential(ref CredentialRef) Option {
	return func(p *Provider) { p.Credential = ref }
}

// Default marks this provider as the one a thought uses when it names no
// provider or model (ADR 0009). Exactly one declared provider may be
// default; Run rejects a set with none or with several.
func Default() Option {
	return func(p *Provider) { p.Default = true }
}

// Ollama describes a local Ollama provider serving model. Ollama takes
// no credential, which makes it the first provider a navi can actually
// run against.
func Ollama(model string, opts ...Option) Provider {
	p := Provider{
		Name:     "ollama",
		Model:    model,
		Endpoint: DefaultOllamaEndpoint,
		Mode:     ModeLocal,
	}
	for _, o := range opts {
		if o != nil {
			o(&p)
		}
	}
	return p
}

// OpenRouter describes a cloud OpenRouter provider serving model. It
// requires a credential; the default reference is
// DefaultOpenRouterCredential.
func OpenRouter(model string, opts ...Option) Provider {
	p := Provider{
		Name:       "openrouter",
		Model:      model,
		Endpoint:   DefaultOpenRouterEndpoint,
		Credential: DefaultOpenRouterCredential,
		Mode:       ModeCloud,
	}
	for _, o := range opts {
		if o != nil {
			o(&p)
		}
	}
	return p
}

// RequiresCredential reports whether this descriptor names a secret.
func (p Provider) RequiresCredential() bool { return p.Credential != "" }

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
