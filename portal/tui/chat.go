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
	case "down":
		c.vp.LineDown(1)
	case "up":
		c.vp.LineUp(1)
	case "pgdown":
		c.vp.PageDown()
	case "pgup", "ctrl+b":
		c.vp.PageUp()
	case "ctrl+g", "home":
		c.vp.GotoTop()
	case "ctrl+e", "end":
		c.vp.GotoBottom()
		c.following = true
	case "ctrl+f":
		// Jump to tail: the explicit "resume following" binding. It is a
		// control key because plain `f` is a letter the input needs.
		c.vp.GotoBottom()
		c.following = true
	case "ctrl+o":
		// Expand/collapse the most recent foldable turn. `enter` sends the
		// steer now, so folding needs a key of its own.
		c.toggleLastFold()
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
	for _, b := range blocks {
		// No separator row between turns: each turn is a closed, labelled box,
		// so its own edges mark the boundary and name the role. A blank line on
		// top of that would spend a row per turn saying what the box already
		// says.
		out = append(out, b...)
	}
	if len(out) == 0 {
		out = append(out, th.Subtle("  (no conversation yet — stream events land here)"))
	}
	return out
}

// roleBand is a role's label and the style it is drawn in. The label is what
// distinguishes a turn; the glyph only says which way it points.
func roleBand(role TurnRole, th theme) (string, func(string) string) {
	switch role {
	case TurnUser:
		return "you", th.User
	case TurnAssistant:
		return "assistant", th.Assistant
	case TurnTool:
		return "tool", th.Tool
	case TurnThinking:
		return "thinking", th.Subtle
	case TurnSystem:
		return "system", th.System
	}
	return string(role), th.System
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
	label, labelStyle := roleBand(t.role, th)

	// A turn's text is trimmed of leading and trailing blank lines before it
	// is laid out. Model output routinely ends with newlines (an assistant
	// message that hands off to a tool call is often `"…first.\n\n"`), and
	// those blank lines are invisible data that render as real rows — which
	// is why a gap appeared after some turns and not others.
	text := trimBlankLines(t.text)

	// The body is the turn's content laid out to the box's INNER width, then
	// handed to box() which draws the frame around it. A turn and the editor
	// are the same kind of object (see box), so they share the frame.
	inner := boxInner(w)
	body := func(prefix, text string, style func(string) string) []string {
		return labelledLines(prefix, text, style, inner)
	}

	switch t.role {
	case TurnUser:
		return box(label, labelStyle, nil, body("", sanitizeLine(text), th.User), w)
	case TurnAssistant:
		// Assistant content wraps: split on newlines, no sanitization.
		return box(label, labelStyle, nil, body("", text, th.Assistant), w)
	case TurnThinking:
		// Reasoning: dimmed and collapsible. Renders nothing when there is no
		// reasoning content, which is the state until NAVI-590 lands the
		// upstream event.
		if strings.TrimSpace(text) == "" {
			return nil
		}
		if t.collapsed {
			return box(label, labelStyle, nil, []string{th.Subtle("(ctrl+o to expand)")}, w)
		}
		return box(label, labelStyle, nil, labelledWrapped("", sanitizeLine(text), th.Subtle, inner), w)
	case TurnTool:
		return c.renderToolTurn(t, th, label, labelStyle)
	case TurnSystem:
		// A system line is a short lifecycle note (`turn_end`). It is boxed
		// like every other turn, but with the note as the label so it reads as
		// a marker rather than as content with an empty body.
		return box(sanitizeLine(t.text), th.System, nil, nil, w)
	}
	return box(sanitizeLine(t.text), th.System, nil, nil, w)
}

// renderToolTurn renders a tool call compactly by default and in full when
// expanded. The compact form is one line: the tool name plus a short result
// marker, so a run with twenty tool calls is readable rather than a wall.
func (c *chat) renderToolTurn(t turn, th theme, label string, labelStyle func(string) string) []string {
	name := sanitizeLine(t.tool)
	if name == "" {
		name = "tool"
	}
	inner := boxInner(c.width)
	if t.collapsed {
		// The box is labelled `tool`, so the body carries the call itself:
		// what was run, and a preview of what came back.
		summary := th.Tool("⚙ " + name)
		if t.toolOut != "" {
			summary += th.Subtle(" · " + truncate(firstLine(sanitizeLine(t.toolOut)), 60, "…"))
		} else if t.toolIn != "" {
			summary += th.Subtle(" · " + truncate(firstLine(sanitizeLine(t.toolIn)), 60, "…"))
		}
		summary += th.Subtle("  (ctrl+o to expand)")
		return box(label, labelStyle, nil, []string{summary}, c.width)
	}
	lines := []string{th.Tool("⚙ " + name)}
	if t.toolIn != "" {
		lines = append(lines, labelledWrapped("in: ", sanitizeLine(t.toolIn), th.Tool, inner)...)
	}
	if t.toolOut != "" {
		lines = append(lines, labelledWrapped("out: ", sanitizeLine(t.toolOut), th.Tool, inner)...)
	}
	return box(label, labelStyle, nil, lines, c.width)
}

// trimBlankLines removes leading and trailing blank lines from a turn's text,
// keeping interior ones. Blank lines at the ends are not content: they are an
// artefact of how a model or a tool framed its output, and rendering them
// puts unexplained gaps between turns.
func trimBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	end := len(lines)
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return strings.Join(lines[start:end], "\n")
}

// firstLine is the first line of a block, for a collapsed summary: a tool
// result's first line is its most identifying part.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// labelledWrapped lays text out with a prefix on the first line and a hanging
// indent on the rest, WRAPPING at the width rather than truncating.
//
// A tool result is a block of machine output — a command's stdout, a file's
// contents, an API error — and its tail is usually the part that matters, so
// cutting it off at the edge would hide the answer. Turn text (a prompt, an
// assistant reply) is truncated instead, because it is prose the reader can
// re-read in full elsewhere.
func labelledWrapped(prefix, text string, style func(string) string, width int) []string {
	indent := strings.Repeat(" ", displayWidth(prefix))
	room := width - displayWidth(prefix)
	if room < 1 {
		room = 1
	}
	var out []string
	for i, l := range strings.Split(text, "\n") {
		p := prefix
		if i > 0 {
			p = indent
		}
		for _, seg := range wrapRunes(l, room) {
			out = append(out, style(p+seg))
			p = indent
		}
	}
	return out
}

// wrapRunes breaks a line into segments of at most width display columns,
// preferring a break at a space so words stay whole. A single word longer
// than the width is broken rather than dropped.
func wrapRunes(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	if displayWidth(s) <= width {
		return []string{s}
	}
	var out []string
	var cur strings.Builder
	curW := 0
	lastSpace := -1
	for _, r := range s {
		rw := displayWidth(string(r))
		if curW+rw > width {
			// Break here, backing up to the last space if there is one.
			line := cur.String()
			if lastSpace > 0 {
				out = append(out, strings.TrimRight(line[:lastSpace], " "))
				cur.Reset()
				cur.WriteString(line[lastSpace+1:])
				curW = displayWidth(cur.String())
			} else {
				out = append(out, line)
				cur.Reset()
				curW = 0
			}
			lastSpace = -1
		}
		if r == ' ' {
			lastSpace = cur.Len()
		}
		cur.WriteRune(r)
		curW += rw
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
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
// chatKeys are the bindings the chat consumes while its input is focused.
//
// The letters `j`/`k`/`g`/`G`/`f` are deliberately NOT here. A chat screen
// with a persistent input has to let those characters be typed — a reader
// steering a thought writes prose, and swallowing `k` to scroll would make the
// input unusable. Scrolling therefore uses the keys that are never text: the
// arrows, Page Up/Down, Home/End, and the ctrl combos. `f` (jump to tail)
// moves to ctrl+f, since plain `f` is a letter.
var chatKeys = map[string]bool{
	"down": true, "up": true,
	"pgdown": true, "pgup": true,
	"home": true, "end": true,
	"ctrl+f": true, "ctrl+b": true,
	"ctrl+g": true, "ctrl+e": true,
	// Folding a turn moves to a control key: `enter` sends the steer, and a
	// tool block expands on ctrl+o (for "open").
	"ctrl+o": true,
}

// chatHandles reports whether the chat should receive this key.
func chatHandles(k tea.KeyMsg) bool { return chatKeys[k.String()] }
