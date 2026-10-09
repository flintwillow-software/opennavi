package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── runtime merge chain ──────────────────────────────────────────────────

func TestMergeChainFull(t *testing.T) {
	file := RuntimeConfig{
		Provider: ProviderConfig{
			Primary:    "openrouter",
			Model:      "z-ai/glm-5.3-flash",
			MaxRetries: 5,
		},
		SessionDB: "/tmp/file.db",
	}
	flags := RuntimeConfig{
		Provider:  ProviderConfig{Model: "flag-model"},
		SessionDB: "/tmp/flag.db",
	}

	t.Setenv("OPENROUTER_API_KEY", "env-key")
	t.Setenv("NAVI_SESSION_DB", "/tmp/env.db")
	t.Setenv("NAVI_MODEL", "env-model")

	// Layer order: defaults → file → env → flags.
	got := MergeRuntime(file, flags)

	// File wins over defaults.
	if got.Provider.Primary != "openrouter" {
		t.Fatalf("primary = %q", got.Provider.Primary)
	}
	if got.Provider.MaxRetries != 5 {
		t.Fatalf("file max_retries = %d", got.Provider.MaxRetries)
	}
	// Env wins over file.
	if got.Provider.APIKey != "env-key" {
		t.Fatalf("env api key = %q", got.Provider.APIKey)
	}
	// Env (NAVI_SESSION_DB) wins over file...
	if got.SessionDB == "/tmp/env.db" {
		// ..unless flags override: flags win over env.
	}
	if got.SessionDB != "/tmp/flag.db" {
		t.Fatalf("session db = %q, want flags layer", got.SessionDB)
	}
	// Flags win over env for model.
	if got.Provider.Model != "flag-model" {
		t.Fatalf("model = %q, want flag-model", got.Provider.Model)
	}
}

func TestMergeChainEnvEndpointOllama(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "http://10.0.0.50:11434")
	got := MergeRuntime(RuntimeConfig{}, RuntimeConfig{Provider: ProviderConfig{Primary: "ollama"}})
	if got.Provider.Endpoint != "http://10.0.0.50:11434" {
		t.Fatalf("endpoint = %q", got.Provider.Endpoint)
	}
	// Non-ollama primary: OLLAMA_HOST does not override the endpoint.
	t.Setenv("OLLAMA_HOST", "http://10.0.0.50:11434")
	got = MergeRuntime(RuntimeConfig{}, RuntimeConfig{Provider: ProviderConfig{Primary: "openrouter"}})
	if strings.HasPrefix(got.Provider.Endpoint, "http://10.0.0.50") {
		t.Fatalf("endpoint leaked ollama host for non-ollama primary: %q", got.Provider.Endpoint)
	}
}

func TestMergeChainDefaultsWhenEmpty(t *testing.T) {
	got := MergeRuntime(RuntimeConfig{}, RuntimeConfig{})
	if got.Provider.Primary != "ollama" {
		t.Fatalf("primary = %q, want default ollama", got.Provider.Primary)
	}
	if got.SessionDB != "./navi.db" {
		t.Fatalf("session db = %q, want default", got.SessionDB)
	}
	if got.Provider.BackoffFactor != 2.0 {
		t.Fatalf("backoff = %v", got.Provider.BackoffFactor)
	}
}

func TestRuntimeFileLayer(t *testing.T) {
	path := t.TempDir() + "/runtime.yaml"
	yaml := `
provider:
  primary: openrouter
  fallback: ollama
  model: z-ai/glm-5.3-flash
tools:
  blocked: [shell_exec]
  timeouts:
    file_read: 5s
session_db: /data/navi.db
thought_queue:
  max_concurrent: 4
  priority_slots: 2
  slot_exhaustion: queue
cron:
  schedules:
    - name: health-check
      expr: "*/5 * * * *"
      prompt: "Check service health."
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, err := LoadRuntimeFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if rc.Provider.Primary != "openrouter" || rc.Provider.Fallback != "ollama" {
		t.Fatalf("provider = %+v", rc.Provider)
	}
	if len(rc.Tools.Blocked) != 1 || rc.Tools.Blocked[0] != "shell_exec" {
		t.Fatalf("blocked = %v", rc.Tools.Blocked)
	}
	if rc.Tools.Timeout["file_read"] != 5*time.Second {
		t.Fatalf("timeout = %v", rc.Tools.Timeout)
	}
	if rc.SessionDB != "/data/navi.db" {
		t.Fatalf("session db = %q", rc.SessionDB)
	}
	if rc.ThoughtQueue == nil || rc.ThoughtQueue.MaxConcurrent != 4 || rc.ThoughtQueue.PrioritySlots != 2 {
		t.Fatalf("thought queue = %+v", rc.ThoughtQueue)
	}
	if rc.Cron == nil || len(rc.Cron.Schedules) != 1 || rc.Cron.Schedules[0].Name != "health-check" {
		t.Fatalf("cron = %+v", rc.Cron)
	}
	if err := rc.Validate(); err != nil {
		t.Fatalf("validate = %v", err)
	}
}

func TestRuntimeValidateSelfContained(t *testing.T) {
	// Under the go-first split the runtime config validates its own
	// invariants — there is no definition to cross-check.
	cases := []struct {
		name    string
		rc      RuntimeConfig
		wantErr string
	}{
		{"empty ok", RuntimeConfig{}, ""},
		{"queue ok", RuntimeConfig{ThoughtQueue: &ThoughtQueueConfig{MaxConcurrent: 2, PrioritySlots: 1, SlotExhaustion: "queue"}}, ""},
		{"negative concurrency", RuntimeConfig{ThoughtQueue: &ThoughtQueueConfig{MaxConcurrent: -1}}, "max_concurrent"},
		{"negative slots", RuntimeConfig{ThoughtQueue: &ThoughtQueueConfig{PrioritySlots: -2}}, "priority_slots"},
		{"bad exhaustion policy", RuntimeConfig{ThoughtQueue: &ThoughtQueueConfig{SlotExhaustion: "explode"}}, "slot_exhaustion"},
		{"bad cron", RuntimeConfig{Cron: &CronConfig{Schedules: []CronSchedule{{Name: "a", Expr: "* * * * *"}}}}, "prompt is required"},
		{"good cron", RuntimeConfig{Cron: &CronConfig{Schedules: []CronSchedule{{Name: "a", Expr: "* * * * *", Prompt: "p"}}}}, ""},
	}
	for _, c := range cases {
		err := c.rc.Validate()
		if c.wantErr == "" && err != nil {
			t.Errorf("%s: Validate = %v, want nil", c.name, err)
		}
		if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%s: Validate = %v, want containing %q", c.name, err, c.wantErr)
		}
	}
}

func TestMergeChainThoughtQueueAndCron(t *testing.T) {
	file := RuntimeConfig{ThoughtQueue: &ThoughtQueueConfig{MaxConcurrent: 2}}
	flags := RuntimeConfig{ThoughtQueue: &ThoughtQueueConfig{MaxConcurrent: 8, PrioritySlots: 3}}
	got := MergeRuntime(file, flags)
	if got.ThoughtQueue == nil || got.ThoughtQueue.MaxConcurrent != 8 || got.ThoughtQueue.PrioritySlots != 3 {
		t.Fatalf("merged thought queue = %+v", got.ThoughtQueue)
	}
	// Nil in later layer keeps the earlier layer's value.
	got = MergeRuntime(file, RuntimeConfig{})
	if got.ThoughtQueue == nil || got.ThoughtQueue.MaxConcurrent != 2 {
		t.Fatalf("merged thought queue = %+v", got.ThoughtQueue)
	}
}

func TestEnvKeyWithFlagSelectedOpenrouter(t *testing.T) {
	// Regression: OPENROUTER_API_KEY was dropped when the FLAGS layer
	// selected openrouter — applyEnv gated the key on the file-layer
	// primary. Env secrets must resolve against the FINAL primary.
	t.Setenv("OPENROUTER_API_KEY", "env-key")
	flags := RuntimeConfig{Provider: ProviderConfig{Primary: "openrouter"}}
	got := MergeRuntime(RuntimeConfig{}, flags)
	if got.Provider.APIKey != "env-key" {
		t.Fatalf("api key = %q, want env-key (post-merge env resolution)", got.Provider.APIKey)
	}
}

func TestFlagKeyWinsOverEnv(t *testing.T) {
	// Invariant: explicit flags always win over env secrets.
	t.Setenv("OPENROUTER_API_KEY", "env-key")
	flags := RuntimeConfig{Provider: ProviderConfig{
		Primary: "openrouter",
		APIKey:  "flag-key",
	}}
	got := MergeRuntime(RuntimeConfig{}, flags)
	if got.Provider.APIKey != "flag-key" {
		t.Fatalf("api key = %q, want flag-key (flags > env)", got.Provider.APIKey)
	}
}

func TestEnvKeyNotAppliedToOtherPrimaries(t *testing.T) {
	// The env secret resolves only against its own provider.
	t.Setenv("OPENROUTER_API_KEY", "env-key")
	flags := RuntimeConfig{Provider: ProviderConfig{Primary: "ollama"}}
	got := MergeRuntime(RuntimeConfig{}, flags)
	if got.Provider.APIKey != "" {
		t.Fatalf("api key = %q, want empty (primary is ollama)", got.Provider.APIKey)
	}
}

func TestEnvKeyWinsOverFile(t *testing.T) {
	// Regression: the env fill was gated on the MERGED key value, so a
	// file-layer api_key blocked the env fill (file > env, inverting
	// the chain). Gate is now the flags value: file < env < flags.
	t.Setenv("OPENROUTER_API_KEY", "env-key")
	file := RuntimeConfig{Provider: ProviderConfig{
		Primary: "openrouter",
		APIKey:  "file-key",
	}}
	got := MergeRuntime(file, RuntimeConfig{})
	if got.Provider.APIKey != "env-key" {
		t.Fatalf("api key = %q, want env-key (env beats file)", got.Provider.APIKey)
	}
}

func TestFileKeyWhenNoEnvAndNoFlag(t *testing.T) {
	// With neither env nor flag set, the file-layer key stands.
	os.Unsetenv("OPENROUTER_API_KEY")
	file := RuntimeConfig{Provider: ProviderConfig{
		Primary: "openrouter",
		APIKey:  "file-key",
	}}
	got := MergeRuntime(file, RuntimeConfig{})
	if got.Provider.APIKey != "file-key" {
		t.Fatalf("api key = %q, want file-key", got.Provider.APIKey)
	}
}

func TestFlagKeyWinsOverFile(t *testing.T) {
	os.Unsetenv("OPENROUTER_API_KEY")
	file := RuntimeConfig{Provider: ProviderConfig{Primary: "openrouter", APIKey: "file-key"}}
	flags := RuntimeConfig{Provider: ProviderConfig{Primary: "openrouter", APIKey: "flag-key"}}
	got := MergeRuntime(file, flags)
	if got.Provider.APIKey != "flag-key" {
		t.Fatalf("api key = %q, want flag-key", got.Provider.APIKey)
	}
}

// TestThinkingResolution pins the tri-state: absent says nothing (a reasoning
// model reasons), false turns reasoning off, and a flag beats the file.
func TestThinkingResolution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.yaml")
	if err := os.WriteFile(path, []byte("provider:\n  thinking: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := LoadRuntimeFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if file.Provider.Thinking == nil || *file.Provider.Thinking {
		t.Fatalf("file thinking = %v, want an explicit false", file.Provider.Thinking)
	}

	// Unset by default: no reasoning parameter is sent at all.
	if got := DefaultRuntimeConfig(); got.Provider.Thinking != nil {
		t.Fatalf("default thinking = %v, want unset", got.Provider.Thinking)
	}

	// The file applies when no flag is set, and a flag wins in both directions.
	merged := MergeRuntime(file, RuntimeConfig{})
	if merged.Provider.Thinking == nil || *merged.Provider.Thinking {
		t.Fatalf("merged thinking = %v, want the file's false", merged.Provider.Thinking)
	}
	on := true
	merged = MergeRuntime(file, RuntimeConfig{Provider: ProviderConfig{Thinking: &on}})
	if merged.Provider.Thinking == nil || !*merged.Provider.Thinking {
		t.Fatalf("merged thinking = %v, want the flag's true", merged.Provider.Thinking)
	}
}
