package tui

import (
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// The keymap is declared once, here, and everything else derives from it
// (NAVI-593, ADR 0038 decision 7):
//
//   - what a key DOES is decided by matching against these bindings, and
//   - what the footer ADVERTISES is generated from the same bindings.
//
// That is the point of declaring them rather than comparing strings at the
// call site: a key that renders in the help line but does nothing is the
// defect this revamp started from, and it is only preventable if the help and
// the dispatch read the same data. Before this, `helpLine` was a hand-kept
// string per mode and the dispatch was a `switch k.String()` — two lists that
// had to be kept in agreement by hand, and did not always agree.
//
// The bindings are grouped so the full help (`?`) can render them as columns
// and the short help (the footer) can pick the ones that matter in context.

// keyMap is the model's bindings, grouped by what they act on. Every binding
// is declared once; a context selects which of them are live, so the same key
// cannot be advertised in one place and handled differently in another.
type keyMap struct {
	// Global: available on every screen.
	Quit    key.Binding
	Help    key.Binding
	Home    key.Binding
	Actions key.Binding
	Back    key.Binding
	Debug   key.Binding

	// Navigation (list).
	Up     key.Binding
	Down   key.Binding
	Open   key.Binding
	Types  key.Binding
	New    key.Binding
	Attach key.Binding

	// Actions on the focused object.
	Pause   key.Binding
	Resume  key.Binding
	Cancel  key.Binding
	Retry   key.Binding
	RisePri key.Binding
	DropPri key.Binding

	// The conversation (home/live).
	Send     key.Binding
	Blur     key.Binding
	Focus    key.Binding
	Fold     key.Binding
	Follow   key.Binding
	ScrollUp key.Binding
	ScrollDn key.Binding
	PageUp   key.Binding
	PageDn   key.Binding
	Top      key.Binding
	Bottom   key.Binding
	ClearIn  key.Binding

	// The input flow (enqueue/attach).
	Submit      key.Binding
	CancelInput key.Binding
}

// defaultKeyMap builds the bindings. Keys are the strings bubbletea reports
// (key.Matches compares against these), so `ctrl+f` is the control chord and
// `f` the letter — which is why the conversation's scroll keys are the ctrl
// chords: a printable letter has to remain typable in the steer input.
func defaultKeyMap() keyMap {
	return keyMap{
		Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		Help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Home:    key.NewBinding(key.WithKeys("1"), key.WithHelp("1", "home")),
		Actions: key.NewBinding(key.WithKeys("ctrl+a"), key.WithHelp("ctrl+a", "actions")),
		Back:    key.NewBinding(key.WithKeys("esc", "escape", "l"), key.WithHelp("esc", "back")),
		Debug:   key.NewBinding(key.WithKeys("D"), key.WithHelp("D", "diagnostics")),

		Up:     key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("j/k", "move")),
		Down:   key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("j/k", "move")),
		Open:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		Types:  key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "type")),
		New:    key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new")),
		Attach: key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "attach")),

		Pause:   key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "pause")),
		Resume:  key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "resume")),
		Cancel:  key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "cancel")),
		Retry:   key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "retry")),
		RisePri: key.NewBinding(key.WithKeys("p"), key.WithHelp("p/P", "priority")),
		DropPri: key.NewBinding(key.WithKeys("P"), key.WithHelp("P", "priority")),

		Send:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
		Blur:     key.NewBinding(key.WithKeys("esc", "escape"), key.WithHelp("esc", "clear/blur")),
		Focus:    key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "blur")),
		Fold:     key.NewBinding(key.WithKeys("ctrl+o"), key.WithHelp("ctrl+o", "fold")),
		Follow:   key.NewBinding(key.WithKeys("ctrl+f"), key.WithHelp("ctrl+f", "follow")),
		ScrollUp: key.NewBinding(key.WithKeys("up"), key.WithHelp("↑/↓", "scroll")),
		ScrollDn: key.NewBinding(key.WithKeys("down"), key.WithHelp("↑/↓", "scroll")),
		PageUp:   key.NewBinding(key.WithKeys("pgup", "ctrl+b"), key.WithHelp("pgup/pgdn", "page")),
		PageDn:   key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgup/pgdn", "page")),
		Top:      key.NewBinding(key.WithKeys("home", "ctrl+g"), key.WithHelp("home/end", "jump")),
		Bottom:   key.NewBinding(key.WithKeys("end", "ctrl+e"), key.WithHelp("home/end", "jump")),
		ClearIn:  key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("ctrl+u", "clear line")),

		Submit:      key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "next")),
		CancelInput: key.NewBinding(key.WithKeys("esc", "escape"), key.WithHelp("esc", "cancel")),
	}
}

// matchesAny reports whether a key matches any of the bindings. It is how the
// dispatch asks "is this key one of ours" without repeating the key strings —
// the binding is the single source of truth for both the check and the help.
func matchesAny(k tea.KeyMsg, bs ...key.Binding) bool {
	for _, b := range bs {
		if key.Matches(k, b) {
			return true
		}
	}
	return false
}

// ── per-context keymaps ─────────────────────────────────────────────────
//
// A context's keymap decides what the footer advertises. Each is a small type
// implementing help.KeyMap over the bindings that are LIVE in that context, so
// the help cannot list a key the dispatch would ignore.

// conversationKeys is the keymap while a conversation is open (home or live).
// Which half is live depends on whether the steer input has focus: focused, a
// printable rune is text, so the letters are not advertised as actions;
// blurred, they are.
type conversationKeys struct {
	km      keyMap
	focused bool
}

func (c conversationKeys) ShortHelp() []key.Binding {
	if c.focused {
		return []key.Binding{
			c.km.Send, c.km.Actions, c.km.Blur, c.km.Fold, c.km.Follow, c.km.Help,
		}
	}
	return []key.Binding{
		c.km.Pause, c.km.Resume, c.km.Cancel, c.km.RisePri,
		c.km.Actions, c.km.Back, c.km.Help,
	}
}

func (c conversationKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{c.km.Send, c.km.Actions, c.km.Fold, c.km.Follow},
		{c.km.Pause, c.km.Resume, c.km.Cancel, c.km.Retry, c.km.RisePri, c.km.DropPri},
		{c.km.ScrollUp, c.km.PageUp, c.km.Top, c.km.Blur, c.km.Focus},
		{c.km.Home, c.km.Back, c.km.Help, c.km.Quit},
	}
}

// listKeys is the keymap on the queue list.
type listKeys struct{ km keyMap }

func (l listKeys) ShortHelp() []key.Binding {
	return []key.Binding{
		l.km.Up, l.km.Open, l.km.New, l.km.Attach, l.km.RisePri,
		l.km.Pause, l.km.Cancel, l.km.Home, l.km.Help,
	}
}

func (l listKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{l.km.Up, l.km.Down, l.km.Open, l.km.Types, l.km.New, l.km.Attach},
		{l.km.Pause, l.km.Resume, l.km.Cancel, l.km.Retry, l.km.RisePri, l.km.DropPri},
		{l.km.Actions, l.km.Home, l.km.Help, l.km.Debug, l.km.Quit},
	}
}

// detailKeys is the keymap on a pushed detail screen.
type detailKeys struct{ km keyMap }

func (d detailKeys) ShortHelp() []key.Binding {
	return []key.Binding{
		d.km.Pause, d.km.Resume, d.km.Cancel, d.km.RisePri,
		d.km.Actions, d.km.Back, d.km.Help,
	}
}

func (d detailKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{d.km.Pause, d.km.Resume, d.km.Cancel, d.km.Retry, d.km.RisePri, d.km.DropPri},
		{d.km.Actions, d.km.Home, d.km.Back, d.km.Help, d.km.Quit},
	}
}

// inputKeys is the keymap while the staged input flow owns the keyboard.
type inputKeys struct{ km keyMap }

func (i inputKeys) ShortHelp() []key.Binding {
	return []key.Binding{i.km.Submit, i.km.CancelInput, i.km.Help}
}

func (i inputKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{i.km.Submit, i.km.CancelInput, i.km.Help, i.km.Quit},
	}
}

// menuKeys is the keymap while the leader action menu is open: the actions it
// offers are the focused object's declared ones, which are not known until the
// menu is built, so only the framing keys are bindings.
type menuKeys struct{ km keyMap }

func (m menuKeys) ShortHelp() []key.Binding {
	return []key.Binding{m.km.Actions, m.km.Back, m.km.Help}
}

func (m menuKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{{m.km.Actions, m.km.Back, m.km.Help, m.km.Quit}}
}

// helpKeys is the keymap while the full-screen help overlay is open.
type helpKeys struct{ km keyMap }

func (h helpKeys) ShortHelp() []key.Binding {
	return []key.Binding{h.km.Help, h.km.Back, h.km.Quit}
}

func (h helpKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{{h.km.Help, h.km.Back, h.km.Quit}}
}

// keyMapFor is the KeyMap for the current context: the ONE place the context
// decides what is advertised. helpLine and the help overlay both read it, so
// the footer and `?` cannot disagree.
//
// The help overlay deliberately reports the keys of the screen UNDERNEATH it,
// not its own: a reader who pressed `?` wants to know what they can do on the
// screen they were on, and telling them the overlay's own two keys answers a
// question nobody asked. (The overlay's dismissal is stated in its body.)
func (m *tuiModel) keyMapFor() help.KeyMap {
	if m.menuOpen {
		return menuKeys{m.keys}
	}
	switch m.mode {
	case ModeHome, ModeLive:
		return conversationKeys{km: m.keys, focused: m.inputFocused}
	case ModeDetail:
		return detailKeys{m.keys}
	case ModeInput:
		return inputKeys{m.keys}
	}
	return listKeys{m.keys}
}

// newHelp builds the help component. Width is set per frame, so the short help
// truncates to the terminal rather than overflowing it — which is what the
// hand-kept footer string did on a narrow terminal.
func newHelp() help.Model {
	h := help.New()
	h.ShortSeparator = " · "
	h.FullSeparator = "   "
	h.Ellipsis = "…"
	return h
}
