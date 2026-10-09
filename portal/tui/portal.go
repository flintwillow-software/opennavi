package tui

import (
	"context"
	"errors"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/thought"
)

// This file makes the TUI a portal component (ADR 0002/0010): a
// portal.Portal a navi can add to n.Portals. It builds its transport from
// the handles it receives through Deps (NAVI-352) — control via the
// thought-system handle, observation via the bounded subscription — so it
// no longer needs the concrete queue at construction, and its public API
// depends only on component packages.

// NewDepsTransport builds the Deps-backed transport a TUI portal uses: the
// thought system, the live observation feed, and the typed-object surface
// (ADR 0017), so the TUI renders descriptors rather than a typed handle per
// concern.
func NewDepsTransport(deps portal.Deps) (*inProcessTransport, error) {
	return NewInProcess(deps)
}

// Portal makes the TUI a navi component: Start builds the Deps-backed
// transport and launches the Bubble Tea program; Stop quits it. It
// satisfies portal.Portal so a navi can register the TUI in n.Portals.
type Portal struct {
	mu      sync.Mutex
	tr      Transport
	program *tea.Program
	done    chan error
}

// NewPortal builds the TUI portal component (no handles yet; they arrive
// at Start, per the Deps lifecycle rule). Done is live from construction,
// so a host can cancel its run when the user quits.
func NewPortal() *Portal { return &Portal{done: make(chan error, 1)} }

// Start builds the transport from Deps and launches the TUI. The program
// runs until the user quits or Stop is called; its completion is recorded
// on done (exposed for the composition root/tests).
func (p *Portal) Start(ctx context.Context, deps portal.Deps) error {
	tr, err := NewDepsTransport(deps)
	if err != nil {
		return err
	}
	model := NewModel(tr)
	prog := tea.NewProgram(model, tea.WithAltScreen(), tea.WithoutSignalHandler())

	p.mu.Lock()
	p.tr = tr
	p.program = prog
	if p.done == nil {
		p.done = make(chan error, 1)
	}
	p.mu.Unlock()

	go func() {
		_, rerr := prog.Run()
		p.mu.Lock()
		done := p.done
		p.mu.Unlock()
		if done != nil {
			done <- rerr
		}
	}()
	return nil
}

// Done exposes the program's exit signal (nil-safe before Start).
func (p *Portal) Done() <-chan error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.done
}

// Stop quits the TUI program. Safe before Start and more than once.
func (p *Portal) Stop() error {
	p.mu.Lock()
	prog := p.program
	p.program = nil
	p.mu.Unlock()
	if prog != nil {
		prog.Quit()
	}
	return nil
}

// Attach is a no-op for the local-trust in-process TUI: the console user
// is the trusted principal pre-IAM.
func (p *Portal) Attach(identity string) error { return nil }

func (p *Portal) transport() (Transport, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tr == nil {
		return nil, errors.New("tui: portal not started")
	}
	return p.tr, nil
}

// Steer routes a message to a thought through the transport.
func (p *Portal) Steer(identity, thoughtID, message string) error {
	tr, err := p.transport()
	if err != nil {
		return err
	}
	return tr.Steer(thoughtID, message)
}

// Pause parks a running thought.
func (p *Portal) Pause(identity, thoughtID string) error {
	tr, err := p.transport()
	if err != nil {
		return err
	}
	return tr.Pause(thoughtID)
}

// Resume re-queues a paused thought.
func (p *Portal) Resume(identity, thoughtID string) error {
	tr, err := p.transport()
	if err != nil {
		return err
	}
	return tr.Resume(thoughtID)
}

// Cancel removes queued/paused work.
func (p *Portal) Cancel(identity, thoughtID string) error {
	tr, err := p.transport()
	if err != nil {
		return err
	}
	return tr.Cancel(thoughtID)
}

// BumpPriority changes a queued thought's priority.
func (p *Portal) BumpPriority(identity, thoughtID string, prio int) error {
	tr, err := p.transport()
	if err != nil {
		return err
	}
	return tr.Bump(thoughtID, prio)
}

// Retry re-queues a failed thought.
func (p *Portal) Retry(identity, thoughtID string) error {
	tr, err := p.transport()
	if err != nil {
		return err
	}
	return tr.Retry(thoughtID)
}

// Enqueue schedules a thought.
func (p *Portal) Enqueue(identity string, t *thought.Thought) error {
	tr, err := p.transport()
	if err != nil {
		return err
	}
	return tr.Enqueue(t.Prompt, t.Priority, t.DeferUntil)
}

// Compile-time checks: the in-process transport satisfies Transport and
// the TUI portal satisfies portal.Portal.
var (
	_ Transport     = (*inProcessTransport)(nil)
	_ portal.Portal = (*Portal)(nil)
)
