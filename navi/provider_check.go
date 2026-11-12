package navi

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flintwillow-software/opennavi/internal/config"
	"github.com/flintwillow-software/opennavi/internal/provider"
	"github.com/flintwillow-software/opennavi/model"
)

// Provider authentication check (NAVI-606): the navi's declared providers are
// a build-time set (ADR 0009), but whether each one is REACHABLE and
// AUTHENTICATED is a runtime fact. A navi that silently fails a provider at
// the first thought is worse than one that says at startup which providers it
// can actually use — so this is the seam that answers the question before any
// work is queued.
//
// It lives on the navi rather than in the template or in model/ because only
// the composition root can turn a declared model.Provider descriptor into a
// live adapter: the descriptor carries a credential REFERENCE (ADR 0008) and
// the code that resolves it to a value, and the adapter to a live client, is
// internal (internal/provider). A template cannot reach it, and model/ stays
// stdlib-only and secret-free. So the check is an SDK capability the template
// merely calls — which is why it ships as its own commit.

// ProviderStatus is one declared provider's runtime usability: whether the
// navi could reach it and authenticate, and if not, why. It carries no
// credential VALUES — only the reference name (ADR 0008), so the report is
// safe to log and to show in a portal.
type ProviderStatus struct {
	// Name is the provider identifier ("ollama", "openrouter").
	Name string `json:"name"`
	// Model is the model the descriptor serves by default.
	Model string `json:"model"`
	// Default marks the provider a thought uses when it names none.
	Default bool `json:"default"`
	// Local reports whether the provider leaves the machine.
	Local bool `json:"local,omitempty"`
	// Credential is the reference the descriptor names, never its value.
	Credential string `json:"credential_ref,omitempty"`
	// Usable is true when the provider resolved and answered a ping.
	Usable bool `json:"usable"`
	// Reason explains an unusable provider. Empty when Usable.
	Reason string `json:"reason,omitempty"`
}

// ProviderCheckTimeout bounds one provider's ping. A provider that does not
// answer within it is reported unusable rather than hanging the check.
const ProviderCheckTimeout = 5 * time.Second

// CheckProviders pings every declared provider and reports which are usable.
// It is safe to call before Run: it resolves the same descriptors assembly
// would, with the same endpoint overrides (a runtime config file's
// provider.endpoint, and OLLAMA_HOST for the ollama provider), so its answer
// describes the navi as it will actually run.
//
// A provider that is unreachable or unauthenticated is NOT an error: the
// report is the answer, and the caller decides what to do (the standard
// template logs one line per provider and continues). An error is returned
// only when no provider could be checked at all — a navi with none declared.
//
// The runtime config is read from n.runtimeFile when WithRuntimeConfig named
// one; an unreadable file is returned as an error, because a check against the
// wrong endpoints would be worse than no check.
func (n *Navi) CheckProviders(ctx context.Context) ([]ProviderStatus, error) {
	models := n.Models.All()
	if len(models) == 0 {
		return nil, fmt.Errorf("%w: no providers declared to check", ErrInvalid)
	}

	// Apply the same endpoint override assembly applies, so the ping goes to
	// the endpoint the navi would really use. Assembly resolves a default
	// selection and applies the file's provider.endpoint to it; the check
	// mirrors that for the default, and OLLAMA_HOST wins for the ollama
	// provider regardless of a config file.
	fileEndpoint := ""
	if n.runtimeFile != "" {
		rc, err := config.LoadRuntimeFile(n.runtimeFile)
		if err != nil {
			return nil, fmt.Errorf("navi: provider check: runtime config: %w", err)
		}
		fileEndpoint = rc.Provider.Endpoint
	}
	defaultName := n.defaultProviderName()
	for i := range models {
		override := ""
		if models[i].Name == defaultName {
			override = fileEndpoint
		}
		models[i].Endpoint = pickEndpoint(models[i], override)
	}

	set := provider.New(models, nil)
	out := make([]ProviderStatus, 0, len(models))
	for _, p := range models {
		out = append(out, n.checkOne(ctx, set, p))
	}
	return out, nil
}

// checkOne resolves and pings one provider.
func (n *Navi) checkOne(ctx context.Context, set *provider.Set, p model.Provider) ProviderStatus {
	st := ProviderStatus{
		Name:       p.Name,
		Model:      p.Model,
		Default:    p.Default,
		Local:      p.Mode == model.ModeLocal,
		Credential: string(p.Credential),
	}
	if n.override != nil {
		// A navi with an overridden provider (a test seam) checks the
		// override rather than the declared descriptor.
		st.Usable, st.Reason = pingStatus(ctx, n.override)
		return st
	}
	live, err := set.Resolve(p)
	if err != nil {
		// The fail-closed credential error names the reference, not its
		// value (ADR 0008).
		st.Reason = err.Error()
		return st
	}
	st.Usable, st.Reason = pingStatus(ctx, live)
	return st
}

// pingStatus pings a live provider under a bounded timeout.
func pingStatus(ctx context.Context, p pinger) (bool, string) {
	pingCtx, cancel := context.WithTimeout(ctx, ProviderCheckTimeout)
	defer cancel()
	if err := p.Ping(pingCtx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return false, fmt.Sprintf("no answer within %s", ProviderCheckTimeout)
		}
		return false, err.Error()
	}
	return true, ""
}

// pinger is the one method of a live provider this file needs.
type pinger interface{ Ping(context.Context) error }

// defaultProviderName resolves the default provider the same way model_list
// does: the explicit WithDefaultModel wins, else the descriptor marker.
func (n *Navi) defaultProviderName() string {
	if n.defaultProvider != "" {
		return n.defaultProvider
	}
	for _, p := range n.Models.All() {
		if p.Default {
			return p.Name
		}
	}
	return ""
}

// pickEndpoint applies the endpoint override rules assembly uses: an explicit
// override wins, else OLLAMA_HOST for the ollama provider.
func pickEndpoint(p model.Provider, override string) string {
	if override != "" {
		return override
	}
	if p.Name == "ollama" {
		if ep := envValue(config.EnvOllamaHost); ep != "" {
			return ep
		}
	}
	return p.Endpoint
}
