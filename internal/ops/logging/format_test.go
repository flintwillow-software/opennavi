package logging

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func capture(t *testing.T, fn func(l *Logger)) []string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	l := New(w, Info, nil)
	fn(l)
	l.mu.Lock()
	l.out = nil // release
	l.mu.Unlock()
	w.Close()
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

// captureWith builds the logger with the given format applied.
func captureWith(t *testing.T, f Format) []string {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	l := New(w, Info, nil)
	l.SetFormat(f)
	l.Info("hello", map[string]any{"thought": "abc", "count": 3})
	w.Close()
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

// ── JSON default unchanged (NAVI-328 acceptance: existing callers) ──

func TestDefaultIsJSON(t *testing.T) {
	out := capture(t, func(l *Logger) {
		l.Info("hello", map[string]any{"k": "v"})
	})
	if len(out) != 1 {
		t.Fatalf("lines = %d", len(out))
	}
	var line map[string]any
	if err := json.Unmarshal([]byte(out[0]), &line); err != nil {
		t.Fatalf("default output must be valid JSON: %v (%q)", err, out[0])
	}
	if line["msg"] != "hello" || line["level"] != "info" || line["k"] != "v" {
		t.Fatalf("fields lost: %v", line)
	}
}

// ── human formatter ──────────────────────────────────────────────────

func TestHumanFormatShape(t *testing.T) {
	out := captureWith(t, FormatHuman)
	if len(out) != 1 {
		t.Fatalf("lines = %d", len(out))
	}
	line := out[0]
	// Shape: time LEVEL msg k=v k2=v2 — fields sorted, level padded.
	if !strings.HasPrefix(line, "20") {
		t.Fatalf("missing timestamp: %q", line)
	}
	if !strings.Contains(line, "INFO  hello") {
		t.Fatalf("level/message not aligned: %q", line)
	}
	if !strings.Contains(line, " count=3") || !strings.Contains(line, " thought=abc") {
		t.Fatalf("fields not rendered: %q", line)
	}
	// Deterministic field order (sorted): count before thought.
	if strings.Index(line, "count=") > strings.Index(line, "thought=") {
		t.Fatalf("fields not sorted: %q", line)
	}
}

func TestHumanFormatLevelPaddingAndValues(t *testing.T) {
	r, w, _ := os.Pipe()
	l := New(w, Debug, nil)
	l.SetFormat(FormatHuman)
	l.Debug("debug msg", nil)
	l.Error("boom", map[string]any{"err": "boom", "code": 7})
	l.Warn("warn msg", map[string]any{"detail": map[string]any{"nested": true}})
	w.Close()

	sc := bufio.NewScanner(r)
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) != 3 {
		t.Fatalf("lines = %d: %v", len(lines), lines)
	}
	// Levels padded to 5, uppercased.
	if !strings.Contains(lines[0], "DEBUG") {
		t.Fatalf("debug line: %q", lines[0])
	}
	if !strings.Contains(lines[1], "ERROR") {
		t.Fatalf("error line: %q", lines[1])
	}
	if !strings.Contains(lines[1], " code=7 err=boom") {
		t.Fatalf("fields sorted (code < err) wrong: %q", lines[1])
	}
	// Nested values render as JSON.
	if !strings.Contains(lines[2], " detail={\"nested\":true}") {
		t.Fatalf("nested value: %q", lines[2])
	}
}

func TestSetFormatSwitchesAtRuntime(t *testing.T) {
	r, w, _ := os.Pipe()
	l := New(w, Info, nil)
	l.Info("json line", nil) // default JSON
	l.SetFormat(FormatHuman)
	l.Info("human line", nil)
	l.SetFormat(FormatJSON)
	l.Info("json again", nil)
	l.SetFormat(Format(99)) // unknown → no-op
	l.Info("still json", nil)
	w.Close()

	sc := bufio.NewScanner(r)
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) != 4 {
		t.Fatalf("lines = %d", len(lines))
	}
	if !strings.HasPrefix(lines[0], "{") || !strings.HasPrefix(lines[2], "{") || !strings.HasPrefix(lines[3], "{") {
		t.Fatalf("json lines malformed: %q", lines)
	}
	if !strings.Contains(lines[1], "INFO  human line") {
		t.Fatalf("human line malformed: %q", lines[1])
	}
}

// ── ParseFormat ──────────────────────────────────────────────────────

func TestParseFormat(t *testing.T) {
	if ParseFormat("human") != FormatHuman {
		t.Fatal("human")
	}
	if ParseFormat("HUMAN") != FormatHuman {
		t.Fatal("case-insensitive human")
	}
	if ParseFormat("json") != FormatJSON {
		t.Fatal("json")
	}
	// Unknown → structured default (safe fallback for logs).
	if ParseFormat("") != FormatJSON || ParseFormat("bogus") != FormatJSON {
		t.Fatal("unknown must fall back to json")
	}
	if !(FormatJSON).Valid() || !(FormatHuman).Valid() || Format(99).Valid() {
		t.Fatal("Valid() broken")
	}
}

// ── base fields + human ──────────────────────────────────────────────

func TestHumanBaseFieldsRendered(t *testing.T) {
	r, w, _ := os.Pipe()
	l := New(w, Info, map[string]any{"navi": "hermes"})
	l.SetFormat(FormatHuman)
	l.Info("started", nil)
	w.Close()

	sc := bufio.NewScanner(r)
	if !sc.Scan() {
		t.Fatal("no line")
	}
	if !strings.Contains(sc.Text(), " navi=hermes") {
		t.Fatalf("base fields not rendered: %q", sc.Text())
	}
}
