package llm

import (
	"errors"
	"math"
	"math/rand"
	"net"
	"time"
)

// RetryConfig governs provider retry behaviour (spec §05/§6.2).
type RetryConfig struct {
	MaxRetries    int           `json:"max_retries"`
	InitialDelay  time.Duration `json:"initial_delay"`
	MaxDelay      time.Duration `json:"max_delay"`
	BackoffFactor float64       `json:"backoff_factor"`
	JitterEnabled bool          `json:"jitter_enabled"`
}

// DefaultRetryConfig is the spec's default retry configuration.
var DefaultRetryConfig = RetryConfig{
	MaxRetries:    3,
	InitialDelay:  500 * time.Millisecond,
	MaxDelay:      10 * time.Second,
	BackoffFactor: 2.0,
	JitterEnabled: true,
}

// ShouldRetry reports whether the error is retryable at the given
// attempt index (0-based; attempt >= MaxRetries means exhausted).
// ProviderError classification applies per provider family; network
// timeouts are always retryable (spec §6.2).
func (c *RetryConfig) ShouldRetry(err error, attempt int) bool {
	if err == nil || attempt >= c.MaxRetries {
		return false
	}

	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		// Classify by FAMILY, not the literal provider name — a provider
		// named "primary" still uses OpenAI-style rules.
		switch providerFamily(providerErr.Provider) {
		case "anthropic":
			return IsAnthropicRetryable(providerErr.Code, providerErr.Message)
		default:
			return IsOpenAIRetryable(providerErr.Code)
		}
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}

	return false
}

// Delay computes the wait before the given attempt (exponential
// backoff from InitialDelay by BackoffFactor, capped at MaxDelay, with
// additive jitter up to half the delay when enabled).
func (c *RetryConfig) Delay(attempt int) time.Duration {
	factor := c.BackoffFactor
	if factor <= 0 {
		factor = 1
	}
	delay := float64(c.InitialDelay) * math.Pow(factor, float64(attempt))
	if c.MaxDelay > 0 && delay > float64(c.MaxDelay) {
		delay = float64(c.MaxDelay)
	}
	if c.JitterEnabled {
		jitter := time.Duration(rand.Int63n(int64(delay)/2 + 1))
		return time.Duration(delay) + jitter
	}
	return time.Duration(delay)
}
