package agent

import (
	"context"
	"sync"
)

// mockProvider scripts a sequence of stream-event responses, one per
// Stream call, and records incoming requests.
type mockProvider struct {
	mu        sync.Mutex
	responses [][]Event
	requests  [][]Message
	calls     int
	streamErr error
}

func (m *mockProvider) Stream(ctx context.Context, messages []Message, tools []AgentTool, config AgentConfig) (<-chan Event, error) {
	m.mu.Lock()
	idx := m.calls
	m.calls++
	m.requests = append(m.requests, messages)
	err := m.streamErr
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}

	var resp []Event
	if idx < len(m.responses) {
		resp = m.responses[idx]
	}
	out := make(chan Event, len(resp)+1)
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

func (m *mockProvider) Close() error { return nil }

func (m *mockProvider) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func (m *mockProvider) lastRequest() []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.requests) == 0 {
		return nil
	}
	return m.requests[len(m.requests)-1]
}

// blockingProvider returns a channel that emits nothing until ctx is
// cancelled — for Abort testing.
type blockingProvider struct{ entered chan struct{} }

func (b *blockingProvider) Stream(ctx context.Context, _ []Message, _ []AgentTool, _ AgentConfig) (<-chan Event, error) {
	out := make(chan Event)
	close(b.entered)
	<-ctx.Done()
	close(out)
	return out, nil
}

func (b *blockingProvider) Close() error { return nil }
