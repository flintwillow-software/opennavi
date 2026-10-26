package navi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/internal/config"
	"github.com/flintwillow-software/opennavi/model"
	"github.com/flintwillow-software/opennavi/thought"
)

// clearProviderEnv makes a test independent of the developer's
// environment. A test that reads an ambient credential is testing
// something different on a laptop than on CI — which is precisely how
// these tests first passed locally (where OPENROUTER_API_KEY happened to
// be set) and failed on the runner. Every test in this file states its
// environment rather than inheriting one.
func clearProviderEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		config.EnvProvider,
		config.EnvModel,
		string(model.DefaultOpenRouterCredential),
		"OLLAMA_HOST",
	} {
		t.Setenv(key, "")
	}
}

// withFakeOpenRouterCredential satisfies the fail-closed check so that a
// test about *selection* does not also become a test about credential
// resolution. The value is deliberately not a real key: nothing here
// reaches the network.
func withFakeOpenRouterCredential(t *testing.T) {
	t.Helper()
	t.Setenv(string(model.DefaultOpenRouterCredential), "test-key-not-a-secret")
}

// writeConfig drops a runtime config YAML in a temp dir and returns its
// path — the file layer of the override chain.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// testNavi declares two providers with a build-time default on ollama.
func testNavi(t *testing.T, opts ...Option) *Navi {
	t.Helper()
	n := New("t", opts...)
	if err := n.Models.Add(model.Ollama("qwen3.8:27b", model.Default())); err != nil {
		t.Fatal(err)
	}
	if err := n.Models.Add(model.OpenRouter("anthropic/claude-sonnet-4",
		model.WithCredential("OPENROUTER_API_KEY"))); err != nil {
		t.Fatal(err)
	}
	return n
}

// --- the override chain: env > config > build-time --------------------

func TestDefaultFromBuildTime(t *testing.T) {
	clearProviderEnv(t)

	n := testNavi(t)
	if err := n.assemble(); err != nil {
		t.Fatal(err)
	}
	got := n.Default()
	if got.Provider != "ollama" || got.Model != "qwen3.8:27b" {
		t.Fatalf("build-time default not honoured: %+v", got)
	}
	if got.Source != SourceBuild {
		t.Fatalf("want source %q, got %q", SourceBuild, got.Source)
	}
}

func TestConfigOverridesBuildTime(t *testing.T) {
	clearProviderEnv(t)
	withFakeOpenRouterCredential(t)
	path := writeConfig(t, "provider:\n  primary: openrouter\n  model: anthropic/claude-sonnet-4\n")

	n := testNavi(t, WithRuntimeConfig(path))
	if err := n.assemble(); err != nil {
		t.Fatal(err)
	}
	got := n.Default()
	if got.Provider != "openrouter" {
		t.Fatalf("config should override the build-time default: %+v", got)
	}
	if got.Source != SourceConfig {
		t.Fatalf("want source %q, got %q", SourceConfig, got.Source)
	}
}

func TestEnvOverridesConfigAndBuildTime(t *testing.T) {
	clearProviderEnv(t)
	withFakeOpenRouterCredential(t)
	path := writeConfig(t, "provider:\n  primary: ollama\n  model: from-config\n")
	t.Setenv(config.EnvProvider, "openrouter")
	t.Setenv(config.EnvModel, "from-env")

	n := testNavi(t, WithRuntimeConfig(path))
	if err := n.assemble(); err != nil {
		t.Fatal(err)
	}
	got := n.Default()
	if got.Provider != "openrouter" || got.Model != "from-env" {
		t.Fatalf("env must win over config and build time: %+v", got)
	}
	if got.Source != SourceEnv {
		t.Fatalf("want source %q, got %q", SourceEnv, got.Source)
	}
}

// TestRetargetingProviderDoesNotDragTheOldModel is the subtle one: an
// operator who overrides only the provider must get that provider's own
// model, not the compiled default's model, which belongs to a different
// backend entirely.
func TestRetargetingProviderDoesNotDragTheOldModel(t *testing.T) {
	clearProviderEnv(t)
	withFakeOpenRouterCredential(t)
	t.Setenv(config.EnvProvider, "openrouter")

	n := testNavi(t)
	if err := n.assemble(); err != nil {
		t.Fatal(err)
	}
	got := n.Default()
	if got.Provider != "openrouter" {
		t.Fatalf("provider: %+v", got)
	}
	if got.Model != "anthropic/claude-sonnet-4" {
		t.Fatalf("model must follow the selected provider, got %q", got.Model)
	}
}

func TestBlankEnvIsNotAnOverride(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv(config.EnvProvider, "   ")
	t.Setenv(config.EnvModel, "   ")

	n := testNavi(t)
	if err := n.assemble(); err != nil {
		t.Fatal(err)
	}
	if got := n.Default(); got.Source != SourceBuild || got.Provider != "ollama" {
		t.Fatalf("a blank env var is not an override: %+v", got)
	}
}

// --- build-time declaration rules ------------------------------------

// An explicit WithDefaultModel naming one provider while a *different*
// provider carries the descriptor marker is an ambiguity, not a
// precedence puzzle: refuse to guess.
func TestConflictingBuildTimeDefaultsRejected(t *testing.T) {
	clearProviderEnv(t)

	n := New("t", WithDefaultModel("openrouter"))
	if err := n.Models.Add(model.Ollama("qwen3.8:27b", model.Default())); err != nil {
		t.Fatal(err)
	}
	if err := n.Models.Add(model.OpenRouter("anthropic/claude-sonnet-4",
		model.WithCredential("OPENROUTER_API_KEY"))); err != nil {
		t.Fatal(err)
	}
	if err := n.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("conflicting defaults must be rejected, got %v", err)
	}
}

func TestTwoMarkersRejected(t *testing.T) {
	clearProviderEnv(t)

	n := New("t")
	_ = n.Models.Add(model.Ollama("a", model.Default()))
	_ = n.Models.Add(model.OpenRouter("b", model.Default()))
	if err := n.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("two defaults must be rejected, got %v", err)
	}
	if !strings.Contains(errString(n.Validate()), "exactly one") {
		t.Fatalf("error should explain the rule: %v", n.Validate())
	}
}

// --- fail-closed behaviour -------------------------------------------

func TestRunFailsWhenTheDefaultProvidersCredentialIsMissing(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv(config.EnvProvider, "openrouter")

	n := testNavi(t)
	err := n.Run(context.Background())
	if err == nil {
		t.Fatal("a default provider that cannot authenticate must fail the run")
	}
	if !strings.Contains(err.Error(), "OPENROUTER_API_KEY") {
		t.Fatalf("error should name the credential reference: %v", err)
	}
}

func TestRunFailsOnUnknownProviderFromEnv(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv(config.EnvProvider, "gpt-5")

	n := testNavi(t)
	err := n.Run(context.Background())
	if !errors.Is(err, model.ErrUnknownProvider) {
		t.Fatalf("want ErrUnknownProvider, got %v", err)
	}
}

// --- per-thought selection (ADR 0009) --------------------------------

func TestResolveForPrefersTheThoughtsOverride(t *testing.T) {
	clearProviderEnv(t)
	withFakeOpenRouterCredential(t)

	n := testNavi(t)
	if err := n.assemble(); err != nil {
		t.Fatal(err)
	}

	// No override: the navi default.
	sel, err := n.resolveFor(&thought.Thought{ID: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if sel.provider.Name() != "ollama" || sel.model != "qwen3.8:27b" {
		t.Fatalf("default resolution: %s/%s", sel.provider.Name(), sel.model)
	}

	// Per-thought override wins over the navi default.
	sel, err = n.resolveFor(&thought.Thought{
		ID: "t2", Provider: "openrouter", Model: "some/other-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sel.provider.Name() != "openrouter" || sel.model != "some/other-model" {
		t.Fatalf("thought override ignored: %s/%s", sel.provider.Name(), sel.model)
	}
}

func TestResolveForIsLoudOnAnUnknownThoughtProvider(t *testing.T) {
	clearProviderEnv(t)

	n := testNavi(t)
	if err := n.assemble(); err != nil {
		t.Fatal(err)
	}
	_, err := n.resolveFor(&thought.Thought{ID: "t1", Provider: "nope"})
	if !errors.Is(err, model.ErrUnknownProvider) {
		t.Fatalf("ADR 0009 forbids a silent fallback; want ErrUnknownProvider, got %v", err)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// TestPerModelOptionsReachTheRequest pins ADR 0034 end to end through the
// navi's own resolution: the routing declared for the SELECTED model reaches
// the request config, and a per-thought model override carries that model's
// options rather than the default's.
//
// The drift this guards is the reason the options travel with the selection
// (decision 6): resolving the provider, the model, and the options separately
// is how they end up disagreeing.
func TestPerModelOptionsReachTheRequest(t *testing.T) {
	clearProviderEnv(t)
	withFakeOpenRouterCredential(t)

	n := New("opts-e2e")
	if err := n.Models.Add(model.Ollama("qwen3.8:27b")); err != nil {
		t.Fatal(err)
	}
	if err := n.Models.Add(model.OpenRouter("deepseek/deepseek-v4.1-flash",
		model.Default(),
		model.WithCredential("OPENROUTER_API_KEY"),
		model.WithRouting("deepseek/deepseek-v4.1-flash", model.Routing{
			Only:           []string{"inference-net/fp8"},
			AllowFallbacks: model.Bool(false),
		}),
		model.WithReasoning("deepseek/deepseek-v4.1-flash", model.Reasoning{Effort: "high"}),
	)); err != nil {
		t.Fatal(err)
	}
	if err := n.assemble(); err != nil {
		t.Fatal(err)
	}

	// The default model: options present.
	sel, err := n.resolveFor(&thought.Thought{ID: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if sel.options.Routing == nil || sel.options.Reasoning == nil {
		t.Fatalf("options did not travel with the selection: %+v", sel.options)
	}
	if len(sel.options.Routing.Only) != 1 || sel.options.Routing.Only[0] != "inference-net/fp8" {
		t.Errorf("routing = %+v", sel.options.Routing)
	}
	if sel.options.Reasoning.Effort != "high" {
		t.Errorf("effort = %q", sel.options.Reasoning.Effort)
	}

	// A per-thought override to a model with no declaration carries NO
	// options — not the default model's routing, which would be routing a
	// model to an upstream that does not serve it.
	over, err := n.resolveFor(&thought.Thought{
		ID: "t2", Provider: "openrouter", Model: "anthropic/claude-sonnet-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if over.options.Routing != nil || over.options.Reasoning != nil {
		t.Fatalf("an undeclared model must carry no options, got %+v", over.options)
	}
}
