package navi

import (
	"context"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/model"
)

// TestModelListDiscoversDeclaredModels pins ADR 0009 discovery: a navi
// that declares providers gains the model_list tool, it is active, and it
// reports the set with the default marked and credentials as REFERENCES
// only (never values).
func TestModelListDiscoversDeclaredModels(t *testing.T) {
	clearProviderEnv(t)
	withFakeOpenRouterCredential(t)

	n := New("models", WithWorkers(1))
	if err := n.Models.Add(model.Ollama("qwen3.8:27b", model.Default())); err != nil {
		t.Fatal(err)
	}
	if err := n.Models.Add(model.OpenRouter("anthropic/claude-sonnet-4",
		model.WithCredential(model.DefaultOpenRouterCredential))); err != nil {
		t.Fatal(err)
	}
	if err := n.assemble(); err != nil {
		t.Fatal(err)
	}

	active := n.registry.ActiveNames()
	if len(active) != 1 || active[0] != "model_list" {
		t.Fatalf("active set = %v, want [model_list]", active)
	}

	out, err := n.registry.Dispatch(context.Background(), "model_list", []byte(`{}`))
	if err != nil {
		t.Fatalf("dispatch model_list: %v", err)
	}
	for _, want := range []string{"ollama", "qwen3.8:27b", "openrouter", "anthropic/claude-sonnet-4", string(model.DefaultOpenRouterCredential)} {
		if !strings.Contains(out, want) {
			t.Fatalf("model_list output missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, `"default":"ollama"`) {
		t.Fatalf("default provider not marked in output:\n%s", out)
	}
}

// TestModelListAbsentWithoutModels keeps the catalog parity gate honest:
// the discovery tool appears only when there is a set to discover, so a
// model-less navi's surface is exactly its declared tools.
func TestModelListAbsentWithoutModels(t *testing.T) {
	n := New("no-models", WithProvider(llm.NewMockProvider()), WithWorkers(1))
	if err := n.assemble(); err != nil {
		t.Fatal(err)
	}
	if _, ok := n.registry.Get("model_list"); ok {
		t.Fatal("model_list must not be registered when no models are declared")
	}
}
