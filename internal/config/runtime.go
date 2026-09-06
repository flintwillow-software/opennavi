package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ProviderConfig is the LLM provider configuration (runtime) — it maps
// onto the #189 registry/fallback layer and #183's llm.Config.
type ProviderConfig struct {
	// Primary is the primary provider name ("ollama", "openrouter", ...).
	Primary string `yaml:"primary,omitempty"`
	// Fallback is the optional fallback provider name.
	Fallback string `yaml:"fallback,omitempty"`
	// Model is the default model for the primary provider.
	Model string `yaml:"model,omitempty"`
	// Endpoint overrides the provider's default base URL
	// (e.g. Ollama host). Env OLLAMA_HOST takes precedence when set.
	Endpoint string `yaml:"endpoint,omitempty"`
	// APIKey is the provider API key (env override: see Env()).
	APIKey string `yaml:"api_key,omitempty"`
	// Mode is "local" or "cloud" (informational; selects defaults).
	Mode string `yaml:"mode,omitempty"`
	// Retry is the retry configuration; nil = llm.DefaultRetryConfig.
	MaxRetries    int           `yaml:"max_retries,omitempty"`
	InitialDelay  time.Duration `yaml:"initial_delay,omitempty"`
	MaxDelay      time.Duration `yaml:"max_delay,omitempty"`
	BackoffFactor float64       `yaml:"backoff_factor,omitempty"`
}

// ToolPolicy is per-tool access control (spec: tool config with
// allowed/blocked lists, per-tool timeouts).
type ToolPolicy struct {
	Allowed []string                 `yaml:"allowed,omitempty"` // if non-empty, ONLY these run
	Blocked []string                 `yaml:"blocked,omitempty"` // denied tools (deny wins over allowed)
	Timeout map[string]time.Duration `yaml:"timeouts,omitempty"`
}

// RuntimeConfig is the user-modifiable configuration (spec "Static
// Definition vs. Runtime Config Separation"): LLM provider keys,
// session settings, tool policy overrides. It never modifies the
// compiled NaviDefinition.
type RuntimeConfig struct {
	Provider ProviderConfig `yaml:"provider,omitempty"`
	Tools    ToolPolicy     `yaml:"tools,omitempty"`
	// SessionDB is the session/memory SQLite path.
	SessionDB string `yaml:"session_db,omitempty"`
	// EmbedModel is the local embedding model (memory Tier 5).
	EmbedModel string `yaml:"embed_model,omitempty"`
}

// DefaultRuntimeConfig is the baked-in baseline of the merge chain.
func DefaultRuntimeConfig() RuntimeConfig {
	return RuntimeConfig{
		Provider: ProviderConfig{
			Primary:       "ollama",
			Mode:          "local",
			InitialDelay:  500 * time.Millisecond,
			MaxDelay:      10 * time.Second,
			BackoffFactor: 2.0,
			MaxRetries:    3,
		},
		SessionDB:  "./navi.db",
		EmbedModel: "nomic-embed-text",
	}
}

// LoadRuntimeFile reads a runtime config YAML file (the "file" layer).
func LoadRuntimeFile(path string) (RuntimeConfig, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return RuntimeConfig{}, fmt.Errorf("config: read runtime config %s: %w", path, err)
	}
	var rc RuntimeConfig
	if err := yaml.Unmarshal(blob, &rc); err != nil {
		return RuntimeConfig{}, fmt.Errorf("config: parse runtime config %s: %w", path, err)
	}
	return rc, nil
}

// MergeRuntime applies the merge chain (defaults → file → env → flags).
// file may be zero-valued (no file layer); env is read from the
// environment inside; flags carries the explicitly-set CLI values.
func MergeRuntime(file RuntimeConfig, flags RuntimeConfig) RuntimeConfig {
	out := DefaultRuntimeConfig()

	// Layer 2: file.
	mergeRuntime(&out, file)
	// Layer 3: env.
	applyEnv(&out)
	// Layer 4: flags (explicit CLI values win).
	mergeRuntime(&out, flags)

	// OLLAMA_HOST fills the endpoint only when the FINAL primary is
	// ollama — the env layer runs before flags, so the final primary
	// is not known until here.
	if strings.HasPrefix(out.Provider.Primary, "ollama") && out.Provider.Endpoint == "" {
		if v := os.Getenv("OLLAMA_HOST"); v != "" {
			out.Provider.Endpoint = v
		}
	}
	return out
}

func mergeRuntime(dst *RuntimeConfig, src RuntimeConfig) {
	if src.Provider.Primary != "" {
		dst.Provider.Primary = src.Provider.Primary
	}
	if src.Provider.Fallback != "" {
		dst.Provider.Fallback = src.Provider.Fallback
	}
	if src.Provider.Model != "" {
		dst.Provider.Model = src.Provider.Model
	}
	if src.Provider.Endpoint != "" {
		dst.Provider.Endpoint = src.Provider.Endpoint
	}
	if src.Provider.APIKey != "" {
		dst.Provider.APIKey = src.Provider.APIKey
	}
	if src.Provider.Mode != "" {
		dst.Provider.Mode = src.Provider.Mode
	}
	if src.Provider.MaxRetries != 0 {
		dst.Provider.MaxRetries = src.Provider.MaxRetries
	}
	if src.Provider.InitialDelay != 0 {
		dst.Provider.InitialDelay = src.Provider.InitialDelay
	}
	if src.Provider.MaxDelay != 0 {
		dst.Provider.MaxDelay = src.Provider.MaxDelay
	}
	if src.Provider.BackoffFactor != 0 {
		dst.Provider.BackoffFactor = src.Provider.BackoffFactor
	}
	if len(src.Tools.Allowed) > 0 {
		dst.Tools.Allowed = src.Tools.Allowed
	}
	if len(src.Tools.Blocked) > 0 {
		dst.Tools.Blocked = src.Tools.Blocked
	}
	if len(src.Tools.Timeout) > 0 {
		dst.Tools.Timeout = src.Tools.Timeout
	}
	if src.SessionDB != "" {
		dst.SessionDB = src.SessionDB
	}
	if src.EmbedModel != "" {
		dst.EmbedModel = src.EmbedModel
	}
}

// applyEnv applies environment overrides (layer 3). Known variables:
// OLLAMA_HOST, OPENROUTER_API_KEY, NAVI_SESSION_KEY, NAVI_SESSION_DB,
// NAVI_MODEL.
func applyEnv(rc *RuntimeConfig) {
	if v := os.Getenv("OPENROUTER_API_KEY"); v != "" && rc.Provider.Primary == "openrouter" {
		rc.Provider.APIKey = v
	}
	if v := os.Getenv("NAVI_SESSION_DB"); v != "" {
		rc.SessionDB = v
	}
	if v := os.Getenv("NAVI_MODEL"); v != "" {
		rc.Provider.Model = v
	}
}

// Validate checks the runtime config against a definition: referenced
// tools must exist in the compiled set.
func (rc RuntimeConfig) Validate(d *NaviDefinition) error {
	defined := map[string]bool{}
	for _, t := range d.Tools {
		defined[t.Name] = true
	}
	for _, name := range append(append([]string{}, rc.Tools.Allowed...), rc.Tools.Blocked...) {
		if !defined[name] {
			return fmt.Errorf("config: tool %q not defined in navi definition", name)
		}
	}
	return nil
}
