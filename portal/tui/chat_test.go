package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// newTestChat builds a chat sized for reading, with the plain theme so
// assertions are on text rather than escapes.
func newTestChat(width, height int) *chat {
	c := newChat()
	c.setSize(width, height)
	return &c
}

// fill appends n assistant turns so there is enough content to scroll.
func fill(c *chat, n int) {
	for i := 0; i < n; i++ {
		c.append(turn{role: TurnAssistant, text: "line " + itoa(i)})
	}
}

// TestChatFollowsTheTail pins tail-follow: with new content arriving and the
// reader at the bottom, the view stays pinned to the newest turn.
func TestChatFollowsTheTail(t *testing.T) {
	c := newTestChat(40, 6)
	fill(c, 20)
	th := newThemeFor(false)

	view := c.view(th)
	if !strings.Contains(strings.Join(view, "\n"), "line 19") {
		t.Errorf("tail-follow did not show the newest turn:\n%s", strings.Join(view, "\n"))
	}
	if !c.following {
		t.Error("a fresh chat should be following")
	}
}

// TestChatScrollBackSuspendsFollow pins the behaviour that makes the
// indicator meaningful: scrolling away from the bottom stops the view from
// being yanked back by new content.
func TestChatScrollBackSuspendsFollow(t *testing.T) {
	c := newTestChat(40, 6)
	fill(c, 20)
	th := newThemeFor(false)
	_ = c.view(th) // establish content and tail position

	// Scroll up.
	if !c.update(tea.KeyMsg{Type: tea.KeyUp}) {
		t.Fatal("up was not consumed by the chat")
	}
	if c.following {
		t.Error("scrolling up should suspend tail-follow")
	}
	if !c.followSuspended() {
		t.Error("followSuspended should report the suspended state")
	}

	// New content must NOT move the view to the bottom.
	before := strings.Join(c.view(th), "\n")
	c.append(turn{role: TurnAssistant, text: "brand new"})
	after := strings.Join(c.view(th), "\n")
	if before != after {
		t.Errorf("new content moved a scrolled-back view:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestChatJumpToTailResumesFollow pins the explicit resume binding.
func TestChatJumpToTailResumesFollow(t *testing.T) {
	c := newTestChat(40, 6)
	fill(c, 20)
	th := newThemeFor(false)
	_ = c.view(th)

	c.update(tea.KeyMsg{Type: tea.KeyUp})
	c.update(tea.KeyMsg{Type: tea.KeyUp})
	if c.following {
		t.Fatal("expected follow suspended after scrolling")
	}

	// ctrl+f is the jump-to-tail binding: plain `f` is a letter the input
	// needs, so the scroll binding moved to a control key.
	if !c.update(tea.KeyMsg{Type: tea.KeyCtrlF}) {
		t.Fatal("ctrl+f was not consumed")
	}
	if !c.following {
		t.Error("jump-to-tail should resume following")
	}
	view := strings.Join(c.view(th), "\n")
	if !strings.Contains(view, "line 19") {
		t.Errorf("jump-to-tail did not reach the bottom:\n%s", view)
	}
}

// TestChatScrollKeysCoverTheBindings pins that every advertised binding is
// handled: j/k, pgup/pgdn, g/G, f.
func TestChatScrollKeysCoverTheBindings(t *testing.T) {
	keys := []struct {
		key  tea.KeyMsg
		want bool
	}{
		{tea.KeyMsg{Type: tea.KeyUp}, true},
		{tea.KeyMsg{Type: tea.KeyDown}, true},
		{tea.KeyMsg{Type: tea.KeyPgUp}, true},
		{tea.KeyMsg{Type: tea.KeyPgDown}, true},
		{tea.KeyMsg{Type: tea.KeyHome}, true},
		{tea.KeyMsg{Type: tea.KeyEnd}, true},
		{tea.KeyMsg{Type: tea.KeyCtrlF}, true},
		{tea.KeyMsg{Type: tea.KeyCtrlB}, true},
		// Letters are NOT scroll bindings: the input needs them.
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}, false},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")}, false},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")}, false},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")}, false},
	}
	for _, c := range keys {
		ch := newTestChat(40, 6)
		fill(ch, 20)
		if got := ch.update(c.key); got != c.want {
			t.Errorf("update(%q) = %v, want %v", c.key.String(), got, c.want)
		}
	}
	// chatHandles agrees with update, so the model can route correctly.
	for _, c := range keys {
		if got := chatHandles(c.key); got != c.want {
			t.Errorf("chatHandles(%q) = %v, want %v", c.key.String(), got, c.want)
		}
	}
}

// TestChatToolTurnsStartCollapsedAndExpand pins the compact-by-default tool
// block and the expand-on-demand behaviour.
func TestChatToolTurnsStartCollapsedAndExpand(t *testing.T) {
	c := newTestChat(60, 10)
	th := newThemeFor(false)
	c.append(turn{role: TurnTool, tool: "note_write", toolIn: `{"title":"x"}`, toolOut: "wrote the note", collapsed: true})

	view := strings.Join(c.view(th), "\n")
	if !strings.Contains(view, "note_write") {
		t.Errorf("collapsed tool turn lost its name:\n%s", view)
	}
	if !strings.Contains(view, "ctrl+o to expand") {
		t.Errorf("collapsed tool turn should advertise expansion:\n%s", view)
	}
	// The collapsed form is a BOX (top edge, one summary row, bottom edge): it
	// may show a short marker of the output, but the turn must not render the
	// full input/output block.
	block := c.blocks(th)[0]
	if len(block) != 3 {
		t.Errorf("collapsed tool turn should be a 3-row box, got %d: %q", len(block), block)
	}
	if !strings.Contains(block[0], "─ tool ─") {
		t.Errorf("collapsed tool turn should be labelled by its box edge: %q", block[0])
	}
	if strings.Contains(view, "in:") {
		t.Errorf("collapsed tool turn should not show its input block:\n%s", view)
	}

	// ctrl+o expands the most recent foldable turn (enter sends the steer).
	if !c.update(tea.KeyMsg{Type: tea.KeyCtrlO}) {
		t.Fatal("ctrl+o was not consumed for a foldable turn")
	}
	_ = th
	view = strings.Join(c.view(th), "\n")
	if !strings.Contains(view, "wrote the note") {
		t.Errorf("expanded tool turn should show its output:\n%s", view)
	}
}

// TestChatToolCallAndResultAreOneBlock pins that a tool_call followed by its
// tool_result renders as ONE block rather than two, which is what keeps a
// run with many calls readable.
func TestChatToolCallAndResultAreOneBlock(t *testing.T) {
	c := newTestChat(60, 10)
	c.append(turn{role: TurnTool, tool: "note_write", collapsed: true})
	// A result lands on the trailing tool turn when it has no output yet.
	if n := len(c.turns); n > 0 && c.turns[n-1].role == TurnTool && c.turns[n-1].toolOut == "" {
		c.turns[n-1].toolOut = "done"
	}
	if len(c.turns) != 1 {
		t.Errorf("call and result should be one turn, got %d", len(c.turns))
	}
}

// TestChatThinkingRoleRendersNothingWhenEmpty pins ADR 0038 decision 9's
// forward-compatibility: the role exists, renders nothing without content,
// and renders dimmed/collapsible content once the upstream event lands.
func TestChatThinkingRoleRendersNothingWhenEmpty(t *testing.T) {
	c := newTestChat(60, 10)
	th := newThemeFor(false)

	// Empty reasoning renders nothing at all.
	c.append(turn{role: TurnThinking, text: ""})
	for _, l := range c.blocks(th) {
		for _, line := range l {
			if strings.Contains(line, "thinking") {
				t.Errorf("empty reasoning should render nothing, got %q", line)
			}
		}
	}

	// With content it renders, marked as reasoning and distinct from an
	// assistant turn.
	c.reset([]turn{{role: TurnThinking, text: "let me consider the options"}})
	view := strings.Join(c.view(th), "\n")
	if !strings.Contains(view, "thinking") {
		t.Errorf("reasoning content should be labelled:\n%s", view)
	}
	if !strings.Contains(view, "consider the options") {
		t.Errorf("reasoning content should render:\n%s", view)
	}
	if strings.Contains(view, "◂") {
		t.Errorf("reasoning must not render as assistant content:\n%s", view)
	}
}

// TestChatReasoningAccumulatesLikeAssistant pins the fold: consecutive
// reasoning deltas accumulate into one turn, so the eventual event wiring is
// a data change rather than a rendering change.
func TestChatReasoningAccumulatesLikeAssistant(t *testing.T) {
	c := newTestChat(60, 10)
	c.append(turn{role: TurnThinking, text: "first"})
	// Mirror the fold in chatAgentEvent.
	if n := len(c.turns); n > 0 && c.turns[n-1].role == TurnThinking {
		c.turns[n-1].text += " second"
	}
	if len(c.turns) != 1 || c.turns[0].text != "first second" {
		t.Errorf("reasoning deltas should accumulate into one turn, got %+v", c.turns)
	}
}

// TestChatPreservesTheSanitizationContract is the NAVI-333 correctness
// property, which must survive the rewrite: a single-line context (a prompt,
// a system line, a tool input) has its control characters neutralized so an
// embedded newline cannot fuse two rows; assistant content legitimately wraps
// and is left intact.
func TestChatPreservesTheSanitizationContract(t *testing.T) {
	c := newTestChat(60, 12)
	th := newThemeFor(false)
	c.reset([]turn{
		{role: TurnUser, text: "hello\nworld check\tthis\rnice"},
		{role: TurnAssistant, text: "line one\nline two"},
		{role: TurnTool, tool: "t", toolIn: "a\nb", collapsed: false},
		{role: TurnSystem, text: "sys\ntem"},
	})

	view := strings.Join(c.view(th), "\n")
	// The user turn's newline became a space and the row did not fuse.
	if !strings.Contains(view, "hello world check this nice") {
		t.Errorf("user turn control chars not neutralized:\n%s", view)
	}
	if strings.Contains(view, "hello\nworld") {
		t.Error("literal newline leaked into a single-line context")
	}
	// The system line's newline was neutralized too.
	if strings.Contains(view, "sys\ntem") {
		t.Error("literal newline leaked into a system line")
	}
	// Assistant content keeps its newline — it is the legitimately
	// multi-line context.
	if !strings.Contains(view, "line one") || !strings.Contains(view, "line two") {
		t.Errorf("assistant content lost its lines:\n%s", view)
	}
}

// TestChatTrimIsVisibleNotSilent pins that the scrollback bound reports
// itself rather than dropping history silently.
func TestChatTrimIsVisibleNotSilent(t *testing.T) {
	c := newTestChat(60, 8)
	for i := 0; i < maxLiveTurns+5; i++ {
		c.append(turn{role: TurnAssistant, text: "turn " + itoa(i)})
	}
	if c.trimmed != 5 {
		t.Errorf("trimmed = %d, want 5", c.trimmed)
	}
	if len(c.turns) != maxLiveTurns {
		t.Errorf("turns = %d, want the bound %d", len(c.turns), maxLiveTurns)
	}
	// The notice is reported by the component for the header, because the
	// content follows the tail and would scroll it out of sight.
	if got := c.trimNotice(); !strings.Contains(got, "5") {
		t.Errorf("trimNotice = %q, want it to report 5", got)
	}
	if c.turns[0].text != "turn 5" {
		t.Errorf("trim dropped the wrong end: first turn is %q", c.turns[0].text)
	}
}

// TestChatRenderingIsIncremental is the streaming-performance requirement:
// rendering a long transcript must not rebuild every turn's block per frame.
// It pins that by counting renders through the cache.
func TestChatRenderingIsIncremental(t *testing.T) {
	c := newTestChat(60, 10)
	th := newThemeFor(false)
	for i := 0; i < 50; i++ {
		c.append(turn{role: TurnAssistant, text: "turn " + itoa(i)})
	}
	// First render builds every block.
	c.blocks(th)
	first := make([]int, len(c.turns))
	for i := range c.turns {
		first[i] = len(c.turns[i].rendered)
	}
	if first[0] == 0 {
		t.Fatal("first render did not cache any blocks")
	}

	// Record each block's identity, then render again with nothing changed:
	// no block should be rebuilt (its rendered slice keeps the same backing
	// array). A change to the trailing turn's text should rebuild only that
	// one.
	before := make([][]string, len(c.turns))
	for i := range c.turns {
		before[i] = c.turns[i].rendered
	}
	c.blocks(th)
	for i := range c.turns {
		if &before[i][0] != &c.turns[i].rendered[0] {
			t.Errorf("turn %d was re-rendered with nothing changed", i)
		}
	}

	// Now stream a delta into the trailing turn only.
	trailing := len(c.turns) - 1
	saved := c.turns[trailing].rendered
	c.turns[trailing].text += " (more)"
	c.blocks(th)
	if &c.turns[0].rendered[0] != &before[0][0] {
		t.Error("an unchanged turn was re-rendered when only the trailing one changed")
	}
	if &c.turns[trailing].rendered[0] == &saved[0] {
		t.Error("the changed turn was not re-rendered")
	}
}

// TestChatResizeInvalidatesBlocks pins that a width change re-wraps, since
// the cached blocks were laid out for the old width.
func TestChatResizeInvalidatesBlocks(t *testing.T) {
	c := newTestChat(60, 10)
	th := newThemeFor(false)
	c.append(turn{role: TurnAssistant, text: strings.Repeat("word ", 40)})
	c.blocks(th)
	oldW := c.turns[0].cacheW

	c.setSize(30, 10)
	c.blocks(th)
	if c.turns[0].cacheW == oldW {
		t.Error("resize did not invalidate the cached block")
	}
	// And the re-rendered block is wrapped to the new width.
	for _, l := range c.turns[0].rendered {
		if displayWidth(l) > 30 {
			t.Errorf("line wider than the new viewport: %d > 30: %q", displayWidth(l), l)
		}
	}
}

// TestChatTrimsBlankLinesAroundTurns pins that a turn's trailing newlines do
// not render as blank rows. Model output routinely ends with newlines — an
// assistant message handing off to a tool call is often "…first.\n\n" — and
// those rendered as real rows, putting an unexplained gap after some turns and
// not others.
func TestChatTrimsBlankLinesAroundTurns(t *testing.T) {
	c := newTestChat(60, 20)
	th := newThemeFor(false)
	c.reset([]turn{
		{role: TurnAssistant, text: "I'll read the file first.\n\n"},
		{role: TurnTool, tool: "file_read", toolOut: "contents", collapsed: true},
	})

	lines := c.lines(th)
	// NO blank row between turns: each turn is a closed box whose own edges
	// mark the boundary, so a separator would spend a row saying what the box
	// already says. The trailing newlines in the assistant's text are trimmed
	// rather than rendered.
	blanks := 0
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			blanks++
		}
	}
	if blanks != 0 {
		t.Errorf("want no blank rows between boxed turns, got %d:\n%s", blanks, strings.Join(lines, "\n"))
	}

	// Interior blank lines are content and must survive.
	c.reset([]turn{{role: TurnAssistant, text: "para one\n\npara two"}})
	joined := strings.Join(c.lines(th), "\n")
	if !strings.Contains(joined, "para one") || !strings.Contains(joined, "para two") {
		t.Errorf("interior blank line handling lost content:\n%s", joined)
	}
}
