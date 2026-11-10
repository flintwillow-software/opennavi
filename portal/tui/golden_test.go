package tui

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/flintwillow-software/opennavi/thought"
)

// This is the render test seam (ADR 0038, NAVI-588): rendering is asserted
// against golden files at fixed terminal sizes, so a later slice that
// rewrites rendering shows up as a reviewable diff rather than a wall of
// changed substring assertions.
//
// Golden files are the contract for "what the TUI looks like at this size".
// They are escape-free by construction: the harness builds an uncoloured
// theme, so a leaked escape code fails the golden comparison and the colour
// layer can be tested separately (see TestNoColorProducesNoEscapes).

// update regenerates the golden files instead of comparing against them.
// Run: go test ./portal/tui/ -update
var update = flag.Bool("update", false, "update golden files")

// goldenDir is where the fixed-size renders live.
const goldenDir = "testdata/golden"

// fixedSizes are the form factors the issue names, plus the split boundary:
// the standard form at 80, the wide form at 120, the narrow form at 60, and
// one below the floor to pin the too-narrow notice.
var fixedSizes = []struct {
	name string
	w, h int
}{
	{"standard_80x24", 80, 24},
	{"wide_120x40", 120, 40},
	{"narrow_60x20", 60, 20},
	{"below_floor_50x20", 50, 20},
	{"tiny_40x10", 40, 10},
}

// newGoldenModel builds a model over a fixed, deterministic fixture: two
// thoughts in different states with a running one carrying an activity
// preview, so a golden render exercises the list, the badges, and the
// activity column rather than an empty screen.
func newGoldenModel(t *testing.T) *tuiModel {
	t.Helper()
	q := newQueue(t)
	tport, _ := newInProcess(t, q)
	m := NewModel(tport)
	// Plain output: the golden files must not carry escape codes.
	m.theme = newThemeFor(false)
	_ = m.Init()

	// Enqueue requires a pending status; the running state is a live overlay
	// (the badge below), exactly as the real stream supplies it.
	running := th("t-run", 2)
	running.Prompt = "summarise the release notes"
	if err := q.Enqueue(running); err != nil {
		t.Fatal(err)
	}
	pending := th("t-pending", 4)
	pending.Prompt = "draft the migration guide"
	if err := q.Enqueue(pending); err != nil {
		t.Fatal(err)
	}
	drain(m)

	// A live overlay on the running thought, so the row shows status and an
	// activity preview rather than only declared fields.
	m.setBadge("t-run", func(b *badge) {
		b.status = string(thought.ThoughtRunning)
		b.activity = "⚙ note_write"
	})
	m.setBadge("t-pending", func(b *badge) {
		b.status = string(thought.ThoughtPending)
	})
	return m
}

// checkGolden compares got against testdata/golden/<name>.txt, writing it
// when -update is set.
func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join(goldenDir, name+".txt")
	if *update {
		if err := os.MkdirAll(goldenDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update to create it)", path, err)
	}
	if got != string(want) {
		t.Errorf("render differs from %s\n--- want ---\n%s\n--- got ---\n%s", path, string(want), got)
	}
}

// TestGoldenRendersAtFixedSizes pins the whole frame at each form factor for
// every screen, so a change to one screen shows up as a diff against its own
// golden rather than against a screen it shares a size with. It is the seam
// every later rendering change is reviewed against.
func TestGoldenRendersAtFixedSizes(t *testing.T) {
	for _, size := range fixedSizes {
		t.Run(size.name, func(t *testing.T) {
			// The list is no longer the default screen (home is, ADR 0038
			// decision 3), so it is reached explicitly.
			m := newGoldenModel(t)
			resize(m, size.w, size.h)
			showList(m)
			checkGolden(t, "list_"+size.name, m.View())
		})
	}
}

// TestGoldenHomeAtFixedSizes pins the HOME screen: the default screen, the
// running thought as a conversation, and the persistent steer input. It is the
// screen NAVI-587 adds, so it gets its own golden at every form factor.
func TestGoldenHomeAtFixedSizes(t *testing.T) {
	for _, size := range fixedSizes {
		t.Run(size.name, func(t *testing.T) {
			m := newGoldenModel(t)
			// Make the running thought home's tracked one, with a conversation
			// worth pinning rather than an empty viewport.
			m.homeID = "t-run"
			// Seed the conversation directly rather than through
			// loadTranscript: the fixture's surface has no transcript detail,
			// so a load would only set an error status the golden would then
			// pin as if it were the screen's real state.
			m.chat.reset([]turn{
				{role: TurnUser, text: "summarise the release notes"},
				{role: TurnAssistant, text: "Here are the highlights:\n1. terminal sizing\n2. rune-correct columns"},
				{role: TurnTool, tool: "note_write", toolIn: `{"title":"summary"}`, toolOut: "wrote summary", collapsed: true},
				{role: TurnSystem, text: "turn_end"},
			})
			resize(m, size.w, size.h)
			checkGolden(t, "home_"+size.name, m.View())
		})
	}
}

// TestGoldenHomeEmptyAtFixedSizes pins home's EMPTY state: nothing running, so
// the input is contextual and becomes an enqueue box (ADR 0038 decision 3 —
// "the screen is never a dead end").
func TestGoldenHomeEmptyAtFixedSizes(t *testing.T) {
	for _, size := range fixedSizes {
		t.Run(size.name, func(t *testing.T) {
			m := newGoldenModel(t)
			// Nothing running: clear the overlay that marks t-run running.
			m.setBadge("t-run", func(b *badge) { b.status = string(thought.ThoughtComplete) })
			m.trackRunning("")
			if m.homeID != "" {
				t.Fatalf("precondition: nothing should be running, tracked %q", m.homeID)
			}
			resize(m, size.w, size.h)
			checkGolden(t, "home_empty_"+size.name, m.View())
		})
	}
}

// TestGoldenActionMenuAtFixedSizes pins the leader menu (`ctrl+a`): the
// focused object's declared actions, which is how an action is reached while
// the steer input owns the keyboard (NAVI-587's keymap resolution).
func TestGoldenActionMenuAtFixedSizes(t *testing.T) {
	for _, size := range fixedSizes {
		t.Run(size.name, func(t *testing.T) {
			m := newGoldenModel(t)
			m.homeID = "t-run"
			m.loadTranscript("t-run")
			resize(m, size.w, size.h)
			m.openActionMenu()
			if !m.menuOpen {
				t.Fatal("action menu did not open for the running thought")
			}
			checkGolden(t, "menu_"+size.name, m.View())
		})
	}
}

// TestGoldenLiveChatAtFixedSizes pins the chat window, which is the screen
// with the most layout logic (tail window, wrapping, per-role styling).
func TestGoldenLiveChatAtFixedSizes(t *testing.T) {
	for _, size := range fixedSizes {
		if size.w < narrowFloor {
			continue // the chat is not reachable below the floor
		}
		t.Run(size.name, func(t *testing.T) {
			m := newGoldenModel(t)
			resize(m, size.w, size.h)
			showList(m)
			m.sel = 0
			_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			if m.mode != ModeLive {
				t.Fatalf("did not enter live view, mode=%s", m.mode)
			}
			// Give the conversation a shape worth pinning: a reasoning turn, an
			// assistant turn with wrapping, a collapsed tool block, and a
			// system line, so the golden exercises every role rather than an
			// almost-empty viewport.
			m.chat.reset([]turn{
				{role: TurnUser, text: "summarise the release notes"},
				{role: TurnThinking, text: "the user wants the highlights, not every commit"},
				{role: TurnAssistant, text: "Here are the highlights:\n1. terminal sizing\n2. rune-correct columns"},
				{role: TurnTool, tool: "note_write", toolIn: `{"title":"summary"}`, toolOut: "wrote summary", collapsed: true},
				{role: TurnSystem, text: "turn_end"},
			})
			checkGolden(t, "live_"+size.name, m.View())
		})
	}
}

// TestGoldenHelpOverlayAtFixedSizes pins the full-screen help overlay (`?`).
// It is GENERATED from the keymap (NAVI-593), so this golden is also the check
// that the generated help is legible at each form factor — the narrow case is
// where a hand-kept help line used to overflow.
func TestGoldenHelpOverlayAtFixedSizes(t *testing.T) {
	for _, size := range fixedSizes {
		t.Run(size.name, func(t *testing.T) {
			m := newGoldenModel(t)
			resize(m, size.w, size.h)
			// `?` is typable while the steer input is focused, so the overlay
			// is reached from the blurred state.
			m.inputFocused = false
			_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
			if !m.helpOpen {
				t.Fatal("? did not open the help overlay")
			}
			checkGolden(t, "help_"+size.name, m.View())
		})
	}
}

// TestFrameIsExactlyTerminalSized is the "fills the terminal" contract: the
// render is exactly height lines, each exactly width display columns. It is
// asserted directly rather than through a golden so a failure names the
// dimension that broke.
func TestFrameIsExactlyTerminalSized(t *testing.T) {
	for _, size := range fixedSizes {
		t.Run(size.name, func(t *testing.T) {
			m := newGoldenModel(t)
			resize(m, size.w, size.h)
			out := m.View()
			lines := strings.Split(out, "\n")
			if len(lines) != size.h {
				t.Errorf("height = %d lines, want %d", len(lines), size.h)
			}
			for i, l := range lines {
				if w := displayWidth(l); w != size.w {
					t.Errorf("line %d width = %d cols, want %d: %q", i, w, size.w, l)
				}
			}
		})
	}
}

// TestNoColorProducesNoEscapes is the NO_COLOR contract: an uncoloured theme
// renders the same text with no escape sequences, so the golden files are
// plain text and a colour bug cannot hide in them.
func TestNoColorProducesNoEscapes(t *testing.T) {
	m := newGoldenModel(t)
	resize(m, 100, 30)
	plain := m.View()
	if strings.ContainsRune(plain, 0x1b) {
		t.Errorf("uncoloured render contains an escape sequence:\n%q", plain)
	}

	// And the coloured theme DOES emit escapes, so the test above is not
	// passing merely because styling is broken everywhere. The profile is
	// forced because a test process has no TTY and lipgloss would otherwise
	// correctly choose Ascii (no colour) for it.
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })

	m.theme = newThemeFor(true)
	colored := m.View()
	if !strings.ContainsRune(colored, 0x1b) {
		t.Error("coloured render has no escape sequences; styling is not being applied")
	}
	// The visible text is the same either way: colour is decoration.
	if stripANSI(colored) != plain {
		t.Errorf("colour changed the text, not just the styling:\nplain: %q\ncolor: %q", plain, stripANSI(colored))
	}
}

// stripANSI removes escape sequences so styled and plain renders can be
// compared by their visible text.
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		switch {
		case inEsc:
			// An SGR sequence ends at 'm'; other CSI sequences at a letter.
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
		case r == 0x1b:
			inEsc = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
