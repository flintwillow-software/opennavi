package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func decode(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	return m
}

func TestLevels(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, Debug, nil)
	l.Debug("dbg", nil)
	l.Info("inf", nil)
	l.Warn("wrn", nil)
	l.Error("err", nil)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4", len(lines))
	}
	levels := []string{"debug", "info", "warn", "error"}
	for i, want := range levels {
		m := decode(t, bytes.NewBufferString(lines[i]))
		if m["level"] != want {
			t.Errorf("line %d level = %v, want %q", i, m["level"], want)
		}
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, Warn, nil)
	l.Debug("hidden", nil)
	l.Info("hidden", nil)
	l.Warn("shown", nil)
	l.Error("shown", nil)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2 (warn + error only)", len(lines))
	}
}

func TestJSONShape(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, Info, nil)
	l.Info("thought complete", map[string]any{
		"thought": "cli-123", "duration_ms": 4521,
	})
	m := decode(t, &buf)
	if m["msg"] != "thought complete" {
		t.Fatalf("msg = %v", m["msg"])
	}
	if m["level"] != "info" {
		t.Fatalf("level = %v", m["level"])
	}
	if m["thought"] != "cli-123" {
		t.Fatalf("thought = %v", m["thought"])
	}
	// duration_ms is a number, not a string
	if _, ok := m["duration_ms"].(float64); !ok {
		t.Fatalf("duration_ms type = %T", m["duration_ms"])
	}
	// time is present and parseable as a string
	ts, ok := m["time"].(string)
	if !ok || len(ts) < 20 {
		t.Fatalf("time = %v", m["time"])
	}
}

func TestBaseFieldsInjected(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, Info, map[string]any{"navi": "oncall-bot"})
	l.Info("started", nil)
	m := decode(t, &buf)
	if m["navi"] != "oncall-bot" {
		t.Fatalf("base field navi = %v", m["navi"])
	}
}

func TestWithFieldsContextInjection(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, Info, map[string]any{"navi": "oncall-bot"})
	child := l.WithFields(map[string]any{"thought": "cli-123"})
	child.Info("complete", nil)

	m := decode(t, &buf)
	if m["navi"] != "oncall-bot" || m["thought"] != "cli-123" {
		t.Fatalf("merged fields = %v", m)
	}
	// Parent unchanged.
	buf.Reset()
	l.Info("no thought field", nil)
	m = decode(t, &buf)
	if _, has := m["thought"]; has {
		t.Fatal("parent logger polluted by child")
	}
}

func TestReservedKeysNotOverridden(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, Info, nil)
	l.Info("msg", map[string]any{"level": "fake", "time": "fake", "msg": "fake"})
	m := decode(t, &buf)
	if m["level"] != "info" || m["msg"] != "msg" {
		t.Fatalf("reserved keys overridden: %v", m)
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]Level{
		"debug": Debug, "DEBUG": Debug, "Debug": Debug,
		"info": Info, "": Info, "unknown": Info,
		"warn": Warn, "warning": Warn,
		"error": Error, "ERROR": Error,
	}
	for input, want := range cases {
		if got := ParseLevel(input); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("short", 10); got != "short" {
		t.Errorf("short = %q", got)
	}
	long := strings.Repeat("a", 100)
	got := Truncate(long, 20)
	if len([]rune(got)) != 21 { // 20 + ellipsis
		t.Fatalf("truncated = %d runes, want 21", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatal("truncated missing ellipsis")
	}
}

func TestConcurrent(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, Info, nil)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				l.Info("concurrent", map[string]any{"worker": n})
			}
		}(i)
	}
	wg.Wait()
	count := strings.Count(buf.String(), "\n")
	if count == 0 {
		t.Fatal("no lines written")
	}
}
