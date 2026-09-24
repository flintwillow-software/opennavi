// Package tui implements the Bubble Tea portal client (S000406): the
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

	"github.com/flintwillow-software/opennavi/internal/interfaces"
	"github.com/flintwillow-software/opennavi/internal/thought"
)

// LocalIdentity is the identity the in-process front door presents.
const LocalIdentity = "local"

// DefaultFeedCap bounds the TUI's subscription mailbox (bounded
// drop-oldest; ReadAfter reports gaps as a UI notice).
const DefaultFeedCap = 1024

// LocalAuthorizer trusts the local interactive user for every portal
// action — the pre-IAM stand-in for the console's trust boundary (the
// CLI already trusts the local user; IAM later refines this). Remote
// control stays fail-closed until IAM lands.
type LocalAuthorizer struct{}

func (a LocalAuthorizer) Authorize(action interfaces.PortalAction, identity string) error {
	return nil
}

// Transport is the portal attachment seam: one interface so the same
// model serves in-process (direct Portal) and remote (gateway HTTP
// cursor-poll + control POST). Feed returns the subscription mailbox
// the model drains; control ops return result-or-error for immediate
// UI feedback (stream acks arrive on the feed where the portal emits
// them).
type Transport interface {
	// Feed returns this transport's subscription mailbox.
	Feed() *interfaces.PortalMailbox
	// Snapshot returns current queue state for seeding (in-process:
	// full; remote: converges from events, may return empty).
	Snapshot() ([]*thought.Thought, error)
	Steer(thoughtID, text string) error
	Bump(thoughtID string, prio int) error
	Pause(thoughtID string) error
	Resume(thoughtID string) error
	Cancel(thoughtID string) error
	Retry(thoughtID string) error
	// Enqueue creates a session thought (S000406): prompt with
	// optional priority (1-10; 0 = default) and not-before time
	// (zero = due now). In-process thoughts are foreground-tagged so
	// they claim the foreground slot (S010003).
	Enqueue(prompt string, prio int, deferUntil time.Time) error
}

// ── in-process transport ─────────────────────────────────────────────────

// inProcessTransport attaches directly to a portal built over the
// navi's own queue with the local-trust authorizer: the console user
// is the trusted principal pre-IAM. The daemon's served portal is
// untouched (and stays fail-closed until IAM lands).
type inProcessTransport struct {
	portal *interfaces.Portal
	feed   *interfaces.PortalMailbox
}

// NewInProcess builds the in-process transport over the navi's queue.
func NewInProcess(q *thought.ThoughtQueue) (*inProcessTransport, error) {
	if q == nil {
		return nil, errors.New("tui: in-process transport requires a queue")
	}
	p := interfaces.NewPortal(q, LocalAuthorizer{})
	return &inProcessTransport{portal: p, feed: p.Subscribe(DefaultFeedCap)}, nil
}

func (t *inProcessTransport) Feed() *interfaces.PortalMailbox { return t.feed }
func (t *inProcessTransport) Snapshot() ([]*thought.Thought, error) {
	if t.portal.Queue() == nil {
		return []*thought.Thought{}, nil
	}
	return t.portal.Queue().GetThoughts(), nil
}
func (t *inProcessTransport) Steer(thoughtID, text string) error {
	return t.portal.Steer(LocalIdentity, thoughtID, text)
}
func (t *inProcessTransport) Bump(thoughtID string, prio int) error {
	return t.portal.BumpPriority(LocalIdentity, thoughtID, prio)
}
func (t *inProcessTransport) Pause(thoughtID string) error {
	return t.portal.Pause(LocalIdentity, thoughtID)
}
func (t *inProcessTransport) Resume(thoughtID string) error {
	return t.portal.Resume(LocalIdentity, thoughtID)
}
func (t *inProcessTransport) Cancel(thoughtID string) error {
	return t.portal.Cancel(LocalIdentity, thoughtID)
}
func (t *inProcessTransport) Retry(thoughtID string) error {
	return t.portal.Retry(LocalIdentity, thoughtID)
}

// newThoughtID builds a session-created thought id (pattern: the
// headless loop's fg- ids).
func newThoughtID() string {
	return fmt.Sprintf("tui-%d", time.Now().UTC().UnixNano())
}

// Enqueue creates a session thought through the portal: foreground-
// tagged so it claims the foreground slot (S010003), with optional
// priority (1-10; out-of-range or 0 keeps the default 4) and not-before
// time.
func (t *inProcessTransport) Enqueue(prompt string, prio int, deferUntil time.Time) error {
	now := time.Now().UTC()
	if prio < 1 || prio > 10 {
		prio = 4
	}
	th := &thought.Thought{
		ID:         newThoughtID(),
		Prompt:     prompt,
		Priority:   prio,
		Status:     thought.ThoughtPending,
		Foreground: true,
		DeferUntil: deferUntil,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	return t.portal.Enqueue(LocalIdentity, th)
}

// ── remote transport (gateway attachment) ────────────────────────────────

// remoteTransport attaches to a gateway portal endpoint. Events arrive
// via cursor polling (a pump goroutine keeps the feed mailbox topped
// up; ReadAfter semantics are used at both ends, so a gap surfaces as
// lost); control actions POST /portal/control. In dev the daemon wires
// no authorizer, so remote observation/control fail closed (403) until
// IAM — tests exercise the seam against the shim's authorized paths
// with an injected authorizer.
type remoteTransport struct {
	base     string
	identity string
	feed     *interfaces.PortalMailbox
	mu       sync.Mutex
	cursor   int
	closed   bool
	client   *http.Client
}

// NewRemote builds the remote transport at base (http://host:port of a
// gateway portal) for identity.
func NewRemote(base, identity string) (*remoteTransport, error) {
	if base == "" || identity == "" {
		return nil, fmt.Errorf("tui: remote transport requires base and identity")
	}
	t := &remoteTransport{
		base:     base,
		identity: identity,
		feed:     interfaces.NewPortalMailbox(DefaultFeedCap),
		client:   &http.Client{Timeout: 5 * time.Second},
	}
	go t.pump()
	return t, nil
}

func (t *remoteTransport) Feed() *interfaces.PortalMailbox { return t.feed }

// Snapshot: remote clients converge from the event stream (the shim
// has no snapshot endpoint); returns empty for the seed.
func (t *remoteTransport) Snapshot() ([]*thought.Thought, error) { return []*thought.Thought{}, nil }

// eventsURL builds the cursor-poll endpoint.
func (t *remoteTransport) eventsURL() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return fmt.Sprintf("%s/portal/events?identity=%s&from=%d", t.base, t.identity, t.cursor)
}

// pump polls the gateway endpoint and tops up the feed mailbox; it
// stops once the transport is marked closed.
func (t *remoteTransport) pump() {
	time.Sleep(100 * time.Millisecond) // let the server come up
	for {
		t.mu.Lock()
		closed := t.closed
		t.mu.Unlock()
		if closed {
			return
		}
		if resp, err := t.client.Get(t.eventsURL()); err == nil {
			defer resp.Body.Close()
			if blob, rerr := io.ReadAll(resp.Body); rerr == nil {
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
		t.feed.Push(interfaces.PortalEvent{
			Category: interfaces.PortalQueue,
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
		t.feed.Push(interfaces.PortalEvent{
			Category:  interfaces.PortalCategory(str("category")),
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
	resp, err := t.client.Do(req)
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
	resp, err := t.client.Do(req)
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
	ModeList  ModeKind = "list"
	ModeLive  ModeKind = "live"
	ModeInput ModeKind = "input"
)

// row is one queue row.
type row struct {
	thoughtID  string
	status     string
	priority   int
	prompt     string
	age        string // seconds since last activity
	checkpoint bool   // thought carries a checkpoint (parked/resumable)
	session    string // session-log pointer
}

// feedMsg delivers a drained mailbox batch to the model.
type feedMsg struct {
	evs  []interfaces.PortalEvent
	lost bool
}

// tuiModel is the Bubble Tea model: pure state + Init/Update/View, so
// all of it is unit-testable without a terminal (the tea program is
// only assembled by Run).
type tuiModel struct {
	transport Transport
	feed      *interfaces.PortalMailbox
	cursor    int
	rows      []row
	byID      map[string]int
	sel       int
	mode      ModeKind
	liveID    string
	live      []string
	input     string
	inputID   string
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
	return &tuiModel{
		transport: tr,
		feed:      tr.Feed(),
		byID:      make(map[string]int),
		mode:      ModeList,
	}
}

// poll returns the re-arming feed-drain command: a non-blocking
// mailbox read, so the tea event loop never blocks on the portal (a
// slow UI just drains fewer events per tick) — S000406, the
// "within one event delivery" guarantee holds per mailbox push.
func (m *tuiModel) poll() tea.Cmd {
	return tea.Every(150*time.Millisecond, func(t time.Time) tea.Msg {
		evs, next, lost := m.feed.ReadAfter(m.cursor)
		if len(evs) == 0 && !lost {
			return nil
		}
		m.cursor = next
		return feedMsg{evs: evs, lost: lost}
	})
}

// Init seeds from the transport snapshot (S000406) and starts the
// feed poll.
func (m *tuiModel) Init() tea.Cmd {
	if thoughts, err := m.transport.Snapshot(); err == nil {
		m.rebuild(thoughts)
	}
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
		m.applyFeed(msg)
		return m, m.poll()
	}
	return m, m.poll()
}

func (m *tuiModel) View() string { return m.render() }

// rebuild replaces the row set from a snapshot.
func (m *tuiModel) rebuild(thoughts []*thought.Thought) {
	m.rows = []row{}
	m.byID = make(map[string]int)
	for _, t := range thoughts {
		m.upsert(t.ID, string(t.Status), t.EffectivePriority(), t.Prompt, len(t.Checkpoint) > 0, t.SessionID, t.UpdatedAt)
	}
}

func (m *tuiModel) upsert(id, status string, prio int, prompt string, checkpoint bool, session string, at time.Time) {
	if i, ok := m.byID[id]; ok && i < len(m.rows) {
		m.rows[i].status = status
		m.rows[i].priority = prio
		if prompt != "" {
			m.rows[i].prompt = prompt
		}
		m.rows[i].checkpoint = checkpoint
		m.rows[i].session = session
		m.rows[i].age = m.age(at)
		return
	}
	m.rows = append(m.rows, row{
		thoughtID:  id,
		status:     status,
		priority:   prio,
		prompt:     prompt,
		checkpoint: checkpoint,
		session:    session,
		age:        m.age(at),
	})
	m.byID[id] = len(m.rows) - 1
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
		case interfaces.PortalQueue:
			m.queueEvent(ev)
		case interfaces.PortalAck:
			m.ackEvent(ev)
		case interfaces.PortalAgent:
			m.agentEvent(ev)
		}
	}
}

func (m *tuiModel) queueEvent(ev interfaces.PortalEvent) {
	if ev.Kind == "stream_lost" {
		m.lost = true
		m.status = ev.Detail
		return
	}
	// A thought this run never saw arrived: refresh from the snapshot
	// once so the row carries full fields (in-process: complete;
	// remote: converges as events continue).
	if _, ok := m.byID[ev.ThoughtID]; !ok {
		if thoughts, _ := m.transport.Snapshot(); len(thoughts) > 0 {
			m.rebuild(thoughts)
		} else {
			m.upsert(ev.ThoughtID, ev.Kind, 4, "(remote)", false, "", time.Now().UTC())
		}
		_ = ok
	}
	if i, ok := m.byID[ev.ThoughtID]; ok && i < len(m.rows) {
		m.rows[i].status = displayStatus(ev.Kind)
		m.rows[i].age = m.age(time.Now().UTC())
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

func (m *tuiModel) ackEvent(ev interfaces.PortalEvent) {
	if ev.Kind == "message_routed" {
		if ev.Delivery == "" {
			m.status = fmt.Sprintf("✗ %s: %s", ev.ThoughtID, ev.Detail)
		} else {
			m.status = fmt.Sprintf("✓ delivered to %s (%s)", ev.ThoughtID, ev.Delivery)
		}
		return
	}
	m.status = fmt.Sprintf("%s: %s", ev.ThoughtID, ev.Detail)
}

func (m *tuiModel) agentEvent(ev interfaces.PortalEvent) {
	if m.mode != ModeLive || ev.ThoughtID != m.liveID {
		return
	}
	line := ev.Kind
	if ev.Detail != "" {
		line += " " + m.clip(ev.Detail, 60)
	}
	m.live = append(m.live, m.clip(line, 100))
	if len(m.live) > 200 {
		m.live = m.live[len(m.live)-180:]
	}
}

func (m *tuiModel) clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// key handles a key press; returns true when the user quits.
func (m *tuiModel) key(k tea.KeyMsg) bool {
	switch k.String() {
	case "q", "ctrl+c":
		return true
	case "esc", "escape", "l":
		if m.mode == ModeLive || m.mode == ModeInput {
			m.mode = ModeList
			m.input = ""
			m.inputID = ""
			m.enqueue = false
			m.stage = 0
		}
	case "up", "k":
		if m.mode == ModeList && m.sel > 0 {
			m.sel--
		}
	case "down", "j":
		if m.mode == ModeList && m.sel < len(m.rows)-1 {
			m.sel++
		}
	case "enter":
		switch m.mode {
		case ModeList:
			if len(m.rows) > 0 {
				m.liveID = m.rows[m.sel].thoughtID
				m.live = []string{}
				m.mode = ModeLive
			}
		case ModeInput:
			if m.inputID != "" {
				// Attach-message flow (steer): unchanged.
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
			} else {
				// Enqueue flow: prompt -> optional priority -> optional
				// deferral (minutes); the enqueued event comes back on
				// the stream and lands the row in the queue view.
				m.enqueueStep()
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
		if m.mode == ModeList && len(m.rows) > 0 {
			m.enqueue = false
			m.inputID = m.rows[m.sel].thoughtID
			m.stage = 0
			m.input = ""
			m.mode = ModeInput
		}
	case "p":
		if m.mode == ModeList && len(m.rows) > 0 {
			m.doAction("bump", m.rows[m.sel].thoughtID, maxPriority(m.rows[m.sel].priority-1))
		}
	case "P":
		if m.mode == ModeList && len(m.rows) > 0 {
			m.doAction("bump", m.rows[m.sel].thoughtID, minPriority(m.rows[m.sel].priority+1))
		}
	case "s":
		if m.mode == ModeList && len(m.rows) > 0 {
			m.doAction("pause", m.rows[m.sel].thoughtID, 0)
		}
	case "r":
		if m.mode == ModeList && len(m.rows) > 0 {
			m.doAction("resume", m.rows[m.sel].thoughtID, 0)
		}
	case "c":
		if m.mode == ModeList && len(m.rows) > 0 {
			m.doAction("cancel", m.rows[m.sel].thoughtID, 0)
		}
	case "R":
		if m.mode == ModeList && len(m.rows) > 0 {
			m.doAction("retry", m.rows[m.sel].thoughtID, 0)
		}
	case "backspace":
		if m.mode == ModeInput && len(m.input) > 0 {
			m.input = m.input[:len(m.input)-1]
		}
	default:
		if m.mode == ModeInput && len(k.Runes) > 0 {
			for _, r := range k.Runes {
				m.input += string(r)
			}
		}
	}
	return false
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

func (m *tuiModel) doAction(kind, id string, prio int) {
	var err error
	switch kind {
	case "bump":
		err = m.transport.Bump(id, prio)
	case "pause":
		err = m.transport.Pause(id)
	case "resume":
		err = m.transport.Resume(id)
	case "cancel":
		err = m.transport.Cancel(id)
	case "retry":
		err = m.transport.Retry(id)
	}
	if err != nil {
		m.status = fmt.Sprintf("%s %s failed: %v", kind, id, err)
	} else {
		m.status = fmt.Sprintf("%s → %s", kind, id)
	}
}

// render draws the current screen (plain text; Bubble Tea repaints it
// on every Update).
func (m *tuiModel) render() string {
	out := " navi · thought queue (portal" + terminalSuffix(m) + ")\n"
	out += repeat("-", 60) + "\n"
	switch m.mode {
	case ModeList:
		out += m.renderList()
	case ModeLive:
		out += m.renderLive()
	case ModeInput:
		out += m.renderInput()
	}
	out += repeat("-", 60) + "\n"
	out += " " + m.status + "\n"
	out += " q quit · j/k move · enter/i live · n new · a attach · p/P priority · s pause · r resume · c cancel · R retry · esc back\n"
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

func (m *tuiModel) renderList() string {
	out := ""
	if len(m.rows) == 0 {
		out += " (queue empty)\n"
		return out
	}
	for i, r := range m.rows {
		mark := " "
		if i == m.sel {
			mark = ">"
		}
		flags := "  "
		if r.checkpoint {
			flags = " C "
		}
		if r.session != "" {
			flags += "S"
		}
		out += fmt.Sprintf(" %s %-3s %-11s p%d %6s %s %s\n",
			mark, r.thoughtID, r.status, r.priority, r.age, flags, m.clip(r.prompt, 30))
	}
	return out
}

func (m *tuiModel) renderInput() string {
	if m.inputID != "" {
		return fmt.Sprintf(" attach to %s > %s█\n", m.inputID, m.input)
	}
	switch m.stage {
	case 0:
		return fmt.Sprintf(" new thought prompt > %s█\n", m.input)
	case 1:
		return fmt.Sprintf(" priority 1-10 (default 4) > %s█\n", m.input)
	case 2:
		return fmt.Sprintf(" defer minutes (0 = now) > %s█\n", m.input)
	}
	return ""
}

func (m *tuiModel) renderLive() string {
	out := fmt.Sprintf(" live: %s (esc returns)\n", m.liveID)
	for _, line := range m.live {
		out += " " + line + "\n"
	}
	return out
}

// ── run entry ───────────────────────────────────────────────────────────

// Run drives the TUI over tr until the user quits; the tea program
// keeps its own signal handler DISABLED — the navi runtime's signal
// ctx owns SIGINT (park-with-checkpoint semantics, S010003).
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
