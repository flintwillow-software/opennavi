package llm

import (
	"context"
	"strings"
	"testing"
)

// TestFallbackRetargetsTheModel pins NAVI-572's second bug: a fallback between
// providers that serve DIFFERENT models must send the fallback its own model.
//
// The chain used to rewrite only the provider, so a navi whose default was
// OpenRouter deepseek-v4.1-flash and whose fallback was Ollama qwen3.8:27b
// asked Ollama for `deepseek/deepseek-v4.1-flash` — a 404, from the very
// configuration the fallback exists to survive.
func TestFallbackRetargetsTheModel(t *testing.T) {
	var got Config
	fallback := &capturingProvider{name: "ollama", model: "qwen3.8:27b", got: &got}
	primary := &capturingProvider{name: "openrouter", model: "deepseek/deepseek-v4.1-flash", fail: true}

	chain, err := NewProviderFallback(primary, fallback, &RetryConfig{MaxRetries: 0})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = chain.Stream(context.Background(), nil, nil, Config{
		Provider:  "openrouter",
		Model:     "deepseek/deepseek-v4.1-flash",
		Routing:   &Routing{Only: []string{"inference-net/fp8"}},
		Reasoning: &Reasoning{Effort: "high"},
	})

	if got.Model != "qwen3.8:27b" {
		t.Fatalf("fallback received model %q, want the fallback's own model", got.Model)
	}
	if got.Provider != "ollama" {
		t.Errorf("fallback provider = %q, want ollama", got.Provider)
	}
	// The primary's per-model options belong to the primary's model
	// (ADR 0034); sending them to a different model's provider is wrong.
	if got.Routing != nil {
		t.Errorf("routing leaked to the fallback: %+v", got.Routing)
	}
	if got.Reasoning != nil {
		t.Errorf("reasoning leaked to the fallback: %+v", got.Reasoning)
	}
}

// TestFallbackKeepsTheModelWhenTheFallbackHasNone pins the same-model case:
// a fallback that declares no model of its own (two endpoints of one service)
// keeps the requested model rather than blanking it.
func TestFallbackKeepsTheModelWhenTheFallbackHasNone(t *testing.T) {
	var got Config
	fallback := &capturingProvider{name: "backup", model: "", got: &got}
	primary := &capturingProvider{name: "openrouter", model: "", fail: true}

	chain, err := NewProviderFallback(primary, fallback, &RetryConfig{MaxRetries: 0})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = chain.Stream(context.Background(), nil, nil, Config{Provider: "openrouter", Model: "some/model"})
	if got.Model != "some/model" {
		t.Fatalf("model = %q, want the requested model kept", got.Model)
	}
}

// TestChainErrorNamesBothHalves pins the observability fix: the primary's
// failure is what explains a chain failure, and reporting only the fallback's
// (as it did) sends a reader to the wrong place. This cost real diagnosis time
// on NAVI-572.
func TestChainErrorNamesBothHalves(t *testing.T) {
	primary := &capturingProvider{name: "openrouter", model: "m", fail: true, failMsg: "primary exploded"}
	fallback := &capturingProvider{name: "ollama", model: "m", fail: true, failMsg: "fallback exploded"}

	chain, err := NewProviderFallback(primary, fallback, &RetryConfig{MaxRetries: 0})
	if err != nil {
		t.Fatal(err)
	}
	_, err = chain.Stream(context.Background(), nil, nil, Config{Provider: "openrouter", Model: "m"})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"primary exploded", "fallback exploded"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err.Error(), want)
		}
	}
}

// capturingProvider records the Config it was called with, and optionally
// fails, so a fallback chain's retargeting is observable.
type capturingProvider struct {
	name    string
	model   string
	got     *Config
	fail    bool
	failMsg string
}

func (c *capturingProvider) Name() string      { return c.name }
func (c *capturingProvider) ModelName() string { return c.model }
func (c *capturingProvider) Ping(context.Context) error {
	return nil
}
func (c *capturingProvider) TokenCount([]Message) int    { return 0 }
func (c *capturingProvider) MaxContextWindow(Config) int { return 0 }
func (c *capturingProvider) Complete(context.Context, []Message, []Tool, Config) (*Response, error) {
	return &Response{}, nil
}
func (c *capturingProvider) Stream(_ context.Context, _ []Message, _ []Tool, config Config) (<-chan StreamEvent, error) {
	if c.got != nil {
		*c.got = config
	}
	if c.fail {
		msg := c.failMsg
		if msg == "" {
			msg = "boom"
		}
		return nil, &ProviderError{Provider: c.name, Code: 404, Message: msg}
	}
	ch := make(chan StreamEvent, 1)
	ch <- StreamEvent{Type: StreamEventStop}
	close(ch)
	return ch, nil
}
