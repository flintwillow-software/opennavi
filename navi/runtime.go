package navi

import (
	"fmt"
	"os"
	"strings"

	"github.com/flintwillow-software/opennavi/internal/config"
	"github.com/flintwillow-software/opennavi/internal/provider"
	"github.com/flintwillow-software/opennavi/llm"
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
// YAML the daemon reads. A *path* rather than a parsed struct, so the
// public navi package does not leak the internal config type and an
// out-of-repo author can still supply one. Empty means no file layer.
func WithRuntimeConfig(path string) Option {
	return func(n *Navi) { n.runtimeFile = path }
}

// Default returns the resolved default provider/model and the layer that
// decided it. Zero before Run has assembled the navi.
func (n *Navi) Default() DefaultSelection { return n.selection }

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

	set := provider.New(n.Models.All(), nil)
	if sel.Provider != "" {
		if _, err := set.For(sel.Provider); err != nil {
			return fmt.Errorf("navi: default provider: %w", err)
		}
	}
	n.providers = set
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
	if n.providers == nil {
		return nil, "", fmt.Errorf("navi: resolve provider before serving")
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

// envValue reads a runtime env var, trimmed. Missing and blank are the
// same thing: an empty override is not an override.
func envValue(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}
