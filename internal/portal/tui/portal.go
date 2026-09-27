package tui

import (
	"context"
	"errors"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/flintwillow-software/opennavi/internal/session"
	pubportal "github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/thought"
)

// This file makes the TUI a portal component (ADR 0002/0010): a
// portal.Portal that a navi can add to n.Portals. It builds its transport
// from the handles it receives through Deps (NAVI-352) — control via the
// thought-system handle, observation via the bounded subscription — so it
// no longer needs the concrete queue at construction.

// depsTransport is the Transport a TUI portal builds from Deps.
// Transcript replay is disabled here: a session store is not part of the
// component contract (deliberately — sessions are runtime history, not an
// extension surface), so this path renders live only.
type depsTransport struct {
	ts   thought.ThoughtSystem
	feed pubportal.Subscription
}

// NewDepsTransport builds the Deps-backed transport a TUI portal uses.
func NewDepsTransport(deps pubportal.Deps) (*depsTransport, error) {
	if deps.ThoughtSystem == nil {
		return nil, errors.New("tui: thought system is required")
	}
	if deps.Observation == nil {
		return nil, errors.New("tui: observation source is required")
	}
	return &depsTransport{
		ts:   deps.ThoughtSystem,
		feed: deps.Observation.Subscribe(DefaultFeedCap),
	}, nil
}

func (t *depsTransport) Feed() pubportal.Subscription { return t.feed }
func (t *depsTransport) Snapshot() ([]*thought.Thought, error) {
	return t.ts.GetThoughts(), nil
}
func (t *depsTransport) Steer(thoughtID, text string) error {
	return t.ts.RouteMessage(thoughtID, text)
}
func (t *depsTransport) Bump(thoughtID string, prio int) error {
	return t.ts.BumpPriority(thoughtID, prio)
}
func (t *depsTransport) Pause(thoughtID string) error  { return t.ts.Pause(thoughtID) }
func (t *depsTransport) Resume(thoughtID string) error { return t.ts.Resume(thoughtID) }
func (t *depsTransport) Cancel(thoughtID string) error { return t.ts.Cancel(thoughtID) }
func (t *depsTransport) Retry(thoughtID string) error  { return t.ts.Retry(thoughtID) }
func (t *depsTransport) Enqueue(prompt string, prio int, deferUntil time.Time) error {
	now := time.Now().UTC()
	if prio < 1 || prio > 10 {
		prio = 4
	}
	return t.ts.Enqueue(&thought.Thought{
		ID:         newThoughtID(),
		Prompt:     prompt,
		Priority:   prio,
		Status:     thought.ThoughtPending,
		Foreground: true,
		DeferUntil: deferUntil,
		CreatedAt:  now,
		UpdatedAt:  now,
	})
}

// Transcript: no session store from Deps, so no replay on this path.
func (t *depsTransport) Transcript(thoughtID string) (*session.Session, error) {
	return nil, nil
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
// at Start, per the Deps lifecycle rule).
func NewPortal() *Portal { return &Portal{} }

// Start builds the transport from Deps and launches the TUI. The program
// runs until the user quits or Stop is called; its completion is recorded
// on done (exposed for the composition root/tests).
func (p *Portal) Start(ctx context.Context, deps pubportal.Deps) error {
	tr, err := NewDepsTransport(deps)
	if err != nil {
		return err
	}
	model := NewModel(tr)
	prog := tea.NewProgram(model, tea.WithAltScreen(), tea.WithoutSignalHandler())

	p.mu.Lock()
	p.tr = tr
	p.program = prog
	p.done = make(chan error, 1)
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
// is the trusted principal pre-IAM (same stance as LocalAuthorizer).
func (p *Portal) Attach(identity string) error { return nil }

// Steer routes a message to a thought through the transport.
func (p *Portal) Steer(identity, thoughtID, message string) error {
	p.mu.Lock()
	tr := p.tr
	p.mu.Unlock()
	if tr == nil {
		return errors.New("tui: portal not started")
	}
	return tr.Steer(thoughtID, message)
}

// Pause parks a running thought.
func (p *Portal) Pause(identity, thoughtID string) error {
	p.mu.Lock()
	tr := p.tr
	p.mu.Unlock()
	if tr == nil {
		return errors.New("tui: portal not started")
	}
	return tr.Pause(thoughtID)
}

// Resume re-queues a paused thought.
func (p *Portal) Resume(identity, thoughtID string) error {
	p.mu.Lock()
	tr := p.tr
	p.mu.Unlock()
	if tr == nil {
		return errors.New("tui: portal not started")
	}
	return tr.Resume(thoughtID)
}

// Cancel removes queued/paused work.
func (p *Portal) Cancel(identity, thoughtID string) error {
	p.mu.Lock()
	tr := p.tr
	p.mu.Unlock()
	if tr == nil {
		return errors.New("tui: portal not started")
	}
	return tr.Cancel(thoughtID)
}

// BumpPriority changes a queued thought's priority.
func (p *Portal) BumpPriority(identity, thoughtID string, prio int) error {
	p.mu.Lock()
	tr := p.tr
	p.mu.Unlock()
	if tr == nil {
		return errors.New("tui: portal not started")
	}
	return tr.Bump(thoughtID, prio)
}

// Retry re-queues a failed thought.
func (p *Portal) Retry(identity, thoughtID string) error {
	p.mu.Lock()
	tr := p.tr
	p.mu.Unlock()
	if tr == nil {
		return errors.New("tui: portal not started")
	}
	return tr.Retry(thoughtID)
}

// Enqueue schedules a thought.
func (p *Portal) Enqueue(identity string, t *thought.Thought) error {
	p.mu.Lock()
	tr := p.tr
	p.mu.Unlock()
	if tr == nil {
		return errors.New("tui: portal not started")
	}
	return tr.Enqueue(t.Prompt, t.Priority, t.DeferUntil)
}

// Compile-time checks: the Deps transport satisfies Transport and the TUI
// portal satisfies portal.Portal.
var (
	_ Transport        = (*depsTransport)(nil)
	_ pubportal.Portal = (*Portal)(nil)
)
