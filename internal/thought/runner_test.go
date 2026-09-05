package thought

import "context"

// context0 aliases the standard context for Runner implementations in
// tests.
type context0 = context.Context

// RunnerFunc adapts a function onto Runner.
type RunnerFunc func(ctx context0, t *Thought) error

func (f RunnerFunc) Run(ctx context0, t *Thought) error { return f(ctx, t) }
