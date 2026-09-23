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

// ThoughtQueueConfig tunes the daemon's thought queue (deployment
// tuning: worker concurrency and priority slots). Runtime side of the
// S000200 §04 split — queue throughput is an operator concern, not a
// compiled-definition concern.
type ThoughtQueueConfig struct {
	MaxConcurrent  int    `yaml:"max_concurrent,omitempty"`
	PrioritySlots  int    `yaml:"priority_slots,omitempty"`
	SlotExhaustion string `yaml:"slot_exhaustion,omitempty"`
}

// ToolCapsConfig tunes tool-level caps and egress policy (NAVI-272,
// S130500): note body caps and http_request egress policy. Nil
// sections and zero fields keep the tool packages' safe defaults.
type ToolCapsConfig struct {
	Notes *NoteCapsConfig   `yaml:"notes,omitempty"`
	HTTP  *HTTPEgressConfig `yaml:"http,omitempty"`
}

// NoteCapsConfig caps note tool storage (S130500 §05).
type NoteCapsConfig struct {
	// MaxBodyBytes caps one note body (default 64 KiB).
	MaxBodyBytes int `yaml:"max_body_bytes,omitempty"`
	// MaxNaviBytes caps the navi's total stored note body bytes
	// (default 10 MiB).
	MaxNaviBytes int `yaml:"max_navi_bytes,omitempty"`
}

// HTTPEgressConfig tunes http_request egress (S130500 §06). Allowlist
// entries exempt internal services from the SSRF rules; denylist
// entries remove them again (deny wins).
type HTTPEgressConfig struct {
	MaxResponseBytes int      `yaml:"max_response_bytes,omitempty"` // default 256 KiB
	Allowlist        []string `yaml:"allowlist,omitempty"`
	Denylist         []string `yaml:"denylist,omitempty"`
}

// RuntimeConfig is the user-modifiable configuration (spec "Static
// Definition vs. Runtime Config Separation"): LLM provider keys,
// session settings, tool policy overrides. It never modifies the
// compiled-in Go navi definition (S000200 §04).
type RuntimeConfig struct {
	Provider ProviderConfig `yaml:"provider,omitempty"`
	Tools    ToolPolicy     `yaml:"tools,omitempty"`
	// ToolCaps tunes tool caps and egress (nil = package defaults).
	ToolCaps *ToolCapsConfig `yaml:"tool_caps,omitempty"`
	// SessionDB is the session/memory SQLite path.
	SessionDB string `yaml:"session_db,omitempty"`
	// EmbedModel is the local embedding model (memory Tier 5).
	EmbedModel string `yaml:"embed_model,omitempty"`
	// ThoughtQueue tunes the daemon's thought queue (nil = defaults).
	ThoughtQueue *ThoughtQueueConfig `yaml:"thought_queue,omitempty"`
	// Cron declares the daemon's cron schedules (nil = none).
	Cron *CronConfig `yaml:"cron,omitempty"`
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

	// Post-merge env resolution for provider secrets/endpoints that
	// depend on the FINAL primary (the env layer runs before flags, so
	// the final primary is not known until here).
	//
	// Precedence for these fields is file < env < flags: the fills are
	// gated on the FLAG value being empty (not the merged value) -
	// gating on the merged value would let a file-layer key block the
	// env fill, inverting the chain.
	if strings.HasPrefix(out.Provider.Primary, "ollama") && flags.Provider.Endpoint == "" {
		if v := os.Getenv("OLLAMA_HOST"); v != "" {
			out.Provider.Endpoint = v
		}
	}
	if out.Provider.Primary == "openrouter" && flags.Provider.APIKey == "" {
		if v := os.Getenv("OPENROUTER_API_KEY"); v != "" {
			out.Provider.APIKey = v
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
	if src.ThoughtQueue != nil {
		tq := *src.ThoughtQueue
		dst.ThoughtQueue = &tq
	}
	if src.Cron != nil {
		c := *src.Cron
		dst.Cron = &c
	}
	if src.ToolCaps != nil {
		tc := *src.ToolCaps
		if src.ToolCaps.Notes != nil {
			n := *src.ToolCaps.Notes
			tc.Notes = &n
		}
		if src.ToolCaps.HTTP != nil {
			h := *src.ToolCaps.HTTP
			tc.HTTP = &h
		}
		dst.ToolCaps = &tc
	}
}

// applyEnv applies environment overrides (layer 3). Known variables:
// OLLAMA_HOST, OPENROUTER_API_KEY (post-merge), NAVI_SESSION_DB,
// NAVI_MODEL. NAVI_SESSION_KEY is consumed by the cipher seam
// (sqlite.CipherFromEnv), not by this package.
func applyEnv(rc *RuntimeConfig) {
	if v := os.Getenv("NAVI_SESSION_DB"); v != "" {
		rc.SessionDB = v
	}
	if v := os.Getenv("NAVI_MODEL"); v != "" {
		rc.Provider.Model = v
	}
}

// Validate checks the runtime config's self-contained invariants:
// thought-queue tuning bounds and cron schedule consistency. There is
// deliberately no cross-check against a navi definition — under the
// go-first split (S000200 §04, S000207) the compiled definition is not
// visible to the runtime-config layer; the navi binary itself enforces
// its compiled-in tool policy.
func (rc RuntimeConfig) Validate() error {
	if tq := rc.ThoughtQueue; tq != nil {
		if tq.MaxConcurrent < 0 {
			return fmt.Errorf("config: thought_queue.max_concurrent %d must be >= 0", tq.MaxConcurrent)
		}
		if tq.PrioritySlots < 0 {
			return fmt.Errorf("config: thought_queue.priority_slots %d must be >= 0", tq.PrioritySlots)
		}
		if tq.SlotExhaustion != "" && tq.SlotExhaustion != "queue" && tq.SlotExhaustion != "reject" {
			return fmt.Errorf("config: thought_queue.slot_exhaustion %q must be \"queue\" or \"reject\"", tq.SlotExhaustion)
		}
	}
	if rc.Cron != nil {
		if err := rc.Cron.Validate(); err != nil {
			return err
		}
	}
	if tc := rc.ToolCaps; tc != nil {
		if tc.Notes != nil {
			if tc.Notes.MaxBodyBytes < 0 {
				return fmt.Errorf("config: tool_caps.notes.max_body_bytes %d must be >= 0", tc.Notes.MaxBodyBytes)
			}
			if tc.Notes.MaxNaviBytes < 0 {
				return fmt.Errorf("config: tool_caps.notes.max_navi_bytes %d must be >= 0", tc.Notes.MaxNaviBytes)
			}
		}
		if tc.HTTP != nil && tc.HTTP.MaxResponseBytes < 0 {
			return fmt.Errorf("config: tool_caps.http.max_response_bytes %d must be >= 0", tc.HTTP.MaxResponseBytes)
		}
	}
	return nil
}
