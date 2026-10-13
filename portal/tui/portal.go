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
	mu         sync.Mutex
	tr         Transport
	program    *tea.Program
	done       chan error
	pumpCancel context.CancelFunc
}

// NewPortal builds the TUI portal component (no handles yet; they arrive
// at Start, per the Deps lifecycle rule). Done is live from construction,
// so a host can cancel its run when the user quits.
func NewPortal() *Portal { return &Portal{done: make(chan error, 1)} }

// RunAttached runs the TUI against a remote navi's portal gateway until the
// user quits. It is the whole client: no thought system, model, or datastore,
// because everything it renders and every action it posts goes through the
// remote object surface and observation stream (ADR 0017) — which is what a
// remote front-end is.
func RunAttached(ctx context.Context, base string, creds portal.CredentialSource) error {
	tr, err := NewRemote(base, creds)
	if err != nil {
		return err
	}
	defer tr.Close()
	prog := tea.NewProgram(NewModel(tr),
		tea.WithAltScreen(), tea.WithoutSignalHandler(), tea.WithContext(ctx))
	pumpCtx, pumpCancel := context.WithCancel(ctx)
	defer pumpCancel()
	// The pump pushes mailbox batches into the program; the program's Send
	// unblocks on its context, so the goroutine ends when the TUI exits.
	go Pump(pumpCtx, tr.Feed(), func(msg tea.Msg) { prog.Send(msg) })
	_, err = prog.Run()
	return err
}

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

	pumpCtx, pumpCancel := context.WithCancel(context.Background())

	p.mu.Lock()
	p.tr = tr
	p.program = prog
	p.pumpCancel = pumpCancel
	if p.done == nil {
		p.done = make(chan error, 1)
	}
	p.mu.Unlock()

	// The feed is pushed into the program from its own goroutine, woken by
	// the mailbox rather than a timer (ADR 0019 decision 5). It is never the
	// queue's emitting goroutine, and a slow UI only loses its own events.
	go Pump(pumpCtx, tr.Feed(), func(msg tea.Msg) { prog.Send(msg) })

	go func() {
		_, rerr := prog.Run()
		pumpCancel()
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
	cancel := p.pumpCancel
	p.program = nil
	p.pumpCancel = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if prog != nil {
		prog.Quit()
	}
	return nil
}

// Attach is a no-op for the in-process console: it IS the binary, with no
// transport, so it is trusted structurally rather than by a credential (ADR
// 0021 decision 9). Its actions still carry the console principal the navi
// supplied through Deps, so the record names an actor.
func (p *Portal) Attach(portal.Actor) error { return nil }

func (p *Portal) transport() (Transport, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tr == nil {
		return nil, errors.New("tui: portal not started")
	}
	return p.tr, nil
}

// act dispatches a declared action on a thought through the object surface:
// the one write path (ADR 0019 decision 7). These portal methods exist to
// satisfy portal.Portal; they are deliberately not a second way to reach the
// queue.
func (p *Portal) act(thoughtID, action string, args map[string]any) error {
	tr, err := p.transport()
	if err != nil {
		return err
	}
	return tr.Objects().Act(objectThought, thoughtID, action, args)
}

// Steer routes a message to a thought.
func (p *Portal) Steer(_ portal.Actor, thoughtID, message string) error {
	return p.act(thoughtID, "steer", map[string]any{"message": message})
}

// Pause parks a running thought.
func (p *Portal) Pause(_ portal.Actor, thoughtID string) error {
	return p.act(thoughtID, "pause", nil)
}

// Resume re-queues a paused thought.
func (p *Portal) Resume(_ portal.Actor, thoughtID string) error {
	return p.act(thoughtID, "resume", nil)
}

// Cancel removes queued/paused work.
func (p *Portal) Cancel(_ portal.Actor, thoughtID string) error {
	return p.act(thoughtID, "cancel", nil)
}

// BumpPriority changes a queued thought's priority.
func (p *Portal) BumpPriority(_ portal.Actor, thoughtID string, prio int) error {
	return p.act(thoughtID, "bump", map[string]any{"priority": prio})
}

// Retry re-queues a failed thought.
func (p *Portal) Retry(_ portal.Actor, thoughtID string) error {
	return p.act(thoughtID, "retry", nil)
}

// Enqueue schedules a thought.
func (p *Portal) Enqueue(_ portal.Actor, t *thought.Thought) error {
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
