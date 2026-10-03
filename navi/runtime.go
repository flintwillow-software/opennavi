package navi

import (
	"fmt"
	"os"
	"strings"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/internal/config"
	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/internal/memory"
	"github.com/flintwillow-software/opennavi/internal/provider"
	"github.com/flintwillow-software/opennavi/model"
	"github.com/flintwillow-software/opennavi/thought"
)

// DefaultSource names the layer that decided the effective default, so a
// running navi can explain where its routing came from instead of leaving
// an operator to guess.
type DefaultSource string

const (
	// SourceEnv — NAVI_PROVIDER / NAVI_MODEL.
	SourceEnv DefaultSource = "env"
	// SourceConfig — the runtime config file's provider.primary/model.
	SourceConfig DefaultSource = "config"
	// SourceBuild — the compiled default (model.Default() or
	// WithDefaultModel).
	SourceBuild DefaultSource = "build-time"
)

// DefaultSelection is the provider/model used by thoughts that name
// neither, plus the layer that chose it.
type DefaultSelection struct {
	Provider string
	Model    string
	Source   DefaultSource
}

// WithRuntimeConfig points the navi at a runtime config file — the same
// YAML the reference runtime reads. A *path* rather than a parsed struct,
// so the public navi package does not leak the internal config type and
// an out-of-repo author can still supply one. Empty means no file layer.
func WithRuntimeConfig(path string) Option {
	return func(n *Navi) { n.runtimeFile = path }
}

// Default returns the resolved default provider/model and the layer that
// decided it. Zero before Run has assembled the navi.
func (n *Navi) Default() DefaultSelection { return n.selection }

// WithProvider replaces provider resolution with a single provider —
// the seam the retired daemon path exposed as RuntimeParams.OverrideProvider.
// It bypasses the declared set and credential resolution entirely, so it is
// for tests and embedding, not for production wiring; a production navi
// declares providers and lets the host resolve them.
func WithProvider(p llm.Provider) Option {
	return func(n *Navi) { n.override = p }
}

// assemble resolves the runtime layers into an effective default and
// builds the provider set. It runs after validation and before freezing,
// because resolution can fail — and a navi whose default provider cannot
// authenticate must fail before it accepts work it could never run.
func (n *Navi) assemble() error {
	// Load the FILE layer only. The baked defaults must not be treated
	// as an explicit config choice, or the compiled default could never
	// win — the same trap the merge chain documents for its flag layer.
	var file *config.RuntimeConfig
	if n.runtimeFile != "" {
		rc, err := config.LoadRuntimeFile(n.runtimeFile)
		if err != nil {
			return err
		}
		file = &rc
	}

	sel, err := n.resolveDefault(file)
	if err != nil {
		return err
	}
	n.selection = sel

	// Runtime endpoint override: the file's provider.endpoint, with
	// OLLAMA_HOST (Ollama-specific) winning for the ollama provider, is
	// applied to the selected descriptor before the provider set is
	// built. This is what lets WithRuntimeConfig retarget an endpoint
	// without the author baking it in.
	models := n.Models.All()
	endpoint := ""
	if file != nil {
		endpoint = strings.TrimSpace(file.Provider.Endpoint)
	}
	if ep := envValue(config.EnvOllamaHost); ep != "" && sel.Provider == "ollama" {
		endpoint = ep
	}
	models = withEndpoint(models, sel.Provider, endpoint)

	set := provider.New(models, nil)
	if n.override != nil {
		// An overridden provider needs no credential and no declared set;
		// the default is still resolved so the selection remains
		// reportable.
		n.providers = set
	} else {
		if sel.Provider != "" {
			if _, err := set.For(sel.Provider); err != nil {
				return fmt.Errorf("navi: default provider: %w", err)
			}
		}
		n.providers = set
	}

	// Fallback chain (ADR 0009): only for thoughts that name no provider,
	// matching the reference runtime's resolution. An unknown fallback fails
	// assembly rather than degrading silently.
	if n.override == nil {
		fbName := n.fallbackName
		if fbName == "" && file != nil {
			fbName = strings.TrimSpace(file.Provider.Fallback)
		}
		if fbName != "" {
			primary, perr := set.For(sel.Provider)
			if perr != nil {
				return fmt.Errorf("navi: fallback primary: %w", perr)
			}
			fb, ferr := set.For(fbName)
			if ferr != nil {
				return fmt.Errorf("navi: fallback provider %q: %w", fbName, ferr)
			}
			chain, cerr := llm.NewProviderFallback(primary, fb, retryFromConfig(file))
			if cerr != nil {
				return fmt.Errorf("navi: fallback chain: %w", cerr)
			}
			n.fallback = chain
		}
	}

	// Resolve the datastore candidate set and bind the stable handle.
	// This is assembly-time binding validation (ADR 0011 decision 4):
	// registration checked the shape, this checks that a handle with a
	// consumer actually resolves.
	if err := n.assembleDatastore(file); err != nil {
		return err
	}

	// Short-term memory (when declared) needs the bound datastore, and adds
	// its write tool to the catalog before the registry is built.
	if err := n.assembleMemory(); err != nil {
		return err
	}

	// The compiled skill set: authored skills plus the generated core skills
	// and inventory (ADR 0013 decision 6). Built after the component
	// collections are known so the inventory cannot drift from main.go.
	n.assembleSkills()

	// Build the runtime tool registry from the declared collection and set
	// the compiled default active set under the permission ceiling.
	reg, err := n.buildRegistry()
	if err != nil {
		return err
	}
	n.registry = reg
	return nil
}

// assembleMemory builds the short-term memory service when the navi declares
// it (WithShortTermMemory). Memory requires a datastore (ADR 0011 decision
// 9): declaring it with no bound candidate fails here with a clear error
// rather than silently dropping what the navi believed it was keeping.
func (n *Navi) assembleMemory() error {
	if !n.shortTermMemory {
		return nil
	}
	if !n.Datastores.handle.Bound() {
		return fmt.Errorf("%w: short-term memory requires a datastore, but no candidate is bound", ErrInvalid)
	}

	svc, err := memory.New(memory.Config{
		Memory:  n.Datastores.handle.Memory(),
		Submit:  n.Datastores.handle.Submit,
		Subject: n.name,
	})
	if err != nil {
		return fmt.Errorf("navi: short-term memory: %w", err)
	}
	n.shortMemory = svc

	tools, err := memory.Tools(svc)
	if err != nil {
		return fmt.Errorf("navi: short-term memory tools: %w", err)
	}
	for _, t := range tools {
		if err := n.Tools.Add(t); err != nil {
			return err
		}
	}
	return nil
}

// assembleDatastore selects the active backend from the compiled candidates
// and the runtime config, opens it, and binds the stable handle. The navi
// runtime itself consumes the thought/transcript view, so declaring a
// candidate means history is persisted through it.
func (n *Navi) assembleDatastore(file *config.RuntimeConfig) error {
	// The run path is RAM-first: the ordered writer owns persistence,
	// configured before the handle binds the active backend.
	n.Datastores.handle.SetWriterConfig(datastore.WriterConfig{
		Interval: n.datastoreFlushInterval,
		OnError: func(w datastore.Write, err error) {
			n.log.Error("datastore write failed; will retry next drain",
				"aggregate", string(w.Aggregate), "key", w.Key, "err", err)
		},
	})

	cfg := datastore.Config{}
	if file != nil && file.Datastore != nil {
		cfg.Backend = file.Datastore.Backend
		cfg.Params = file.Datastore.Params
		if len(file.Datastore.Routing) > 0 {
			routing := make(map[datastore.Aggregate]string, len(file.Datastore.Routing))
			for agg, backend := range file.Datastore.Routing {
				routing[datastore.Aggregate(agg)] = backend
			}
			cfg.Routing = routing
		}
	}
	if err := n.Datastores.activate(cfg); err != nil {
		return err
	}
	if n.Datastores.handle.Bound() {
		n.transcripts = n.Datastores.handle.Thoughts()
		// The queue was built before the datastore resolved; adopt any
		// thoughts persisted in a previous process now that its store is
		// bound. Idempotent.
		n.Thoughts.Recover()
	}
	return nil
}

// resolveDefault applies the override chain for thoughts that name no
// provider/model: env > config > build-time. The two halves resolve
// independently, so an operator can retarget the provider without also
// pinning a model — and retargeting the provider does NOT drag the old
// provider's model along, which is why the model's build-time fallback
// follows the *selected* provider rather than the compiled default.
func (n *Navi) resolveDefault(file *config.RuntimeConfig) (DefaultSelection, error) {
	buildProvider, buildSource, err := n.buildTimeDefault()
	if err != nil {
		return DefaultSelection{}, err
	}

	sel := DefaultSelection{Provider: buildProvider, Source: buildSource}
	switch {
	case envValue(config.EnvProvider) != "":
		sel.Provider, sel.Source = envValue(config.EnvProvider), SourceEnv
	case file != nil && strings.TrimSpace(file.Provider.Primary) != "":
		sel.Provider, sel.Source = strings.TrimSpace(file.Provider.Primary), SourceConfig
	}

	switch {
	case envValue(config.EnvModel) != "":
		sel.Model = envValue(config.EnvModel)
	case file != nil && strings.TrimSpace(file.Provider.Model) != "":
		sel.Model = strings.TrimSpace(file.Provider.Model)
	default:
		// The selected provider's own declared model.
		if p, ok := n.Models.Get(sel.Provider); ok {
			sel.Model = p.Model
		}
	}
	return sel, nil
}

// buildTimeDefault returns the compiled default: the explicit
// WithDefaultModel option if set, otherwise the descriptor marked
// model.Default(). Both are the build-time layer; the explicit option is
// the more specific statement and wins.
func (n *Navi) buildTimeDefault() (string, DefaultSource, error) {
	if n.defaultProvider != "" {
		p, ok := n.Models.Get(n.defaultProvider)
		if !ok {
			return "", "", fmt.Errorf("%w: default model %q is not a declared provider",
				ErrInvalid, n.defaultProvider)
		}
		return p.Name, SourceBuild, nil
	}
	for _, p := range n.Models.All() {
		if p.Default {
			return p.Name, SourceBuild, nil
		}
	}
	return "", SourceBuild, nil
}

// resolveFor picks the adapter and model for one thought. The thought's
// own overrides win over the navi default (ADR 0009), and an unknown name
// is an error rather than a silent fallback — routing work to a different
// model than the caller asked for is worse than failing.
func (n *Navi) resolveFor(pt *thought.Thought) (llm.Provider, string, error) {
	if n.override != nil {
		modelName := pt.Model
		if modelName == "" {
			modelName = n.selection.Model
		}
		return n.override, modelName, nil
	}
	if n.providers == nil {
		return nil, "", fmt.Errorf("navi: resolve provider before serving")
	}
	// A thought that names no provider uses the default selection, and the
	// configured fallback chain applies to exactly that path.
	if pt.Provider == "" && n.fallback != nil {
		modelName := pt.Model
		if modelName == "" {
			modelName = n.selection.Model
		}
		return n.fallback, modelName, nil
	}
	name := pt.Provider
	if name == "" {
		name = n.selection.Provider
	}
	if name == "" {
		return nil, "", fmt.Errorf("navi: thought %s names no provider and no default is declared", pt.ID)
	}
	p, err := n.providers.For(name)
	if err != nil {
		return nil, "", err
	}
	modelName := pt.Model
	if modelName == "" {
		modelName = n.selection.Model
	}
	return p, modelName, nil
}

// withEndpoint returns a copy of the provider descriptors with the named
// provider's Endpoint replaced (empty endpoint leaves the set untouched).
func withEndpoint(models []model.Provider, providerName, endpoint string) []model.Provider {
	if endpoint == "" || providerName == "" {
		return models
	}
	out := make([]model.Provider, len(models))
	copy(out, models)
	for i := range out {
		if out[i].Name == providerName {
			out[i].Endpoint = endpoint
		}
	}
	return out
}

// envValue reads a runtime env var, trimmed. Missing and blank are the
// same thing: an empty override is not an override.
func envValue(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

// retryFromConfig maps the runtime provider retry knobs onto the llm retry
// config (nil = defaults).
func retryFromConfig(file *config.RuntimeConfig) *llm.RetryConfig {
	if file == nil {
		return nil
	}
	pc := file.Provider
	if pc.MaxRetries == 0 && pc.InitialDelay == 0 {
		return nil
	}
	return &llm.RetryConfig{
		MaxRetries:    pc.MaxRetries,
		InitialDelay:  pc.InitialDelay,
		MaxDelay:      pc.MaxDelay,
		BackoffFactor: pc.BackoffFactor,
		JitterEnabled: true,
	}
}
