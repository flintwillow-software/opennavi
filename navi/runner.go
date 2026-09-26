package navi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/flintwillow-software/opennavi/llm"
	"github.com/flintwillow-software/opennavi/store"
	"github.com/flintwillow-software/opennavi/thought"
)

// Runner executes one thought to a terminal state. It is a seam: what
// ships here is deliberately minimal, and the full agent runner (tool
// dispatch, steering, checkpointing) replaces it at the catalog port
// without the navi's shape changing.
//
// A Runner returns nil for success and an error for a failed thought —
// the navi records the terminal state, so a Runner never needs to know
// how the queue represents success or failure.
type Runner interface {
	Run(ctx context.Context, t *thought.Thought) error
}

// WithRunner replaces the default runner. Tests inject one; the agent
// port will.
func WithRunner(r Runner) Option {
	return func(n *Navi) {
		if r != nil {
			n.runner = r
		}
	}
}

// WithSessionStore sets where thought transcripts are recorded. Defaults
// to an in-memory store, which means transcripts do not survive the
// process — the durable store arrives with the store port.
func WithSessionStore(s store.Store) Option {
	return func(n *Navi) {
		if s != nil {
			n.sessions = s
		}
	}
}

// WithWorkers sets how many thoughts may execute concurrently. The queue's
// own slot policy is the real limiter; extra workers simply find nothing
// claimable.
func WithWorkers(count int) Option {
	return func(n *Navi) {
		if count > 0 {
			n.workers = count
		}
	}
}

// SessionIDFor returns the session id recording a thought's transcript.
// One session per thought, keyed by the thought id — the same shape the
// daemon uses. Exported because portals and tooling need to find a
// thought's history from its id.
func SessionIDFor(thoughtID string) string { return "thought:" + thoughtID }

// CompletionRunner is the minimal runner that ships with the navi: one
// provider round trip per thought, and no tools.
//
// It exists so the thought system is exercisable against a real model
// before the catalog lands — which is also what makes this navi's
// end-to-end gate meaningful rather than a lifecycle rehearsal. It is
// deliberately NOT a poor imitation of an agent: there is no loop, no
// tool surface, and no steering, because those arrive together at the
// catalog port and guessing at them here would produce something to
// throw away.
type CompletionRunner struct {
	navi *Navi
	log  *slog.Logger
}

// Run resolves the thought's provider, makes one completion call, and
// records the exchange.
func (r *CompletionRunner) Run(ctx context.Context, t *thought.Thought) error {
	provider, modelName, err := r.navi.resolveFor(t)
	if err != nil {
		// An unknown provider fails THIS thought, loudly (ADR 0009).
		return err
	}

	msgs := r.navi.prompt(t)
	resp, err := provider.Complete(ctx, msgs, nil, llm.Config{Model: modelName})
	if err != nil {
		return fmt.Errorf("provider %s: %w", provider.Name(), err)
	}

	reply := ""
	if resp != nil {
		reply = resp.Message.Content
	}
	if reply == "" {
		// Not a failure. Reasoning models legitimately emit only thinking
		// tokens under a small budget, so an empty visible reply is a
		// signal to look, not a broken provider to fail on.
		r.log.Warn("thought produced no visible content",
			"thought", t.ID, "provider", provider.Name(), "model", modelName)
	}
	// The completion message rides the thought, so the queue itself can
	// answer "what did this produce?" without going to the transcript.
	t.Result = reply
	return r.navi.record(t, msgs, reply)
}

// dispatch wakes workers when work may be claimable. It is
// level-triggered through a one-slot channel: a signal that arrives while
// a worker is busy is dropped, but the worker re-drains the queue before
// sleeping, so no wakeup is lost.
type dispatch struct{ wake chan struct{} }

func newDispatch() *dispatch { return &dispatch{wake: make(chan struct{}, 1)} }

func (d *dispatch) signal() {
	select {
	case d.wake <- struct{}{}:
	default: // a wakeup is already pending
	}
}

// startWorkers runs the execution pool until ctx is cancelled.
func (n *Navi) startWorkers(ctx context.Context) *sync.WaitGroup {
	var wg sync.WaitGroup
	for i := 0; i < n.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n.workLoop(ctx)
		}()
	}
	return &wg
}

func (n *Navi) workLoop(ctx context.Context) {
	for {
		// Drain everything claimable before sleeping: one wakeup may
		// cover several thoughts, and Dequeue returns false both for
		// "empty" and "no slot free".
		for {
			t, ok := n.Thoughts.Dequeue()
			if !ok {
				break
			}
			n.runOne(ctx, t)
		}
		select {
		case <-ctx.Done():
			return
		case <-n.dispatch.wake:
		}
	}
}

// runOne executes a thought and records its terminal state.
func (n *Navi) runOne(ctx context.Context, t *thought.Thought) {
	err := n.runner.Run(ctx, t)
	if err == nil {
		if cerr := n.Thoughts.Complete(t.ID, t.Result); cerr != nil {
			n.log.Error("record thought completion", "thought", t.ID, "err", cerr)
		}
		return
	}

	// Cancellation is shutdown, not failure: park the thought so it
	// resumes rather than being marked failed. This is the
	// park-not-kill promise the queue already keeps for the daemon.
	if ctx.Err() != nil {
		if perr := n.Thoughts.Pause(t.ID); perr != nil {
			n.log.Error("park interrupted thought", "thought", t.ID, "err", perr)
		}
		return
	}

	n.log.Error("thought failed", "thought", t.ID, "err", err)
	if ferr := n.Thoughts.Fail(t.ID, err.Error()); ferr != nil {
		n.log.Error("record thought failure", "thought", t.ID, "err", ferr)
	}
}

// prompt builds the conversation for a thought: the navi's personality as
// the system turn, then the thought's prompt with its accumulated context.
func (n *Navi) prompt(t *thought.Thought) []llm.Message {
	var msgs []llm.Message
	if n.personality != "" {
		msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Content: n.personality})
	}
	body := t.Prompt
	if t.Context != "" {
		body += "\n\n" + t.Context
	}
	return append(msgs, llm.Message{Role: llm.RoleUser, Content: body})
}

// record writes the exchange to the thought's session. Sessions record
// history (#240) — what happened — which is why the transcript lives
// here and not on the thought itself: the queue owns scheduling state,
// and a thought's clone is discarded after a run except where the queue
// explicitly persists.
func (n *Navi) record(t *thought.Thought, msgs []llm.Message, reply string) error {
	sess, err := n.sessionFor(t)
	if err != nil {
		return err
	}
	for _, m := range msgs {
		sess.AppendEntry(store.NewEntry(store.Role(m.Role), m.Content))
	}
	sess.AppendEntry(store.NewEntry(store.RoleAssistant, reply))
	return n.sessions.SaveSession(sess)
}

// sessionFor loads the thought's session, creating it on first run.
func (n *Navi) sessionFor(t *thought.Thought) (*store.Session, error) {
	id := SessionIDFor(t.ID)
	sess, err := n.sessions.LoadSession(id)
	if err == nil {
		return sess, nil
	}
	if !errors.Is(err, store.ErrSessionNotFound) {
		return nil, fmt.Errorf("load session for thought %s: %w", t.ID, err)
	}
	sess = store.NewSession(n.name, t.ID)
	sess.ID = id
	return sess, nil
}

// SessionStore returns the store backing thought transcripts.
func (n *Navi) SessionStore() store.Store { return n.sessions }

// watchForWork wakes the execution pool whenever the thought system
// reports activity — enqueue, resume, retry, or a freed slot.
func (n *Navi) watchForWork() {
	n.Thoughts.OnEvent(func(thought.Event) { n.dispatch.signal() })
}
