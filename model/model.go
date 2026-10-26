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
	// Options carries per-model request options (ADR 0034), keyed by model
	// id. Keyed by model rather than held on the descriptor because routing
	// is a property of a model, not of a provider: an upstream that serves
	// one model does not serve another, and a thought may override the model
	// per run (ADR 0009). An override to a model with no entry carries no
	// options, rather than another model's.
	Options map[string]ModelOptions
}

// ModelOptions is the per-model request-option set (ADR 0034). A zero value
// is "say nothing": the request body is unchanged.
type ModelOptions struct {
	// Routing selects the upstream vendor that serves the model
	// (OpenRouter's `provider` object). Nil sends nothing.
	Routing *Routing `json:"routing,omitempty"`
	// Reasoning requests the model's extended reasoning, or its absence.
	// Nil sends nothing, leaving the model's own default.
	Reasoning *Reasoning `json:"reasoning,omitempty"`
}

// Routing is OpenRouter's per-request upstream routing (ADR 0034): which
// vendors may serve the model, in what order, and whether degrading to
// another is permitted. It is a typed value rather than a passthrough map so
// a typo is a composition error, not a request that silently routes nothing.
// The JSON tags make the value readable through the object surface and
// model_list (ADR 0017), so a declared routing is inspectable rather than
// only greppable in main.go.
type Routing struct {
	// Only restricts serving to these upstream vendors. Empty = unrestricted.
	Only []string `json:"only,omitempty"`
	// Order prefers these upstream vendors, in order. Empty = no preference.
	Order []string `json:"order,omitempty"`
	// Ignore excludes these upstream vendors. Empty = exclude none.
	Ignore []string `json:"ignore,omitempty"`
	// AllowFallbacks permits OpenRouter to fall back to another vendor when
	// the preferred one cannot serve the request. Nil sends nothing (keeping
	// OpenRouter's default of true); false is a deliberate refusal to
	// degrade, so "this model" cannot become "a different backend".
	AllowFallbacks *bool `json:"allow_fallbacks,omitempty"`
	// RequireParameters refuses upstreams that do not support every
	// parameter the request sends (tools, temperature, and so on).
	RequireParameters *bool `json:"require_parameters,omitempty"`
	// Quantizations restricts serving to these quantizations (e.g. "fp8").
	Quantizations []string `json:"quantizations,omitempty"`
	// Sort orders candidate upstreams by a criterion (e.g. "throughput",
	// "price", "latency"). Empty = no sort.
	Sort string `json:"sort,omitempty"`
}

// Reasoning requests a reasoning model's extended thinking (ADR 0034),
// emitted as OpenRouter's `reasoning` object. Effort is a level name
// ("none", "low", "medium", "high", or a model-specific value); empty
// sends nothing.
type Reasoning struct {
	// Effort is the reasoning level to request. Empty = send nothing.
	Effort string `json:"effort,omitempty"`
}

// Bool returns a pointer to v, for the tri-state option fields where
// "unset" and "false" mean different things (ADR 0034).
func Bool(v bool) *bool { return &v }

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

// WithRouting declares upstream routing for one model (ADR 0034). It is
// keyed by model id: routing is a property of a model, not of a provider.
func WithRouting(model string, r Routing) Option {
	return func(p *Provider) {
		if p.Options == nil {
			p.Options = map[string]ModelOptions{}
		}
		o := p.Options[model]
		o.Routing = &r
		p.Options[model] = o
	}
}

// WithReasoning declares extended-reasoning options for one model
// (ADR 0034), keyed by model id.
func WithReasoning(model string, r Reasoning) Option {
	return func(p *Provider) {
		if p.Options == nil {
			p.Options = map[string]ModelOptions{}
		}
		o := p.Options[model]
		o.Reasoning = &r
		p.Options[model] = o
	}
}

// OptionsFor returns the request options declared for a model, or the zero
// value when none are (ADR 0034). A provider that declares no options for a
// model sends nothing extra for it.
func (p Provider) OptionsFor(model string) ModelOptions {
	if p.Options == nil {
		return ModelOptions{}
	}
	return p.Options[model]
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
