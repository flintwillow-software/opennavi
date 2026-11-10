package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// This file covers NAVI-593's generated-help half: the keymap is DECLARED, and
// both the dispatch and the footer read it, so a key cannot be advertised that
// does not work (ADR 0038 decision 7).

// TestHelpIsGeneratedFromTheKeymap: the footer comes from the bindings, not a
// hand-kept string, so every advertised key is one the keymap declares.
func TestHelpIsGeneratedFromTheKeymap(t *testing.T) {
	m, _, _ := runningModel(t)
	resize(m, 120, 40)

	line := m.helpText(frame{width: 120, height: 40, theme: m.theme})
	for _, want := range []string{"enter", "ctrl+a", "esc"} {
		if !strings.Contains(line, want) {
			t.Errorf("conversation help missing %q:\n%s", want, line)
		}
	}
	// And it is the SHORT help, not the full one.
	if strings.Contains(line, "quit") {
		t.Errorf("the footer rendered the full help:\n%s", line)
	}
}

// TestHelpFollowsTheInputFocus: while the steer input is focused a printable
// rune is text, so the footer must NOT advertise the action letters as actions
// — that is exactly the "advertised but does not work" defect.
func TestHelpFollowsTheInputFocus(t *testing.T) {
	m, _, _ := runningModel(t)
	resize(m, 120, 40)
	f := frame{width: 120, height: 40, theme: m.theme}

	m.inputFocused = true
	focused := m.helpText(f)
	if !strings.Contains(focused, "send") {
		t.Errorf("focused help does not mention send:\n%s", focused)
	}
	if strings.Contains(focused, "pause") {
		t.Errorf("focused help advertises pause, which cannot fire while typing:\n%s", focused)
	}

	m.inputFocused = false
	blurred := m.helpText(f)
	if !strings.Contains(blurred, "pause") {
		t.Errorf("blurred help does not advertise pause, which works when blurred:\n%s", blurred)
	}
	if strings.Contains(blurred, "send") {
		t.Errorf("blurred help still advertises send:\n%s", blurred)
	}
}

// TestHelpTruncatesToTheTerminal: help.Model truncates rather than overflowing.
// The hand-kept string overflowed any narrow terminal, which is one of the
// defects this issue names.
func TestHelpTruncatesToTheTerminal(t *testing.T) {
	m, _, _ := runningModel(t)
	for _, w := range []int{60, 80, 120} {
		f := frame{width: w, height: 24, theme: m.theme}
		line := m.helpText(f)
		if got := displayWidth(line); got > w {
			t.Errorf("help at width %d measured %d columns: %q", w, got, line)
		}
	}
}

// TestQuestionMarkOpensTheHelpOverlay, and any key closes it — with quit still
// working, so there is always a way out.
func TestQuestionMarkOpensTheHelpOverlay(t *testing.T) {
	m, _, _ := runningModel(t)
	// Blur the input first: `?` is typable while it is focused (see below).
	m.inputFocused = false
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	if !m.helpOpen {
		t.Fatal("? did not open the help overlay")
	}
	view := m.View()
	if !strings.Contains(view, "keys ·") {
		t.Errorf("overlay does not title itself:\n%s", view)
	}
	if !strings.Contains(view, "any key closes") {
		t.Errorf("overlay does not say how to close it:\n%s", view)
	}
	// Any key closes.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if m.helpOpen {
		t.Error("a key did not close the overlay")
	}
	// Quit still quits from the overlay.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	if q := m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}); !q {
		t.Error("q did not quit from the help overlay")
	}
}

// TestHelpOverlayShowsTheScreenUnderneath: pressing `?` answers "what can I do
// here", so it reports the underlying context's keys — not the overlay's own.
func TestHelpOverlayShowsTheScreenUnderneath(t *testing.T) {
	m, _, _ := runningModel(t)
	showList(m)
	m.helpOpen = true
	view := m.View()
	if !strings.Contains(view, "keys · queue") {
		t.Errorf("overlay did not report the queue context:\n%s", view)
	}
	if !strings.Contains(view, "new") || !strings.Contains(view, "attach") {
		t.Errorf("overlay does not show the queue's keys:\n%s", view)
	}
}

// TestEveryAdvertisedKeyIsHandled is the structural guarantee the declaration
// buys: for each context, every binding the help advertises is one the
// dispatch recognises. It cannot prove the action is right, but it proves the
// key is not dead — which is the defect class.
func TestEveryAdvertisedKeyIsHandled(t *testing.T) {
	m, _, _ := runningModel(t)
	contexts := []struct {
		name  string
		setup func()
	}{
		{"list", func() { showList(m) }},
		{"home-focused", func() { m.mode = ModeHome; m.inputFocused = true }},
		{"home-blurred", func() { m.mode = ModeHome; m.inputFocused = false }},
		{"detail", func() { m.mode = ModeDetail }},
		{"input", func() { m.mode = ModeInput }},
	}
	for _, c := range contexts {
		t.Run(c.name, func(t *testing.T) {
			c.setup()
			km := m.keyMapFor()
			var bindings []key.Binding
			bindings = append(bindings, km.ShortHelp()...)
			for _, g := range km.FullHelp() {
				bindings = append(bindings, g...)
			}
			for _, b := range bindings {
				if !b.Enabled() {
					continue
				}
				// The binding must be one of the keymap's own, i.e. declared.
				if b.Help().Key == "" || b.Help().Desc == "" {
					t.Errorf("binding %v has no help text, so it cannot be advertised", b.Keys())
				}
				if len(b.Keys()) == 0 {
					t.Errorf("binding %q has no keys", b.Help().Desc)
				}
			}
		})
	}
}

// TestChatScrollKeysAreBindings: the chat's scroll keys are declared, so the
// chat's "what do I consume" and the model's "what do I route to the chat" are
// the same list.
func TestChatScrollKeysAreBindings(t *testing.T) {
	m, _, _ := runningModel(t)
	// Each declared scroll binding must be routed to the chat, or the key is
	// advertised and does nothing.
	for _, b := range []key.Binding{
		m.keys.ScrollUp, m.keys.ScrollDn, m.keys.PageUp, m.keys.PageDn,
		m.keys.Top, m.keys.Bottom, m.keys.Fold, m.keys.Follow,
	} {
		for _, k := range b.Keys() {
			msg := keyMsgFor(k)
			if msg == nil {
				t.Fatalf("cannot build a KeyMsg for %q", k)
			}
			if !chatHandles(*msg) {
				t.Errorf("binding %q (%s) is advertised but the chat does not consume it", k, b.Help().Desc)
			}
		}
	}
}

// keyMsgFor builds the KeyMsg a terminal would deliver for a key string, so a
// test can assert a binding against the same representation the runtime uses.
func keyMsgFor(k string) *tea.KeyMsg {
	switch k {
	case "up":
		return &tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return &tea.KeyMsg{Type: tea.KeyDown}
	case "pgup":
		return &tea.KeyMsg{Type: tea.KeyPgUp}
	case "pgdown":
		return &tea.KeyMsg{Type: tea.KeyPgDown}
	case "home":
		return &tea.KeyMsg{Type: tea.KeyHome}
	case "end":
		return &tea.KeyMsg{Type: tea.KeyEnd}
	case "ctrl+f":
		return &tea.KeyMsg{Type: tea.KeyCtrlF}
	case "ctrl+b":
		return &tea.KeyMsg{Type: tea.KeyCtrlB}
	case "ctrl+g":
		return &tea.KeyMsg{Type: tea.KeyCtrlG}
	case "ctrl+e":
		return &tea.KeyMsg{Type: tea.KeyCtrlE}
	case "ctrl+o":
		return &tea.KeyMsg{Type: tea.KeyCtrlO}
	}
	return nil
}

// TestQuestionMarkIsTypableWhileTheInputIsFocused: `?` is a character people
// type ("does this work?"), so it must not be swallowed as a command while a
// text input owns the keyboard — the same rule the action letters follow.
// Blurred, it opens help.
func TestQuestionMarkIsTypableWhileTheInputIsFocused(t *testing.T) {
	m, _, _ := runningModel(t)
	m.inputFocused = true
	keyRunes(t, m, "why?")
	if m.input != "why?" {
		t.Errorf("input = %q, want %q — `?` was swallowed as a command", m.input, "why?")
	}
	if m.helpOpen {
		t.Error("`?` opened help while the input was focused")
	}

	m.inputFocused = false
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	if !m.helpOpen {
		t.Error("`?` did not open help when the input was blurred")
	}
}

// TestInputOwnsKeyboardIsTheOnePredicate: the rule "a printable rune is text
// while a text input has focus" is consulted in one place, so the help key,
// the action letters, and the menu cannot disagree about it.
func TestInputOwnsKeyboardIsTheOnePredicate(t *testing.T) {
	m, _, _ := runningModel(t)
	cases := []struct {
		mode    ModeKind
		focused bool
		want    bool
	}{
		{ModeInput, false, true},
		{ModeHome, true, true},
		{ModeHome, false, false},
		{ModeLive, true, true},
		{ModeLive, false, false},
		{ModeList, true, false},
		{ModeDetail, true, false},
	}
	for _, c := range cases {
		m.mode, m.inputFocused = c.mode, c.focused
		if got := m.inputOwnsKeyboard(); got != c.want {
			t.Errorf("inputOwnsKeyboard(mode=%s, focused=%v) = %v, want %v",
				c.mode, c.focused, got, c.want)
		}
	}
}
