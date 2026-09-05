package llm

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
)

// Live tests against the real OpenRouter API. Skipped unless
// OPENROUTER_API_KEY is set — CI and local runs without the key stay
// green. Uses the cost-safe z-ai/glm-5.3-flash model (~$0.075/M input).
func liveKey(t *testing.T) string {
	t.Helper()
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		t.Skip("OPENROUTER_API_KEY not set — skipping live OpenRouter tests")
	}
	return key
}

func liveModel() string { return "z-ai/glm-5.3-flash" }

func TestLiveOpenRouterStream(t *testing.T) {
	key := liveKey(t)
	p := NewOpenRouterProvider()
	p.APIKey = key

	// glm-5.3-flash is reasoning-capable: thinking tokens can consume a
	// small budget before visible text, so only assert stream
	// well-formedness, not content length.
	ch, err := p.Stream(context.Background(),
		[]Message{{Role: RoleUser, Content: "Reply with exactly one word: pong"}},
		nil, Config{Model: liveModel(), MaxTokens: 300})
	if err != nil {
		t.Fatal(err)
	}
	var content strings.Builder
	sawStop := false
	for ev := range ch {
		switch ev.Type {
		case StreamEventContentDelta:
			content.WriteString(ev.Content)
		case StreamEventStop:
			sawStop = true
		case StreamEventError:
			t.Fatalf("live stream error: %v", ev.Error)
		}
	}
	if !sawStop {
		t.Fatal("live stream missing stop event")
	}
	t.Logf("live content: %q", strings.TrimSpace(content.String()))
}

func TestLiveOpenRouterToolCall(t *testing.T) {
	key := liveKey(t)
	p := NewOpenRouterProvider()
	p.APIKey = key

	tools := []Tool{{
		Name:        "get_color",
		Description: "Returns the name of a color. Call this when asked about colors.",
		Parameters:  []byte(`{"type":"object","properties":{},"required":[]}`),
	}}
	ch, err := p.Stream(context.Background(),
		[]Message{{Role: RoleUser, Content: "Call the get_color tool now."}},
		tools, Config{Model: liveModel(), MaxTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	sawToolStart, sawToolEnd := false, false
	for ev := range ch {
		switch ev.Type {
		case StreamEventToolCallStart:
			sawToolStart = true
		case StreamEventToolCallEnd:
			sawToolEnd = true
			if ev.ToolCall == nil || ev.ToolCall.ID == "" || ev.ToolCall.Name != "get_color" {
				t.Fatalf("tool call end = %+v", ev.ToolCall)
			}
		case StreamEventError:
			t.Fatalf("live stream error: %v", ev.Error)
		}
	}
	if !sawToolStart || !sawToolEnd {
		t.Fatalf("tool call events missing: start=%v end=%v", sawToolStart, sawToolEnd)
	}
}

func TestLiveOpenRouterNon200Shape(t *testing.T) {
	key := liveKey(t)
	p := NewOpenRouterProvider()
	p.APIKey = key

	// Non-200 shape: invalid model → ProviderError with code.
	_, err := p.Stream(context.Background(), nil, nil,
		Config{Model: "no/such/model-xyz", APIKey: key, MaxTokens: 5})
	if err == nil {
		t.Fatal("expected error for invalid model")
	}
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("error = %v, want ProviderError", err)
	}
}

func TestLiveOpenRouterFallbackSmoke(t *testing.T) {
	key := liveKey(t)
	// Primary fails immediately (bad endpoint); fallback is the real
	// OpenRouter — first real-provider exercise for the chain.
	primary := &failAlwaysProvider{name: "mock-primary"}
	realOpenRouter := &OpenRouterProvider{BaseURL: DefaultOpenRouterURL, APIKey: key, client: http.DefaultClient}
	fb, err := NewProviderFallback(primary, realOpenRouter, &RetryConfig{MaxRetries: 0})
	if err != nil {
		t.Fatal(err)
	}

	// Smoke: the chain must produce a well-formed stream from the real
	// fallback. Content length is not asserted strictly - reasoning-
	// capable models can emit thinking tokens that eat a small budget
	// before visible text.
	ch, ferr := fb.Stream(context.Background(),
		[]Message{{Role: RoleUser, Content: "Reply with exactly one word: pong"}},
		nil, Config{Model: liveModel(), MaxTokens: 300})
	if ferr != nil {
		t.Fatal(ferr)
	}
	var content strings.Builder
	sawStop := false
	for ev := range ch {
		switch ev.Type {
		case StreamEventContentDelta:
			content.WriteString(ev.Content)
		case StreamEventStop:
			sawStop = true
		case StreamEventError:
			t.Fatalf("fallback stream error: %v", ev.Error)
		}
	}
	if !sawStop {
		t.Fatal("fallback stream missing stop event")
	}
	t.Logf("fallback content: %q", strings.TrimSpace(content.String()))
}

// hasProviderError reports whether the error chain contains a
// *ProviderError (stdlib errors.As wrapper for the live tests).
func hasProviderError(err error) bool {
	var pe *ProviderError
	return errors.As(err, &pe)
}
