// Package tui implements the Bubble Tea portal client: the
// human door into the house — a terminal UI bound to the navi's
// thought queue through the portal surface (NAVI-318). The same model
// runs in-process (binary run, NAVI-320) or against the gateway
// attachment; the transport sits behind one seam. The queue is only
// ever touched through the portal — never directly under q.mu — and
// the feed mailbox is drained by a push pump (Pump) woken by the
// mailbox itself, so a slow UI never blocks the queue fanout and an
// idle portal is never blind (ADR 0019 decision 5).
//
// This package uses the Charm stack: bubbletea (the runtime),
// lipgloss (styling, layout, rune-width measurement), and bubbles
// (viewport, textarea, help). Bubble Tea was the repo's first
// third-party dependency, an explicitly approved, documented exception
// scoped to NAVI-319 (case-study thought-loop, Open Design Point #4);
// ADR 0038 decision 1 supersedes that "Bubble Tea only" scoping, so the
// exception is now to the Charm stack as a whole — still confined to
// this package. The no-new-deps convention holds everywhere else.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/portal/gateway"
	"github.com/flintwillow-software/opennavi/portal/view"
	"github.com/flintwillow-software/opennavi/thought"
)

// DefaultFeedCap bounds the TUI's subscription mailbox (bounded
// drop-oldest; ReadAfter reports gaps as a UI notice).
const DefaultFeedCap = 1024

// Transport is the portal attachment seam: it is the unified portal stream
// (ADR 0019) — one stream per portal carrying the observation feed, the
// object surface, and creation. The same model serves in-process and remote
// because both bindings deliver the same contract; control ops return
// result-or-error for immediate UI feedback (stream acks arrive on the feed
// where the portal emits them).
type Transport = portal.Stream

// ── in-process transport ───────────────────────────────────────────────

// inProcessTransport attaches directly to the runtime through the
// component handles (NAVI-352): control through the thought-system
// handle, observation through the bounded subscription. The console user
// is the trusted principal pre-IAM, so control is direct. It is
// live-only: transcript restore applies to every portal and is tracked
// separately (NAVI-353).
type inProcessTransport struct {
	ts      thought.ThoughtSystem
	objects object.Surface
	feed    portal.Subscription
}

// NewInProcess builds the in-process transport from the portal's Deps: the
// thought-system handle for control, the runtime's observation source for the
// live feed, and the typed-object surface for everything the model renders
// (ADR 0017). A navi always exposes its own aggregates, so a TUI hosted by
// one always has a surface.
func NewInProcess(deps portal.Deps) (*inProcessTransport, error) {
	if deps.ThoughtSystem == nil {
		return nil, errors.New("tui: in-process transport requires a thought system")
	}
	if deps.Observation == nil {
		return nil, errors.New("tui: in-process transport requires an observation source")
	}
	if deps.Objects == nil {
		return nil, errors.New("tui: in-process transport requires an object surface")
	}
	return &inProcessTransport{
		ts:      deps.ThoughtSystem,
		objects: deps.Objects,
		feed:    deps.Observation.Subscribe(DefaultFeedCap),
	}, nil
}

func (t *inProcessTransport) Feed() portal.Subscription { return t.feed }

// Objects is the navi's typed-object surface, handed through Deps.
func (t *inProcessTransport) Objects() object.Surface { return t.objects }

// Close releases the feed subscription. Safe more than once.
func (t *inProcessTransport) Close() error {
	t.feed.Close()
	return nil
}

// newThoughtID builds a session-created thought id (pattern: the fg- ids).
func newThoughtID() string {
	return fmt.Sprintf("tui-%d", time.Now().UTC().UnixNano())
}

// Enqueue creates a session thought: foreground-tagged so it claims the
// foreground slot, with optional priority (1-10; out-of-range or
// 0 keeps the default 4) and not-before time.
func (t *inProcessTransport) Enqueue(prompt string, prio int, deferUntil time.Time) error {
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

// ── remote transport (gateway stream attachment) ─────────────────────────

// NewRemote opens the unified stream at base (http://host:port of a gateway
// portal) presenting the credential creds produces, and returns the stream
// client. The client IS the transport: it implements portal.Stream
// (Feed/Objects/Enqueue/Close) and object.Surface, so a remote front-end is
// handed the same contract an in-process one is (ADR 0017/0019). OpenStream
// returns once the opening snapshot has arrived, so descriptors are ready
// before the model renders. A nil source presents no credential — which a
// development gateway accepts and a real one refuses (fail closed).
func NewRemote(base string, creds portal.CredentialSource) (*gateway.Client, error) {
	if base == "" {
		return nil, fmt.Errorf("tui: remote transport requires a base URL")
	}
	c := gateway.NewClient(base, creds)
	// Background, not a timeout: the stream must outlive this call (OpenStream
	// bounds only the opening snapshot).
	if err := c.OpenStream(context.Background()); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// objectThought is the wire name of the thought type the navi exposes
// (navi.TypeThought; the TUI cannot import the composition root).
const objectThought = "thought"

func stringField(obj object.Object, name string) string {
	v, _ := obj.Fields[name].(string)
	return v
}

func stringListField(obj object.Object, name string) []string {
	switch raw := obj.Fields[name].(type) {
	case []string:
		return raw
	case []any:
		out := make([]string, 0, len(raw))
		for _, v := range raw {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// Pump drives a Bubble Tea loop from a portal stream's feed. It runs on its
// own goroutine — never the queue's emitting goroutine — and is woken by the
// mailbox (portal.Waker) rather than a timer, so an idle portal is woken the
// instant an event lands and no cadence is chosen (ADR 0019 decision 5).
//
// A slow UI only loses its own events: send blocks on the program's message
// queue while the bounded, drop-oldest mailbox keeps the producer free, so a
// stuck UI reports a gap instead of stalling the navi. The cursor filter in
// Update keeps a duplicate delivery harmless (NAVI-380).
func Pump(ctx context.Context, feed portal.Subscription, send func(tea.Msg)) {
	cursor := 0
	for {
		wake := notifyChan(feed)
		evs, next, lost := feed.ReadAfter(cursor)
		if next > cursor {
			cursor = next
		}
		if len(evs) > 0 || lost {
			select {
			case <-ctx.Done():
				return
			default:
			}
			send(feedMsg{evs: evs, next: cursor, lost: lost})
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		}
	}
}

// notifyChan returns the feed's push signal. A subscription that cannot wake
// a consumer yields a channel that never fires, so the pump drains once and
// waits (the caller chose a non-waking subscription).
func notifyChan(feed portal.Subscription) <-chan struct{} {
	if wk, ok := feed.(portal.Waker); ok {
		return wk.Notify()
	}
	return nil
}

// ── the model ────────────────────────────────────────────────────────────

// ModeKind selects the rendered screen.
type ModeKind string

// Page is a top-level destination in the tab bar (ADR 0038 decision 4). It is
// coarser than ModeKind: a page is where you ARE, a mode is what the page is
// showing (a list, a pushed detail, a conversation).
type Page string

const (
	// PageHome is the running thought beside the queue. It is the default.
	PageHome Page = "home"
	// PageQueue is the queue as its own page (NAVI-592).
	PageQueue Page = "queue"
	// PageStatus is the navi's health (NAVI-598).
	PageStatus Page = "status"
)

// pages is the tab bar's inventory, in order. It is the ONE list: the tab bar
// renders from it and the number keys select from it, so a page cannot be
// reachable by a key the bar does not show.
var pages = []struct {
	key   string
	page  Page
	label string
}{
	{"1", PageHome, "home"},
	{"2", PageQueue, "queue"},
	{"3", PageStatus, "status"},
}

// pageFor returns the page a number key selects.
func pageFor(key string) (Page, bool) {
	for _, p := range pages {
		if p.key == key {
			return p.page, true
		}
	}
	return "", false
}

const (
	// ModeHome is the default screen (ADR 0038 decision 3): the running
	// thought as a conversation, with a persistent steer input focused on
	// arrival — or, when nothing is running, an empty state that enqueues.
	ModeHome   ModeKind = "home"
	ModeList   ModeKind = "list"
	ModeDetail ModeKind = "detail"
	ModeLive   ModeKind = "live"
	ModeInput  ModeKind = "input"
	// ModeStatus is the navi health page (NAVI-598).
	ModeStatus ModeKind = "status"
)

// badge is the live overlay for one object: state the descriptor carries but
// the event stream updates faster than a surface re-read (status), plus
// derived or transient state the descriptor does not carry at all — a derived
// blocked flag (ADR 0015), a checkpoint marker (sensitive, so absent from the
// served object), and a running preview (NAVI-335). The columns and cells come
// from the toolkit; the badge decorates them.
type badge struct {
	status     string
	blocked    bool
	activity   string
	at         time.Time // last activity time — the render computes the age (ticking clock)
	checkpoint bool      // thought carries a checkpoint (parked/resumable)
}

// feedMsg delivers a drained mailbox batch to the model. next is the
// cursor the batch ends at; Update advances the model's cursor with it
// (NAVI-380 — the pump's goroutine must not touch the model).
type feedMsg struct {
	evs  []portal.Event
	next int
	lost bool
}

// unseen drops the events at or before the consumer cursor. Mailbox
// sequences are assigned in delivery order, so already-applied events
// form a prefix — the filter is allocation-free.
func unseen(evs []portal.Event, cursor int) []portal.Event {
	start := 0
	for start < len(evs) && evs[start].Seq < cursor {
		start++
	}
	return evs[start:]
}

// tuiModel is the Bubble Tea model: pure state + Init/Update/View, so
// all of it is unit-testable without a terminal (the tea program is
// only assembled by Run).
type tuiModel struct {
	transport Transport
	feed      portal.Subscription
	cursor    int
	// objects is the typed-object surface the model renders and acts through
	// (ADR 0017) — the same contract in-process and remote.
	objects object.Surface
	// types is the exposed descriptor set (loaded once the surface answers)
	// and typeIdx selects which one the list renders, so every exposed type is
	// reachable without a per-type screen. typesErr is why the last load
	// failed, so an empty screen can say why.
	types    []object.Type
	typesErr error
	typeIdx  int
	// list is the selected type's page, resolved by the toolkit; items keeps
	// the objects it carried so the live overlay can read fields the declared
	// columns do not include.
	list  view.List
	items map[string]object.Object
	// badges is the live overlay keyed by object id; detail is the open
	// object's descriptor-driven view (ModeDetail).
	badges map[string]badge
	detail *view.Detail
	sel    int
	mode   ModeKind
	liveID string
	// homeID is the thought the HOME screen tracks: the running thought, or
	// the one the reader picked from the running-thoughts overview. It is the
	// focused object on home, and it follows the queue (see trackRunning).
	// It is deliberately separate from liveID: live is a pushed screen you
	// leave, home is where you land.
	homeID string
	// inputFocused is whether the persistent steer/enqueue input owns the
	// keyboard. It is true on arrival (steering is the default gesture, ADR
	// 0038 decision 3) and `esc` on an empty buffer blurs it, so the plain
	// action letters can act on the focused object without leaving the screen.
	// This is a focus state WITHIN a screen, not a screen mode: the defect the
	// revamp removes is actions gated on `m.mode`, not on which widget is
	// focused.
	inputFocused bool
	// actionMenu is the leader (`ctrl+a`) menu: the focused object's declared
	// actions, so an action is reachable even while the input owns the
	// keyboard. Empty when the menu is closed.
	actionMenu []view.Action
	// menuOpen is true while the action menu is showing.
	menuOpen bool
	// chat is the shared conversation component (NAVI-589, ADR 0038
	// decision 5): home's running thought and a thought opened from the queue
	// render through this one implementation, which owns the turns, the
	// scroll viewport, and the per-turn blocks. Presentation-only — the
	// stream stays the source of truth.
	chat chat
	// transcripts accumulates each thought's conversation from the event
	// stream, regardless of which view is open, so a thought enqueued
	// from the list still shows its response when opened after it ran.
	// In-session only; durable restore is NAVI-353.
	transcripts map[string][]turn
	input       string
	// active reports the running thought's inference activity in the
	// home view status line (NAVI-335).
	active   string
	activeID string
	inputID  string
	// enqueue input flow: when inputID is set the input line attaches a
	// message to that thought; otherwise it walks the enqueue prompt
	// stages (0 prompt, 1 priority, 2 defer minutes) and Enqueues.
	enqueue   bool
	stage     int
	enqPrompt string
	enqPrio   int
	enqMin    int
	status    string
	lost      bool

	// width and height are the terminal size, from tea.WindowSizeMsg
	// (NAVI-588). NewModel seeds them with a standard terminal so a model
	// built before the first resize still renders; Bubble Tea then delivers
	// the real size on start and on every resize.
	width  int
	height int
	// theme is the single source of colour and text style (theme.go).
	theme theme
	// page is the TAB the model is on (ADR 0038 decision 4). Modes are the
	// finer-grained screen state WITHIN a page: the queue page's list and a
	// pushed detail both live on different pages.
	page Page
	// keys is the declared keymap: what a key does and what the help
	// advertises are the same data (NAVI-593, ADR 0038 decision 7).
	keys keyMap
	// help renders the footer help from the active bindings, truncating to
	// the terminal rather than overflowing it.
	help help.Model
	// helpOpen is the full-screen help overlay (`?`), which shows every
	// binding in the current context grouped as columns.
	helpOpen bool
	// debug shows the diagnostic chrome (form factor, terminal size) in the
	// header. Off by default: it is information about the renderer, not about
	// the navi, so it belongs behind a toggle rather than in every screen's
	// chrome.
	debug bool
}

// NewModel builds the model over a transport.
func NewModel(tr Transport) *tuiModel {
	m := &tuiModel{
		transport:   tr,
		objects:     tr.Objects(),
		feed:        tr.Feed(),
		items:       make(map[string]object.Object),
		badges:      make(map[string]badge),
		transcripts: make(map[string][]turn),
		mode:        ModeHome,
		page:        PageHome,
		theme:       newTheme(),
		keys:        defaultKeyMap(),
		help:        newHelp(),
		// The input is focused on arrival: steering the running thought is the
		// default gesture (ADR 0038 decision 3).
		inputFocused: true,
		// A model built before the first tea.WindowSizeMsg (a unit test, or
		// the instant before Bubble Tea delivers the real size) renders at a
		// standard terminal rather than at 0×0. The real size arrives as a
		// WindowSizeMsg on start, so this is a floor, not an assumption.
		width:  defaultWidth,
		height: defaultHeight,
	}
	m.loadTypes()
	return m
}

// listPage bounds the page the list renders.
const listPage = 200

// defaultWidth and defaultHeight are the size a model renders at before the
// terminal reports its own (see NewModel). 80×24 is the classic terminal and
// sits in the standard form factor.
const (
	defaultWidth  = 80
	defaultHeight = 24
)

// loadTypes caches the exposed descriptors. A remote surface fetches them
// as its opening snapshot, so a failure is recorded and retried on the next
// feed batch rather than leaving the list empty forever.
func (m *tuiModel) loadTypes() {
	if m.objects == nil {
		return
	}
	types, err := m.objects.Types()
	if err != nil {
		m.typesErr = err
		return
	}
	if len(types) > 0 {
		m.types = types
		m.typesErr = nil
		// Start on a type that can be listed. The first registered type is
		// not necessarily one — a navi may register a collection type
		// (a thought's transcript entries) before the type it belongs to.
		if !listable(m.types[m.typeIdx]) {
			for i := range m.types {
				if listable(m.types[i]) {
					m.typeIdx = i
					break
				}
			}
		}
	}
}

// currentType is the descriptor the list renders.
func (m *tuiModel) currentType() (object.Type, bool) {
	if len(m.types) == 0 || m.typeIdx >= len(m.types) {
		return object.Type{}, false
	}
	return m.types[m.typeIdx], true
}

// refreshList re-reads the selected type's page through the surface and
// resolves it with the toolkit, so the list is descriptor-driven (ADR 0017)
// and identical in-process and remote.
func (m *tuiModel) refreshList() {
	t, ok := m.currentType()
	if !ok || m.objects == nil {
		return
	}
	objs, next, err := m.objects.List(t.Name, object.Query{Limit: listPage})
	if err != nil {
		// A failed read clears the page. Keeping the previous type's rows on
		// screen under the new type's title is the worst of both: the reader
		// sees data that does not belong to what the panel says it is.
		m.list = view.List{}
		m.items = make(map[string]object.Object)
		m.sel = 0
		m.status = "list " + t.Name + ": " + err.Error()
		return
	}
	m.list = view.ListOf(t, objs, next)
	m.items = make(map[string]object.Object, len(objs))
	for _, o := range objs {
		m.items[o.ID] = o
	}
	if m.sel >= len(m.list.Rows) {
		m.sel = len(m.list.Rows) - 1
	}
	if m.sel < 0 {
		m.sel = 0
	}
	m.refreshBlocked()
}

// Init seeds the first screen from the surface. The feed is driven by the
// portal's Pump, not by a timer, so Init arms no command (ADR 0019
// decision 5).
func (m *tuiModel) Init() tea.Cmd {
	m.loadTypes()
	m.refreshList()
	// Seed home from the surface: a thought already running when the TUI
	// opens is what home shows, so the screen is correct on the first frame
	// rather than only after the next lifecycle event.
	m.trackRunning("")
	return nil
}

// Update is the Elm update: keys and feed batches mutate the model.
func (m *tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// The TUI is sized by the terminal, not by an assumed 80 columns
		// (NAVI-588). Bubble Tea delivers this once on start and again on
		// every resize.
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		if m.key(msg) {
			return m, tea.Quit
		}
		return m, nil
	case feedMsg:
		// Apply only events past the consumer cursor, and never move the
		// cursor backwards: a duplicate delivery of an applied range must
		// not re-append its turns (NAVI-380).
		raw := msg.evs
		msg.evs = unseen(msg.evs, m.cursor)
		if len(raw) > 0 && len(msg.evs) == 0 {
			msg.lost = false // a fully-applied duplicate reports no gap
		}
		if msg.next > m.cursor {
			m.cursor = msg.next
		}
		m.applyFeed(msg)
		return m, nil
	}
	return m, nil
}

func (m *tuiModel) View() string { return m.render() }

// refreshBlocked derives the display-only "blocked" state: a pending object
// whose dependency has not completed (ADR 0015). "Blocked" is not a lifecycle
// status, so it is computed from the objects the page carries — the same way
// in-process and remote.
func (m *tuiModel) refreshBlocked() {
	status := make(map[string]string, len(m.list.Rows))
	for _, row := range m.list.Rows {
		status[row.ID] = m.statusOf(row.ID)
	}
	for _, row := range m.list.Rows {
		m.setBadge(row.ID, func(b *badge) {
			b.blocked = false
			if m.statusOf(row.ID) != string(thought.ThoughtPending) {
				return
			}
			for _, dep := range stringListField(m.items[row.ID], "depends_on") {
				if status[dep] != string(thought.ThoughtComplete) {
					b.blocked = true
					return
				}
			}
		})
	}
}

// statusOf reports an object's status: the live overlay when the event stream
// has updated it, else the declared field the surface served.
func (m *tuiModel) statusOf(id string) string {
	if b, ok := m.badges[id]; ok && b.status != "" {
		return b.status
	}
	return stringField(m.items[id], "status")
}

// setBadge merges the live overlay for one object.
func (m *tuiModel) setBadge(id string, mutate func(*badge)) {
	b := m.badges[id]
	mutate(&b)
	m.badges[id] = b
}

func (m *tuiModel) age(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	return fmt.Sprintf("%ds", int(time.Since(t).Seconds()))
}

// applyFeed folds one drained mailbox batch into the model.
func (m *tuiModel) applyFeed(f feedMsg) {
	// A surface may not have answered with its descriptors yet (a stream that
	// opened before the snapshot landed). Retry on each pushed batch until it
	// answers, then read the page once.
	if len(m.types) == 0 {
		m.loadTypes()
		if len(m.types) > 0 {
			m.refreshList()
		} else if m.typesErr != nil {
			// Say why the screen is empty: an unreachable portal and a navi
			// that exposes nothing look identical otherwise.
			m.status = "portal: " + m.typesErr.Error()
		}
	}
	if f.lost {
		m.lost = true
		m.status = "notice: portal stream dropped events (slow UI — gap)"
	}
	for _, ev := range f.evs {
		switch ev.Category {
		case portal.CategoryQueue:
			m.queueEvent(ev)
		case portal.CategoryAck:
			m.ackEvent(ev)
		case portal.CategoryAgent:
			m.agentEvent(ev)
		}
	}
}

func (m *tuiModel) queueEvent(ev portal.Event) {
	if ev.Kind == "stream_lost" {
		m.lost = true
		m.status = ev.Detail
		return
	}
	// An object this run never saw arrived: re-read the page so the row
	// carries its declared fields (the live overlay cannot invent them).
	if _, known := m.items[ev.ThoughtID]; !known {
		m.refreshList()
	}
	m.setBadge(ev.ThoughtID, func(b *badge) {
		b.status = displayStatus(ev.Kind)
		b.at = time.Now().UTC()
	})
	if ev.Kind == "completed" && m.activeID == ev.ThoughtID {
		m.active = ""
		m.activeID = ""
	}
	// A dependency's completion releases its dependents: recompute the
	// derived blocked state from the objects the page carries.
	m.refreshBlocked()
	// Home follows the running thought as the queue changes (ADR 0038
	// decision 3): when the tracked thought finishes, the next one takes its
	// place. This runs on every lifecycle event because the pane re-derives on
	// lifecycle rather than at start-up.
	m.trackRunning(ev.ThoughtID)
	// Lifecycle transitions render as system lines in the live chat
	// window (NAVI-330): started/paused/resumed/completed/failed/...
	if ev.Kind != "enqueued" {
		if m.mode == ModeLive && ev.ThoughtID == m.liveID {
			m.addTurn(turn{role: TurnSystem, text: displayStatus(ev.Kind)})
		} else if m.mode == ModeHome && ev.ThoughtID == m.homeID {
			m.addTurn(turn{role: TurnSystem, text: displayStatus(ev.Kind)})
		}
	}
}

// trackRunning keeps home pointed at the thought worth watching. It is called
// on every lifecycle event, so the tracked thought follows the queue rather
// than being chosen once at start-up — which is what makes home correct in any
// session (`MaxConcurrent` defaults to 2, so "one running" is not a start-up
// property).
//
// The rule has three parts, and the middle one matters as much as the others:
//
//  1. A thought that is still on the board (pending, running, or paused) stays
//     tracked. Pausing the thought you are watching must NOT make it vanish
//     from under you — you paused it in order to resume it, and home is where
//     you do that.
//  2. When the tracked thought reaches a terminal state (complete/failed/
//     cancelled), the next running one takes its place — but if none is
//     running, THE FINISHED CONVERSATION STAYS ON SCREEN. Watching a thought
//     finish and then reading its result is the whole point of home; blanking
//     the transcript at the moment it completes destroys the thing the reader
//     was waiting for. The status suffix says `complete`, and the empty state
//     is reached by starting something new (or by the thought leaving the
//     page).
//  3. Home only falls to its empty state when there is nothing left to show:
//     no running thought, and the tracked one has left the page entirely.
//
// The reader's INPUT BUFFER IS NEVER DISCARDED here — the buffer is their text,
// and dropping it because the queue advanced is the same class of defect as a
// key that does nothing. It re-targets on send instead, and the panel title
// names the current target so the re-target is visible.
func (m *tuiModel) trackRunning(changed string) {
	// A lifecycle change to some OTHER thought cannot move the tracked one
	// unless that thought is now running (it may become the next target) —
	// otherwise there is nothing to re-derive.
	if changed != "" && changed != m.homeID && m.statusOf(changed) != "running" {
		return
	}
	// Still on the board (pending/running/paused): stay on it.
	if m.homeID != "" && m.isOnBoard(m.homeID) {
		return
	}
	next, ok := m.firstRunning()
	if !ok {
		// Nothing is running. Keep a finished conversation on screen (rule 2);
		// only fall to the empty state when the tracked thought is gone from
		// the page, or there was never one.
		if m.homeID != "" && m.isOnPage(m.homeID) {
			return
		}
		m.homeID = ""
		m.chat.reset(nil)
		return
	}
	if next == m.homeID {
		return
	}
	m.homeID = next
	m.loadTranscript(next)
}

// isOnBoard reports whether a thought is still somewhere home should stay
// pointed: not terminal, and not unknown. A thought that has left the page
// entirely is treated as gone.
func (m *tuiModel) isOnBoard(id string) bool {
	if id == "" {
		return false
	}
	if !m.isOnPage(id) {
		return false
	}
	switch m.statusOf(id) {
	case "complete", "failed", "cancelled":
		return false
	}
	return true
}

// isOnPage reports whether a thought is one the page carries, regardless of
// status. It is the difference between "finished" and "gone": a completed
// thought is still readable, and home keeps showing it until the page no longer
// has it (or a new thought starts running).
func (m *tuiModel) isOnPage(id string) bool {
	if id == "" {
		return false
	}
	if _, known := m.items[id]; known {
		return true
	}
	// The transcript cache outlives the page entry: a thought that scrolled
	// off the page can still be read from what the stream accumulated.
	_, cached := m.transcripts[id]
	return cached
}

// firstRunning returns the id of a running thought, in page order. It reads
// the live overlay first (the stream's own view of status) and falls back to
// the declared field, so it works before the overlay has any entry for a
// thought.
func (m *tuiModel) firstRunning() (string, bool) {
	for _, row := range m.list.Rows {
		if m.statusOf(row.ID) == "running" {
			return row.ID, true
		}
	}
	return "", false
}

// displayStatus maps queue event kinds onto friendly thought statuses
// (the seed uses ThoughtStatus vocabulary; the stream delivers event
// kinds).
func displayStatus(kind string) string {
	switch kind {
	case "enqueued", "resumed":
		return "pending"
	case "started":
		return "running"
	case "paused", "preempted":
		return "paused"
	case "completed":
		return "complete"
	case "failed":
		return "failed"
	case "cancelled":
		return "cancelled"
	}
	return kind
}

func (m *tuiModel) ackEvent(ev portal.Event) {
	if ev.Kind == "message_routed" {
		// Steering deliveries render as user turns in the live chat
		// window, at the point they were injected (NAVI-330):
		// context-append and resume-with-message land a user turn; a
		// rejected delivery stays a status/rejection line.
		if ev.Delivery != "" && ((m.mode == ModeLive && ev.ThoughtID == m.liveID) ||
			(m.mode == ModeHome && ev.ThoughtID == m.homeID)) {
			m.addTurn(turn{role: TurnUser, text: "(steered — " + ev.Delivery + ")"})
		}
		if ev.Delivery == "" {
			m.status = fmt.Sprintf("✗ %s: %s", ev.ThoughtID, ev.Detail)
		} else {
			m.status = fmt.Sprintf("✓ delivered to %s (%s)", ev.ThoughtID, ev.Delivery)
		}
		return
	}
	m.status = fmt.Sprintf("%s: %s", ev.ThoughtID, ev.Detail)
}

func (m *tuiModel) agentEvent(ev portal.Event) {
	// Accumulate the conversation for every thought (bounded), not only
	// the live one, so opening a completed thought's live view shows its
	// response (in-session; durable restore is NAVI-353).
	m.chatAgentEvent(ev)
	// List view (NAVI-335): consume agent events for RUNNING thoughts —
	// latest tool call or delta snippet on the row, plus the status
	// activity line. Home's conversation is already fed by chatAgentEvent.
	if m.mode == ModeList {
		m.homeAgentEvent(ev)
	}
}

// chatAgentEvent feeds an agent event into its thought's accumulated
// transcript, and into the rendered turns when that thought is live.
func (m *tuiModel) chatAgentEvent(ev portal.Event) {
	tr := m.transcripts[ev.ThoughtID]
	add := func(t turn) {
		tr = append(tr, t)
		if len(tr) > maxLiveTurns {
			tr = tr[len(tr)-maxLiveTurns:]
		}
	}
	switch ev.Kind {
	case "content_delta":
		// Accumulate deltas into ONE assistant turn.
		if n := len(tr); n > 0 && tr[n-1].role == TurnAssistant {
			tr[n-1].text += ev.Detail
		} else {
			add(turn{role: TurnAssistant, text: ev.Detail})
		}
	case "reasoning_delta", "agent_reasoning_delta":
		// Reasoning content (ADR 0038 decision 9). The upstream event does
		// not exist yet (NAVI-590), so this case is unreachable today — it is
		// here so that landing the event is a change to the provider and the
		// agent rather than to this fold. Accumulates like assistant text.
		if n := len(tr); n > 0 && tr[n-1].role == TurnThinking {
			tr[n-1].text += ev.Detail
		} else {
			add(turn{role: TurnThinking, text: ev.Detail, collapsed: false})
		}
	case "turn_start", "agent_start":
		add(turn{role: TurnSystem, text: ev.Kind})
	case "tool_call":
		add(turn{role: TurnTool, tool: m.clip(ev.Detail, 40), collapsed: true})
	case "tool_result":
		// A result lands on the trailing tool turn when there is one, so a
		// call and its result are ONE block rather than two.
		if n := len(tr); n > 0 && tr[n-1].role == TurnTool && tr[n-1].toolOut == "" {
			tr[n-1].toolOut = ev.Detail
		} else {
			add(turn{role: TurnTool, tool: "result", toolOut: ev.Detail, collapsed: true})
		}
	case "agent_end":
		add(turn{role: TurnSystem, text: "agent ended"})
	case "error":
		add(turn{role: TurnSystem, text: "error: " + m.clip(ev.Detail, 60)})
	default:
		add(turn{role: TurnSystem, text: ev.Kind})
	}
	m.transcripts[ev.ThoughtID] = tr
	// The shared chat renders whichever thought is on screen: the open one in
	// live, the tracked one on home (ADR 0038 decision 5, one renderer).
	if (m.mode == ModeLive && m.liveID == ev.ThoughtID) ||
		(m.mode == ModeHome && m.homeID == ev.ThoughtID) {
		m.chat.reset(tr)
	}
}

// homeAgentEvent updates the running thought's row preview + the
// status activity line from agent events (NAVI-335).
func (m *tuiModel) homeAgentEvent(ev portal.Event) {
	preview := ""
	switch ev.Kind {
	case "content_delta":
		preview = m.clip(ev.Detail, 24)
	case "tool_call":
		preview = "⚙ " + m.clip(ev.Detail, 20)
	case "tool_result":
		preview = "→ " + m.clip(ev.Detail, 20)
	case "agent_end":
		preview = "done"
	default:
		return // lifecycle-ish agent events stay off the preview
	}
	if _, known := m.items[ev.ThoughtID]; known {
		m.setBadge(ev.ThoughtID, func(b *badge) {
			b.activity = preview
			b.at = time.Now().UTC()
		})
	}
	m.activeID = ev.ThoughtID
	m.active = preview
}

// addTurn appends a chat turn to the open conversation, and mirrors it into
// the thought's accumulated transcript so it survives a re-open. The chat
// component owns the scrollback bound.
func (m *tuiModel) addTurn(t turn) {
	m.chat.append(t)
	if m.liveID != "" && m.transcripts != nil {
		m.transcripts[m.liveID] = m.chat.turns
	}
}

// clip shortens s to at most n display columns. It is width-aware, not
// byte-aware, so it never splits a multi-byte rune (the NAVI-588 bug: the
// old byte slice corrupted any clipped CJK or emoji value).
func (m *tuiModel) clip(s string, n int) string {
	return truncate(s, n, "…")
}

// sanitizeLine neutralizes control characters before SINGLE-LINE
// rendering (NAVI-333): an embedded newline/CR/tab in a prompt would
// terminate the row early and fuse the next row onto the same
// physical line. Rune-aware (strings.Map), so UTF-8 content is left
// intact; render-only — the queue/persistence data never changes.
// The live CHAT view may legitimately wrap multi-line assistant
// content, so sanitization is scoped to single-line contexts (queue
// rows, status/input lines, and non-assistant chat lines).
func sanitizeLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// enterLive opens a thought's conversation: the persisted transcript detail,
// read through the object surface and resolved by the toolkit, so a completed
// thought opened after the fact shows its full conversation and the in-process
// and remote paths render the same thing (ADR 0017). Live events accumulate on
// top of the seed.
//
// It is the pushed detail screen; HOME is the default screen that tracks the
// running thought instead (see trackRunning).
func (m *tuiModel) enterLive(thoughtID string) {
	m.liveID = thoughtID
	m.mode = ModeLive
	m.inputFocused = true
	m.loadTranscript(thoughtID)
}

// loadTranscript loads a thought's conversation into the shared chat
// component, using the accumulated stream when there is one and the persisted
// transcript otherwise. It is the one place a conversation is attached to the
// chat, so home and live cannot drift.
func (m *tuiModel) loadTranscript(thoughtID string) {
	if tr := m.transcripts[thoughtID]; len(tr) > 0 {
		// The accumulated stream may have started mid-run: the prompt opens
		// the conversation when nothing else does.
		if tr[0].role != TurnUser {
			tr = append([]turn{{role: TurnUser, text: m.promptOf(thoughtID)}}, tr...)
			m.transcripts[thoughtID] = tr
		}
		m.chat.reset(tr)
		return
	}
	tr := m.seedTurns(thoughtID)
	m.transcripts[thoughtID] = tr
	m.chat.reset(tr)
}

// seedTurns reads a thought's transcript through the surface and maps it to
// turns. A navi with no datastore (or a thought with no persisted history yet)
// yields just the prompt, so an empty transcript still renders something.
func (m *tuiModel) seedTurns(thoughtID string) []turn {
	var out []turn
	if m.objects != nil {
		if t, ok := m.typeByName(objectThought); ok {
			detail := "transcript"
			if v, ok := view.Hint(t, object.ViewChat); ok && v.Detail != "" {
				detail = v.Detail
			}
			chat, err := view.ReadChat(m.objects, t.Name, thoughtID, detail, object.Query{Limit: listPage})
			if err != nil {
				// A failed transcript read is not silent: a completed thought
				// whose conversation will not load is a defect the reader should
				// see rather than a blank panel they must interpret.
				m.status = "transcript " + thoughtID + ": " + err.Error()
			} else {
				out = turnsOf(chat)
			}
		}
	}
	if len(out) == 0 {
		out = append(out, turn{role: TurnUser, text: m.promptOf(thoughtID)})
	}
	return out
}

// turnsOf converts toolkit chat turns into the model's rendering turns.
func turnsOf(chat view.Chat) []turn {
	out := make([]turn, 0, len(chat.Turns))
	for _, t := range chat.Turns {
		// A seeded tool turn starts collapsed, exactly as a live one does: a
		// tool result is machine output that can be thousands of lines, so an
		// opened transcript must not open by dumping all of it. The live path
		// sets this in chatAgentEvent; the two must agree or the same
		// conversation renders differently depending on when it is opened.
		switch {
		case t.Role == "tool" || (t.Tool != "" && t.Output != ""):
			out = append(out, turn{role: TurnTool, tool: t.Tool, toolOut: t.Output, collapsed: true})
		case t.Tool != "" && t.Text == "":
			out = append(out, turn{role: TurnTool, tool: t.Tool, toolIn: t.Input, collapsed: true})
		default:
			role := TurnAssistant
			switch t.Role {
			case "user":
				role = TurnUser
			case "system":
				role = TurnSystem
			}
			out = append(out, turn{role: role, text: t.Text})
		}
	}
	return out
}

// promptOf returns a thought's prompt from the listed page (falling back to the
// id, so a conversation always has an opening turn).
func (m *tuiModel) promptOf(thoughtID string) string {
	if s := stringField(m.items[thoughtID], "prompt"); s != "" {
		return s
	}
	return thoughtID
}

// typeByName finds an exposed descriptor.
func (m *tuiModel) typeByName(name string) (object.Type, bool) {
	for _, t := range m.types {
		if t.Name == name {
			return t, true
		}
	}
	return object.Type{}, false
}

// key handles a key press; returns true when the user quits.
func (m *tuiModel) key(k tea.KeyMsg) bool {
	// The help overlay owns the keyboard while it is open: any key closes it
	// (a reader who opened it wants to get back, not to navigate from it).
	// `q` and ctrl+c still quit, so there is always a way out.
	if m.helpOpen {
		if matchesAny(k, m.keys.Quit) {
			return true
		}
		m.helpOpen = false
		return false
	}
	// `?` opens the overlay — but NOT while the steer input has focus, because
	// `?` is a character people type ("does this work?"), and swallowing it
	// would make the input unable to express a question. This is the same rule
	// the action letters follow: while the input owns the keyboard, printable
	// runes are text. `esc` reaches the blurred state, where `?` opens help.
	if matchesAny(k, m.keys.Help) && !m.inputOwnsKeyboard() {
		m.helpOpen = true
		return false
	}
	// Input mode owns the keyboard: every printable rune goes into the
	// buffer and only explicit control keys act. Command keys (q/n/a/p/s/…)
	// must never fire while typing — a real terminal delivers them one
	// rune per KeyMsg, so the command switch below would otherwise swallow
	// them (and q would quit mid-message).
	if m.mode == ModeInput {
		switch k.String() {
		case "ctrl+c":
			return true
		case "esc", "escape":
			m.mode = ModeList
			m.input = ""
			m.inputID = ""
			m.enqueue = false
			m.stage = 0
		case "enter":
			m.submitInput()
		case "backspace":
			if r := []rune(m.input); len(r) > 0 {
				m.input = string(r[:len(r)-1])
			}
		case "up", "down", "left", "right", "tab":
			// No cursor movement / completion yet.
		default:
			if len(k.Runes) > 0 {
				m.input += string(k.Runes)
			}
		}
		return false
	}

	// The action menu (leader `ctrl+a`) is open: a letter picks one of the
	// focused object's DECLARED actions. It owns the keyboard while open, so
	// the same letter cannot both pick from the menu and type into the input.
	if m.menuOpen {
		return m.menuKey(k)
	}

	// The leader opens the menu for the focused object. `ctrl+a` is not a
	// printable rune, so it is reachable even while the input owns the
	// keyboard — that is what makes an action possible without leaving the
	// steer field. It is checked before the input so it always wins.
	if matchesAny(k, m.keys.Actions) {
		m.openActionMenu()
		return false
	}

	// The conversation screens (home, live) carry a persistent input. While it
	// has focus every printable rune is text: a reader steering a thought
	// writes prose, and swallowing `s` to pause would make the input unusable —
	// the same defect as an advertised key that does nothing, in the other
	// direction. Actions are reached with the leader above, or by blurring the
	// input with `esc` (below) and using the plain letters.
	if m.mode == ModeHome || m.mode == ModeLive {
		return m.conversationKey(k)
	}

	// The dispatch matches against the SAME bindings the help renders, so a key
	// cannot be advertised and handled differently (NAVI-593).
	switch {
	case matchesAny(k, m.keys.Quit):
		return true
	case m.pageKey(k) != "":
		m.gotoPage(m.pageKey(k))
	case matchesAny(k, m.keys.Debug):
		// Toggle the diagnostic header (form factor, terminal size).
		m.debug = !m.debug
	case matchesAny(k, m.keys.Back) && m.mode == ModeDetail:
		m.mode = ModeList
		m.detail = nil
	case matchesAny(k, m.keys.Up):
		if m.mode == ModeList && m.sel > 0 {
			m.sel--
		}
	case matchesAny(k, m.keys.Down):
		if m.mode == ModeList && m.sel < len(m.list.Rows)-1 {
			m.sel++
		}
	case matchesAny(k, m.keys.Open) && m.mode == ModeList:
		if id, ok := m.focused(); ok {
			m.open(id)
		}
	case matchesAny(k, m.keys.Types) && m.mode == ModeList:
		if next, ok := m.nextListableType(); ok {
			m.typeIdx = next
			m.sel = 0
			m.detail = nil
			m.refreshList()
			m.status = "type: " + m.typeLabel(m.typeIdx)
		}
	case matchesAny(k, m.keys.New):
		// `n` opens the enqueue input from ANY page: home is watch-only
		// (NAVI-604), so this is how a new thought is started without a
		// dedicated editor region there.
		m.enqueue = true
		m.inputID = ""
		m.stage = 0
		m.input = ""
		m.mode = ModeInput
		m.inputFocused = true
	case matchesAny(k, m.keys.Attach) && m.mode == ModeList && len(m.list.Rows) > 0:
		m.enqueue = false
		m.inputID = m.list.Rows[m.sel].ID
		m.stage = 0
		m.input = ""
		m.mode = ModeInput
	default:
		// The action letters are bound to the FOCUSED OBJECT in every mode,
		// not to list mode (ADR 0038 decision 7).
		return m.actionKey(k)
	}
	return false
}

// isActionLetter reports whether a key is one of the plain action bindings.
// It is what lets a blurred conversation distinguish "pause this thought"
// (`s`) from "start writing a word containing s" — the action letters are
// reserved while blurred, and any other rune resumes typing.
func (m *tuiModel) isActionLetter(k tea.KeyMsg) bool {
	return matchesAny(k, m.keys.Pause, m.keys.Resume, m.keys.Cancel,
		m.keys.Retry, m.keys.RisePri, m.keys.DropPri)
}

// actionKey dispatches the plain action letters on the focused object. It is
// the shared tail of the keymap: the same letters mean the same thing on the
// queue, in detail, and on a conversation whose input is blurred.
func (m *tuiModel) actionKey(k tea.KeyMsg) bool {
	switch k.String() {
	case "p", "P":
		id, ok := m.focused()
		if !ok {
			return false
		}
		prio := intField(m.items[id].Fields["priority"])
		if k.String() == "p" {
			prio = maxPriority(prio - 1)
		} else {
			prio = minPriority(prio + 1)
		}
		m.doAction("bump", id, prio)
	case "s":
		m.actOnFocused("pause")
	case "r":
		m.actOnFocused("resume")
	case "c":
		m.actOnFocused("cancel")
	case "R":
		m.actOnFocused("retry")
	}
	return false
}

// actOnFocused dispatches a zero-argument declared action on the focused
// object, or says why it did not. The status line carries the reason — an
// inert key with no explanation is the defect this replaces, and the check
// mirrors the menu's guard so the two agree about what applies.
func (m *tuiModel) actOnFocused(action string) {
	id, ok := m.focused()
	if !ok {
		m.status = "nothing focused for " + action
		return
	}
	if !actionApplies(action, m.statusOf(id)) {
		m.status = action + " does not apply to " + id + " (" + m.statusOf(id) + ")"
		return
	}
	m.doAction(action, id, 0)
}

// conversationKey handles a key on a screen with the persistent input (home,
// live). The input has focus by default; `esc` on an empty buffer blurs it, so
// the plain action letters become reachable without leaving the screen.
func (m *tuiModel) conversationKey(k tea.KeyMsg) bool {
	// Quit and the leader always win, focused or not.
	if k.String() == "ctrl+c" || (matchesAny(k, m.keys.Quit) && !m.inputFocused) {
		return true
	}
	// `tab` toggles focus explicitly, for a reader who does not want to clear
	// or leave anything.
	if matchesAny(k, m.keys.Focus) {
		m.inputFocused = !m.inputFocused
		return false
	}
	// `n` starts a thought. It is a LETTER, so it must not fire while the
	// steer input owns the keyboard — typing "focus on the tests" would
	// otherwise switch screens mid-word. On home's empty state there is no
	// editor, so nothing owns the keyboard and `n` works there.
	if matchesAny(k, m.keys.New) && !m.inputOwnsKeyboard() {
		m.enqueue = true
		m.inputID = ""
		m.stage = 0
		m.input = ""
		m.mode = ModeInput
		m.inputFocused = true
		return false
	}
	// `1` is the tab bar's home key (ADR 0038 decision 4). It is not a
	// printable rune, so it works with the input focused, and it is a no-op on
	// home itself.
	if p := m.pageKey(k); p != "" {
		m.gotoPage(p)
		return false
	}

	if !m.inputFocused {
		// Blurred: the scroll/fold keys stay the chat's, the ACTION LETTERS act
		// on the focused object, and any other printable rune re-focuses the
		// input and starts typing — so typing is never a mode you must
		// re-enter. The action letters are checked BEFORE the generic rune
		// case; otherwise every letter would re-focus and type, and the action
		// keys would be unreachable on the very screen this issue is about.
		switch {
		case chatHandles(k):
			m.chat.update(k)
			return false
		case matchesAny(k, m.keys.Back):
			m.leaveConversation()
			return false
		case matchesAny(k, m.keys.Send):
			// Blurred, `enter` OPENS the thought's own page — the same gesture
			// as `enter` on a queue row opening its detail. Focused, it sends
			// (below), so the two meanings never collide.
			if id, ok := m.focused(); ok {
				m.open(id)
			}
			return false
		}
		if m.isActionLetter(k) {
			return m.actionKey(k)
		}
		if len(k.Runes) > 0 {
			m.inputFocused = true
			m.input += string(k.Runes)
			return false
		}
		return false
	}

	// Focused: printable runes are text, and only the keys that can never be
	// text act.
	switch {
	case matchesAny(k, m.keys.Blur):
		// Clear a non-empty buffer first; on an empty buffer `esc` BLURS the
		// input (staying on the screen) rather than leaving, so the action
		// letters are one key away. A second `esc` leaves the conversation.
		if m.input != "" {
			m.input = ""
			return false
		}
		m.inputFocused = false
		return false
	case matchesAny(k, m.keys.Send):
		// Enter submits: steering on a running thought, enqueuing on home's
		// empty state. A newline needs a modifier, which is what a one-line
		// field implies.
		m.submitConversation()
		return false
	case matchesAny(k, m.keys.ClearIn):
		m.input = ""
		return false
	}
	if k.String() == "backspace" {
		if r := []rune(m.input); len(r) > 0 {
			m.input = string(r[:len(r)-1])
		}
		return false
	}
	if chatHandles(k) {
		m.chat.update(k)
		return false
	}
	if len(k.Runes) > 0 {
		m.input += string(k.Runes)
		return false
	}
	return false
}

// inputOwnsKeyboard reports whether a text input currently has focus, in which
// case a printable rune is text and must not be claimed as a command. It is
// the ONE predicate the dispatch consults for that rule, so the help key, the
// action letters, and any future printable binding cannot disagree about it.
func (m *tuiModel) inputOwnsKeyboard() bool {
	if m.mode == ModeInput {
		return true
	}
	if m.mode == ModeHome || m.mode == ModeLive {
		// The editor is only PRESENT where there is something to steer. Home
		// with nothing running shows no box, so nothing owns the keyboard
		// there and `n` (and the action letters) work without blurring first.
		return m.inputFocused && m.editorPresent()
	}
	return false
}

// steerable reports whether a thought can receive a steer right now. It mirrors
// the queue's own rule (only a RUNNING thought has a steering bridge), so the
// editor box is present exactly when submitting would do something.
func (m *tuiModel) steerable(id string) bool {
	return id != "" && m.statusOf(id) == "running"
}

// editorPresent reports whether the current screen actually draws an editor
// box. It is the predicate inputOwnsKeyboard consults, so "an input has focus"
// and "there is an input" cannot disagree — a focused-but-absent editor would
// swallow every printable key on a screen with nowhere to type.
func (m *tuiModel) editorPresent() bool {
	switch m.mode {
	case ModeInput:
		return true
	case ModeHome:
		return m.steerable(m.homeID)
	case ModeLive:
		return m.liveID != ""
	}
	return false
}

// leaveConversation returns from a conversation screen to the queue list,
// which is the navigable index.
func (m *tuiModel) leaveConversation() {
	m.mode = ModeList
	m.detail = nil
	m.inputFocused = true
}

// goHome returns to the home screen. Home is the default screen (ADR 0038
// decision 3) and must be reachable from wherever you are, or the default is
// a place you can only visit once. The tab bar of decision 4 is not built yet
// (NAVI-593), so this is the binding until it is; `1` is the tab bar's own
// home key, so it survives that change.
func (m *tuiModel) goHome() { m.gotoPage(PageHome) }

// gotoPage switches tabs (ADR 0038 decision 4). Each page sets the mode its
// content starts in, so a page is a destination rather than a mode the caller
// has to know the shape of. It is the ONE place a page transition happens, so
// the tab bar, the number keys, and any future palette cannot disagree about
// what a page is.
func (m *tuiModel) gotoPage(p Page) {
	m.page = p
	m.detail = nil
	m.menuOpen = false
	m.actionMenu = nil
	m.helpOpen = false
	m.inputFocused = true
	switch p {
	case PageQueue:
		m.mode = ModeList
		m.inputFocused = false
	case PageStatus:
		m.mode = ModeStatus
		m.inputFocused = false
	default:
		m.mode = ModeHome
		// Re-derive the tracked thought: the queue may have moved while we were
		// elsewhere.
		m.trackRunning("")
	}
}

// submitConversation handles enter on a conversation screen. The input is
// CONTEXTUAL (ADR 0038 decision 3): a steer box when a thought is running, an
// enqueue box when none is — so there is never a steer field with no target.
func (m *tuiModel) submitConversation() {
	if m.homeID != "" || m.mode == ModeLive {
		m.sendSteer()
		return
	}
	// Nothing running: home's empty state enqueues inline, reusing the staged
	// prompt -> priority -> defer flow rather than a second input path.
	m.enqueueStep()
}

// openActionMenu builds the leader menu from the FOCUSED object's declared
// actions. It is descriptor-driven, so it needs no per-type binding and a new
// object type gets the menu for free (ADR 0038 decisions 2 and 7).
func (m *tuiModel) openActionMenu() {
	id, ok := m.focused()
	if !ok {
		m.status = "nothing focused"
		return
	}
	t, ok := m.currentType()
	if !ok {
		return
	}
	actions := m.actionsFor(t, id)
	if len(actions) == 0 {
		m.status = "no actions for " + id
		return
	}
	m.actionMenu = actions
	m.menuOpen = true
	m.status = "actions for " + id
}

// menuKey handles a key while the action menu is open: a letter dispatches the
// matching declared action, esc closes.
func (m *tuiModel) menuKey(k tea.KeyMsg) bool {
	if k.String() == "ctrl+c" {
		return true
	}
	if k.String() == "esc" || k.String() == "escape" {
		m.menuOpen = false
		m.actionMenu = nil
		m.status = ""
		return false
	}
	if len(k.Runes) == 1 {
		letter := string(k.Runes)
		id, ok := m.focused()
		if !ok {
			m.menuOpen = false
			m.actionMenu = nil
			return false
		}
		for _, a := range m.actionMenu {
			if menuLetter(a.Name) == letter {
				m.menuOpen = false
				m.actionMenu = nil
				m.dispatchDeclared(id, a)
				return false
			}
		}
		m.status = "no action on " + letter
		return false
	}
	return false
}

// actionsFor resolves the actions a type grants for one object: the
// descriptor's declared actions, minus the ones whose guard the object fails
// (a completed thought has nothing to pause) and minus the ones that already
// have a dedicated input. The filter is what keeps the menu honest — a menu
// entry that cannot work, or whose advertised key is taken by another entry,
// is the same defect as an advertised key that does nothing.
func (m *tuiModel) actionsFor(t object.Type, id string) []view.Action {
	out := make([]view.Action, 0, len(t.Actions))
	seen := make(map[string]bool, len(t.Actions))
	for _, a := range t.Actions {
		// `steer` is deliberately absent: the persistent input IS its UI, and
		// its natural letter collides with `s` pause.
		if a.Name == "steer" {
			continue
		}
		if !actionApplies(a.Name, m.statusOf(id)) {
			continue
		}
		letter := menuLetter(a.Name)
		if letter == "" || seen[letter] {
			continue // no key left for it: an unreachable entry is a lie
		}
		seen[letter] = true
		out = append(out, view.Action{Name: a.Name, Label: a.Label, Confirm: a.Confirm, Args: a.Args})
	}
	return out
}

// actionApplies reports whether an action is meaningful for a status. It is a
// display-side guard only: the surface still validates every call, so this
// cannot grant an action, only hide one that would be rejected. It mirrors the
// queue's own rules — in particular `cancel` does NOT apply to a running
// thought (the queue refuses it: "pause it before cancelling"), so offering it
// there would advertise a key whose only outcome is an error.
func actionApplies(action, status string) bool {
	switch action {
	case "pause":
		return status == "running"
	case "resume":
		return status == "paused"
	case "cancel":
		return status == "pending" || status == "paused"
	case "retry":
		return status == "failed"
	case "steer":
		return status == "running"
	}
	return true
}

// dispatchDeclared runs a declared action from the menu, supplying the args
// the descriptor carries for it (the bump priority is special-cased because
// the argument is a delta the descriptor cannot express).
func (m *tuiModel) dispatchDeclared(id string, a view.Action) {
	switch a.Name {
	case "bump":
		m.doAction("bump", id, maxPriority(intField(m.items[id].Fields["priority"])-1))
	default:
		m.doAction(a.Name, id, 0)
	}
}

// menuLetter is the key that picks a declared action from the leader menu. It
// matches the plain binding where one exists, so the leader is a way to reach
// the SAME keys rather than a second vocabulary to learn: pause/s, resume/r,
// cancel/c, retry/R (case preserved so `R` retry does not collide with `r`
// resume), bump/p. An unknown action falls back to its first letter, and a
// caller drops any entry whose letter is already taken rather than
// advertising a key that cannot fire.
func menuLetter(name string) string {
	switch name {
	case "pause":
		return "s"
	case "resume":
		return "r"
	case "cancel":
		return "c"
	case "retry":
		return "R"
	case "bump":
		return "p"
	}
	if name == "" {
		return ""
	}
	return strings.ToLower(name[:1])
}

// sendSteer routes the input buffer to the FOCUSED thought through the one
// write path (object.Surface.Act, ADR 0019 decision 7) — the same call the
// list's attach flow uses, so there is no second way to reach the queue. On
// home the target is the tracked thought; in live it is the open one. They are
// the same thought whenever home opened it, and re-targeting on send is what
// keeps a buffer written against one thought from silently steering another
// (see trackRunning).
func (m *tuiModel) sendSteer() {
	msg := strings.TrimSpace(m.input)
	if msg == "" {
		return
	}
	target := m.liveID
	if m.mode == ModeHome {
		target = m.homeID
	}
	if target == "" {
		return
	}
	if err := m.act(target, "steer", map[string]any{"message": msg}); err != nil {
		m.status = "steer failed: " + err.Error()
		return
	}
	// The buffer clears on send, not on the ack: the ack arrives on the stream
	// and lands as a turn, and holding the text until then would let a slow
	// round trip block the next message.
	m.status = "steered " + target
	m.input = ""
}

// submitInput handles enter in input mode: an attach-message flow steers
// the selected thought; otherwise it advances the enqueue stages
// (prompt -> priority -> defer).
func (m *tuiModel) submitInput() {
	if m.inputID != "" {
		if strings.TrimSpace(m.input) != "" {
			if err := m.act(m.inputID, "steer", map[string]any{"message": m.input}); err != nil {
				m.status = "steer failed: " + err.Error()
			} else {
				m.status = fmt.Sprintf("steered %s", m.inputID)
			}
		}
		m.mode = ModeList
		m.input = ""
		m.inputID = ""
		return
	}
	m.enqueueStep()
}

func maxPriority(p int) int {
	if p < 1 {
		return 1
	}
	return p
}

// parsePrio parses a priority 1-10 (empty/invalid keeps the default 4).
func parsePrio(s string) int {
	if n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 32); err == nil && n >= 1 && n <= 10 {
		return int(n)
	}
	return 4
}

// parseMinutes parses a deferral in minutes (empty/invalid = 0).
func parseMinutes(s string) int {
	if n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 32); err == nil && n > 0 {
		return int(n)
	}
	return 0
}

// enqueueStep walks the new-thought input stages (prompt -> priority
// -> defer minutes) and enqueues through the transport; the enqueued
// event comes back on the stream and lands the row in the queue view.
//
// It is shared by the list's staged `n` flow and home's INLINE empty state
// (ADR 0038 decision 3: "an input box to enqueue"), so there is one enqueue
// path rather than two. It returns to the screen it started on: from home it
// stays on home, where the stream then delivers the new thought and the screen
// becomes the steer page.
func (m *tuiModel) enqueueStep() {
	// The flow runs in ModeInput, so the page it STARTED from is remembered
	// rather than inferred from the mode — otherwise an enqueue begun on home
	// would always land on the queue list.
	from := m.page
	finish := func() {
		m.enqueue = false
		m.stage = 0
		m.input = ""
		m.gotoPage(from)
	}
	switch m.stage {
	case 0:
		m.enqPrompt = m.input
		m.input = ""
		if strings.TrimSpace(m.enqPrompt) == "" {
			m.status = "enqueue cancelled: prompt is required"
			finish()
			return
		}
		m.stage = 1
		// Stay in the input, focused, so the next field is typed straight
		// away: the staged flow is one continuous gesture, not three visits to
		// a mode.
		m.inputFocused = true
	case 1:
		m.enqPrio = parsePrio(m.input)
		m.input = ""
		m.stage = 2
		m.inputFocused = true
	case 2:
		m.enqMin = parseMinutes(m.input)
		var notBefore time.Time
		if m.enqMin > 0 {
			notBefore = time.Now().UTC().Add(time.Duration(float64(m.enqMin) * float64(time.Minute)))
		}
		if err := m.transport.Enqueue(m.enqPrompt, m.enqPrio, notBefore); err != nil {
			m.status = "enqueue failed: " + err.Error()
		} else {
			deferNote := ""
			if !notBefore.IsZero() {
				deferNote = " (deferred)"
			}
			m.status = fmt.Sprintf("enqueued at p%d%s", m.enqPrio, deferNote)
		}
		finish()
	}
}
func minPriority(p int) int {
	if p > 10 {
		return 10
	}
	return p
}

// focused returns the id of the object the actions act on — the FOCUSED
// OBJECT, not "the selected row in list mode" (ADR 0038 decision 7). This is
// the fix for the defect the revamp started from: `selected()` returned false
// outside list mode, so every action key was inert on the chat screen where a
// reader most wants to pause or cancel the thought they are watching.
//
// The focused object is, per screen:
//
//	list    — the highlighted row
//	home    — the tracked thought (the running one, or the picked overview row)
//	live    — the open thought (the conversation you are reading)
//	detail  — the open object
//	input   — the object the staged flow attaches to, if any
//
// It is a pure read: the dispatch path (m.act) is unchanged and remains the
// one write path (ADR 0019 decision 7).
func (m *tuiModel) focused() (string, bool) {
	switch m.mode {
	case ModeHome:
		if m.homeID != "" {
			return m.homeID, true
		}
		// Nothing running: home's empty state enqueues rather than acting, so
		// there is no focused object. A stale selection must not leak through.
		return "", false
	case ModeLive:
		if m.liveID != "" {
			return m.liveID, true
		}
		return "", false
	case ModeDetail:
		if m.detail != nil && m.detail.Object.ID != "" {
			return m.detail.Object.ID, true
		}
		return "", false
	case ModeInput:
		if m.inputID != "" {
			return m.inputID, true
		}
		return "", false
	}
	if m.sel < 0 || m.sel >= len(m.list.Rows) {
		return "", false
	}
	return m.list.Rows[m.sel].ID, true
}

// intField reads an int field that may have crossed the wire as a number.
func intField(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// open renders an object: a type that declares a chat view opens the
// conversation (the thought case), anything else opens the sectioned detail
// view. Both come from the descriptor, so a new type needs no new screen.
func (m *tuiModel) open(id string) {
	t, ok := m.currentType()
	if !ok {
		return
	}
	if _, isChat := view.Hint(t, object.ViewChat); isChat {
		m.enterLive(id)
		return
	}
	d, err := view.ReadDetail(m.objects, t.Name, id)
	if err != nil {
		m.status = "detail " + id + ": " + err.Error()
		return
	}
	m.detail = &d
	m.mode = ModeDetail
}

// doAction dispatches a declared action through the object surface (ADR 0017):
// the descriptor decides what a type supports, so the model does not hardcode
// verbs per type, and the same call works in-process and remote.
// act dispatches a declared action on one object of the selected type through
// the surface — the single write path (ADR 0019 decision 7). Creation is the
// only exception (`Enqueue`), because an object that does not exist yet has no
// id to act on.
func (m *tuiModel) act(id, action string, args map[string]any) error {
	t, ok := m.currentType()
	if !ok || m.objects == nil {
		return fmt.Errorf("tui: no object surface")
	}
	return m.objects.Act(t.Name, id, action, args)
}

// doAction dispatches one of the list's action keys. The descriptor decides
// what a type supports, so the model does not hardcode verbs per type, and the
// same call works in-process and remote.
func (m *tuiModel) doAction(action, id string, prio int) {
	args := map[string]any{}
	if action == "bump" {
		args["priority"] = prio
	}
	if err := m.act(id, action, args); err != nil {
		m.status = fmt.Sprintf("%s %s failed: %v", action, id, err)
		return
	}
	m.status = fmt.Sprintf("%s → %s", action, id)
}

// render draws the current screen into the terminal-sized frame (NAVI-588).
// The frame owns the width, the height, and the chrome; a screen returns
// body lines and never emits its own rule or sizes itself.
func (m *tuiModel) render() string {
	f := newFrame(m.width, m.height, m.theme)

	// The HEADER region: where you are on the left, the secondary facts on the
	// right. The facts are the thought's lifecycle and the view's own state
	// (following, trimmed) — the things worth a glance that are not identity.
	f.tabs = m.tabsBar()
	f.title = m.panelLabel()
	f.subtitle = m.headerSubtitle()
	f.subtitleStyle = m.conversationStatusStyle()
	f.hasHeader = true

	// The EDITOR region: present only on screens that take typing. The input
	// is CONTEXTUAL (ADR 0038 decision 3) — a steer box when a thought is
	// running, an enqueue box when none is — so there is never a steer field
	// with no target.
	// The editor region is on the screens that STEER: home (the thought it is
	// tracking, which is the whole point of watching it) and a thought's own
	// page. The label is the ACTION, not a prompt glyph, in the same
	// vocabulary a turn uses.
	if m.mode == ModeHome || m.mode == ModeLive || m.mode == ModeInput {
		switch {
		case m.mode == ModeInput:
			f.inputPrompt = m.enqueueLabel()
		case m.mode == ModeHome && m.homeID == "":
			// Nothing running: home's empty state has no steer target, so the
			// box would be a field with nothing to send to.
			f.inputPrompt = ""
		case m.mode == ModeHome && !m.steerable(m.homeID):
			// Home is still SHOWING a finished thought (its transcript stays
			// on screen, NAVI-587), but a finished thought cannot be steered —
			// so the box would be a field whose only outcome is a refusal.
			f.inputPrompt = ""
		default:
			f.inputPrompt = "steer"
		}
		f.inputFocused = m.inputFocused
		if m.inputFocused {
			f.input = m.input + m.theme.Marker("█")
		} else {
			f.input = m.input
		}
	}

	// The NOTICE region: the one thing worth interrupting for is an incomplete
	// view (a dropped stream). It gets its own line above the status bar
	// rather than being a suffix on the title.
	if m.lost {
		f.notice = "stream gap — the portal dropped events (slow UI); this view may be incomplete"
	}
	// The diagnostic readout is a notice too: it is information about the
	// renderer, not about the navi.
	if m.debug {
		f.notice = f.form().String() + " " + itoa(f.width) + "×" + itoa(f.height)
		if m.lost {
			f.notice = "stream gap · " + f.notice
		}
		f.noticeStyle = m.theme.Subtle
	}

	// The STATUS region: the transient message on the left, the generated help
	// on the right. The running activity line rides on the left when there is
	// one, so the thing a thought is DOING right now is visible without
	// opening it (NAVI-335).
	f.status = m.status
	if m.mode == ModeList && m.active != "" {
		f.status = m.activeID + " · " + m.active
		if m.status != "" {
			f.status += "   " + m.status
		}
	}
	f.help = m.helpText(f)

	// A terminal below the floor gets a notice rather than a corrupted row.
	if f.tooNarrow() {
		f.inputPrompt = ""
		f.notice = "terminal too narrow — need at least " + itoa(narrowFloor) + " columns, have " + itoa(f.width)
		f.noticeStyle = m.theme.Warn
		f.hasHeader = true
		return f.render("", []string{}, "")
	}

	return f.render("", m.bodyLines(f), "")
}

// tabsBar renders the tab bar: the page inventory with the active page marked,
// so where you can GO is always on screen (ADR 0038 decision 4). It reads the
// same `pages` list the number keys select from, so a page cannot be reachable
// by a key the bar does not show.
func (m *tuiModel) tabsBar() string {
	var b strings.Builder
	b.WriteString(" ")
	for i, p := range pages {
		if i > 0 {
			b.WriteString(m.theme.Subtle(" · "))
		}
		label := p.key + " " + p.label
		if p.page == m.page {
			b.WriteString(m.theme.Marker(label))
		} else {
			b.WriteString(m.theme.Subtle(label))
		}
	}
	return b.String()
}

// pageKey returns the page a number key selects, or "" when the key is not a
// page key. It reads the same `pages` list the tab bar renders, so a page is
// never reachable by a key the bar does not show.
func (m *tuiModel) pageKey(k tea.KeyMsg) Page {
	if len(k.Runes) != 1 {
		return ""
	}
	if p, ok := pageFor(string(k.Runes)); ok {
		return p
	}
	return ""
}

// headerSubtitle is the HEADER region's right-hand text: the secondary facts
// about what you are looking at. On a conversation that is the thought's
// lifecycle and the view's own state; on the queue it is the running
// activity, which is the thing a reader glancing at the list wants to know
// (NAVI-335).
func (m *tuiModel) headerSubtitle() string {
	if m.mode == ModeHome || m.mode == ModeLive {
		return m.chatStatus()
	}
	if m.active != "" {
		return m.activeID + " · " + m.active
	}
	return ""
}

// panelLabel is the label on the panel's top edge: the screen and the object
// it is showing, so the frame says where you are without a separate header.
func (m *tuiModel) panelLabel() string {
	switch m.mode {
	case ModeHome:
		if m.homeID == "" {
			return "home · nothing running"
		}
		return "home · " + m.homeID
	case ModeLive:
		return "chat · " + m.liveID
	case ModeDetail:
		if m.detail != nil {
			return "detail · " + m.detail.Title
		}
		return "detail"
	case ModeInput:
		return "input"
	case ModeStatus:
		return "status"
	}
	if m.page == PageQueue {
		return "queue"
	}
	return m.typeLabel(m.typeIdx)
}

// typeLabel is a type's name for a LIST context. The descriptor's own Label
// is singular — it names one object in a detail view ("Thought") — while a
// list view carries its own plural label ("Thoughts"). Using the singular for
// a list reads wrong ("the Thought list" for six thoughts), so the list
// view's label wins where the descriptor declares one.
func (m *tuiModel) typeLabel(idx int) string {
	if idx < 0 || idx >= len(m.types) {
		return "objects"
	}
	t := m.types[idx]
	if v, ok := view.Hint(t, object.ViewList); ok && v.Label != "" {
		return v.Label
	}
	if t.Label != "" {
		return t.Label
	}
	return t.Name
}

// nextListableType is the next type in declaration order that can be listed
// on its own, wrapping around. It returns false when there is no other one,
// so the switcher does not cycle onto a type it cannot render.
func (m *tuiModel) nextListableType() (int, bool) {
	n := len(m.types)
	if n == 0 {
		return 0, false
	}
	for step := 1; step <= n; step++ {
		i := (m.typeIdx + step) % n
		if listable(m.types[i]) {
			return i, i != m.typeIdx
		}
	}
	return m.typeIdx, false
}

// listable reports whether a type is meant to be listed on its own. A type
// with no list view is reached through another type's detail collection — the
// `entry` type is a thought's transcript, and its provider refuses a bare
// List — so the type switcher must not offer it. Cycling to it rendered the
// previous type's rows under the new type's title, because the failed List
// left the model's stale page in place.
func listable(t object.Type) bool {
	v, ok := view.Hint(t, object.ViewList)
	return ok && len(v.Columns) > 0
}

// bodyLines dispatches to the current screen and returns its body rows. The
// action menu, when open, is drawn OVER the screen's body: it is a transient
// overlay, not a screen, so it costs no mode and the reader keeps their
// context behind it.
func (m *tuiModel) bodyLines(f frame) []string {
	if m.helpOpen {
		return m.bodyHelp(f)
	}
	if m.menuOpen {
		return m.bodyActionMenu(f)
	}
	switch m.mode {
	case ModeDetail:
		return m.bodyDetail(f)
	case ModeHome:
		return m.bodyHome(f)
	case ModeLive:
		return m.bodyLive(f)
	case ModeInput:
		return m.bodyInput(f)
	case ModeStatus:
		return m.bodyStatus(f)
	}
	return m.bodyList(f)
}

// bodyStatus draws the navi health page (NAVI-598, ADR 0038 decision 4). It is
// the ONE page with a bespoke element: queue depth, concurrency, and stream
// health are model state, not descriptors, so they cannot come from the object
// surface the way every other screen's content does. Everything it shows is
// state the model already tracks — nothing here is a private data path.
func (m *tuiModel) bodyStatus(f frame) []string {
	out := []string{"", " " + m.theme.Title("navi"), ""}

	running, pending, paused, terminal := 0, 0, 0, 0
	for _, row := range m.list.Rows {
		switch m.statusOf(row.ID) {
		case "running":
			running++
		case "pending":
			pending++
		case "paused":
			paused++
		default:
			terminal++
		}
	}
	row := func(label, value string) string {
		return "   " + fit(label, 14) + " " + value
	}
	out = append(out,
		row("thoughts", itoa(len(m.list.Rows))+" total"),
		row("running", itoa(running)),
		row("queued", itoa(pending)),
		row("paused", itoa(paused)),
		row("finished", itoa(terminal)),
		"",
	)
	// The stream's own health: whether the portal has answered with its
	// descriptors, and whether the feed has dropped events. Both are the
	// things a reader consults a status page to find out.
	types := "ok"
	if len(m.types) == 0 {
		types = "waiting for the navi's object types…"
		if m.typesErr != nil {
			types = "unreachable: " + m.typesErr.Error()
		}
	}
	out = append(out, row("portal", types))
	stream := m.theme.Good("ok")
	if m.lost {
		stream = m.theme.Bad("dropped events — this view may be incomplete")
	}
	out = append(out, row("stream", stream))
	// The in-session limitation, said out loud rather than left for the reader
	// to discover (NAVI-353 is the durable-restore work).
	out = append(out, row("transcript", m.theme.Subtle("in-session only (durable restore is NAVI-353)")))
	return out
}

// bodyHelp is the full-screen help overlay (`?`): every binding live in the
// current context, grouped as columns. It is generated from the same keymap
// the dispatch uses, so it cannot list a key that does nothing — which is the
// whole reason the keymap is declared rather than compared (ADR 0038
// decision 7).
func (m *tuiModel) bodyHelp(f frame) []string {
	km := m.keyMapFor()
	// The overlay shows the FULL help, not the short form: a reader who asked
	// for help wants everything available here, not the footer again. It calls
	// FullHelpView explicitly rather than setting help.ShowAll, because the
	// footer is rendered from the same help.Model in the same frame — a
	// ShowAll flag would still be set when the footer rendered and would turn
	// the footer into the full help too.
	m.help.Width = maxInt(1, f.innerWidth())

	title := "keys"
	switch m.mode {
	case ModeHome, ModeLive:
		title = "keys · conversation"
	case ModeDetail:
		title = "keys · detail"
	case ModeInput:
		title = "keys · input"
	default:
		title = "keys · queue"
	}
	out := []string{"", " " + m.theme.Title(title), ""}
	for _, l := range strings.Split(m.help.FullHelpView(km.FullHelp()), "\n") {
		out = append(out, " "+l)
	}
	out = append(out, "", " "+m.theme.Subtle("any key closes this"))
	return out
}

// bodyActionMenu draws the leader menu: the focused object and the actions its
// descriptor declares for it, each with the key that picks it. It is drawn
// from the descriptor rather than a per-type table, so a new object type gets
// a correct menu with no new binding (ADR 0038 decisions 2 and 7).
func (m *tuiModel) bodyActionMenu(f frame) []string {
	id, _ := m.focused()
	out := []string{
		"",
		" " + m.theme.Title("actions") + m.theme.Subtle(" · "+sanitizeLine(id)),
		"",
	}
	for _, a := range m.actionMenu {
		line := "   " + m.theme.Marker(menuLetter(a.Name)) + "  " + sanitizeLine(actionName(a))
		if a.Confirm {
			// `Confirm` is declared but unenforced today; the marker says so
			// rather than implying a guard that does not exist (NAVI-591
			// enforces it).
			line += m.theme.Confirm("  !")
		}
		out = append(out, line)
	}
	out = append(out, "", " "+m.theme.Subtle("esc to close"))
	return out
}

// bodyHome draws the home screen (ADR 0038 decision 3): the running thought
// as a conversation, or an empty state that enqueues when nothing is running.
// The chat viewport and the turn renderer are the SAME component the pushed
// detail screen uses (decision 5) — home is a context, not a second
// implementation.
func (m *tuiModel) bodyHome(f frame) []string {
	// The wide form factor splits home (ADR 0038 decision 3, decision 6):
	// the conversation on the left, the queue as a sidebar on the right. The
	// sidebar is a CONTENT pane, not the persistent navigation sidebar
	// decision 4 rejected — it shows what is queued while you watch what is
	// running, and below the wide floor it collapses because the queue is
	// reachable as its own page.
	if f.form() == formWide {
		return m.bodyHomeSplit(f)
	}
	if m.homeID == "" {
		return m.bodyHomeEmpty(f)
	}
	// The viewport gets the whole body, exactly as in live: the header already
	// names the thought (`home · <id>`).
	m.chat.setSize(f.innerWidth(), f.bodyHeight())
	out := make([]string, 0, f.bodyHeight())
	for _, l := range m.chat.view(m.theme) {
		out = append(out, truncate(l, f.innerWidth(), "…"))
	}
	return out
}

// bodyHomeSplit composes home's two panes on a wide terminal: the
// conversation on the left at 66%, the queue sidebar on the right at 33%
// (ADR 0038 decision 3). The left pane keeps the same three states it has
// single-pane — the steer page, the empty state — and the right pane is always
// the queue, so what is running and what is waiting are visible together.
func (m *tuiModel) bodyHomeSplit(f frame) []string {
	rows := f.bodyHeight()
	// A one-column gap between the panes, so they read as separate columns
	// rather than one wide run of text.
	leftW, rightW := splitColumns(f.innerWidth(), splitLeftFrac)
	if rightW < minSidebarWidth {
		// Too narrow to be useful: fall back to the single pane rather than
		// render a sidebar of ellipses. The left pane then takes the full
		// width, which is what the single-pane path already renders.
		left := m.homeLeftPane(f.innerWidth(), rows)
		return left
	}
	left := m.homeLeftPane(leftW, rows)
	right := m.queuePane(rightW, rows)
	return f.joinColumns(left, leftW, right, rightW, rows)
}

// homeLeftPane renders home's conversation pane at the given width.
func (m *tuiModel) homeLeftPane(w, rows int) []string {
	if m.homeID == "" {
		// The empty state, sized to the pane.
		return m.bodyHomeEmpty(frame{width: w, height: rows, theme: m.theme})
	}
	m.chat.setSize(w, rows)
	out := make([]string, 0, rows)
	for _, l := range m.chat.view(m.theme) {
		out = append(out, truncate(l, w, "…"))
	}
	return out
}

// minSidebarWidth is the narrowest the queue sidebar may be before it is
// dropped. Below it the columns are too squeezed to read, and the queue is
// reachable as its own page, so nothing is lost.
const minSidebarWidth = 28

// statusW is the width of the sidebar's status column. It fits the longest
// lifecycle word (`cancelled`) so the column never reflows as states change.
const statusW = 9

// queuePane renders the queue as a sidebar: a titled list of the thoughts in
// the queue, marking the one home is tracking. It is deliberately compact —
// the full table with filters and sorting is its own page (NAVI-592) — so it
// shows the shape of the queue, not every field.
func (m *tuiModel) queuePane(w, rows int) []string {
	out := make([]string, 0, rows)
	if len(m.list.Rows) == 0 {
		return append(out, "", " "+m.theme.Subtle("queue empty"))
	}
	// A header row, so the column is legible as a list rather than a run of
	// ids. It names the count, which is the fact a sidebar is for.
	title := "queue · " + itoa(len(m.list.Rows))
	out = append(out, " "+m.theme.Title(title))
	if rows > 2 {
		out = append(out, m.theme.Subtle(" "+strings.Repeat("─", maxInt(1, w-1))))
	}
	// The rows themselves: a marker gutter, the prompt (what a reader
	// recognises), and the status. The id is deliberately absent — a sidebar
	// this narrow cannot show an id AND a prompt, and the prompt is what a
	// reader scans for.
	for i, row := range m.list.Rows {
		if len(out) >= rows {
			break
		}
		mark := " "
		if row.ID == m.homeID {
			mark = m.theme.Marker("▸")
		} else if i == m.sel && m.mode == ModeList {
			mark = m.theme.Marker("·")
		}
		prompt := cellValue(row, "prompt")
		if prompt == "" {
			prompt = row.ID
		}
		status := m.statusOf(row.ID)
		// The status is right-aligned in a fixed column so the states line up
		// and the eye can scan them, which is the whole reason the column
		// exists; the prompt takes what is left.
		room := w - 2 - statusW - 1
		if room < 4 {
			room = 4
		}
		// Status takes the RAW value and pads it itself (that is its contract:
		// it colours the value and lays the cell out, so the escape codes
		// never count toward the width). Pre-fitting here padded twice and
		// made the coloured and uncoloured renders differ in width.
		line := mark + " " + fit(prompt, room) + " " +
			m.theme.Status(status, status, statusW, alignRight)
		out = append(out, truncate(line, w, "…"))
	}
	return out
}

// bodyHomeEmpty is home with nothing running: the state the ADR calls the
// "new-thought page", in the shape of an editor's landing state. It offers
// enqueue inline rather than dead-ending, and it names the queue as the way to
// reach what has already run.
func (m *tuiModel) bodyHomeEmpty(f frame) []string {
	// Two empty states, and the difference matters:
	//
	//   - work is QUEUED (pending or paused) — something is about to run, so
	//     home stays quiet and says what is waiting;
	//   - the queue is DONE (nothing running, nothing waiting) — the navi is
	//     idle, and the screen should say so and name the way out.
	//
	// Collapsing the two would either nag a reader who is waiting for a
	// queued thought, or leave an idle navi looking like a stalled one.
	if m.queuedWork() {
		return m.bodyHomeWaiting()
	}
	return m.bodyHomeSplash(f)
}

// queuedWork reports whether anything is waiting to run: pending (about to be
// claimed) or paused (resumable). A terminal thought is not work.
func (m *tuiModel) queuedWork() bool {
	for _, row := range m.list.Rows {
		switch m.statusOf(row.ID) {
		case "pending", "paused":
			return true
		}
	}
	return false
}

// bodyHomeWaiting is home with work queued: something is about to run, so the
// screen stays quiet and says what is waiting.
func (m *tuiModel) bodyHomeWaiting() []string {
	out := []string{
		"",
		" " + m.theme.Title("waiting for work to start"),
		"",
		" " + m.theme.Subtle("a thought is queued and will start shortly"),
	}
	if m.stage > 0 {
		// Mid-staging: say which field the input is asking for, so the
		// multi-step flow is legible rather than a sequence of bare prompts.
		out = append(out, "", " "+m.theme.Subtle(m.stageHint()))
	}
	return out
}

// bodyHomeSplash is home with NOTHING left to do: no running thought, nothing
// queued. It is a splash rather than a quiet empty state because an idle navi
// and a stalled one look identical otherwise — the screen has to say which it
// is and name the hotkey that starts work. It names the KEY, not the concept,
// because a reader who has never used the TUI needs to know what to press.
func (m *tuiModel) bodyHomeSplash(f frame) []string {
	done := 0
	for _, row := range m.list.Rows {
		if s := m.statusOf(row.ID); s == "complete" || s == "failed" || s == "cancelled" {
			done++
		}
	}
	out := []string{
		"",
		" " + m.theme.Title("idle"),
		"",
		" " + m.theme.Subtle("no thought is running and nothing is queued."),
		"",
	}
	// The hotkey, set apart, because it is the one thing the reader needs.
	out = append(out,
		" "+m.theme.Subtle("press ")+m.theme.Marker("n")+m.theme.Subtle(" to queue a new thought"),
		"",
	)
	if done > 0 {
		out = append(out,
			" "+m.theme.Subtle(itoa(done)+" thought"+plural(done)+" finished")+
				m.theme.Subtle(" — press 2 for the queue"),
		)
	}
	return out
}

// enqueuePrompt labels the contextual input line for home's empty state, per
// stage, so the persistent field says what submitting will do.
func (m *tuiModel) enqueueLabel() string {
	switch m.stage {
	case 1:
		return "priority"
	case 2:
		return "defer"
	}
	return "new thought"
}

// stageHint describes the current enqueue stage for the empty state.
func (m *tuiModel) stageHint() string {
	switch m.stage {
	case 1:
		return "priority 1-10 (enter for the default 4)"
	case 2:
		return "defer minutes (enter for now)"
	}
	return "prompt"
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// helpText is the STATUS region's right-hand text: the generated help for the
// current context (NAVI-593, ADR 0038 decision 7). It reads the same keymap
// the dispatch does, so a key that renders here is one that works.
//
// The menu's own letters are not in the keymap — they are the focused object's
// DECLARED actions, unknown until the menu is built — so the menu advertises
// itself instead.
func (m *tuiModel) helpText(f frame) string {
	if m.menuOpen {
		line := "actions:"
		for _, a := range m.actionMenu {
			line += " " + m.theme.Marker(menuLetter(a.Name)) + " " + m.theme.Subtle(actionName(a))
		}
		return m.theme.Subtle(line)
	}
	m.help.Width = maxInt(1, f.width/2)
	line := m.help.View(m.keyMapFor())
	// help truncates ITEMS but can still emit a line wider than Width (its
	// shouldAddItem adds the item when there is no room for the ellipsis), so
	// the final width is enforced here.
	return truncate(line, f.width/2, "…")
}

// actionName is a declared action's label for the menu, falling back to the
// verb so an unlabelled action is still named.
func actionName(a view.Action) string {
	if a.Label != "" {
		return a.Label
	}
	return a.Name
}

// itoa is strconv.Itoa without the import at every call site in the
// renderer.
func itoa(n int) string { return strconv.Itoa(n) }

// bodyList draws the selected type's page from the descriptor-resolved
// columns, decorating each row with the live overlay (status, a derived
// blocked flag, a checkpoint marker, a running preview).
func (m *tuiModel) bodyList(f frame) []string {
	if _, ok := m.currentType(); !ok {
		return []string{" " + m.theme.Subtle("(waiting for the navi's object types…)")}
	}
	var out []string
	if len(m.list.Rows) == 0 {
		return append(out, " "+m.theme.Subtle("(no "+strings.ToLower(m.typeLabel(m.typeIdx))+" yet)"))
	}

	// The rows are a grid: a marker gutter, the declared columns separated by
	// a rule, and a trailing flags/activity gutter. The grid draws the
	// header, the rule beneath it, and the rows from ONE column model, so the
	// header and the body cannot drift apart.
	g := m.buildGrid(f)
	out = append(out, g.header(m.theme)...)
	out = append(out, g.rule(m.theme))
	for i, row := range m.list.Rows {
		// A light dotted rule between rows, and none after the last. It says
		// "next record in the same table" where a full rule would read as a
		// new section, and it is a character rather than a colour, so it
		// survives NO_COLOR. Without it, consecutive rows are three lines of
		// text with nothing marking where one ends.
		if i > 0 {
			out = append(out, g.separator(m.theme))
		}
		out = append(out, g.row(m.theme, m, i, row)...)
	}
	return out
}

// grid is the resolved column model for the list: the columns, their widths,
// and their alignment. It is built once per frame and used for the header,
// the rule, and every row, so all three agree by construction.
type grid struct {
	cols    []laidOutColumn
	widths  []int
	markW   int
	gutterW int
}

// buildGrid resolves the descriptor's columns against the frame.
func (m *tuiModel) buildGrid(f frame) grid {
	// The marker column is two cells: the selection glyph and a space.
	g := grid{markW: 2}
	g.cols = m.columnLayout(f.innerWidth())
	for _, c := range g.cols {
		g.widths = append(g.widths, c.width)
	}
	// The trailing gutter holds the flags and, where there is room, the
	// running activity preview. It is computed here and used by both the row
	// and the spine, so the two cannot disagree about where a column ends.
	g.gutterW = flagsReserve
	if f.innerWidth() >= activityMinFrame && m.pageHasActivity() {
		g.gutterW += activityReserve
	}
	return g
}

// colSep is the column separator. A single rule character with a space each
// side reads as a table without stealing much width.
const colSep = "│"

// header renders the column labels, aligned to match their column's kind and
// separated by the same rule as the rows.
func (g grid) header(th theme) []string {
	line := strings.Repeat(" ", g.markW)
	for i, c := range g.cols {
		if i > 0 {
			line += th.Subtle(" " + colSep + " ")
		}
		line += th.Subtle(alignCell(sanitizeLine(c.column.Label), c.width, c.align))
	}
	return []string{line}
}

// rule is the line under the header: a rule character spanning each column,
// joined at the column separators. It is what makes the rows read as a grid
// rather than as floating text.
func (g grid) rule(th theme) string {
	return th.Subtle(g.spine("─", "─", "┼"))
}

// spine builds a horizontal line across the grid: the marker column, each
// column's width, and the trailing gutter, joined at the column separators.
// The rule, the dotted separator, and the header all derive their shape from
// this one function, so they cannot disagree about where a column ends.
func (g grid) spine(fill, join, cross string) string {
	var b strings.Builder
	b.WriteString(strings.Repeat(fill, g.markW))
	for i, w := range g.widths {
		if i > 0 {
			b.WriteString(fill + cross + fill)
		}
		b.WriteString(strings.Repeat(fill, w))
	}
	b.WriteString(strings.Repeat(fill, g.gutterW))
	return b.String()
}

// separator is the light rule drawn between rows: a dotted line through each
// column, joined at the separators. It is deliberately lighter than the
// header rule so the header still reads as the table's edge.
func (g grid) separator(th theme) string {
	return th.Subtle(g.spine("·", "·", "┼"))
}

// row renders one data row: the marker gutter, each cell aligned by its
// column's kind, the flags, and the activity preview.
func (g grid) row(th theme, m *tuiModel, i int, row view.Row) []string {
	b := m.badges[row.ID]

	mark := " "
	if i == m.sel {
		mark = th.Marker("▸")
	}
	line := mark + " "
	for ci, c := range g.cols {
		if ci > 0 {
			line += th.Subtle(" " + colSep + " ")
		}
		value := cellValue(row, c.column.Field)
		if c.column.Field == "status" {
			value = m.statusOf(row.ID)
			if b.blocked {
				value = "blocked"
			}
		}
		raw := sanitizeLine(value)
		if c.column.Field == "id" {
			raw = truncateLeft(raw, c.width, "…")
		}
		cell := alignCell(raw, c.width, c.align)
		// Status is an enum: colour it so the eye finds a row's state without
		// reading every word. The text is identical either way.
		if c.column.Kind == object.KindEnum {
			cell = th.Status(sanitizeLine(value), cell, c.width, c.align)
		}
		line += cell
	}

	// The flags gutter: a checkpoint marker and a verification flag, always
	// reserved so a flag can never be squeezed off the edge.
	flags := "  "
	if b.checkpoint {
		flags = " C"
	}
	if v, _ := m.items[row.ID].Fields["verification"].(bool); v {
		flags += "V"
	} else {
		flags += " "
	}
	line += " " + th.Subtle(flags)

	if b.activity != "" && g.gutterW > flagsReserve {
		line += " " + th.Subtle("«"+truncate(sanitizeLine(b.activity), activityReserve-2, "…"))
	}
	return []string{line}
}

// alignCell pads a value to the column width according to its alignment.
//
// Truncation is from the LEFT for an identity column: a generated id puts its
// distinguishing characters at the end (`cli-1794295713624121722`), so cutting
// the tail would render every row as the same constant prefix and leave the
// reader unable to tell two records apart — which is what made a list of three
// different thoughts look like three copies of one row.
// alignCell lays a cell value out to exactly width display columns, aligned by
// the column's kind. It is lipgloss's own Align/Width/MaxWidth rather than
// hand-written space arithmetic: the style truncates by display width and pads
// to the same width, so a wide rune cannot shift the columns after it
// (NAVI-588) and the escape codes a styled cell carries never count toward the
// width.
func alignCell(s string, width int, a align) string {
	if width <= 0 {
		return ""
	}
	// Truncate before aligning, for the same reason fit does: Width WRAPS
	// overflow rather than clipping it, and a table cell that wrapped would
	// break the row.
	s = truncate(s, width, "…")
	return lipgloss.NewStyle().
		Width(width).
		Align(a.position()).
		Render(s)
}

// align is how a column's values sit within their width. Alignment follows
// the value's kind, because that is what makes a table scannable: numbers and
// dates line up on the right, short enums sit centred, text reads left.
type align int

const (
	alignLeft align = iota
	alignRight
	alignCenter
)

// position maps the column vocabulary onto lipgloss's, so the alignment a
// descriptor's field kind implies is expressed in the library's terms.
func (a align) position() lipgloss.Position {
	switch a {
	case alignRight:
		return lipgloss.Right
	case alignCenter:
		return lipgloss.Center
	}
	return lipgloss.Left
}

// alignFor picks the alignment for a field kind.
func alignFor(k object.Kind) align {
	switch k {
	case object.KindInt, object.KindFloat, object.KindTime:
		return alignRight
	case object.KindEnum, object.KindBool:
		return alignCenter
	}
	return alignLeft
}

// laidOutColumn is a declared column paired with the width it was given.
type laidOutColumn struct {
	column view.Column
	width  int
	align  align
}

// columnLayout sizes the descriptor's columns to the frame. It reserves room
// for the leading marker, the trailing flags, and the activity preview, then
// distributes what is left. A text column absorbs the slack so a prompt has
// room to be read; everything else keeps a floor so a header stays legible.
func (m *tuiModel) columnLayout(frameWidth int) []laidOutColumn {
	const (
		// leading is the marker column: the selection glyph and the space
		// after it. The frame's own gutter is NOT included here — the layout
		// is given the inner width, which is already net of the rails.
		leading = 2
		gap     = 1
	)
	cols := m.list.Columns
	if len(cols) == 0 {
		return nil
	}

	// A column every row leaves empty is dropped before any sizing. Reserving
	// width for a column that renders nothing steals it from the columns that
	// do have something to say — the fixture's empty `model` was pushing the
	// `updated` date off the edge at 80 columns, and shrinking to `…` at 60.
	// A column that is empty for THIS page is not shown; the descriptor still
	// declares it, so a page that fills it shows it.
	cols = m.dropEmptyColumns(cols)
	if len(cols) == 0 {
		return nil
	}

	// The flags gutter (checkpoint / verification markers) is part of every
	// row, so its room is reserved before the columns are sized — otherwise
	// the markers are the first thing squeezed off the edge, and a flag that
	// silently disappears is exactly the "advertised but does not work"
	// defect this revamp exists to remove.
	flags := flagsReserve

	// The activity preview shares the row with the columns. Reserve room for
	// it only when the page actually has one to show, and only when the
	// terminal is wide enough: reserving unconditionally left a dead gap
	// before the right rail on every page whose thoughts were not running,
	// which is most of them.
	activity := 0
	if frameWidth >= activityMinFrame && m.pageHasActivity() {
		activity = activityReserve
	}

	// Each column after the first costs a separator: " " + rule + " ".
	seps := 0
	if len(cols) > 1 {
		seps = (len(cols) - 1) * 3
	}

	// avail is exactly the space the columns may occupy: the inner width less
	// the marker column, the inter-column separators, and the trailing gutter
	// (flags plus, where reserved, the activity preview). Deriving it this way
	// — rather than subtracting loose constants — is what makes the row fill
	// its width and the rule reach the rail.
	gutter := flags
	if activity > 0 {
		gutter += activity
	}
	avail := frameWidth - leading - gutter - seps
	if avail < 1 {
		avail = 1
	}

	// On a narrow terminal, drop low-value columns rather than squeeze every
	// one to illegibility: an id truncated to `t-pen…` and a status truncated
	// to `runni…` tell the reader less than three whole columns would. The
	// identity column and the text column are never dropped; the rest go from
	// the end until the survivors fit at their floors.
	keep := len(cols)
	for keep > 2 {
		if columnsMinTotal(cols[:keep], gap) <= avail {
			break
		}
		keep--
	}
	cols = cols[:keep]

	// Start every column at its natural width, capped. The inter-column
	// separators are accounted for separately (seps), so they are NOT added
	// here — adding both double-counted them and left the row short.
	out := make([]laidOutColumn, len(cols))
	total := 0
	textIdx := -1
	for i, c := range cols {
		w := naturalColumnWidth(c)
		out[i] = laidOutColumn{column: c, width: w, align: alignFor(c.Kind)}
		total += w
		if c.Kind == object.KindText {
			textIdx = i
		}
	}

	// Too wide: shrink the text column first (it can spare width), then the
	// widest remaining column, never below a per-column floor.
	for total > avail {
		shrunk := false
		if textIdx >= 0 && out[textIdx].width > minTextColumn {
			out[textIdx].width--
			total--
			shrunk = true
		} else {
			// Shrink the widest column that is still above its own floor, so
			// width comes off the most generous column rather than off
			// whichever happens to be first — and so the identity column keeps
			// its wider floor while a date column gives up its slack.
			widest, widestW := -1, 0
			for i := range out {
				floor := columnFloor(out[i].column)
				if out[i].width <= floor {
					continue
				}
				if out[i].width > widestW {
					widest, widestW = i, out[i].width
				}
			}
			if widest >= 0 {
				out[widest].width--
				total--
				shrunk = true
			}
		}
		if !shrunk {
			break // every column is at its floor; the frame will clip
		}
	}

	// Room left over goes to the text column, so a wide terminal shows more
	// prompt rather than a stretched status column. When there is no text
	// column the last column takes it, so the row always fills its width —
	// an under-filled row leaves a dead gap before the right rail.
	grow := textIdx
	if grow < 0 {
		grow = len(out) - 1
	}
	if grow >= 0 {
		for total < avail {
			out[grow].width++
			total++
		}
	}
	return out
}

const (
	// minColumn is the narrowest a non-text column may be squeezed to.
	minColumn = 6
	// idColumnFloor is the narrowest an identity column may be squeezed to,
	// so an id stays recognisable rather than being cut to a prefix.
	idColumnFloor = 12
	// minTextColumn is the narrowest the text column may be squeezed to
	// before other columns start shrinking.
	minTextColumn = 12
	// maxColumn caps a column so one type cannot dominate a wide terminal.
	maxColumn = 40
)

const (
	// flagsReserve is the display columns held back for the row's marker
	// gutter (a checkpoint `C` and a verification `V`).
	flagsReserve = 4
	// activityReserve is the display columns held back for the running
	// activity preview on a row.
	activityReserve = 16
	// activityMinFrame is the frame width below which the preview is dropped
	// so the columns keep their room.
	activityMinFrame = 90
)

// pageHasActivity reports whether any row on the current page has a running
// activity preview to draw. The column layout uses it to decide whether to
// reserve room for that preview, so a quiet page gets the width back.
func (m *tuiModel) pageHasActivity() bool {
	for _, row := range m.list.Rows {
		if m.badges[row.ID].activity != "" {
			return true
		}
	}
	return false
}

// columnsMinTotal is the total width a column set needs at its floors,
// including the inter-column gaps. The layout uses it to decide how many
// columns fit before it starts truncating them.
func columnsMinTotal(cols []view.Column, gap int) int {
	total := 0
	for _, c := range cols {
		total += columnFloor(c) + gap
	}
	if len(cols) > 0 {
		total -= gap
	}
	return total
}

// columnFloor is the narrowest a column may be rendered. The floor is the
// column's own label width where that is wider than the generic minimum,
// because a header truncated to `Priori…` tells the reader less than no column
// at all — a table whose headings are cut in half is worse than one with fewer
// headings. The identity column gets a wider floor still, since a row whose id
// is unreadable is not a row a reader can act on.
func columnFloor(c view.Column) int {
	floor := minColumn
	if w := displayWidth(c.Label); w > floor {
		floor = w
	}
	switch {
	case c.Kind == object.KindText:
		if minTextColumn > floor {
			floor = minTextColumn
		}
	case c.Kind == object.KindRef || c.Field == "id":
		if idColumnFloor > floor {
			floor = idColumnFloor
		}
	}
	return floor
}

// dropEmptyColumns removes columns whose value is empty in every row of the
// current page. The header label is not a reason to keep a column: a column
// whose every cell is blank is furniture.
func (m *tuiModel) dropEmptyColumns(cols []view.Column) []view.Column {
	if len(m.list.Rows) == 0 {
		return cols
	}
	out := make([]view.Column, 0, len(cols))
	for _, c := range cols {
		empty := true
		for _, row := range m.list.Rows {
			if strings.TrimSpace(cellValue(row, c.Field)) != "" {
				empty = false
				break
			}
		}
		// The identity column is never dropped, and neither is a column the
		// live overlay fills (status), because their emptiness here is an
		// artefact of what the page carried rather than of the type.
		if empty && c.Field != "id" && c.Field != "status" && c.Kind != object.KindText {
			continue
		}
		out = append(out, c)
	}
	return out
}

// naturalColumnWidth is a column's preferred width: a text column wants
// room, others size to their label with a floor.
func naturalColumnWidth(c view.Column) int {
	if c.Kind == object.KindText {
		return 30
	}
	w := displayWidth(c.Label)
	if w < 10 {
		w = 10
	}
	if w > maxColumn {
		w = maxColumn
	}
	return w
}

// cellValue reads one resolved cell from a toolkit row.
func cellValue(row view.Row, field string) string {
	for _, c := range row.Cells {
		if c.Field == field {
			return c.Value
		}
	}
	return ""
}

// bodyDetail draws one object's descriptor-resolved sections and the actions
// its type grants.
func (m *tuiModel) bodyDetail(f frame) []string {
	if m.detail == nil {
		return nil
	}
	d := m.detail
	label := d.Type.Label
	if label == "" {
		label = d.Type.Name
	}
	out := []string{" " + m.theme.Title(sanitizeLine(label)) + " · " + sanitizeLine(d.Title)}
	// The field-label column is sized to the frame so a long label cannot
	// push the value off the edge.
	labelW := 18
	if f.innerWidth() < 60 {
		labelW = 12
	}
	if labelW > f.innerWidth()/2 {
		labelW = f.width / 2
	}
	for _, s := range d.Sections {
		out = append(out, " "+m.theme.Subtle(sanitizeLine(s.Label)))
		for _, field := range s.Fields {
			line := "   " + fit(sanitizeLine(field.Label), labelW) + " " +
				truncate(sanitizeLine(field.Value), f.innerWidth()-4-labelW-1, "…")
			out = append(out, line)
		}
	}
	if len(d.Actions) > 0 {
		line := " actions:"
		for _, a := range d.Actions {
			name := sanitizeLine(a.Name)
			if a.Confirm {
				line += " " + m.theme.Confirm(name+"!")
			} else {
				line += " " + name
			}
		}
		out = append(out, line)
	}
	return out
}

// bodyInput draws the enqueue / attach input stage.
func (m *tuiModel) bodyInput(f frame) []string {
	// The editor BOX carries the prompt (`╭─ new thought ─╮`) and the text, so
	// the body only explains the stage. Repeating the prompt here would say the
	// same thing twice on one screen.
	hint := "enter to continue"
	switch {
	case m.inputID != "":
		hint = "steering " + sanitizeLine(m.inputID) + " — enter to send"
	case m.stage == 1:
		hint = "priority 1-10 (enter for the default 4)"
	case m.stage == 2:
		hint = "defer minutes (enter for now)"
	default:
		hint = "a prompt for a new thought — enter to continue"
	}
	return []string{"", " " + m.theme.Subtle(hint)}
}

// bodyLive draws the conversation through the shared chat component: the
// component renders the turn blocks and owns the scroll position, and this
// only supplies the header and the viewport size.
func (m *tuiModel) bodyLive(f frame) []string {
	// No header line here: the panel's top edge already names the conversation
	// (`chat · <id>`), and repeating it inside the panel was the same defect
	// the list had. The state that header carried — whether the view is
	// following the tail, and whether the scrollback has been trimmed — is in
	// the panel title's status suffix instead (see chatStatus).
	//
	// The viewport gets the whole body, so the conversation fills whatever the
	// terminal gives it rather than losing a row to a title it already has.
	m.chat.setSize(f.innerWidth(), f.bodyHeight())

	// The frame supplies the rail and the leading space, so the chat's lines
	// are laid out to the full inner width without adding a margin of their
	// own — an extra column here pushed the right rail off the edge.
	out := make([]string, 0, f.bodyHeight())
	for _, l := range m.chat.view(m.theme) {
		out = append(out, truncate(l, f.innerWidth(), "…"))
	}
	return out
}

// chatStatus is the state suffix the panel title shows while a conversation is
// open. It leads with the THOUGHT'S LIFECYCLE STATUS — running, paused,
// complete, failed — because that is the thing the reader is watching and the
// screen otherwise never says it: the title names the thought and the
// conversation shows its content, but nothing said whether it was still
// working. The colour follows the same mapping a table cell uses
// (theme.statusStyle), so a status means one thing across the TUI.
//
// After the status comes the view's own state: whether the viewport follows the
// tail, and whether the scrollback window has dropped turns.
func (m *tuiModel) chatStatus() string {
	if m.mode != ModeLive && m.mode != ModeHome {
		return ""
	}
	id := m.conversationID()
	if id == "" {
		return "idle"
	}
	status := m.conversationStatus(id)
	if m.chat.followSuspended() {
		// The indicator is the promise that the view will not yank the reader
		// to the bottom while they are reading history — and that it will
		// resume once they return.
		status += " · following paused (ctrl+f)"
	} else {
		status += " · following"
	}
	if n := m.chat.trimNotice(); n != "" {
		status += " " + n
	}
	return status
}

// conversationID is the thought the open conversation is showing: the tracked
// one on home, the opened one in live. Empty when neither.
func (m *tuiModel) conversationID() string {
	if m.mode == ModeLive {
		return m.liveID
	}
	if m.mode == ModeHome {
		return m.homeID
	}
	return ""
}

// conversationStatus names the lifecycle state of the thought a conversation
// is showing. It reads the live overlay first and the declared field second
// (statusOf), so it is correct for a running thought the stream is updating and
// for a transcript read after the fact.
func (m *tuiModel) conversationStatus(id string) string {
	switch m.statusOf(id) {
	case "running":
		// "live" is load-bearing: a running thought's conversation is being
		// appended to, while an opened completed one is a transcript read.
		return "live"
	case "pending":
		return "queued"
	case "paused":
		return "paused"
	case "complete":
		return "complete"
	case "failed":
		return "failed"
	case "cancelled":
		return "cancelled"
	case "":
		return "unknown"
	}
	return m.statusOf(id)
}

// conversationStatusStyle is the style the panel title's status suffix is drawn
// in, chosen from the thought's lifecycle so the state is legible at a glance.
// Nil on a screen with no conversation, which renders the suffix plain.
func (m *tuiModel) conversationStatusStyle() func(string) string {
	if m.mode != ModeLive && m.mode != ModeHome {
		return nil
	}
	id := m.conversationID()
	if id == "" {
		return m.theme.Subtle
	}
	st := m.theme.statusStyle(m.statusOf(id))
	return func(s string) string { return m.theme.apply(st, s) }
}

// ── run entry ───────────────────────────────────────────────────────────

// Run drives the TUI over tr until the user quits; the tea program
// keeps its own signal handler DISABLED — the navi runtime's signal
// ctx owns SIGINT (park-with-checkpoint semantics).
func Run(tr Transport) error {
	model := NewModel(tr)
	p := tea.NewProgram(model,
		tea.WithAltScreen(),
		tea.WithoutSignalHandler(),
	)
	if _, err := p.Run(); err != nil {
		return err
	}
	return nil
}
