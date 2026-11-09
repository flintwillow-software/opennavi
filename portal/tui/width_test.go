package tui

import (
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/thought"
)

// TestTableColumnsAlignWithWideRunes is the NAVI-588 table bug: a CJK or
// emoji cell must not shift every column after it. The old renderer padded
// with fmt.Sprintf("%-*s", …), which pads by BYTE length, so a 3-byte rune
// consumed three columns of padding while occupying one or two display
// cells — and every subsequent column slid.
//
// The assertion is positional: each column's value starts at the same
// display column on every row, whatever the row contains.
func TestTableColumnsAlignWithWideRunes(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q)
	m := NewModel(tport)
	m.theme = newThemeFor(false)
	resize(m, 120, 24)
	_ = m.Init()

	// One ASCII row, one CJK row, one emoji row — all the same shape.
	ascii := th("a1", 4)
	ascii.Prompt = "plain ascii prompt"
	cjk := th("b2", 4)
	cjk.Prompt = "日本語のプロンプト"
	emoji := th("c3", 4)
	emoji.Prompt = "emoji 🎉 prompt"
	for _, tt := range []*thought.Thought{ascii, cjk, emoji} {
		if err := q.Enqueue(tt); err != nil {
			t.Fatal(err)
		}
	}
	drain(m)

	out := m.View()
	lines := strings.Split(out, "\n")

	// The rows for the three thoughts, in render order.
	var rows []string
	for _, l := range lines {
		if strings.Contains(l, "a1") || strings.Contains(l, "b2") || strings.Contains(l, "c3") {
			rows = append(rows, l)
		}
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d:\n%s", len(rows), out)
	}

	// The column at which each row's status value begins must be identical.
	// The status column follows the prompt column, so any prompt-width error
	// shows up here as a shifted status.
	positions := make([]int, len(rows))
	for i, r := range rows {
		idx := strings.Index(r, "pending")
		if idx < 0 {
			t.Fatalf("row %d has no status cell: %q", i, r)
		}
		// Byte index is not the display column; measure the prefix.
		positions[i] = displayWidth(r[:idx])
	}
	for i := 1; i < len(positions); i++ {
		if positions[i] != positions[0] {
			t.Errorf("status column misaligned: row %d starts at col %d, row 0 at col %d\n%s",
				i, positions[i], positions[0], out)
		}
	}

	// Every rendered line is still exactly the frame width.
	for i, l := range lines {
		if w := displayWidth(l); w != 120 {
			t.Errorf("line %d width = %d, want 120: %q", i, w, l)
		}
	}
}

// TestTruncateRespectsDisplayWidth pins the helper the table layout is built
// on: truncation and padding count display cells, not bytes.
func TestTruncateRespectsDisplayWidth(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		width int
		want  int // expected display width of the result
	}{
		{"ascii fits", "hello", 10, 5},
		{"ascii cut", "hello world", 5, 5},
		{"cjk fits", "日本語", 6, 6},
		{"cjk cut mid-rune", "日本語テスト", 4, 4},
		{"emoji cut", "a🎉b🎉c", 3, 3},
		{"zero width", "anything", 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := truncate(c.in, c.width, "…")
			if w := displayWidth(got); w > c.width {
				t.Errorf("truncate(%q, %d) = %q, width %d > %d", c.in, c.width, got, w, c.width)
			}
			if w := displayWidth(got); c.width > 0 && w == 0 {
				t.Errorf("truncate(%q, %d) = %q, empty result", c.in, c.width, got)
			}
			// The result must be valid UTF-8 — no rune split in half.
			if !isValidUTF8(got) {
				t.Errorf("truncate(%q, %d) = %q, invalid UTF-8 (rune split)", c.in, c.width, got)
			}
		})
	}
}

// TestPadProducesExactWidth pins the padding helper: the result is always
// exactly the requested display width, which is what keeps columns aligned.
func TestPadProducesExactWidth(t *testing.T) {
	for _, s := range []string{"", "a", "hello", "日本語", "🎉🎉", "mixed 日 🎉 text", "a very long value that must be cut"} {
		for _, w := range []int{1, 5, 10, 20} {
			got := pad(s, w)
			if gw := displayWidth(got); gw != w {
				t.Errorf("pad(%q, %d) = %q, width %d, want %d", s, w, got, gw, w)
			}
		}
	}
}

// TestColumnLayoutFitsTheFrame pins that a declared column set is sized to
// the terminal rather than to a fixed 30, so the table never overflows.
func TestColumnLayoutFitsTheFrame(t *testing.T) {
	q := newQueue(t)
	tport, _ := newInProcess(t, q)
	m := NewModel(tport)
	_ = m.Init()
	drain(m)

	for _, width := range []int{60, 80, 100, 120, 200} {
		cols := m.columnLayout(width)
		total := 0
		for _, c := range cols {
			total += c.width + 1 // + the inter-column gap
		}
		if len(cols) > 0 {
			total-- // no trailing gap
		}
		// The layout must not exceed the space the frame leaves it.
		if total > width {
			t.Errorf("width %d: columns total %d, exceeding the frame", width, total)
		}
	}
}

// isValidUTF8 reports whether s is valid UTF-8 (no rune split by truncation).
func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == 0xFFFD {
			return false
		}
	}
	return true
}
