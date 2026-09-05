package llm

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ProviderError classifies a provider failure (spec §05/§6.1).
type ProviderError struct {
	Provider    string
	Code        int // HTTP status or provider-specific code
	Message     string
	IsRetryable bool
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("[%s] %d: %s", e.Provider, e.Code, e.Message)
}

// IsRetryableErr reports whether the error is retryable (sugar for
// errors.As consumers).
func IsRetryableErr(err error) bool {
	var pe *ProviderError
	if errors.As(err, &pe) {
		return pe.IsRetryable
	}
	return false
}

// NewProviderError builds a classified ProviderError, setting
// IsRetryable from the provider family (spec §6.1).
func NewProviderError(provider string, code int, message string) *ProviderError {
	pe := &ProviderError{Provider: provider, Code: code, Message: message}
	switch providerFamily(provider) {
	case "anthropic":
		pe.IsRetryable = IsAnthropicRetryable(code, message)
	default:
		// openai, ollama, openrouter, minimax share OpenAI-style rules
		pe.IsRetryable = IsOpenAIRetryable(code)
	}
	return pe
}

// providerFamily maps a provider name to its error-classification
// family.
func providerFamily(provider string) string {
	switch strings.ToLower(provider) {
	case "anthropic":
		return "anthropic"
	default:
		// openai, ollama, openrouter, minimax: OpenAI-style rules
		return "openai"
	}
}

// IsAnthropicRetryable reports whether an Anthropic error is retryable
// (spec §6.1: 429, 500, 503, or a rate_limit message).
func IsAnthropicRetryable(code int, message string) bool {
	switch code {
	case 429, 500, 503:
		return true
	}
	return strings.Contains(message, "rate_limit")
}

// IsOpenAIRetryable reports whether an OpenAI-compatible error is
// retryable (spec §6.1: 429, 500, 502, 503, 504).
func IsOpenAIRetryable(code int) bool {
	switch code {
	case 429, 500, 502, 503, 504:
		return true
	}
	return false
}

// IsNetworkTimeout reports whether err is a network timeout (always
// retryable per spec §6.2).
func IsNetworkTimeout(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	// url.Error wraps net errors from http.Client calls.
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return IsNetworkTimeout(urlErr.Err)
	}
	return false
}
