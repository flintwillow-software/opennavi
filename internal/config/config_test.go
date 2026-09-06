package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func validDefinition() []byte {
	return []byte(`
name: oncall-bot
version: 1.0.0
personality: You are OnCall Bot.
tools:
  - name: query_grafana
    description: Query Grafana
  - name: file_read
    description: Read files
skills:
  - name: incident-response
    instructions: Triage the incident.
interfaces:
  gateway:
    enabled: true
    port: 443
thought_queue:
  max_concurrent: 3
  priority_slots: 1
  slot_exhaustion: "queue"
permissions:
  allow:
    - tool: query_grafana
      action: ["*"]
  deny:
    - tool: file_read
`)
}

func TestParseDefinitionValid(t *testing.T) {
	d, err := ParseDefinition(validDefinition())
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "oncall-bot" || d.Version != "1.0.0" {
		t.Fatalf("name/version = %q/%q", d.Name, d.Version)
	}
	if len(d.Tools) != 2 || d.Tools[0].Name != "query_grafana" {
		t.Fatalf("tools = %+v", d.Tools)
	}
	if len(d.Skills) != 1 || d.Skills[0].Name != "incident-response" {
		t.Fatalf("skills = %+v", d.Skills)
	}
	if !d.Interfaces["gateway"].Enabled || d.Interfaces["gateway"].Port != 443 {
		t.Fatalf("interfaces = %+v", d.Interfaces)
	}
	if d.ThoughtQueue == nil || d.ThoughtQueue.MaxConcurrent != 3 {
		t.Fatalf("thought queue = %+v", d.ThoughtQueue)
	}
	if len(d.Permissions.Allow) != 1 || d.Permissions.Allow[0].Tool != "query_grafana" {
		t.Fatalf("permissions = %+v", d.Permissions)
	}
}

func TestDefinitionValidationMatrix(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"name uppercase", "name: OnCallBot", "lowercase alphanumeric"},
		{"name underscore", "name: on_call_bot", "lowercase alphanumeric"},
		{"name missing", "version: 1.0.0", "name is required"},
		{"name too long", "name: " + strings.Repeat("a", 65), "too long"},
		{"duplicate tool", "name: x\ntools:\n  - name: f\n  - name: f", "duplicate tool"},
		{"empty tool name", "name: x\ntools:\n  - description: no name", "empty name"},
		{"permission undefined tool", "name: x\ntools:\n  - name: f\npermissions:\n  allow:\n    - tool: ghost", "undefined tool"},
		{"permission no tool no path", "name: x\npermissions:\n  deny:\n    - action: [\"*\"]", "neither tool nor path"},
		{"interface port conflict", "name: x\ninterfaces:\n  gateway:\n    enabled: true\n    port: 8080\n  webui:\n    enabled: true\n    port: 8080", "conflict on port"},
	}
	for _, c := range cases {
		_, err := ParseDefinition([]byte(c.yaml))
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: error = %v, want containing %q", c.name, err, c.wantErr)
		}
	}
}

func TestDefinitionInterfaceCompatible(t *testing.T) {
	d, err := ParseDefinition([]byte(`
name: x
interfaces:
  gateway:
    enabled: true
    port: 443
  webui:
    enabled: true
    port: 8080
`))
	if err != nil {
		t.Fatalf("different ports should be compatible: %v", err)
	}
	if d.Interfaces["gateway"].Port != 443 || d.Interfaces["webui"].Port != 8080 {
		t.Fatalf("ports = %+v", d.Interfaces)
	}
}

func TestDefinitionPersonalityFormats(t *testing.T) {
	for _, p := range []string{"You are a bot.", "file:./persona.md", "dir:./persona/"} {
		if _, err := ParseDefinition([]byte("name: x\npersonality: " + p)); err != nil {
			t.Errorf("personality %q rejected: %v", p, err)
		}
	}
}

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
		// ...unless flags override: flags win over env.
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
}

func TestRuntimeValidateAgainstDefinition(t *testing.T) {
	d, err := ParseDefinition(validDefinition())
	if err != nil {
		t.Fatal(err)
	}
	// Blocked tool not in definition: error.
	bad := RuntimeConfig{Tools: ToolPolicy{Blocked: []string{"ghost"}}}
	if err := bad.Validate(d); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("error = %v", err)
	}
	// Defined tool: ok.
	ok := RuntimeConfig{Tools: ToolPolicy{Blocked: []string{"query_grafana"}}}
	if err := ok.Validate(d); err != nil {
		t.Fatal(err)
	}
}

func TestDefinitionPersonalityDirWithSpace(t *testing.T) {
	// dir: with a trailing space is a missing-path error at validation
	// (YAML-quoted so parsing reaches us).
	_, err := ParseDefinition([]byte(`name: x
personality: "dir: "`))
	if err == nil || !strings.Contains(err.Error(), "missing path") {
		t.Fatalf("error = %v", err)
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
