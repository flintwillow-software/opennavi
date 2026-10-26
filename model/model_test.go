package model

import "testing"

func TestRegistryRegisterFind(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Provider{Name: "ollama", Model: "qwen3.8", Endpoint: "http://localhost:11434"}); err != nil {
		t.Fatal(err)
	}
	p, ok := r.Find("ollama")
	if !ok || p.Name != "ollama" || p.Model != "qwen3.8" {
		t.Fatalf("find failed: %+v", p)
	}
	if _, ok := r.Find("missing"); ok {
		t.Error("unknown provider found")
	}
	if err := r.Register(Provider{Name: "", Endpoint: "x"}); err == nil {
		t.Error("nameless provider accepted")
	}
	if err := r.Register(Provider{Name: "ollama", Model: "new"}); err != nil {
		t.Fatalf("re-register should replace: %v", err)
	}
	p, _ = r.Find("ollama")
	if p.Model != "new" {
		t.Errorf("replacement not applied")
	}
}

func TestModelDescriptor(t *testing.T) {
	m := Model{Name: "fast", ProviderName: "ollama", Description: "cheap fast model"}
	if m.Name != "fast" || m.ProviderName != "ollama" {
		t.Fatalf("model descriptor wrong: %+v", m)
	}
}

// TestWithRoutingIsKeyedByModel pins ADR 0034 decision 1: request options
// attach to a model id, not to the provider, so a thought that overrides the
// model carries that model's options — or none — rather than the default's.
func TestWithRoutingIsKeyedByModel(t *testing.T) {
	p := OpenRouter("deepseek/deepseek-v4.1-flash",
		WithRouting("deepseek/deepseek-v4.1-flash", Routing{
			Only:           []string{"inference-net/fp8"},
			AllowFallbacks: Bool(false),
		}))

	got := p.OptionsFor("deepseek/deepseek-v4.1-flash")
	if got.Routing == nil {
		t.Fatal("routing not attached to its model")
	}
	if len(got.Routing.Only) != 1 || got.Routing.Only[0] != "inference-net/fp8" {
		t.Errorf("only = %v", got.Routing.Only)
	}
	if got.Routing.AllowFallbacks == nil || *got.Routing.AllowFallbacks {
		t.Error("allow_fallbacks false must survive")
	}

	// A different model gets nothing — this is the whole point of keying by
	// model: a per-thought override must not inherit another model's routing.
	if other := p.OptionsFor("anthropic/claude-sonnet-5"); other.Routing != nil {
		t.Error("a model with no declaration must carry no routing")
	}
}

// TestWithRoutingAndReasoningCompose pins that the two options coexist on one
// model rather than one clobbering the other.
func TestWithRoutingAndReasoningCompose(t *testing.T) {
	p := OpenRouter("m",
		WithRouting("m", Routing{Only: []string{"a"}}),
		WithReasoning("m", Reasoning{Effort: "high"}))

	got := p.OptionsFor("m")
	if got.Routing == nil || got.Reasoning == nil {
		t.Fatalf("both options must survive: %+v", got)
	}
	if got.Reasoning.Effort != "high" {
		t.Errorf("effort = %q, want high", got.Reasoning.Effort)
	}
}

// TestOptionsForWithoutDeclaration pins that a provider declaring no options
// answers the zero value rather than panicking on a nil map.
func TestOptionsForWithoutDeclaration(t *testing.T) {
	p := Ollama("qwen3.8:27b")
	if got := p.OptionsFor("anything"); got.Routing != nil || got.Reasoning != nil {
		t.Errorf("undeclared options = %+v, want the zero value", got)
	}
}
