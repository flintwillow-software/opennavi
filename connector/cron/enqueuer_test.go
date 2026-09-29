package cron

import (
	"sync"

	"github.com/flintwillow-software/opennavi/thought"
)

// memoryEnqueuer records enqueued thoughts (implements enqueuer).
type memoryEnqueuer struct {
	mu     sync.Mutex
	values []*thought.Thought
}

func (m *memoryEnqueuer) Enqueue(t *thought.Thought) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values = append(m.values, t)
	return nil
}

func (m *memoryEnqueuer) all() []*thought.Thought {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*thought.Thought(nil), m.values...)
}
