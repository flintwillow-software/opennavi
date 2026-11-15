package tui

import (
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/internal/agent"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/thought"
)

// The tests in this file are the NAVI-605 part 2 deliverable: the TUI was
// verified thoroughly at MaxConcurrent 1 (NAVI-604), but the queue defaults to
// 2 and the operator reported it "really buggy" with several thoughts running.
// Each test drives TWO thoughts running at once and pins one of the properties
// that concurrency 1 cannot exercise.
//
// They are written against the model's own state (transcripts, homeID) rather
// than golden output, because the point is isolation and targeting, not layout.

// startTwoRunning enqueues two thoughts and claims both, so the model sees two
// running rows at once. It returns the model after draining the lifecycle
// events.
func startTwoRunning(t *testing.T) (*tuiModel, *portal.Broker, *thought.Queue) {
	t.Helper()
	q := newQueue(t)
	tport, broker := newInProcess(t, q)
	m := NewModel(tport)
	_ = m.Init()

	for _, id := range []string{"a1", "b2"} {
		if err := q.Enqueue(th(id, 4)); err != nil {
			t.Fatal(err)
		}
	}
	// Claim both: two workers' worth of running thoughts.
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("no first claim")
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("no second claim")
	}
	drain(m)

	running := 0
	for _, row := range m.list.Rows {
		if m.statusOf(row.ID) == "running" {
			running++
		}
	}
	if running != 2 {
		t.Fatalf("setup: %d running rows, want 2", running)
	}
	return m, broker, q
}

// TestConcurrentTranscriptsDoNotCrossContaminate pins the transcript fold:
// transcripts are keyed by thought id, so two interleaved streams must each
// keep their own text. Before concurrency 1 this could never be exercised.
func TestConcurrentTranscriptsDoNotCrossContaminate(t *testing.T) {
	m, broker, _ := startTwoRunning(t)

	// Interleave deltas: a1, b2, a1, b2. Each thought's fold must accumulate
	// only its own fragments, and a fragment for one must never land in the
	// other's transcript.
	observe(broker, "a1", agent.AgentEvent{
		Type:    agent.AgentEventContentDelta,
		Message: &agent.AgentMessage{Role: agent.RoleAssistant, Content: "ALPHA-one "},
	})
	observe(broker, "b2", agent.AgentEvent{
		Type:    agent.AgentEventContentDelta,
		Message: &agent.AgentMessage{Role: agent.RoleAssistant, Content: "BETA-one "},
	})
	observe(broker, "a1", agent.AgentEvent{
		Type:    agent.AgentEventContentDelta,
		Message: &agent.AgentMessage{Role: agent.RoleAssistant, Content: "ALPHA-two"},
	})
	observe(broker, "b2", agent.AgentEvent{
		Type:    agent.AgentEventContentDelta,
		Message: &agent.AgentMessage{Role: agent.RoleAssistant, Content: "BETA-two"},
	})
	drain(m)

	aText := transcriptText(m.transcripts["a1"])
	bText := transcriptText(m.transcripts["b2"])

	// Deltas accumulate into ONE assistant turn by concatenation, so the two
	// fragments of a thought's own stream are adjacent with no separator.
	if !strings.Contains(aText, "ALPHA-oneALPHA-two") {
		t.Errorf("a1 transcript lost its own deltas: %q", aText)
	}
	if !strings.Contains(bText, "BETA-oneBETA-two") {
		t.Errorf("b2 transcript lost its own deltas: %q", bText)
	}
	if strings.Contains(aText, "BETA") {
		t.Errorf("a1 transcript contains b2's text (cross-contamination): %q", aText)
	}
	if strings.Contains(bText, "ALPHA") {
		t.Errorf("b2 transcript contains a1's text (cross-contamination): %q", bText)
	}
}

// TestConcurrentHomeTracksOneThoughtCoherently pins trackRunning with two
// running thoughts: home tracks exactly one of them, its chat shows that
// thought's content, and a delta for the OTHER thought does not overwrite it.
func TestConcurrentHomeTracksOneThoughtCoherently(t *testing.T) {
	m, broker, _ := startTwoRunning(t)

	if m.mode != ModeHome {
		t.Fatalf("expected to start on home, mode = %v", m.mode)
	}
	if m.homeID == "" {
		t.Fatal("home is not tracking a running thought")
	}
	tracked := m.homeID
	other := "a1"
	if tracked == "a1" {
		other = "b2"
	}

	observe(broker, tracked, agent.AgentEvent{
		Type:    agent.AgentEventContentDelta,
		Message: &agent.AgentMessage{Role: agent.RoleAssistant, Content: "TRACKED-TEXT"},
	})
	drain(m)

	if !strings.Contains(transcriptText(m.transcripts[tracked]), "TRACKED-TEXT") {
		t.Fatalf("tracked thought's delta missing from its transcript")
	}
	if !strings.Contains(transcriptText(m.chat.turns), "TRACKED-TEXT") {
		t.Fatalf("home chat does not show the tracked thought's content")
	}

	// A delta for the other running thought must NOT hijack the rendered chat:
	// the shared renderer shows whichever thought is on screen.
	observe(broker, other, agent.AgentEvent{
		Type:    agent.AgentEventContentDelta,
		Message: &agent.AgentMessage{Role: agent.RoleAssistant, Content: "OTHER-TEXT"},
	})
	drain(m)

	if m.homeID != tracked {
		t.Errorf("home re-targeted on an unrelated delta: %q -> %q", tracked, m.homeID)
	}
	if strings.Contains(transcriptText(m.chat.turns), "OTHER-TEXT") {
		t.Errorf("home chat was overwritten by the non-tracked thought's delta")
	}
	if !strings.Contains(transcriptText(m.transcripts[other]), "OTHER-TEXT") {
		t.Errorf("the other thought's transcript did not accumulate its own delta")
	}
}

// TestConcurrentFollowTheRunningDoesNotFlap pins rule 2 of trackRunning when
// the tracked thought completes: with a second thought still running, home
// moves to it and does not oscillate.
func TestConcurrentFollowTheRunningDoesNotFlap(t *testing.T) {
	m, _, q := startTwoRunning(t)

	first := m.homeID
	// Complete the tracked thought; the other is still running.
	if err := q.Complete(first, ""); err != nil {
		t.Fatal(err)
	}
	drain(m)

	if m.homeID == first {
		t.Fatalf("home stayed on the completed thought %q with another still running", first)
	}
	if m.homeID == "" {
		t.Fatalf("home fell to empty while a thought was still running")
	}
	// Settle: repeated lifecycle-free drains must not move it again.
	settled := m.homeID
	for i := 0; i < 3; i++ {
		drain(m)
		if m.homeID != settled {
			t.Fatalf("home flapped between running thoughts: %q -> %q", settled, m.homeID)
		}
	}
}

// transcriptText flattens a transcript's turns to their text, so a test can
// assert on accumulated content without depending on turn roles.
func transcriptText(turns []turn) string {
	var b strings.Builder
	for _, tn := range turns {
		b.WriteString(tn.text)
		b.WriteString(" ")
	}
	return b.String()
}
