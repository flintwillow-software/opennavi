package llm

import (
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestProviderErrorClassification(t *testing.T) {
	// Anthropic rules.
	for _, code := range []int{429, 500, 503} {
		if !IsAnthropicRetryable(code, "") {
			t.Errorf("anthropic %d should be retryable", code)
		}
	}
	if !IsAnthropicRetryable(400, "rate_limit exceeded") {
		t.Error("anthropic rate_limit message should be retryable")
	}
	if IsAnthropicRetryable(400, "bad request") {
		t.Error("anthropic 400 should not be retryable")
	}

	// OpenAI-compatible rules — covers ollama/openrouter/minimax.
	for _, code := range []int{429, 500, 502, 503, 504} {
		if !IsOpenAIRetryable(code) {
			t.Errorf("openai-compatible %d should be retryable", code)
		}
	}
	if IsOpenAIRetryable(400) || IsOpenAIRetryable(401) || IsOpenAIRetryable(404) {
		t.Error("4xx (non-429) should not be retryable")
	}

	// NewProviderError classifies by family.
	pe := NewProviderError("anthropic", 429, "")
	if !pe.IsRetryable {
		t.Fatal("anthropic 429 not marked retryable")
	}
	pe = NewProviderError("ollama", 500, "")
	if !pe.IsRetryable {
		t.Fatal("ollama 500 not marked retryable")
	}
	pe = NewProviderError("ollama", 400, "")
	if pe.IsRetryable {
		t.Fatal("ollama 400 marked retryable")
	}
	if got := pe.Error(); got != "[ollama] 400: " {
		t.Fatalf("Error() = %q", got)
	}
}

func TestIsNetworkTimeout(t *testing.T) {
	// A net.Error with Timeout() == true.
	timeoutErr := &fakeNetTimeout{}
	if !IsNetworkTimeout(timeoutErr) {
		t.Fatal("net timeout should classify as retryable timeout")
	}
	// Plain errors are not timeouts.
	if IsNetworkTimeout(errors.New("nope")) {
		t.Fatal("plain error classified as timeout")
	}
	if IsNetworkTimeout(nil) {
		t.Fatal("nil classified as timeout")
	}
	// Wrapped url.Error (http.Client produces these).
	wrapped := fmt.Errorf("post failed: %w", &fakeURLTimeout{})
	if !IsNetworkTimeout(wrapped) {
		t.Fatal("wrapped url timeout should classify")
	}
}

type fakeNetTimeout struct{}

func (fakeNetTimeout) Error() string   { return "i/o timeout" }
func (fakeNetTimeout) Timeout() bool   { return true }
func (fakeNetTimeout) Temporary() bool { return true }

type fakeURLTimeout struct{}

func (fakeURLTimeout) Error() string   { return "timeout" }
func (fakeURLTimeout) Timeout() bool   { return true }
func (fakeURLTimeout) Temporary() bool { return true }

// satisfy net.Error via the url inner error path: the url.Error wrapper
// requires its Err to expose Timeout through net.Error assertion —
// fakeURLTimeout implements Timeout/Temporary/Error so url.Error's
// Unwrap chain resolves.
var _ net.Error = fakeNetTimeout{}
var _ net.Error = fakeURLTimeout{}

func TestRetryConfigShouldRetry(t *testing.T) {
	cfg := DefaultRetryConfig

	// ProviderError retryable by family.
	ollamaRetryable := NewProviderError("ollama", 503, "")
	if !cfg.ShouldRetry(ollamaRetryable, 0) {
		t.Fatal("503 should retry at attempt 0")
	}
	if cfg.ShouldRetry(ollamaRetryable, cfg.MaxRetries) {
		t.Fatal("exhausted attempts should not retry")
	}
	nonRetryable := NewProviderError("ollama", 400, "")
	if cfg.ShouldRetry(nonRetryable, 0) {
		t.Fatal("400 should not retry")
	}

	// Network timeout retryable.
	if !cfg.ShouldRetry(&fakeNetTimeout{}, 0) {
		t.Fatal("net timeout should retry")
	}

	// nil error never retries.
	if cfg.ShouldRetry(nil, 0) {
		t.Fatal("nil should not retry")
	}
}

func TestRetryConfigDelay(t *testing.T) {
	cfg := RetryConfig{
		MaxRetries:    5,
		InitialDelay:  100 * time.Millisecond,
		MaxDelay:      time.Second,
		BackoffFactor: 2.0,
		JitterEnabled: false,
	}

	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 100 * time.Millisecond},
		{1, 200 * time.Millisecond},
		{2, 400 * time.Millisecond},
		{3, 800 * time.Millisecond},
		{4, 1000 * time.Millisecond}, // capped at MaxDelay
		{5, 1000 * time.Millisecond}, // stays capped
	}
	for _, c := range cases {
		if got := cfg.Delay(c.attempt); got != c.want {
			t.Errorf("Delay(%d) = %v, want %v", c.attempt, got, c.want)
		}
	}

	// Jitter: delay within [base, base*1.5).
	jcfg := cfg
	jcfg.JitterEnabled = true
	for attempt := 0; attempt < 3; attempt++ {
		base := cfg.Delay(attempt)
		for i := 0; i < 20; i++ {
			got := jcfg.Delay(attempt)
			if got < base || got > base+base/2 {
				t.Fatalf("jittered Delay(%d) = %v, outside [%v, %v]", attempt, got, base, base+base/2)
			}
		}
	}
}
