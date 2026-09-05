package llm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// countingProvider is a mock that fails N times then succeeds.
type countingProvider struct {
	name      string
	mu        sync.Mutex
	calls     int
	failUntil int // fail while calls <= failUntil
	afterErr  error
}

func (c *countingProvider) Stream(ctx context.Context, messages []Message, tools []Tool, config Config) (<-chan StreamEvent, error) {
	c.mu.Lock()
	c.calls++
	failing := c.calls <= c.failUntil
	c.mu.Unlock()
	if failing {
		return nil, NewProviderError(c.name, 503, "temporarily unavailable")
	}
	out := make(chan StreamEvent, 2)
	out <- StreamEvent{Type: StreamEventContentDelta, Content: "ok from " + c.name}
	out <- StreamEvent{Type: StreamEventStop}
	close(out)
	return out, nil
}

func (c *countingProvider) Complete(ctx context.Context, messages []Message, tools []Tool, config Config) (*Response, error) {
	c.mu.Lock()
	c.calls++
	failing := c.calls <= c.failUntil
	c.mu.Unlock()
	if failing {
		return nil, NewProviderError(c.name, 503, "temporarily unavailable")
	}
	return &Response{Message: Message{Role: RoleAssistant, Content: "ok from " + c.name}}, nil
}

func (c *countingProvider) Ping(ctx context.Context) error     { return nil }
func (c *countingProvider) Name() string                       { return c.name }
func (c *countingProvider) TokenCount(messages []Message) int  { return 0 }
func (c *countingProvider) MaxContextWindow(config Config) int { return 4096 }

func TestFallbackPrimarySucceeds(t *testing.T) {
	primary := &countingProvider{name: "primary"}
	fallback := &countingProvider{name: "fallback"}
	fb, err := NewProviderFallback(primary, fallback, RetryConfig{MaxRetries: 2, InitialDelay: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	events, err := fb.Stream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil, Config{})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(events)
	if primary.calls != 1 || fallback.calls != 0 {
		t.Fatalf("primary=%d fallback=%d, want 1/0", primary.calls, fallback.calls)
	}
}

func TestFallbackAfterRetryableExhaustion(t *testing.T) {
	primary := &countingProvider{name: "primary", failUntil: 99}
	fallback := &countingProvider{name: "fallback"}
	fb, err := NewProviderFallback(primary, fallback, RetryConfig{
		MaxRetries:    2,
		InitialDelay:  time.Millisecond,
		BackoffFactor: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	events, err := fb.Stream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil, Config{})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(events)

	// Primary attempted 3 times (initial + 2 retries), then fallback.
	if primary.calls != 3 {
		t.Fatalf("primary calls = %d, want 3", primary.calls)
	}
	if fallback.calls != 1 {
		t.Fatalf("fallback calls = %d, want 1", fallback.calls)
	}
}

func TestFallbackNonRetryableSwitchesImmediately(t *testing.T) {
	primary := &countingProvider{name: "primary"}
	primary.afterErr = errors.New("bad request")

	// Use a non-retryable provider error on first call.
	p := &failOnceProvider{name: "primary", err: NewProviderError("primary", 400, "bad request")}
	fallback := &countingProvider{name: "fallback"}
	fb, _ := NewProviderFallback(p, fallback, RetryConfig{MaxRetries: 3, InitialDelay: time.Millisecond})

	events, err := fb.Stream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil, Config{})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(events)
	if p.calls != 1 {
		t.Fatalf("primary called %d times, want 1 (non-retryable)", p.calls)
	}
	if fallback.calls != 1 {
		t.Fatalf("fallback called %d times, want 1", fallback.calls)
	}
}

func TestFallbackAllUnavailable(t *testing.T) {
	primary := &failOnceProvider{name: "primary", err: NewProviderError("primary", 503, "down")}
	fallback := &failOnceProvider{name: "fallback", err: NewProviderError("fallback", 503, "down")}
	fb, _ := NewProviderFallback(primary, fallback, RetryConfig{MaxRetries: 1, InitialDelay: time.Millisecond})

	_, err := fb.Stream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil, Config{})
	if err == nil || !strings.Contains(err.Error(), "all providers unavailable") {
		t.Fatalf("error = %v, want wrapped all-unavailable", err)
	}
}

func TestFallbackComplete(t *testing.T) {
	primary := &countingProvider{name: "primary", failUntil: 99}
	fallback := &countingProvider{name: "fallback"}
	fb, _ := NewProviderFallback(primary, fallback, RetryConfig{MaxRetries: 1, InitialDelay: time.Millisecond})

	resp, err := fb.Complete(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "ok from fallback" {
		t.Fatalf("response = %q", resp.Message.Content)
	}
	if primary.calls != 2 || fallback.calls != 1 {
		t.Fatalf("primary=%d fallback=%d", primary.calls, fallback.calls)
	}
}

func TestFallbackRewritesConfigProvider(t *testing.T) {
	// The fallback receives the config with its own provider name.
	var gotProvider string
	fallback := &configCaptureProvider{capture: &gotProvider}
	primary := &failAlwaysProvider{name: "primary"}
	fb, _ := NewProviderFallback(primary, fallback, RetryConfig{MaxRetries: 0})

	_, _ = fb.Stream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil, Config{Provider: "primary"})
	if gotProvider != "capture" {
		t.Fatalf("fallback config.Provider = %q, want capture (the fallback itself)", gotProvider)
	}
}

func TestRegistryGetList(t *testing.T) {
	r := NewProviderRegistry()
	if err := r.Register(&countingProvider{name: "ollama"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(&countingProvider{name: "mock"}); err != nil {
		t.Fatal(err)
	}

	got, err := r.Get("ollama")
	if err != nil || got.Name() != "ollama" {
		t.Fatalf("Get = %v, %v", got, err)
	}
	if _, err := r.Get("missing"); !errors.Is(err, ErrProviderNotFound) {
		t.Fatalf("missing error = %v", err)
	}
	names := r.List()
	if len(names) != 2 || names[0] != "mock" || names[1] != "ollama" {
		t.Fatalf("List = %v, want sorted [mock ollama]", names)
	}
	if err := r.Register(nil); err == nil {
		t.Fatal("nil provider should be rejected")
	}
}

func TestFallbackChainComposes(t *testing.T) {
	// A chain whose fallback is itself a chain: primary -> fb1 -> fb2.
	inner := &failOnceProvider{name: "fb1", err: NewProviderError("fb1", 503, "down")}
	innerChain, _ := NewProviderFallback(inner, &countingProvider{name: "fb2"}, RetryConfig{MaxRetries: 0})
	outer, _ := NewProviderFallback(&failAlwaysProvider{name: "primary"}, innerChain, RetryConfig{MaxRetries: 0})

	resp, err := outer.Complete(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "ok from fb2" {
		t.Fatalf("response = %q", resp.Message.Content)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────

type failOnceProvider struct {
	name  string
	err   error
	calls int
}

func (f *failOnceProvider) Stream(ctx context.Context, messages []Message, tools []Tool, config Config) (<-chan StreamEvent, error) {
	f.calls++
	return nil, f.err
}

func (f *failOnceProvider) Complete(ctx context.Context, messages []Message, tools []Tool, config Config) (*Response, error) {
	f.calls++
	return nil, f.err
}

func (f *failOnceProvider) Ping(ctx context.Context) error     { return f.err }
func (f *failOnceProvider) Name() string                       { return f.name }
func (f *failOnceProvider) TokenCount(messages []Message) int  { return 0 }
func (f *failOnceProvider) MaxContextWindow(config Config) int { return 4096 }

type failAlwaysProvider struct{ name string }

func (f *failAlwaysProvider) Stream(ctx context.Context, messages []Message, tools []Tool, config Config) (<-chan StreamEvent, error) {
	return nil, NewProviderError(f.name, 503, "down")
}

func (f *failAlwaysProvider) Complete(ctx context.Context, messages []Message, tools []Tool, config Config) (*Response, error) {
	return nil, NewProviderError(f.name, 503, "down")
}

func (f *failAlwaysProvider) Ping(ctx context.Context) error {
	return NewProviderError(f.name, 503, "down")
}
func (f *failAlwaysProvider) Name() string                       { return f.name }
func (f *failAlwaysProvider) TokenCount(messages []Message) int  { return 0 }
func (f *failAlwaysProvider) MaxContextWindow(config Config) int { return 4096 }

type configCaptureProvider struct {
	capture *string
	mu      sync.Mutex
}

func (c *configCaptureProvider) Stream(ctx context.Context, messages []Message, tools []Tool, config Config) (<-chan StreamEvent, error) {
	c.mu.Lock()
	*c.capture = config.Provider
	c.mu.Unlock()
	out := make(chan StreamEvent, 1)
	out <- StreamEvent{Type: StreamEventStop}
	close(out)
	return out, nil
}

func (c *configCaptureProvider) Complete(ctx context.Context, messages []Message, tools []Tool, config Config) (*Response, error) {
	return &Response{}, nil
}

func (c *configCaptureProvider) Ping(ctx context.Context) error     { return nil }
func (c *configCaptureProvider) Name() string                       { return "capture" }
func (c *configCaptureProvider) TokenCount(messages []Message) int  { return 0 }
func (c *configCaptureProvider) MaxContextWindow(config Config) int { return 4096 }

func drainEvents(ch <-chan StreamEvent) {
	for range ch {
	}
}
