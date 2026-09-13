package llm

import (
	"context"
	"fmt"
	"sync"
)

// MockProvider is a scripted Provider implementation for tests and for
// #189 fallback-chain testing: each Stream/Complete call consumes the
// next scripted response.
type MockProvider struct {
	mu         sync.Mutex
	responses  []*Response
	streams    [][]StreamEvent
	streamErrs []error
	requests   [][]Message
	calls      int
	pingErr    error
}

// NewMockProvider builds a mock scripting the given streamed responses.
func NewMockProvider(streams ...[]StreamEvent) *MockProvider {
	return &MockProvider{streams: streams}
}

// WithResponses scripts non-streaming Complete responses.
func (m *MockProvider) WithResponses(responses ...*Response) *MockProvider {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responses = responses
	return m
}

// WithStreamError scripts a Stream call failure.
func (m *MockProvider) WithStreamError(err error) *MockProvider {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.streamErrs = append(m.streamErrs, err)
	return m
}

// WithPingError scripts Ping failures.
func (m *MockProvider) WithPingError(err error) *MockProvider {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pingErr = err
	return m
}

// Requests returns the message sequences received so far.
func (m *MockProvider) Requests() [][]Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]Message, len(m.requests))
	copy(out, m.requests)
	return out
}

// Calls returns the number of Stream invocations.
func (m *MockProvider) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func (m *MockProvider) Stream(ctx context.Context, messages []Message, tools []Tool, config Config) (<-chan StreamEvent, error) {
	m.mu.Lock()
	idx := m.calls
	m.calls++
	m.requests = append(m.requests, messages)
	var streamErr error
	if idx < len(m.streamErrs) {
		streamErr = m.streamErrs[idx]
	}
	var resp []StreamEvent
	if idx < len(m.streams) {
		resp = m.streams[idx]
	}
	m.mu.Unlock()

	if streamErr != nil {
		return nil, streamErr
	}

	out := make(chan StreamEvent, len(resp)+1)
	go func() {
		defer close(out)
		for _, ev := range resp {
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (m *MockProvider) Complete(ctx context.Context, messages []Message, tools []Tool, config Config) (*Response, error) {
	m.mu.Lock()
	idx := m.calls
	m.calls++
	m.requests = append(m.requests, messages)
	m.mu.Unlock()

	if idx >= len(m.responses) {
		return nil, fmt.Errorf("llm: mock: no scripted response for call %d", idx)
	}
	return m.responses[idx], nil
}

func (m *MockProvider) Ping(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pingErr
}

func (m *MockProvider) Name() string { return "mock" }

func (m *MockProvider) TokenCount(messages []Message) int {
	return HeuristicTokenCounter{}.CountTokens(messages)
}

func (m *MockProvider) MaxContextWindow(config Config) int { return 4096 }
