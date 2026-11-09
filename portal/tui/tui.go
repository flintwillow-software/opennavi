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

	tea "github.com/charmbracelet/bubbletea"

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

const (
	ModeList   ModeKind = "list"
	ModeDetail ModeKind = "detail"
	ModeLive   ModeKind = "live"
	ModeInput  ModeKind = "input"
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
	// turns is the reconstructed chat for the live view (NAVI-330):
	// prompt as the opening user turn, assistant deltas accumulated,
	// tool blocks, lifecycle system lines, steers as user turns.
	// Presentation-only — the stream stays the source of truth.
	turns []turn
	// transcripts accumulates each thought's conversation from the event
	// stream, regardless of which view is open, so a thought enqueued
	// from the list still shows its response when opened after it ran.
	// In-session only; durable restore is NAVI-353.
	transcripts map[string][]turn
	liveScroll  int // 0 = tail-follow; >0 = lines scrolled back
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
		mode:        ModeList,
		theme:       newTheme(),
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
	// Lifecycle transitions render as system lines in the live chat
	// window (NAVI-330): started/paused/resumed/completed/failed/...
	if m.mode == ModeLive && ev.ThoughtID == m.liveID && ev.Kind != "enqueued" {
		m.addTurn(turn{role: TurnSystem, text: "· " + displayStatus(ev.Kind)})
	}
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
		if m.mode == ModeLive && ev.ThoughtID == m.liveID && ev.Delivery != "" {
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
	// Home view (NAVI-335): consume agent events for RUNNING thoughts —
	// latest tool call or delta snippet on the row, plus the status
	// activity line.
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
	case "turn_start", "agent_start":
		add(turn{role: TurnSystem, text: ev.Kind})
	case "tool_call":
		add(turn{role: TurnTool, tool: m.clip(ev.Detail, 40), toolIn: ""})
	case "tool_result":
		add(turn{role: TurnTool, tool: "result", toolOut: m.clip(ev.Detail, 60)})
	case "agent_end":
		add(turn{role: TurnSystem, text: "agent ended"})
	case "error":
		add(turn{role: TurnSystem, text: "error: " + m.clip(ev.Detail, 60)})
	default:
		add(turn{role: TurnSystem, text: ev.Kind})
	}
	m.transcripts[ev.ThoughtID] = tr
	if m.liveID == ev.ThoughtID {
		m.turns = tr
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

// addTurn appends a chat turn, bounding the scrollback window; the
// tail (scroll 0) auto-follows.
func (m *tuiModel) addTurn(t turn) {
	m.turns = append(m.turns, t)
	if len(m.turns) > maxLiveTurns {
		m.turns = m.turns[len(m.turns)-maxLiveTurns:]
	}
	if m.liveID != "" && m.transcripts != nil {
		m.transcripts[m.liveID] = m.turns
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

// key handles a key press; returns true when the user quits.
// TurnRole renders one conversation turn in the live chat window.
type TurnRole string

const (
	TurnUser      TurnRole = "user"
	TurnAssistant TurnRole = "assistant"
	TurnTool      TurnRole = "tool"
	TurnSystem    TurnRole = "system"
)

// turn is one chat-window block (NAVI-330 presentation).
type turn struct {
	role    TurnRole
	text    string // accumulated content (assistant deltas append here)
	tool    string // tool name for tool turns
	toolIn  string // input summary for tool turns
	toolOut string // result for tool turns
}

// maxLiveTurns bounds the reconstructed chat (scrollback window).
const maxLiveTurns = 80

// enterLive opens a thought's conversation: the persisted transcript detail,
// read through the object surface and resolved by the toolkit, so a completed
// thought opened after the fact shows its full conversation and the in-process
// and remote paths render the same thing (ADR 0017). Live events accumulate on
// top of the seed.
func (m *tuiModel) enterLive(thoughtID string) {
	m.liveID = thoughtID
	m.mode = ModeLive
	m.liveScroll = 0
	if tr := m.transcripts[thoughtID]; len(tr) > 0 {
		// The accumulated stream may have started mid-run: the prompt opens
		// the conversation when nothing else does.
		if tr[0].role != TurnUser {
			tr = append([]turn{{role: TurnUser, text: m.promptOf(thoughtID)}}, tr...)
			m.transcripts[thoughtID] = tr
		}
		m.turns = tr
		return
	}
	m.turns = m.seedTurns(thoughtID)
	m.transcripts[thoughtID] = m.turns
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
			if chat, err := view.ReadChat(m.objects, t.Name, thoughtID, detail, object.Query{Limit: listPage}); err == nil {
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
		switch {
		case t.Role == "tool" || (t.Tool != "" && t.Output != ""):
			out = append(out, turn{role: TurnTool, tool: t.Tool, toolOut: t.Output})
		case t.Tool != "" && t.Text == "":
			out = append(out, turn{role: TurnTool, tool: t.Tool, toolIn: t.Input})
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

	switch k.String() {
	case "q", "ctrl+c":
		return true
	case "esc", "escape", "l":
		if m.mode == ModeLive || m.mode == ModeDetail {
			m.mode = ModeList
			m.detail = nil
		}
	case "up", "k":
		if m.mode == ModeLive {
			m.liveScroll++
		} else if m.mode == ModeList && m.sel > 0 {
			m.sel--
		}
	case "down", "j":
		if m.mode == ModeLive {
			if m.liveScroll > 0 {
				m.liveScroll--
			}
		} else if m.mode == ModeList && m.sel < len(m.list.Rows)-1 {
			m.sel++
		}
	case "enter":
		if id, ok := m.selected(); ok {
			m.open(id)
		}
	case "t":
		if m.mode == ModeList && len(m.types) > 1 {
			m.typeIdx = (m.typeIdx + 1) % len(m.types)
			m.sel = 0
			m.detail = nil
			m.refreshList()
			if t, ok := m.currentType(); ok {
				m.status = "type: " + t.Name
			}
		}
	case "n":
		if m.mode == ModeList {
			m.enqueue = true
			m.inputID = ""
			m.stage = 0
			m.input = ""
			m.mode = ModeInput
		}
	case "a":
		if m.mode == ModeList && len(m.list.Rows) > 0 {
			m.enqueue = false
			m.inputID = m.list.Rows[m.sel].ID
			m.stage = 0
			m.input = ""
			m.mode = ModeInput
		}
	case "p":
		if id, ok := m.selected(); ok {
			m.doAction("bump", id, maxPriority(intField(m.items[id].Fields["priority"])-1))
		}
	case "P":
		if id, ok := m.selected(); ok {
			m.doAction("bump", id, minPriority(intField(m.items[id].Fields["priority"])+1))
		}
	case "s":
		if id, ok := m.selected(); ok {
			m.doAction("pause", id, 0)
		}
	case "r":
		if id, ok := m.selected(); ok {
			m.doAction("resume", id, 0)
		}
	case "c":
		if id, ok := m.selected(); ok {
			m.doAction("cancel", id, 0)
		}
	case "R":
		if id, ok := m.selected(); ok {
			m.doAction("retry", id, 0)
		}
	}
	return false
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
func (m *tuiModel) enqueueStep() {
	switch m.stage {
	case 0:
		m.enqPrompt = m.input
		m.input = ""
		if strings.TrimSpace(m.enqPrompt) == "" {
			m.status = "enqueue cancelled: prompt is required"
			m.mode = ModeList
			m.enqueue = false
			return
		}
		m.stage = 1
	case 1:
		m.enqPrio = parsePrio(m.input)
		m.input = ""
		m.stage = 2
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
		m.mode = ModeList
		m.enqueue = false
		m.input = ""
	}
}
func minPriority(p int) int {
	if p > 10 {
		return 10
	}
	return p
}

// selected returns the highlighted object's id in list mode.
func (m *tuiModel) selected() (string, bool) {
	if m.mode != ModeList || m.sel < 0 || m.sel >= len(m.list.Rows) {
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

	title := "thought queue"
	if t, ok := m.currentType(); ok && t.Label != "" {
		title = t.Label
	}
	header := " " + m.theme.Title("navi") + " · " + sanitizeLine(title) +
		m.theme.Subtle(" (portal"+terminalSuffix(m)+")") +
		m.theme.Subtle("  "+f.form().String()+" "+itoa(f.width)+"×"+itoa(f.height))

	// A terminal below the floor gets a notice rather than a corrupted row.
	if f.tooNarrow() {
		return f.render(header, []string{
			"",
			" " + m.theme.Warn("terminal too narrow") + " — " +
				m.theme.Subtle("need at least "+itoa(narrowFloor)+" columns, have "+itoa(f.width)),
		}, m.footerLine(f))
	}

	body := m.bodyLines(f)
	return f.render(header, body, m.footerLine(f))
}

// bodyLines dispatches to the current screen and returns its body rows.
func (m *tuiModel) bodyLines(f frame) []string {
	switch m.mode {
	case ModeDetail:
		return m.bodyDetail(f)
	case ModeLive:
		return m.bodyLive(f)
	case ModeInput:
		return m.bodyInput(f)
	}
	return m.bodyList(f)
}

// footerLine is the status plus the (currently fixed) help line. The help
// becomes generated from the keymap in a later slice (NAVI-593); it is one
// place now so that change is local.
func (m *tuiModel) footerLine(f frame) string {
	line := " " + sanitizeLine(m.status)
	if m.mode == ModeList && m.active != "" {
		line = " " + m.theme.Marker("▸") + " " + sanitizeLine(m.activeID) +
			" · " + sanitizeLine(m.active) + "   " + line
	}
	if f.tooNarrow() {
		return line
	}
	help := " q quit · j/k move · enter open · t type · n new · a attach · p/P priority · s pause · r resume · c cancel · R retry · esc back"
	return line + m.theme.Subtle(help)
}

func terminalSuffix(m *tuiModel) string {
	if m.lost {
		return " · STREAM GAP"
	}
	return ""
}

// itoa is strconv.Itoa without the import at every call site in the
// renderer.
func itoa(n int) string { return strconv.Itoa(n) }

// bodyList draws the selected type's page from the descriptor-resolved
// columns, decorating each row with the live overlay (status, a derived
// blocked flag, a checkpoint marker, a running preview).
func (m *tuiModel) bodyList(f frame) []string {
	t, ok := m.currentType()
	if !ok {
		return []string{" " + m.theme.Subtle("(waiting for the navi's object types…)")}
	}
	var out []string
	if len(m.types) > 1 {
		line := " type: "
		for i, ty := range m.types {
			if i > 0 {
				line += " · "
			}
			label := ty.Label
			if label == "" {
				label = ty.Name
			}
			if i == m.typeIdx {
				line += m.theme.Selected("[" + sanitizeLine(label) + "]")
			} else {
				line += sanitizeLine(label)
			}
		}
		out = append(out, line)
	}
	if len(m.list.Rows) == 0 {
		return append(out, " "+m.theme.Subtle("("+sanitizeLine(t.Name)+" empty)"))
	}

	// Columns are laid out from the descriptor's declared columns, sized by
	// display width, and fitted to the frame so a wide value cannot push the
	// rest off the screen (NAVI-588).
	cols := m.columnLayout(f.width)
	out = append(out, "    "+m.renderColumnHeader(cols))
	for i, row := range m.list.Rows {
		b := m.badges[row.ID]
		flags := "  "
		if b.checkpoint {
			flags = " C "
		}
		if v, _ := m.items[row.ID].Fields["verification"].(bool); v {
			flags += "V"
		}
		mark := " "
		if i == m.sel {
			mark = m.theme.Marker(">")
		}
		line := " " + mark + " "
		for _, c := range cols {
			value := cellValue(row, c.column.Field)
			if c.column.Field == "status" {
				value = m.statusOf(row.ID)
				if b.blocked {
					value = "blocked"
				}
			}
			line += pad(sanitizeLine(value), c.width) + " "
		}
		line += flags + " "
		if b.activity != "" && f.width >= activityMinFrame {
			line += " «" + truncate(sanitizeLine(b.activity), activityReserve-2, "…")
		}
		out = append(out, line)
	}
	return out
}

// laidOutColumn is a declared column paired with the width it was given.
type laidOutColumn struct {
	column view.Column
	width  int
}

// columnLayout sizes the descriptor's columns to the frame. It reserves room
// for the leading marker, the trailing flags, and the activity preview, then
// distributes what is left. A text column absorbs the slack so a prompt has
// room to be read; everything else keeps a floor so a header stays legible.
func (m *tuiModel) columnLayout(frameWidth int) []laidOutColumn {
	const (
		leading = 4 // " > " plus the space before the first column
		gap     = 1
	)
	cols := m.list.Columns
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
	// it when there is any, so a running thought's preview is legible rather
	// than squeezed to a single ellipsis at the edge; on a narrow terminal
	// the preview is dropped instead of starving the columns.
	activity := 0
	if frameWidth >= activityMinFrame {
		activity = activityReserve
	}

	avail := frameWidth - leading - flags - activity - gap // trailing space after the last column
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

	// Start every column at its natural width, capped.
	out := make([]laidOutColumn, len(cols))
	total := 0
	textIdx := -1
	for i, c := range cols {
		w := naturalColumnWidth(c)
		out[i] = laidOutColumn{column: c, width: w}
		total += w + gap
		if c.Kind == object.KindText {
			textIdx = i
		}
	}
	total -= gap // no trailing gap after the last column

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
	// prompt rather than a stretched status column.
	if textIdx >= 0 {
		for total < avail {
			out[textIdx].width++
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

// columnFloor is the narrowest a column may be rendered. The identity column
// gets a wider floor than the rest, because a row whose id is unreadable is
// not a row a reader can act on.
func columnFloor(c view.Column) int {
	switch {
	case c.Kind == object.KindText:
		return minTextColumn
	case c.Kind == object.KindRef || c.Field == "id":
		// An identity column: readable ids, not `t-pen…`.
		return idColumnFloor
	}
	return minColumn
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

// renderColumnHeader lays out the column labels at the same widths as the
// rows, so the header and the body cannot drift.
func (m *tuiModel) renderColumnHeader(cols []laidOutColumn) string {
	line := ""
	for _, c := range cols {
		line += pad(m.theme.Subtle(sanitizeLine(c.column.Label)), c.width) + " "
	}
	return line
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
	if f.width < 60 {
		labelW = 12
	}
	if labelW > f.width/2 {
		labelW = f.width / 2
	}
	for _, s := range d.Sections {
		out = append(out, " "+m.theme.Subtle(sanitizeLine(s.Label)))
		for _, field := range s.Fields {
			line := "   " + pad(sanitizeLine(field.Label), labelW) + " " +
				truncate(sanitizeLine(field.Value), f.width-4-labelW-1, "…")
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
	prompt := ""
	switch {
	case m.inputID != "":
		prompt = " attach to " + sanitizeLine(m.inputID) + " > "
	case m.stage == 1:
		prompt = " priority 1-10 (default 4) > "
	case m.stage == 2:
		prompt = " defer minutes (0 = now) > "
	default:
		prompt = " new thought prompt > "
	}
	// The cursor sits after the text; the line is truncated so a long input
	// does not overflow the frame.
	text := sanitizeLine(m.input)
	room := f.width - displayWidth(prompt) - 1
	if room < 0 {
		room = 0
	}
	return []string{"", prompt + truncate(text, room, "…") + m.theme.Marker("█")}
}

// bodyLive draws the chat window: the reconstructed turns, tail-following
// unless the user has scrolled back, sized to the frame.
func (m *tuiModel) bodyLive(f frame) []string {
	head := " " + m.theme.Title("chat") + " · " + sanitizeLine(m.liveID) +
		m.theme.Subtle(" · live (esc returns)")
	if m.liveScroll > 0 {
		head += m.theme.Subtle(" · scrolled −" + itoa(m.liveScroll) + " lines")
	}

	lines := m.chatLines()
	if m.liveScroll >= len(lines) {
		m.liveScroll = len(lines) - 1
	}
	if m.liveScroll < 0 {
		m.liveScroll = 0
	}
	// The window is the body height minus the header line, so the chat fills
	// whatever the terminal gives it rather than a fixed 24 lines.
	window := f.bodyHeight() - 1
	if window < 1 {
		window = 1
	}
	end := len(lines) - m.liveScroll
	start := end - window
	if start < 0 {
		start = 0
	}
	if end > len(lines) {
		end = len(lines)
	}

	out := []string{head}
	for i := start; i < end; i++ {
		out = append(out, " "+truncate(lines[i], f.width-1, "…"))
	}
	return out
}

// chatLines flattens the reconstructed turns into display lines
// (assistant content lines are indented; tool blocks offset).
// Non-assistant lines are sanitized — the assistant content is the
// legitimately multi-line context (NAVI-330/333 distinction).
func (m *tuiModel) chatLines() []string {
	lines := []string{}
	for _, t := range m.turns {
		switch t.role {
		case TurnUser:
			for _, l := range strings.Split(t.text, "\n") {
				lines = append(lines, m.theme.User("▸ "+sanitizeLine(l)))
			}
		case TurnAssistant:
			for _, l := range strings.Split(t.text, "\n") {
				lines = append(lines, m.theme.Assistant("◂ "+l))
			}
		case TurnTool:
			if t.tool != "" {
				lines = append(lines, m.theme.Tool("  ⚙ "+sanitizeLine(t.tool)))
			}
			if t.toolIn != "" {
				lines = append(lines, m.theme.Tool("    in: "+sanitizeLine(t.toolIn)))
			}
			if t.toolOut != "" {
				lines = append(lines, m.theme.Tool("    out: "+sanitizeLine(t.toolOut)))
			}
		case TurnSystem:
			lines = append(lines, m.theme.System("  · "+sanitizeLine(t.text)))
		}
	}
	if len(lines) == 0 {
		lines = append(lines, " (no conversation yet — stream events land here)")
	}
	return lines
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
