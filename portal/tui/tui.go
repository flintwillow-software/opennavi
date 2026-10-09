// Package tui implements the Bubble Tea portal client: the
// human door into the house — a terminal UI bound to the navi's
// thought queue through the portal surface (NAVI-318). The same model
// runs in-process (binary run, NAVI-320) or against the gateway
// attachment; the transport sits behind one seam. The queue is only
// ever touched through the portal — never directly under q.mu — and
// the feed mailbox is drained via tea Cmds (poll/cursor-resume), so
// a slow UI never blocks the queue fanout.
//
// Bubble Tea (charmbracelet/bubbletea) is the repo's first
// third-party dependency: an explicitly approved, documented exception
// scoped to NAVI-319 (case-study thought-loop, Open Design Point #4).
// The no-new-deps convention holds everywhere else.
package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/portal/gateway"
	"github.com/flintwillow-software/opennavi/portal/view"
	"github.com/flintwillow-software/opennavi/thought"
)

// LocalIdentity is the identity the in-process front door presents.
const LocalIdentity = "local"

// DefaultFeedCap bounds the TUI's subscription mailbox (bounded
// drop-oldest; ReadAfter reports gaps as a UI notice).
const DefaultFeedCap = 1024

// Transport is the portal attachment seam: one interface so the same
// model serves in-process (direct Portal) and remote (gateway HTTP
// cursor-poll + control POST). Feed returns the subscription mailbox
// the model drains; control ops return result-or-error for immediate
// UI feedback (stream acks arrive on the feed where the portal emits
// them).
type Transport interface {
	// Feed returns this transport's subscription mailbox.
	Feed() portal.Subscription
	// Objects is the typed-object surface the model renders and acts
	// through (ADR 0017): the same contract in-process and remote, so the
	// views are descriptor-driven in both modes. Reading the queue through it
	// is what replaced the old remote snapshot (which could only converge
	// from events).
	Objects() object.Surface
	Steer(thoughtID, text string) error
	Bump(thoughtID string, prio int) error
	Pause(thoughtID string) error
	Resume(thoughtID string) error
	Cancel(thoughtID string) error
	Retry(thoughtID string) error
	// Enqueue creates a session thought: prompt with
	// optional priority (1-10; 0 = default) and not-before time
	// (zero = due now). In-process thoughts are foreground-tagged so
	// they claim the foreground slot.
	Enqueue(prompt string, prio int, deferUntil time.Time) error
}

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
func (t *inProcessTransport) Steer(thoughtID, text string) error {
	return t.ts.RouteMessage(thoughtID, text)
}
func (t *inProcessTransport) Bump(thoughtID string, prio int) error {
	return t.ts.BumpPriority(thoughtID, prio)
}
func (t *inProcessTransport) Pause(thoughtID string) error  { return t.ts.Pause(thoughtID) }
func (t *inProcessTransport) Resume(thoughtID string) error { return t.ts.Resume(thoughtID) }
func (t *inProcessTransport) Cancel(thoughtID string) error { return t.ts.Cancel(thoughtID) }
func (t *inProcessTransport) Retry(thoughtID string) error  { return t.ts.Retry(thoughtID) }

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

// ── remote transport (gateway attachment) ────────────────────────────────

// remoteTransport attaches to a gateway portal endpoint. Events arrive
// via cursor polling (a pump goroutine keeps the feed mailbox topped
// up; ReadAfter semantics are used at both ends, so a gap surfaces as
// lost); control actions POST /portal/control. The embedded
// gateway.Client binds the same object surface an in-process portal is
// handed (ADR 0017), so a remote front-end lists and reads the navi's
// objects rather than converging from events alone. In dev the reference
// navi wires no authorizer, so remote observation/control fail closed (403)
// until IAM — tests exercise the seam against the shim's authorized paths
// with an injected authorizer.
type remoteTransport struct {
	*gateway.Client

	base     string
	identity string
	feed     *portal.Mailbox
	mu       sync.Mutex
	cursor   int
	closed   bool
	httpc    *http.Client
}

// NewRemote builds the remote transport at base (http://host:port of a
// gateway portal) for identity.
func NewRemote(base, identity string) (*remoteTransport, error) {
	if base == "" || identity == "" {
		return nil, fmt.Errorf("tui: remote transport requires base and identity")
	}
	t := &remoteTransport{
		Client:   gateway.NewClient(base, identity),
		base:     base,
		identity: identity,
		feed:     portal.NewMailbox(DefaultFeedCap),
		httpc:    &http.Client{Timeout: 5 * time.Second},
	}
	go t.pump()
	return t, nil
}

func (t *remoteTransport) Feed() portal.Subscription { return t.feed }

// Objects is the remote binding of the typed-object surface: the gateway
// client this transport embeds.
func (t *remoteTransport) Objects() object.Surface { return t.Client }

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

// eventsURL builds the cursor-poll endpoint.
func (t *remoteTransport) eventsURL() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return fmt.Sprintf("%s/portal/events?identity=%s&from=%d", t.base, t.identity, t.cursor)
}

// pump polls the gateway endpoint and tops up the feed mailbox, and fills
// the descriptor cache once the server is reachable; it stops once the
// transport is marked closed.
func (t *remoteTransport) pump() {
	time.Sleep(100 * time.Millisecond) // let the server come up
	for {
		t.mu.Lock()
		closed := t.closed
		t.mu.Unlock()
		if closed {
			return
		}
		// Descriptors are frozen, so one successful fetch is enough; retry
		// until the server answers (the generic toolkit renders from these).
		if len(t.Types()) == 0 {
			_ = t.RefreshTypes()
		}
		if resp, err := t.httpc.Get(t.eventsURL()); err == nil {
			// Read and close eagerly: a defer inside this loop would not run
			// until the pump exits, leaking one connection per poll.
			blob, rerr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if rerr == nil {
				t.ingest(string(blob))
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ingest parses one /portal/events JSON response and pushes events
// into the feed, advancing the cursor.
func (t *remoteTransport) ingest(body string) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		return
	}
	evs, _ := doc["events"].([]any)
	if v, ok := doc["lost"].(bool); ok && v {
		t.feed.Push(portal.Event{
			Category: portal.CategoryQueue,
			Kind:     "stream_lost",
			Detail:   "portal stream dropped events (resume gap)",
			At:       time.Now().UTC(),
		})
	}
	for _, e := range evs {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		str := func(k string) string {
			if v, ok := m[k].(string); ok {
				return v
			}
			return ""
		}
		t.feed.Push(portal.Event{
			Category:  portal.Category(str("category")),
			Kind:      str("kind"),
			ThoughtID: str("thoughtID"),
			Detail:    str("detail"),
			Delivery:  str("delivery"),
			At:        time.Now().UTC(),
		})
	}
	if n, ok := doc["next"].(float64); ok {
		t.mu.Lock()
		t.cursor = int(n)
		t.mu.Unlock()
	}
}

// control POSTs a control action with the identity; non-200 statuses
// (403 fail-closed without an authorizer) become errors.
func (t *remoteTransport) control(action, thoughtID, text string, prio int) error {
	payload := map[string]any{
		"action":    action,
		"thoughtID": thoughtID,
	}
	if text != "" {
		payload["text"] = text
	}
	if prio > 0 {
		payload["priority"] = prio
	}
	blob, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("tui: encode control: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/portal/control?identity=%s", t.base, t.identity), strings.NewReader(string(blob)))
	if err != nil {
		return fmt.Errorf("tui: build control: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("tui: control %s: %w", action, err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("tui: control %s on %s: unauthorized (fail closed; no authorizer on the gateway)", action, thoughtID)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tui: control %s on %s: http %d: %s", action, thoughtID, resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	return nil
}

func (t *remoteTransport) Steer(thoughtID, text string) error {
	return t.control("steer", thoughtID, text, 0)
}
func (t *remoteTransport) Bump(thoughtID string, prio int) error {
	return t.control("bump", thoughtID, "", prio)
}
func (t *remoteTransport) Pause(thoughtID string) error { return t.control("pause", thoughtID, "", 0) }
func (t *remoteTransport) Resume(thoughtID string) error {
	return t.control("resume", thoughtID, "", 0)
}
func (t *remoteTransport) Cancel(thoughtID string) error {
	return t.control("cancel", thoughtID, "", 0)
}
func (t *remoteTransport) Retry(thoughtID string) error { return t.control("retry", thoughtID, "", 0) }

// Enqueue posts a session thought through the shim's enqueue control
// (`{id, prompt, priority}`; the id is generated client-side). The
// optional not-before time rides along as an ISO field for forward-
// compat — the current shim ignores unknown fields, so remote
// deferral is an in-process capability until the shim learns it.
func (t *remoteTransport) Enqueue(prompt string, prio int, deferUntil time.Time) error {
	if prio < 1 || prio > 10 {
		prio = 4
	}
	payload := map[string]any{
		"action": "enqueue",
		"id":     newThoughtID(),
		"prompt": prompt,
	}
	if prio > 0 {
		payload["priority"] = prio
	}
	if !deferUntil.IsZero() {
		payload["defer_until"] = deferUntil.UTC().Format(time.RFC3339Nano)
	}
	blob, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("tui: encode control: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/portal/control?identity=%s", t.base, t.identity), strings.NewReader(string(blob)))
	if err != nil {
		return fmt.Errorf("tui: build control: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("tui: control enqueue: %w", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("tui: enqueue: unauthorized (fail closed; no authorizer on the gateway)")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tui: enqueue: http %d: %s", resp.StatusCode, strings.TrimSpace(string(rb)))
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
// (NAVI-380 — advancing it in the poll command raced and replayed events).
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
	// polling is true while a feed-drain command is in flight. Every
	// Update path re-arms the drain, so without the guard a key press
	// during an in-flight poll stacks a second timer; two timers read
	// overlapping ranges from the non-destructive mailbox and each applies
	// them, which rendered every event once per timer (NAVI-380 follow-up).
	polling bool
	// objects is the typed-object surface the model renders and acts through
	// (ADR 0017) — the same contract in-process and remote.
	objects object.Surface
	// types is the exposed descriptor set (loaded once the surface answers)
	// and typeIdx selects which one the list renders, so every exposed type is
	// reachable without a per-type screen.
	types   []object.Type
	typeIdx int
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
	}
	m.loadTypes()
	return m
}

// listPage bounds the page the list renders.
const listPage = 200

// loadTypes caches the exposed descriptors. A remote surface fetches them on
// its own schedule, so this is retried until it answers.
func (m *tuiModel) loadTypes() {
	if m.objects == nil {
		return
	}
	if types := m.objects.Types(); len(types) > 0 {
		m.types = types
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

// poll arms the feed-drain command: one non-blocking mailbox read a
// cadence, so the tea event loop never blocks on the portal (a slow UI
// just drains fewer events per tick). AT MOST ONE DRAIN IS IN FLIGHT:
// the guard makes the re-arm idempotent, and Update's cursor filter
// keeps a duplicate delivery harmless if one ever slips through
// (NAVI-380 follow-up — the read is non-destructive, so re-reading an
// applied range re-appends it).
//
// The wrapper message is always non-nil (even for an empty read): the
// idle decision — stop draining until the next user input — belongs to
// Update, because a command runs on a Bubble Tea goroutine and must
// never touch the model (NAVI-380).
func (m *tuiModel) poll() tea.Cmd {
	if m.polling {
		return nil // a drain is already armed; re-arming would stack a timer
	}
	m.polling = true
	// Capture the cursor HERE, on the Update goroutine: the returned
	// command runs on a Bubble Tea goroutine and must not read or write
	// the model (NAVI-380).
	cursor := m.cursor
	return tea.Every(150*time.Millisecond, func(time.Time) tea.Msg {
		evs, next, lost := m.feed.ReadAfter(cursor)
		return feedMsg{evs: evs, next: next, lost: lost}
	})
}

// anyRunning reports whether any row is running (drives the idle-free
// ticking refresh).
func (m *tuiModel) anyRunning() bool {
	for _, row := range m.list.Rows {
		if m.statusOf(row.ID) == "running" {
			return true
		}
	}
	return false
}

// Init seeds from the transport snapshot and starts the
// feed poll.
func (m *tuiModel) Init() tea.Cmd {
	m.loadTypes()
	m.refreshList()
	return m.poll()
}

// Update is the Elm update: keys and feed batches mutate the model.
func (m *tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.key(msg) {
			return m, tea.Quit
		}
		return m, m.poll()
	case feedMsg:
		// The drain delivered: clear the guard, apply the batch, and
		// re-arm — unless the mailbox was genuinely empty and nothing is
		// running (the idle-free tick of NAVI-335).
		m.polling = false
		raw := msg.evs
		// Apply only events past the consumer cursor, and never move the
		// cursor backwards: a duplicate delivery of an applied range must
		// not re-append its turns (NAVI-380 follow-up).
		msg.evs = unseen(msg.evs, m.cursor)
		if len(raw) > 0 && len(msg.evs) == 0 {
			msg.lost = false // a fully-applied duplicate reports no gap
		}
		if msg.next > m.cursor {
			m.cursor = msg.next
		}
		m.applyFeed(msg)
		if len(raw) == 0 && !msg.lost && !m.anyRunning() {
			return m, nil // idle: sleep until user input re-arms the drain
		}
		return m, m.poll()
	}
	return m, m.poll()
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

func (m *tuiModel) clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
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
			if err := m.transport.Steer(m.inputID, m.input); err != nil {
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
func (m *tuiModel) doAction(action, id string, prio int) {
	t, ok := m.currentType()
	if !ok || m.objects == nil {
		return
	}
	args := map[string]any{}
	if action == "bump" {
		args["priority"] = prio
	}
	if err := m.objects.Act(t.Name, id, action, args); err != nil {
		m.status = fmt.Sprintf("%s %s failed: %v", action, id, err)
		return
	}
	m.status = fmt.Sprintf("%s → %s", action, id)
}

// render draws the current screen (plain text; Bubble Tea repaints it
// on every Update).
func (m *tuiModel) render() string {
	title := "thought queue"
	if t, ok := m.currentType(); ok && t.Label != "" {
		title = t.Label
	}
	out := " navi · " + title + " (portal" + terminalSuffix(m) + ")\n"
	out += repeat("-", 60) + "\n"
	switch m.mode {
	case ModeList:
		out += m.renderList()
	case ModeDetail:
		out += m.renderDetail()
	case ModeLive:
		out += m.renderLive()
	case ModeInput:
		out += m.renderInput()
	}
	out += repeat("-", 60) + "\n"
	if m.mode == ModeList && m.active != "" {
		out += " ▸ " + sanitizeLine(m.activeID) + " · " + sanitizeLine(m.active) + "\n"
	}
	out += " " + sanitizeLine(m.status) + "\n"
	out += " q quit · j/k move · enter open · t type · n new · a attach · p/P priority · s pause · r resume · c cancel · R retry · esc back\n"
	return out
}

func terminalSuffix(m *tuiModel) string {
	if m.lost {
		return " · STREAM GAP"
	}
	return ""
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

// renderList draws the selected type's page from the descriptor-resolved
// columns, decorating each row with the live overlay (status, a derived
// blocked flag, a checkpoint marker, a running preview).
func (m *tuiModel) renderList() string {
	t, ok := m.currentType()
	if !ok {
		return " (no object types exposed)\n"
	}
	out := ""
	if len(m.types) > 1 {
		out += " type: "
		for i, ty := range m.types {
			if i > 0 {
				out += " · "
			}
			label := ty.Label
			if label == "" {
				label = ty.Name
			}
			if i == m.typeIdx {
				out += "[" + sanitizeLine(label) + "]"
			} else {
				out += sanitizeLine(label)
			}
		}
		out += "\n"
	}
	if len(m.list.Rows) == 0 {
		return out + " (" + sanitizeLine(t.Name) + " empty)\n"
	}
	out += "    "
	for _, c := range m.list.Columns {
		out += fmt.Sprintf("%-*s ", columnWidth(c), m.clip(sanitizeLine(c.Label), columnWidth(c)))
	}
	out += "\n"
	for i, row := range m.list.Rows {
		mark := " "
		if i == m.sel {
			mark = ">"
		}
		b := m.badges[row.ID]
		flags := "  "
		if b.checkpoint {
			flags = " C "
		}
		if v, _ := m.items[row.ID].Fields["verification"].(bool); v {
			flags += "V"
		}
		out += " " + mark + " "
		for _, c := range m.list.Columns {
			value := cellValue(row, c.Field)
			if c.Field == "status" {
				value = m.statusOf(row.ID)
				if b.blocked {
					value = "blocked"
				}
			}
			out += fmt.Sprintf("%-*s ", columnWidth(c), m.clip(sanitizeLine(value), columnWidth(c)))
		}
		out += flags + " "
		if b.activity != "" {
			out += " «" + m.clip(sanitizeLine(b.activity), 24)
		}
		out += "\n"
	}
	return out
}

// columnWidth bounds a rendered column so one long value cannot push the rest
// off the screen. A text column (a prompt) gets room; everything else sizes to
// its label with a floor.
func columnWidth(c view.Column) int {
	if c.Kind == object.KindText {
		return 30
	}
	w := len(c.Label)
	if w < 10 {
		w = 10
	}
	if w > 30 {
		w = 30
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

// renderDetail draws one object's descriptor-resolved sections and the actions
// its type grants.
func (m *tuiModel) renderDetail() string {
	if m.detail == nil {
		return ""
	}
	d := m.detail
	label := d.Type.Label
	if label == "" {
		label = d.Type.Name
	}
	out := " " + sanitizeLine(label) + " · " + sanitizeLine(d.Title) + "\n"
	for _, s := range d.Sections {
		out += " " + sanitizeLine(s.Label) + "\n"
		for _, f := range s.Fields {
			out += fmt.Sprintf("   %-18s %s\n", sanitizeLine(f.Label), sanitizeLine(f.Value))
		}
	}
	if len(d.Actions) > 0 {
		out += " actions:"
		for _, a := range d.Actions {
			out += " " + sanitizeLine(a.Name)
			if a.Confirm {
				out += "!"
			}
		}
		out += "\n"
	}
	return out
}

func (m *tuiModel) renderInput() string {
	if m.inputID != "" {
		return fmt.Sprintf(" attach to %s > %s█\n", sanitizeLine(m.inputID), sanitizeLine(m.input))
	}
	switch m.stage {
	case 0:
		return fmt.Sprintf(" new thought prompt > %s█\n", sanitizeLine(m.input))
	case 1:
		return fmt.Sprintf(" priority 1-10 (default 4) > %s█\n", sanitizeLine(m.input))
	case 2:
		return fmt.Sprintf(" defer minutes (0 = now) > %s█\n", sanitizeLine(m.input))
	}
	return ""
}

func (m *tuiModel) renderLive() string {
	out := fmt.Sprintf(" chat: %s · live (esc returns)", m.liveID)
	if m.liveScroll > 0 {
		out += fmt.Sprintf(" · scrolled −%d lines", m.liveScroll)
	}
	out += "\n"
	lines := m.chatLines()
	if m.liveScroll >= len(lines) {
		m.liveScroll = len(lines) - 1
	}
	start := len(lines) - m.liveScroll - 24 // tail window (24 lines)
	if start < 0 {
		start = 0
	}
	for i := start; i < len(lines); i++ {
		out += " " + lines[i] + "\n"
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
				lines = append(lines, "▸ "+sanitizeLine(l))
			}
		case TurnAssistant:
			for _, l := range strings.Split(t.text, "\n") {
				lines = append(lines, "◂ "+l)
			}
		case TurnTool:
			if t.tool != "" {
				lines = append(lines, "  ⚙ "+sanitizeLine(t.tool))
			}
			if t.toolIn != "" {
				lines = append(lines, "    in: "+sanitizeLine(t.toolIn))
			}
			if t.toolOut != "" {
				lines = append(lines, "    out: "+sanitizeLine(t.toolOut))
			}
		case TurnSystem:
			lines = append(lines, "  · "+sanitizeLine(t.text))
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
