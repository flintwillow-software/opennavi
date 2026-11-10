package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/flintwillow-software/opennavi/internal/agent"
	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/portal/view"
	"github.com/flintwillow-software/opennavi/thought"
)

// portalAck publishes the ack the portal emits for a routed message, so a test
// can assert the delivery is reflected in the transcript.
func portalAck(thoughtID, delivery string) portal.Event {
	return portal.Event{
		Category: portal.CategoryAck, Kind: "message_routed",
		ThoughtID: thoughtID, Delivery: delivery, Detail: delivery,
	}
}

// viewDetailStub is a resolved detail view for the focused() test: a detail
// view carries the object, which is where its id comes from.
var viewDetailStub = view.Detail{
	Object: object.Object{Type: "todo", ID: "d1"},
	Title:  "d1",
}

// This file covers NAVI-587: home is the default screen and shows the running
// thought with a persistent steer input; the action keys bind to the focused
// object rather than to list mode; home follows the running thought as the
// queue changes; and the empty state enqueues inline.

// runningModel builds a model with one RUNNING thought (claimed from the
// queue, so the status overlay says running) and returns it with the broker
// that can publish agent events for it.
func runningModel(t *testing.T) (*tuiModel, *thought.Queue, *multiSurface) {
	t.Helper()
	q := newQueue(t)
	s := &multiSurface{q: q}
	m := newMultiModel(t, q, s)
	if err := q.Enqueue(th("r1", 4)); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("no claim")
	}
	m.refreshList()
	m.trackRunning("r1")
	return m, q, s
}

// TestHomeIsTheDefaultScreen: the TUI opens on home showing the running
// thought, not on the queue list. This is the whole point of the issue.
func TestHomeIsTheDefaultScreen(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q)
	m := NewModel(tport)
	if m.mode != ModeHome {
		t.Fatalf("default mode = %q, want home", m.mode)
	}
	if err := q.Enqueue(th("h1", 4)); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("no claim")
	}
	drain(m)

	if m.mode != ModeHome {
		t.Fatalf("mode changed to %q after a lifecycle event", m.mode)
	}
	if m.homeID != "h1" {
		t.Fatalf("home tracks %q, want h1", m.homeID)
	}
	view := m.View()
	if !strings.Contains(view, "home · h1") {
		t.Errorf("home does not name the running thought:\n%s", view)
	}
	if !strings.Contains(view, "task h1") {
		t.Errorf("home does not show the thought's conversation:\n%s", view)
	}
	// Home has NO editor: it is for watching, and `enter` opens the thought's
	// own page, where the steer box lives.
	if strings.Contains(view, "─ steer ─") {
		t.Errorf("home has a steer box; it should be watch-only:\n%s", view)
	}
	if !strings.Contains(view, "home · h1") {
		t.Errorf("home does not name the thought it is watching:\n%s", view)
	}
}

// TestHomeSteerDispatchesThroughTheSurface: typing and pressing enter steers
// the displayed thought, and the delivery ack is reflected in the status line.
// The write goes through object.Surface.Act — the one write path — so the test
// asserts on the surface, not on a private field.
// TestSteerDispatchesThroughTheSurface: steering goes through the one write
// path from the THOUGHT'S OWN PAGE. Home is watch-only (NAVI-604), so the
// editor lives on the opened conversation.
func TestSteerDispatchesThroughTheSurface(t *testing.T) {
	steerLog() // clear injections recorded by earlier cases
	m, q, s := runningModel(t)
	m.enterLive("r1")

	keyRunes(t, m, "focus on the tests")
	if m.input != "focus on the tests" {
		t.Fatalf("input = %q, want the typed text", m.input)
	}
	keyEnter(m)

	s.mu.Lock()
	acted := append([]string(nil), s.acted...)
	s.mu.Unlock()
	if len(acted) != 1 || acted[0] != "thought/r1/steer" {
		t.Fatalf("surface saw %v, want thought/r1/steer", acted)
	}
	if m.input != "" {
		t.Errorf("input not cleared on send: %q", m.input)
	}
	if !strings.Contains(m.status, "steered r1") {
		t.Errorf("status does not reflect the steer: %q", m.status)
	}
	// The message was injected into the right running thought.
	if got := steerLog(); len(got) != 1 || got[0] != "r1|focus on the tests" {
		t.Fatalf("steering bridge saw %v, want [r1|focus on the tests]", got)
	}
	_ = q
}

// TestHomeSteerAckLandsInTheTranscript: the ack arrives on the stream and
// renders as a user turn in the conversation home is showing (the "delivery
// ack reflected in the transcript" half of the done-when).
func TestSteerAckLandsInTheTranscript(t *testing.T) {
	q := newQueue(t)
	tport, broker := newInProcess(t, q)
	m := NewModel(tport)
	if err := q.Enqueue(th("a1", 4)); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("no claim")
	}
	drain(m)
	m.enterLive("a1")

	keyRunes(t, m, "please continue")
	keyEnter(m)
	// The ack the portal emits for a routed message.
	broker.Publish(portalAck("a1", "context-append"))
	drain(m)

	if !strings.Contains(m.View(), "steered") {
		t.Errorf("ack did not land in the transcript:\n%s", m.View())
	}
}

// TestHomeEmptyStateEnqueues: with nothing running, home offers enqueue inline
// (prompt -> priority -> defer) through the transport, so the screen is never
// a dead end.
func TestHomeEmptyStateEnqueues(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q)
	m := NewModel(tport)
	_ = m.Init()
	if m.homeID != "" {
		t.Fatalf("precondition: nothing running, tracked %q", m.homeID)
	}
	// Nothing running and nothing queued: the SPLASH, which names the hotkey.
	if !strings.Contains(m.View(), "idle") {
		t.Fatalf("home does not show its idle splash:\n%s", m.View())
	}
	if !strings.Contains(m.View(), "to queue a new thought") {
		t.Errorf("empty state does not say how to enqueue:\n%s", m.View())
	}

	// `n` opens the enqueue input from home, and the staged flow runs there.
	keyRunes(t, m, "n")
	if m.mode != ModeInput {
		t.Fatalf("n did not open the enqueue input: mode=%q", m.mode)
	}
	if !strings.Contains(m.View(), "─ new thought ─") {
		t.Errorf("the input box is not labelled with its action:\n%s", m.View())
	}
	keyRunes(t, m, "draft the migration guide")
	keyEnter(m)
	if m.stage != 1 {
		t.Fatalf("stage = %d, want 1 (priority) after the prompt", m.stage)
	}
	if !strings.Contains(m.View(), "─ priority ─") {
		t.Errorf("the input box did not become the priority stage:\n%s", m.View())
	}
	keyEnter(m) // default priority
	if m.stage != 2 {
		t.Fatalf("stage = %d, want 2 (defer)", m.stage)
	}
	keyEnter(m) // due now

	// The flow returns to the PAGE it started from — home here — so an
	// enqueue begun on home does not dump the reader on another screen.
	if m.mode != ModeHome {
		t.Errorf("after enqueuing from home, mode = %q, want home", m.mode)
	}
	got := q.GetThoughts()
	if len(got) != 1 || got[0].Prompt != "draft the migration guide" {
		t.Fatalf("enqueue did not reach the queue: %+v", got)
	}
	if !strings.Contains(m.status, "enqueued") {
		t.Errorf("status does not confirm the enqueue: %q", m.status)
	}
}

// TestHomeFollowsTheRunningThought: home tracks the running thought as the
// queue changes — when the tracked one completes, the next running one takes
// its place, and the screen re-derives on the lifecycle event rather than at
// start-up.
func TestHomeFollowsTheRunningThought(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q)
	m := NewModel(tport)
	_ = m.Init()

	// The running thought is claimed and tracked.
	if err := q.Enqueue(th("first", 4)); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("no claim for first")
	}
	drain(m)
	if m.homeID != "first" {
		t.Fatalf("home tracks %q, want first", m.homeID)
	}

	// It completes; a second thought is already running.
	if err := q.Enqueue(th("second", 4)); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("no claim for second")
	}
	if err := q.Complete("first", ""); err != nil {
		t.Fatal(err)
	}
	drain(m)

	if m.homeID != "second" {
		t.Fatalf("home tracks %q, want second after the first completed", m.homeID)
	}
	if !strings.Contains(m.View(), "home · second") {
		t.Errorf("home did not re-derive onto the next running thought:\n%s", m.View())
	}

	// Nothing running: home returns to its OWN state. It is a screen for
	// watching, so leaving the last finished conversation on it means home
	// never gets back to being home. (The thought's own page keeps its
	// transcript — that is the other half of this rule.)
	if err := q.Complete("second", ""); err != nil {
		t.Fatal(err)
	}
	drain(m)
	if m.homeID != "" {
		t.Fatalf("home still tracks %q after it finished", m.homeID)
	}
	if strings.Contains(m.View(), "home · second") {
		t.Errorf("home kept showing the finished thought:\n%s", m.View())
	}
	if !strings.Contains(m.View(), "to queue a new thought") {
		t.Errorf("home did not return to its idle splash:\n%s", m.View())
	}

	// A new thought starts running: home advances to it.
	if err := q.Enqueue(th("third", 4)); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("no claim for third")
	}
	drain(m)
	if m.homeID != "third" {
		t.Fatalf("home tracks %q, want third after a new thought started", m.homeID)
	}
}

// TestTheThoughtPageKeepsTheTranscriptWhenItCompletes is the regression for
// the bug found in review: the conversation was wiped the instant the thought
// finished. It is the thought's OWN page that must keep it — you opened it
// deliberately. Home is a watching screen and returns to its own state (see
// TestHomeReturnsToItsEmptyStateWhenTheThoughtFinishes).
func TestTheThoughtPageKeepsTheTranscriptWhenItCompletes(t *testing.T) {
	q := newQueue(t)
	tport, broker := newInProcess(t, q)
	m := NewModel(tport)
	_ = m.Init()
	if err := q.Enqueue(th("done1", 4)); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("no claim")
	}
	drain(m)
	// Open the thought's OWN page: that is what must keep its transcript.
	m.enterLive("done1")

	// A streamed assistant turn lands in the conversation.
	observe(broker, "done1", agent.AgentEvent{
		Type:    agent.AgentEventContentDelta,
		Message: &agent.AgentMessage{Role: agent.RoleAssistant, Content: "the answer is 42"},
	})
	drain(m)
	if !strings.Contains(m.View(), "the answer is 42") {
		t.Fatalf("precondition: the turn should be on screen\n%s", m.View())
	}

	if err := q.Complete("done1", ""); err != nil {
		t.Fatal(err)
	}
	drain(m)

	view := m.View()
	if !strings.Contains(view, "the answer is 42") {
		t.Errorf("the transcript was cleared when the thought completed:\n%s", view)
	}
	if !strings.Contains(view, "chat · done1") {
		t.Errorf("the thought page stopped naming the thought it is showing:\n%s", view)
	}
}

// TestHomeKeepsTheSteerBufferAcrossATrackedThoughtChange: the buffer is the
// reader's text. It is KEPT when the tracked thought changes and re-targets on
// send — silently dropping it because the queue advanced is the same class of
// defect as a key that does nothing. The panel title names the new target, so
// the re-target is visible rather than silent.
// TestTheSteerBufferSurvivesATrackedThoughtChange: the buffer is the reader's
// text, so the queue advancing must not silently discard it. The editor lives
// on the thought's page (NAVI-604), so the send goes to the thought whose page
// the reader is on — the one they opened — rather than to whatever home is
// tracking.
func TestTheSteerBufferSurvivesATrackedThoughtChange(t *testing.T) {
	steerLog() // clear injections recorded by earlier cases
	q := newQueue(t)
	tport, _ := newInProcess(t, q)
	m := NewModel(tport)
	_ = m.Init()

	if err := q.Enqueue(th("old", 4)); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("no claim for old")
	}
	drain(m)
	if m.homeID != "old" {
		t.Fatalf("home tracks %q, want old", m.homeID)
	}
	m.enterLive("old")

	// The reader is part-way through a message when the thought completes and
	// a new one takes its place.
	keyRunes(t, m, "half-written note")
	if err := q.Enqueue(th("new", 4)); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("no claim for new")
	}
	if err := q.Complete("old", ""); err != nil {
		t.Fatal(err)
	}
	drain(m)

	if m.homeID != "new" {
		t.Fatalf("home tracks %q, want new", m.homeID)
	}
	if m.input != "half-written note" {
		t.Fatalf("steer buffer was dropped on the tracked-thought change: %q", m.input)
	}

	// Sending goes to the thought whose PAGE the reader is on — the one they
	// opened — not to whatever home happens to be tracking. Here that thought
	// has just FINISHED, so the steer is refused, and the refusal is REPORTED:
	// silently discarding the message would be the defect, not the refusal.
	keyEnter(m)
	if got := steerLog(); len(got) != 0 {
		t.Fatalf("steer reached a finished thought: %v", got)
	}
	if !strings.Contains(m.status, "steer failed") {
		t.Errorf("a refused steer was not reported: %q", m.status)
	}
	if m.input != "half-written note" {
		t.Errorf("a refused steer discarded the buffer: %q", m.input)
	}
	_ = q
}

// TestActionsWorkFromHome: the defect the revamp started from. `s`/`r`/`c`/
// `p`/`P` act on the thought home is showing, not only on a selected list row.
// The input is blurred first (one key), which is the documented binding.
func TestActionsWorkFromHome(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q)
	m := NewModel(tport)
	_ = m.Init()
	if err := q.Enqueue(th("act", 4)); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("no claim")
	}
	drain(m)
	if m.homeID != "act" || m.mode != ModeHome {
		t.Fatalf("precondition: home should track act, got %q in %q", m.homeID, m.mode)
	}

	// Home arrives BLURRED, so the action letters act immediately. Focus the
	// input first to check the other half of the rule.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if !m.inputFocused {
		t.Fatal("tab did not focus the input")
	}
	keyRunes(t, m, "s")
	if m.input != "s" {
		t.Fatalf("`s` was not typed into the focused input: %q", m.input)
	}
	if m.statusOf("act") != "running" {
		t.Fatalf("`s` fired pause while the input was focused: status=%q", m.statusOf("act"))
	}
	// Clear, then blur: `esc` on a non-empty buffer clears it, and `esc` on an
	// empty one blurs — so the action letters are one key further on.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.input != "" {
		t.Fatalf("esc did not clear the buffer: %q", m.input)
	}
	if !m.inputFocused {
		t.Fatal("esc on a non-empty buffer blurred the input; it should only clear")
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.inputFocused {
		t.Fatal("esc on an empty buffer did not blur the input")
	}

	// Blurred: `p` bumps the displayed thought.
	keyRunes(t, m, "p")
	got, _ := q.GetThought("act")
	if got.Priority != 3 {
		t.Errorf("`p` from home did not bump the priority: %d", got.Priority)
	}
	// `s` pauses it. The queue's event reaches the model as a badge update.
	keyRunes(t, m, "s")
	drain(m)
	if got, _ := q.GetThought("act"); got.Status != thought.ThoughtPaused {
		t.Errorf("`s` from home did not pause: status=%q", got.Status)
	}
	// `r` resumes it.
	keyRunes(t, m, "r")
	drain(m)
	if got, _ := q.GetThought("act"); got.Status != thought.ThoughtPending && got.Status != thought.ThoughtRunning {
		t.Errorf("`r` from home did not resume: status=%q", got.Status)
	}
}

// TestTypingReturnsFocusToTheInput: after blurring, any printable rune starts
// typing again, so the input is not a mode you must explicitly re-enter.
func TestTypingReturnsFocusToTheInput(t *testing.T) {
	m, _, _ := runningModel(t)
	// Home arrives blurred (see gotoPage), so typing is what re-focuses it.
	if m.inputFocused {
		t.Fatal("precondition: home should arrive blurred")
	}
	keyRunes(t, m, "hi")
	if !m.inputFocused {
		t.Error("typing did not re-focus the input")
	}
	if m.input != "hi" {
		t.Errorf("typed text not captured: %q", m.input)
	}
}

// TestLeaderMenuReachesActionsWhileTyping: `ctrl+a` is the escape hatch — it is
// not a printable rune, so the focused object's declared actions are reachable
// without leaving the steer input.
func TestLeaderMenuReachesActionsWhileTyping(t *testing.T) {
	steerLog() // clear injections recorded by earlier cases
	m, q, _ := runningModel(t)
	m.enterLive("r1")
	keyRunes(t, m, "a draft I am not ready to send")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	if !m.menuOpen {
		t.Fatal("ctrl+a did not open the action menu")
	}
	if m.input != "a draft I am not ready to send" {
		t.Fatalf("the menu disturbed the input buffer: %q", m.input)
	}
	if !strings.Contains(m.View(), "actions") {
		t.Errorf("the menu is not rendered:\n%s", m.View())
	}

	// A letter dispatches the declared action on the focused object.
	keyRunes(t, m, "s")
	if m.menuOpen {
		t.Error("the menu stayed open after a pick")
	}
	drain(m)
	if got, _ := q.GetThought("r1"); got.Status != thought.ThoughtPaused {
		t.Errorf("the menu's `s` did not pause: status=%q", got.Status)
	}
	if m.input != "a draft I am not ready to send" {
		t.Errorf("the menu pick disturbed the input buffer: %q", m.input)
	}
	_ = q
}

// TestActionMenuIsDescriptorDrivenAndHonest: the menu comes from the
// descriptor's declared actions, and every entry it shows has a working,
// unique key — no advertised key that does nothing, and no duplicate letter
// silently shadowing another entry.
func TestActionMenuIsDescriptorDrivenAndHonest(t *testing.T) {
	m, _, _ := runningModel(t)
	m.openActionMenu()
	if !m.menuOpen {
		t.Fatal("menu did not open for a running thought")
	}
	seen := map[string]string{}
	for _, a := range m.actionMenu {
		l := menuLetter(a.Name)
		if l == "" {
			t.Errorf("action %q has no key", a.Name)
		}
		if prev, dup := seen[l]; dup {
			t.Errorf("key %q is claimed by both %q and %q", l, prev, a.Name)
		}
		seen[l] = a.Name
		if a.Name == "steer" {
			t.Errorf("steer is in the menu; the persistent input is its UI")
		}
	}
	// A completed thought has nothing to pause, so it is not offered.
	m.setBadge("r1", func(b *badge) { b.status = "complete" })
	m.openActionMenu()
	for _, a := range m.actionMenu {
		if a.Name == "pause" {
			t.Error("pause offered for a completed thought")
		}
	}
}

// TestActionOnAThoughtWithNoApplicableActionSaysSo: an inert key with no
// explanation is the defect this replaces, so a refused action names the
// reason rather than doing nothing.
func TestActionOnAThoughtWithNoApplicableActionSaysSo(t *testing.T) {
	m, _, _ := runningModel(t)
	m.setBadge("r1", func(b *badge) { b.status = "complete" })
	if m.inputFocused {
		t.Fatal("precondition: home should arrive blurred")
	}
	keyRunes(t, m, "s")
	if !strings.Contains(m.status, "does not apply") {
		t.Errorf("refused action gave no reason: %q", m.status)
	}
}

// TestFocusedObjectIsTheOpenThoughtInEveryMode is the unit-level contract for
// the fix: `focused()` answers per screen instead of returning false outside
// list mode.
func TestFocusedObjectIsTheOpenThoughtInEveryMode(t *testing.T) {
	m, _, _ := runningModel(t)

	m.mode = ModeHome
	m.homeID = "r1"
	if id, ok := m.focused(); !ok || id != "r1" {
		t.Errorf("home: focused = %q,%v want r1,true", id, ok)
	}
	m.homeID = ""
	if _, ok := m.focused(); ok {
		t.Error("home with nothing running reported a focused object")
	}

	m.mode = ModeLive
	m.liveID = "r1"
	if id, ok := m.focused(); !ok || id != "r1" {
		t.Errorf("live: focused = %q,%v want r1,true", id, ok)
	}

	m.mode = ModeDetail
	m.detail = &viewDetailStub
	if id, ok := m.focused(); !ok || id != "d1" {
		t.Errorf("detail: focused = %q,%v want d1,true", id, ok)
	}

	m.mode = ModeList
	m.sel = 0
	if id, ok := m.focused(); !ok || id != "r1" {
		t.Errorf("list: focused = %q,%v want r1,true", id, ok)
	}
}

// TestConversationChatIsTheSharedComponent: home and live render through the
// same chat component (ADR 0038 decision 5), so the same conversation cannot
// look different depending on which context opened it.
func TestConversationChatIsTheSharedComponent(t *testing.T) {
	m, _, _ := runningModel(t)
	m.homeID = "r1"
	m.loadTranscript("r1")
	homeTurns := len(m.chat.turns)

	m.liveID = "r1"
	m.mode = ModeLive
	m.loadTranscript("r1")
	if len(m.chat.turns) != homeTurns {
		t.Errorf("live re-seeded %d turns where home had %d: not the same component",
			len(m.chat.turns), homeTurns)
	}
}

// TestHomeDoesNotLoseTurnsFromAgentEvents: agent deltas for the tracked thought
// land in home's conversation, the same way they land in live's.
func TestHomeDoesNotLoseTurnsFromAgentEvents(t *testing.T) {
	q := newQueue(t)
	tport, broker := newInProcess(t, q)
	m := NewModel(tport)
	_ = m.Init()
	if err := q.Enqueue(th("d1", 4)); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("no claim")
	}
	drain(m)

	observe(broker, "d1", agent.AgentEvent{
		Type:    agent.AgentEventContentDelta,
		Message: &agent.AgentMessage{Role: agent.RoleAssistant, Content: "streamed into home"},
	})
	drain(m)
	if !strings.Contains(m.View(), "streamed into home") {
		t.Errorf("home did not render the streamed delta:\n%s", m.View())
	}
}

// TestCancelIsNotOfferedForARunningThought: the queue refuses to cancel a
// running thought ("pause it before cancelling"), so the guard must not offer
// it — a key whose only outcome is an error is the "advertised but does not
// work" defect this revamp removes.
func TestCancelIsNotOfferedForARunningThought(t *testing.T) {
	m, _, _ := runningModel(t)
	if actionApplies("cancel", "running") {
		t.Error("cancel offered for a running thought; the queue refuses it")
	}
	if !actionApplies("cancel", "pending") || !actionApplies("cancel", "paused") {
		t.Error("cancel should apply to pending and paused thoughts")
	}
	m.openActionMenu()
	for _, a := range m.actionMenu {
		if a.Name == "cancel" {
			t.Error("cancel is in the running thought's action menu")
		}
	}
}

// TestHomeIsReachableFromEveryScreen: home is the default screen, so it must
// be reachable from wherever the reader is, or the default is a place you can
// only visit once.
func TestHomeIsReachableFromEveryScreen(t *testing.T) {
	m, _, _ := runningModel(t)
	for _, mode := range []ModeKind{ModeList, ModeDetail, ModeLive} {
		m.mode = mode
		m.detail = nil
		keyRunes(t, m, "1")
		if m.mode != ModeHome {
			t.Errorf("`1` from %q left mode %q, want home", mode, m.mode)
		}
		if m.homeID != "r1" {
			t.Errorf("`1` from %q tracked %q, want r1", mode, m.homeID)
		}
	}
}

// TestHomeKeyWorksWhileTyping: `1` is not a printable rune, so it returns home
// even with the steer input focused (the same property the leader relies on).
func TestHomeKeyWorksWhileTyping(t *testing.T) {
	m, _, _ := runningModel(t)
	m.mode = ModeList
	m.inputFocused = true
	keyRunes(t, m, "1")
	if m.mode != ModeHome {
		t.Fatalf("mode = %q, want home", m.mode)
	}
}

// TestConversationStatusNamesTheLifecycle: the panel title's status suffix
// names the THOUGHT's state, not just the view's scroll behaviour — the screen
// otherwise never says whether the thought you are watching is still working.
func TestConversationStatusNamesTheLifecycle(t *testing.T) {
	m, _, _ := runningModel(t)
	cases := []struct {
		status string
		want   string
	}{
		{"running", "live"},
		{"pending", "queued"},
		{"paused", "paused"},
		{"complete", "complete"},
		{"failed", "failed"},
		{"cancelled", "cancelled"},
	}
	for _, c := range cases {
		m.setBadge("r1", func(b *badge) { b.status = c.status })
		if got := m.chatStatus(); !strings.HasPrefix(got, c.want) {
			t.Errorf("status %q rendered as %q, want a prefix of %q", c.status, got, c.want)
		}
	}
}

// TestConversationStatusColoursFollowTheStatus: the suffix is coloured from the
// same mapping a table cell uses, so a state means one colour across the TUI.
// Asserted on the style's own foreground rather than a rendered escape, because
// a test process has no TTY and lipgloss would correctly choose Ascii (no
// colour) for it — the mapping is what is under test, not the terminal.
func TestConversationStatusColoursFollowTheStatus(t *testing.T) {
	m, _, _ := runningModel(t)
	th := newThemeFor(true)

	fg := func(status string) string {
		m.setBadge("r1", func(b *badge) { b.status = status })
		// The style the panel suffix is drawn in, read from the same function
		// the render path uses. AdaptiveColor resolves against the profile, so
		// take its light variant as the stable identity.
		switch c := th.statusStyle(m.statusOf("r1")).GetForeground().(type) {
		case lipgloss.Color:
			return string(c)
		case lipgloss.AdaptiveColor:
			return c.Light
		}
		return ""
	}
	running, failed, paused, complete := fg("running"), fg("failed"), fg("paused"), fg("complete")
	if running == failed || running == paused {
		t.Errorf("distinct statuses share a colour: running=%s failed=%s paused=%s",
			running, failed, paused)
	}
	if complete == running || complete == failed || complete == paused {
		t.Errorf("a finished thought is coloured like an active one: %s", complete)
	}
	// One mapping: a table cell and the panel suffix agree.
	cellStyle := th.statusStyle("running").GetForeground()
	var cell string
	switch c := cellStyle.(type) {
	case lipgloss.Color:
		cell = string(c)
	case lipgloss.AdaptiveColor:
		cell = c.Light
	}
	if cell != running {
		t.Errorf("table cell and panel suffix disagree: cell=%q panel=%q", cell, running)
	}
}

// TestHomeHasNoSteerBoxInAnyState: home is watch-only. The steer box lives on
// the thought's own page, reached with `enter`.
func TestHomeHasNoSteerBoxInAnyState(t *testing.T) {
	m, _, _ := runningModel(t)
	resize(m, 120, 30)
	for _, st := range []string{"running", "paused", "complete", "failed"} {
		m.setBadge("r1", func(b *badge) { b.status = st })
		if strings.Contains(m.View(), "─ steer ─") {
			t.Errorf("home shows a steer box with status %q:\n%s", st, m.View())
		}
	}
	// But the thought's own page has one.
	m.enterLive("r1")
	m.setBadge("r1", func(b *badge) { b.status = "running" })
	if !strings.Contains(m.View(), "─ steer ─") {
		t.Errorf("the thought page has no steer box:\n%s", m.View())
	}
}

// TestHomeReturnsToItsEmptyStateWhenTheThoughtFinishes: home is for watching,
// so when the tracked thought finishes home goes back to its own state rather
// than leaving the finished conversation on screen. (The thought's OWN page
// keeps its transcript — that is the other half of this rule.)
func TestHomeReturnsToItsEmptyStateWhenTheThoughtFinishes(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q)
	m := NewModel(tport)
	_ = m.Init()
	resize(m, 120, 30)
	if err := q.Enqueue(th("f1", 4)); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("no claim")
	}
	drain(m)
	if m.homeID != "f1" {
		t.Fatalf("home tracks %q, want f1", m.homeID)
	}

	if err := q.Complete("f1", ""); err != nil {
		t.Fatal(err)
	}
	drain(m)
	if m.homeID != "" {
		t.Fatalf("home still tracks %q after it finished", m.homeID)
	}
	// Nothing queued either, so the splash.
	if !strings.Contains(m.View(), "to queue a new thought") {
		t.Errorf("home did not return to its idle splash:\n%s", m.View())
	}
}

// TestHomeDistinguishesWaitingFromIdle: "nothing running" is two different
// states, and collapsing them either nags a reader who is waiting for a queued
// thought or leaves an idle navi looking stalled.
func TestHomeDistinguishesWaitingFromIdle(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q)
	m := NewModel(tport)
	_ = m.Init()
	resize(m, 120, 30)

	// Nothing running, nothing queued: the IDLE SPLASH, naming the hotkey.
	if m.queuedWork() {
		t.Fatal("precondition: nothing should be queued")
	}
	view := m.View()
	if !strings.Contains(view, "idle") || !strings.Contains(view, "to queue a new thought") {
		t.Errorf("idle home does not show the splash with the hotkey:\n%s", view)
	}

	// A thought queued but not yet claimed: the WAITING state, not the splash.
	if err := q.Enqueue(th("waiting", 4)); err != nil {
		t.Fatal(err)
	}
	drain(m)
	if !m.queuedWork() {
		t.Fatal("a pending thought should count as queued work")
	}
	view = m.View()
	if strings.Contains(view, "to queue a new thought") {
		t.Errorf("home still nags to queue while work is waiting:\n%s", view)
	}
	if !strings.Contains(view, "waiting for work") {
		t.Errorf("home does not say it is waiting:\n%s", view)
	}

	// Paused is also work (resumable), so it stays in the waiting state.
	m.setBadge("waiting", func(b *badge) { b.status = "paused" })
	if !m.queuedWork() {
		t.Error("a paused thought should count as queued work")
	}

	// Terminal is not work: back to the splash.
	m.setBadge("waiting", func(b *badge) { b.status = "complete" })
	if m.queuedWork() {
		t.Error("a completed thought should not count as queued work")
	}
	if !strings.Contains(m.View(), "to queue a new thought") {
		t.Errorf("home did not return to the splash:\n%s", m.View())
	}
}

// TestQueueOrderIsLifecycleThenPriority: the queue reads as a queue. Running
// first, then what is waiting by priority (lower number is more urgent), then
// paused, then finished — so a completed thought never sits between two live
// ones, and the reader can see what is happening without scanning every row.
func TestQueueOrderIsLifecycleThenPriority(t *testing.T) {
	mk := func(id, status string, prio int, age time.Duration) (object.Object, string) {
		o := object.Object{Type: "thought", ID: id, Fields: map[string]any{
			"id": id, "status": status, "priority": prio,
			"created_at": time.Now().UTC().Add(-age),
		}}
		return o, status
	}
	objs := []object.Object{}
	status := map[string]string{}
	add := func(o object.Object, s string) { objs = append(objs, o); status[o.ID] = s }

	// Deliberately shuffled, with a completed thought in the middle.
	add(mk("done-old", "complete", 1, 9*time.Hour))
	add(mk("pend-p4", "pending", 4, 1*time.Hour))
	add(mk("run-p4", "running", 4, 5*time.Hour))
	add(mk("pend-p1", "pending", 1, 2*time.Hour))
	add(mk("paused-p2", "paused", 2, 3*time.Hour))
	add(mk("done-new", "complete", 0, 1*time.Minute))

	sortQueueRows(objs, func(id string) string { return status[id] })

	var got []string
	for _, o := range objs {
		got = append(got, o.ID)
	}
	// Finished work is newest-first: "what just happened" is what a completed
	// list answers, and priority is spent by then.
	want := []string{"run-p4", "pend-p1", "pend-p4", "paused-p2", "done-new", "done-old"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("queue order:\n got %v\nwant %v", got, want)
	}
}
