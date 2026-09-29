package llm

import (
	"context"
	"fmt"
	"time"
)

// ProviderFallback implements Provider over a primary/fallback pair
// attempt the primary, retry retryable failures per the
// RetryConfig, then switch to the fallback with the config's provider
// rewritten. A ProviderFallback is itself a Provider, so chains
// compose (a fallback can have its own fallback).
type ProviderFallback struct {
	primary  Provider
	fallback Provider
	// retry is nil = DefaultRetryConfig; non-nil is honoured verbatim
	// (MaxRetries: 0 explicitly means never retry).
	retry *RetryConfig
}

// NewProviderFallback builds a fallback chain. The retry config
// defaults to DefaultRetryConfig when zero-valued.
func NewProviderFallback(primary, fallback Provider, retry *RetryConfig) (*ProviderFallback, error) {
	if primary == nil {
		return nil, fmt.Errorf("llm: fallback chain requires a primary provider")
	}
	if retry == nil {
		r := &DefaultRetryConfig
		return &ProviderFallback{primary: primary, fallback: fallback, retry: r}, nil
	}
	return &ProviderFallback{primary: primary, fallback: fallback, retry: retry}, nil
}

// Name implements Provider: reports the primary's name (the chain is
// addressed by its primary).
func (f *ProviderFallback) Name() string { return f.primary.Name() }

// Ping checks the primary first, then the fallback.
func (f *ProviderFallback) Ping(ctx context.Context) error {
	if err := f.primary.Ping(ctx); err != nil {
		if f.fallback != nil {
			return f.fallback.Ping(ctx)
		}
		return err
	}
	return nil
}

// TokenCount delegates to the primary (estimates are provider-family
// independent at this layer).
func (f *ProviderFallback) TokenCount(messages []Message) int {
	return f.primary.TokenCount(messages)
}

// MaxContextWindow returns the primary's window (the fallback may be
// smaller; budget checks should use the minimum of the chain — TODO
// when multi-provider budgets are wired).
func (f *ProviderFallback) MaxContextWindow(config Config) int {
	return f.primary.MaxContextWindow(config)
}

// Stream attempts the primary (with retries per RetryConfig), then
// falls back. Note: retrying covers connection-time
// failures only; a stream that already emitted events is not replayed
// (that would duplicate partial output) — documented limitation.
func (f *ProviderFallback) Stream(ctx context.Context, messages []Message, tools []Tool, config Config) (<-chan StreamEvent, error) {
	events, err := f.withRetry(ctx, "stream", func() (<-chan StreamEvent, error) {
		return f.primary.Stream(ctx, messages, tools, config)
	})
	if err == nil {
		return events, nil
	}

	if f.fallback != nil {
		fallbackConfig := config
		fallbackConfig.Provider = f.fallback.Name()
		events, fbErr := f.fallback.Stream(ctx, messages, tools, fallbackConfig)
		if fbErr != nil {
			return nil, fmt.Errorf("llm: all providers unavailable: %w", fbErr)
		}
		return events, nil
	}
	return nil, fmt.Errorf("llm: all providers unavailable: %w", err)
}

// Complete attempts the primary (with retries), then falls back.
func (f *ProviderFallback) Complete(ctx context.Context, messages []Message, tools []Tool, config Config) (*Response, error) {
	resp, err := f.withRetryComplete(ctx, func() (*Response, error) {
		return f.primary.Complete(ctx, messages, tools, config)
	})
	if err == nil {
		return resp, nil
	}

	if f.fallback != nil {
		fallbackConfig := config
		fallbackConfig.Provider = f.fallback.Name()
		return f.fallback.Complete(ctx, messages, tools, fallbackConfig)
	}
	return nil, fmt.Errorf("llm: all providers unavailable: %w", err)
}

// withRetry runs fn against the primary, retrying while the error is
// retryable and attempts remain.
func (f *ProviderFallback) withRetry(ctx context.Context, op string, fn func() (<-chan StreamEvent, error)) (<-chan StreamEvent, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		events, err := fn()
		if err == nil {
			return events, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !f.retry.ShouldRetry(err, attempt) {
			return nil, lastErr
		}
		select {
		case <-time.After(f.retry.Delay(attempt)):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (f *ProviderFallback) withRetryComplete(ctx context.Context, fn func() (*Response, error)) (*Response, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		resp, err := fn()
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !f.retry.ShouldRetry(err, attempt) {
			return nil, lastErr
		}
		select {
		case <-time.After(f.retry.Delay(attempt)):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// compile-time check: a fallback chain is a Provider.
var _ Provider = (*ProviderFallback)(nil)
