package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// The chat component is the conversation renderer, extracted so that home's
// running thought and a thought opened from the queue share one
// implementation (ADR 0038 decision 5: one renderer, two contexts). It owns
// the turn list, the scroll position, and the rendering of each turn block;
// the model owns the transport and the event fold.
//
// Rendering is incremental: each turn renders to a block of lines once, and
// the viewport is fed the cached blocks. A streamed assistant turn is the one
// exception — its text grows, so its block is re-rendered and only its own
// lines are rebuilt (NAVI-589's streaming-performance requirement: a long
// transcript must not re-flatten every turn per frame).

// TurnRole renders one conversation turn in the chat window.
type TurnRole string

const (
	TurnUser      TurnRole = "user"
	TurnAssistant TurnRole = "assistant"
	TurnTool      TurnRole = "tool"
	TurnSystem    TurnRole = "system"
	// TurnThinking is reasoning content: a model's hidden deliberation, shown
	// dimmed and collapsible, distinct from its answer (ADR 0038 decision 9).
	// It renders nothing until the upstream reasoning event exists
	// (NAVI-590) — the role is here so that wiring it is a one-line change to
	// the event fold rather than a rendering change.
	TurnThinking TurnRole = "thinking"
)

// turn is one chat block. A turn carries either text (user/assistant/
// thinking/system) or a tool call (tool).
type turn struct {
	role    TurnRole
	text    string // accumulated content (assistant deltas append here)
	tool    string // tool name for tool turns
	toolIn  string // input summary for tool turns
	toolOut string // result for tool turns
	// collapsed is the per-turn fold state: a tool turn starts collapsed to a
	// one-line summary and expands on demand; a thinking turn is collapsible
	// the same way.
	collapsed bool
	// rendered caches this turn's block so a frame that does not change it
	// does not rebuild it. cacheKey is the text it was rendered from, so a
	// streamed turn (whose text grew) re-renders and an unchanged one does
	// not.
	rendered []string
	cacheKey string
	cacheW   int
}

// maxLiveTurns bounds the reconstructed chat. The bound is a scrollback
// window, not a correctness limit — the transcript on disk is the source of
// truth — so the component reports when it has trimmed rather than dropping
// turns silently (NAVI-589: "make the bound visible rather than silent").
const maxLiveTurns = 80

// chat is the conversation component: the turns, the viewport, and whether
// the view is following the tail.
type chat struct {
	turns []turn
	vp    viewport.Model
	// following is tail-follow: new content pins the view to the bottom.
	// Scrolling back suspends it; jumping to the tail restores it.
	following bool
	// trimmed counts turns dropped by the scrollback bound, so the header can
	// say so rather than pretending the conversation starts where it does.
	trimmed int
	// width/height are the last size the viewport was given, so a resize can
	// be detected and the cached blocks invalidated when the wrap changes.
	width  int
	height int
	// dirty forces a full rebuild on the next render (a resize, a re-seed).
	dirty bool
}

// newChat builds an empty chat.
func newChat() chat {
	return chat{following: true, dirty: true}
}

// setSize sizes the viewport. A width change invalidates every cached block,
// because wrapping changed.
func (c *chat) setSize(width, height int) {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	if c.width != width {
		c.dirty = true
	}
	c.width, c.height = width, height
	c.vp.Width = width
	c.vp.Height = height
}

// reset replaces the turn list (opening a different thought, or a re-seed
// from the transcript).
func (c *chat) reset(turns []turn) {
	c.turns = turns
	c.trimmed = 0
	c.dirty = true
	c.following = true
}

// append adds a turn, trimming the scrollback window and counting what was
// dropped.
func (c *chat) append(t turn) {
	c.turns = append(c.turns, t)
	if len(c.turns) > maxLiveTurns {
		drop := len(c.turns) - maxLiveTurns
		c.turns = c.turns[drop:]
		c.trimmed += drop
		c.dirty = true
	}
}

// appendDelta appends text to the trailing assistant turn, or starts one.
// Only the trailing turn changes, so only its block re-renders.
func (c *chat) appendDelta(text string) {
	if n := len(c.turns); n > 0 && c.turns[n-1].role == TurnAssistant {
		c.turns[n-1].text += text
		return
	}
	c.append(turn{role: TurnAssistant, text: text})
}

// toggleFold flips the fold state of the turn at the viewport's top visible
// line's owning turn. For simplicity it toggles the LAST collapsible turn,
// which is what "expand the tool call I just saw" means in practice; a
// per-line mapping would be needed for arbitrary toggling and is not required
// by the issue.
func (c *chat) toggleLastFold() bool {
	for i := len(c.turns) - 1; i >= 0; i-- {
		if c.turns[i].role == TurnTool || c.turns[i].role == TurnThinking {
			c.turns[i].collapsed = !c.turns[i].collapsed
			c.dirty = true
			return true
		}
	}
	return false
}

// update handles a key press that scrolls the view. It returns true when the
// key was consumed by the chat.
//
// Scrolling back suspends tail-follow; jumping to the tail restores it. The
// distinction matters because a live conversation must not yank the reader to
// the bottom while they are reading history, but must resume following once
// they return to the end.
func (c *chat) update(k tea.KeyMsg) bool {
	switch k.String() {
	case "j", "down":
		c.vp.LineDown(1)
	case "k", "up":
		c.vp.LineUp(1)
	case "pgdown", "ctrl+f":
		c.vp.PageDown()
	case "pgup", "ctrl+b":
		c.vp.PageUp()
	case "g", "home":
		c.vp.GotoTop()
	case "G", "end":
		c.vp.GotoBottom()
		c.following = true
	case "f":
		// Jump to tail: the explicit "resume following" binding.
		c.vp.GotoBottom()
		c.following = true
	case "enter":
		// Expand/collapse the most recent foldable turn.
		if c.toggleLastFold() {
			return true
		}
		return false
	default:
		return false
	}
	// A scroll that leaves the bottom suspends follow; one that returns to it
	// resumes. This is what makes the indicator honest.
	if k.String() != "G" && k.String() != "end" && k.String() != "f" {
		c.following = c.vp.AtBottom()
	}
	return true
}

// followSuspended reports whether the reader has scrolled away from the tail,
// which the header shows as an indicator.
func (c *chat) followSuspended() bool { return !c.following }

// trimNotice reports the scrollback trim for the header, or "" when nothing
// has been trimmed. It is the visible half of the scrollback bound: the bound
// is a window over an unbounded transcript, so a reader must be able to tell
// that what they see is not everything.
func (c *chat) trimNotice() string {
	if c.trimmed == 0 {
		return ""
	}
	return "· " + itoa(c.trimmed) + " trimmed"
}

// blocks renders every turn to its cached block, rebuilding only what
// changed. It returns the blocks in order, each already wrapped to the
// viewport width.
func (c *chat) blocks(th theme) [][]string {
	out := make([][]string, len(c.turns))
	for i := range c.turns {
		t := &c.turns[i]
		key := t.text + "\x00" + t.toolIn + "\x00" + t.toolOut
		if t.rendered != nil && t.cacheKey == key && t.cacheW == c.width && !c.dirty {
			out[i] = t.rendered
			continue
		}
		t.rendered = c.renderTurn(*t, th)
		t.cacheKey = key
		t.cacheW = c.width
		out[i] = t.rendered
	}
	c.dirty = false
	return out
}

// lines flattens the blocks into the viewport's content, inserting a blank
// separator line between turns so blocks read as distinct.
//
// The trim notice is deliberately NOT part of the content: the view follows
// the tail by default, so a notice at the top of the content would be
// scrolled off and invisible exactly when it matters. It lives in the header
// (see trimNotice) instead.
func (c *chat) lines(th theme) []string {
	blocks := c.blocks(th)
	var out []string
	for i, b := range blocks {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, b...)
	}
	if len(out) == 0 {
		out = append(out, th.Subtle("  (no conversation yet — stream events land here)"))
	}
	return out
}

// view renders the chat: it refreshes the viewport content (following the
// tail when it should) and returns the visible lines.
func (c *chat) view(th theme) []string {
	wasFollowing := c.following
	c.vp.SetContent(strings.Join(c.lines(th), "\n"))
	if wasFollowing {
		c.vp.GotoBottom()
	}
	return strings.Split(c.vp.View(), "\n")
}

// ── turn rendering ──────────────────────────────────────────────────────

// renderTurn renders one turn to a block of lines, already truncated to the
// viewport width.
//
// The sanitization contract (NAVI-333) is preserved here and is a correctness
// property rather than formatting: a prompt or system line is single-line
// data whose embedded newline/CR/tab would terminate the row early and fuse
// the next row onto the same physical line, so those are neutralized;
// assistant and thinking content legitimately wraps multi-line and is left
// intact.
func (c *chat) renderTurn(t turn, th theme) []string {
	w := c.width
	switch t.role {
	case TurnUser:
		return labelledLines("▸ ", sanitizeLine(t.text), th.User, w)
	case TurnAssistant:
		// Assistant content wraps: split on newlines, no sanitization.
		return labelledLines("◂ ", t.text, th.Assistant, w)
	case TurnThinking:
		// Reasoning: dimmed and italic, collapsible. Renders nothing when
		// there is no reasoning content, which is the state until NAVI-590
		// lands the upstream event.
		if strings.TrimSpace(t.text) == "" {
			return nil
		}
		if t.collapsed {
			return []string{th.Subtle("  ~ thinking (enter to expand)")}
		}
		lines := []string{th.Subtle("  ~ thinking")}
		lines = append(lines, labelledLines("    ", sanitizeLine(t.text), th.Subtle, w)...)
		return lines
	case TurnTool:
		return c.renderToolTurn(t, th)
	case TurnSystem:
		return labelledLines("  · ", sanitizeLine(t.text), th.System, w)
	}
	return labelledLines("  ? ", sanitizeLine(t.text), th.System, w)
}

// renderToolTurn renders a tool call compactly by default and in full when
// expanded. The compact form is one line: the tool name plus a short result
// marker, so a run with twenty tool calls is readable rather than a wall.
func (c *chat) renderToolTurn(t turn, th theme) []string {
	name := sanitizeLine(t.tool)
	if name == "" {
		name = "tool"
	}
	if t.collapsed {
		summary := "⚙ " + name
		if t.toolOut != "" {
			summary += th.Subtle(" · " + truncate(sanitizeLine(t.toolOut), 40, "…"))
		} else if t.toolIn != "" {
			summary += th.Subtle(" · " + truncate(sanitizeLine(t.toolIn), 40, "…"))
		}
		summary += th.Subtle("  (enter to expand)")
		return []string{"  " + th.Tool(summary)}
	}
	lines := []string{"  " + th.Tool("⚙ "+name)}
	if t.toolIn != "" {
		lines = append(lines, labelledLines("    in: ", sanitizeLine(t.toolIn), th.Tool, c.width)...)
	}
	if t.toolOut != "" {
		lines = append(lines, labelledLines("    out: ", sanitizeLine(t.toolOut), th.Tool, c.width)...)
	}
	return lines
}

// labelledLines renders text with a prefix on the first line and a hanging
// indent on the rest, truncating every line to the width. It is the one place
// multi-line turn text is laid out, so the prefix/indent rule is consistent
// across roles.
func labelledLines(prefix, text string, style func(string) string, width int) []string {
	raw := strings.Split(text, "\n")
	out := make([]string, 0, len(raw))
	indent := strings.Repeat(" ", displayWidth(prefix))
	for i, l := range raw {
		p := prefix
		if i > 0 {
			p = indent
		}
		room := width - displayWidth(p)
		if room < 0 {
			room = 0
		}
		out = append(out, style(p+truncate(l, room, "…")))
	}
	return out
}

// chatKeys are the bindings the chat consumes, so the model can decide
// whether to route a key to the chat or handle it itself. This is the seed of
// the generated-help work (NAVI-593): the chat declares what it handles
// rather than the model guessing.
var chatKeys = map[string]bool{
	"j": true, "down": true,
	"k": true, "up": true,
	"pgdown": true, "ctrl+f": true,
	"pgup": true, "ctrl+b": true,
	"g": true, "home": true,
	"G": true, "end": true,
	"f": true, "enter": true,
}

// chatHandles reports whether the chat should receive this key.
func chatHandles(k tea.KeyMsg) bool { return chatKeys[k.String()] }
