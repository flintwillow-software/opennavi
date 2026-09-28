package provider

import (
	"errors"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/model"
)

// envMap builds an env lookup over a literal map, so credential paths are
// testable without touching the process environment.
func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolveOllamaNeedsNoCredential(t *testing.T) {
	s := New([]model.Provider{model.Ollama("qwen3.8:27b", model.Default())}, envMap(nil))

	got, err := s.For("ollama")
	if err != nil {
		t.Fatalf("ollama must resolve without a credential: %v", err)
	}
	op, ok := got.(*llm.OllamaProvider)
	if !ok {
		t.Fatalf("want *llm.OllamaProvider, got %T", got)
	}
	if op.Model != "qwen3.8:27b" {
		t.Fatalf("model not carried onto the adapter: %q", op.Model)
	}
	if op.Name() != "ollama" {
		t.Fatalf("adapter name: %q", op.Name())
	}
}

func TestResolveOllamaHonoursExplicitEndpoint(t *testing.T) {
	s := New([]model.Provider{
		model.Ollama("m", model.WithEndpoint("http://example.invalid:9999")),
	}, envMap(map[string]string{"OLLAMA_HOST": "http://ignored.invalid:1"}))

	got, err := s.Resolve(mustFind(t, s, "ollama"))
	if err != nil {
		t.Fatal(err)
	}
	if op := got.(*llm.OllamaProvider); !strings.Contains(op.BaseURL, "example.invalid") {
		t.Fatalf("explicit endpoint must win over OLLAMA_HOST: %q", op.BaseURL)
	}
}

func TestResolveOpenRouterFailsClosedWithoutCredential(t *testing.T) {
	s := New([]model.Provider{
		model.OpenRouter("anthropic/claude-sonnet-4", model.Default()),
	}, envMap(nil))

	_, err := s.For("openrouter")
	if err == nil {
		t.Fatal("a missing credential must be an error, not a silent unauthenticated request")
	}
	if !strings.Contains(err.Error(), string(model.DefaultOpenRouterCredential)) {
		t.Fatalf("error must name the credential reference, got: %v", err)
	}
}

func TestResolveOpenRouterWithCredential(t *testing.T) {
	s := New([]model.Provider{
		model.OpenRouter("anthropic/claude-sonnet-4"),
	}, envMap(map[string]string{"OPENROUTER_API_KEY": "test-key-not-a-secret"}))

	got, err := s.For("openrouter")
	if err != nil {
		t.Fatalf("credential present, want success: %v", err)
	}
	op, ok := got.(*llm.OpenRouterProvider)
	if !ok {
		t.Fatalf("want *llm.OpenRouterProvider, got %T", got)
	}
	if op.APIKey != "test-key-not-a-secret" {
		t.Fatal("credential value not carried onto the adapter")
	}
}

// TestCredentialErrorNeverLeaksTheValue guards the ADR 0008 rule that a
// descriptor is safe to log: the reference may appear, the value may not.
func TestCredentialErrorNeverLeaksTheValue(t *testing.T) {
	const secret = "sk-do-not-leak-me"
	// A descriptor naming a credential that happens to hold nothing —
	// but also assert against a *set* variable used under a different
	// reference, so a future refactor cannot start echoing values.
	s := New([]model.Provider{
		model.OpenRouter("m", model.WithCredential("SOME_OTHER_VAR")),
	}, envMap(map[string]string{"OPENROUTER_API_KEY": secret}))

	_, err := s.For("openrouter")
	if err == nil {
		t.Fatal("want failure")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("credential value leaked into an error: %v", err)
	}
}

func TestUnknownProviderIsLoud(t *testing.T) {
	s := New([]model.Provider{model.Ollama("m", model.Default())}, envMap(nil))

	_, err := s.For("gpt-5")
	if !errors.Is(err, model.ErrUnknownProvider) {
		t.Fatalf("want ErrUnknownProvider, got %v", err)
	}
	// The error should help: it names what IS declared.
	if !strings.Contains(err.Error(), "ollama") {
		t.Fatalf("error should name the declared set: %v", err)
	}
}

func TestFindPrefersProviderNameOverModelName(t *testing.T) {
	s := New([]model.Provider{
		model.Ollama("openrouter", model.Default()), // model literally named "openrouter"
		model.OpenRouter("anthropic/claude-sonnet-4", model.WithCredential("K")),
	}, envMap(map[string]string{"K": "v"}))

	p, ok := s.Find("openrouter")
	if !ok {
		t.Fatal("want a match")
	}
	if p.Name != "openrouter" {
		t.Fatalf("provider name must win over a model name: got %q", p.Name)
	}
}

func TestDefaultIsReported(t *testing.T) {
	s := New([]model.Provider{
		model.OpenRouter("anthropic/claude-sonnet-4"),
		model.Ollama("qwen3.8:27b", model.Default()),
	}, envMap(nil))

	d, ok := s.Default()
	if !ok || d.Name != "ollama" {
		t.Fatalf("want ollama as default, got %+v (ok=%v)", d, ok)
	}
}

func TestResolveIsCached(t *testing.T) {
	calls := 0
	env := func(k string) string {
		if k == "OPENROUTER_API_KEY" {
			calls++
			return "v"
		}
		return ""
	}
	s := New([]model.Provider{model.OpenRouter("m")}, env)

	first, err := s.For("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.For("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("resolution should be cached: got two different adapters")
	}
	if calls != 1 {
		t.Fatalf("credential resolved %d times, want 1", calls)
	}
}

func mustFind(t *testing.T, s *Set, name string) model.Provider {
	t.Helper()
	p, ok := s.Find(name)
	if !ok {
		t.Fatalf("%s not declared", name)
	}
	return p
}
