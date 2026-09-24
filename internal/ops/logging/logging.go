// Package logging implements structured JSON logging for the opennavi
// daemon (#191): timestamp, level, caller, message, and key-value
// context. One JSON object per line to stderr (default) or a file.
//
// Design: stdlib-only (no zap/logrus — the daemon's only requirement
// is JSON lines with consistent fields, and a 40-line implementation
// avoids a dependency for the MVP).
package logging

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Level is the log severity.
type Level int

const (
	Debug Level = iota
	Info
	Warn
	Error
)

func (l Level) String() string {
	switch l {
	case Debug:
		return "debug"
	case Info:
		return "info"
	case Warn:
		return "warn"
	case Error:
		return "error"
	}
	return "unknown"
}

// ParseLevel converts a string to a Level. Unknown → info.
func ParseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return Debug
	case "warn", "warning":
		return Warn
	case "error":
		return Error
	default:
		return Info
	}
}

// Logger writes structured log lines. Format: JSON (default, one
// object per line) or human (console-friendly). Safe for concurrent
// use.
type Logger struct {
	mu     sync.Mutex
	out    io.Writer
	min    Level
	format Format
	fields map[string]any
}

// New creates a logger writing to w at the given minimum level, with
// optional base fields injected into every line (context injection:
// navi name, host, etc.). The format defaults to FormatJSON; switch
// with SetFormat (NAVI-328 log-format knob).
func New(w io.Writer, min Level, baseFields map[string]any) *Logger {
	return &Logger{out: w, min: min, fields: baseFields}
}

// SetFormat switches the line rendering at runtime (NAVI-328 knob:
// the config surface turns the same knob as --log-format). No-op on
// unknown formats.
func (l *Logger) SetFormat(f Format) {
	if !f.Valid() {
		return
	}
	l.mu.Lock()
	l.format = f
	l.mu.Unlock()
}

// SetLevel adjusts the minimum level at runtime (NAVI-328 knob: the
// config surface mirrors --log-level).
func (l *Logger) SetLevel(min Level) {
	l.mu.Lock()
	l.min = min
	l.mu.Unlock()
}

// Default returns a stderr logger at info level.
func Default() *Logger {
	return New(os.Stderr, Info, nil)
}

// Log writes one JSON line if level >= the logger's minimum.
// Fields override base fields; reserved keys (level, time, msg) are
// ignored in the fields map.
func (l *Logger) Log(level Level, msg string, fields map[string]any) {
	if l == nil || level < l.min {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	line := map[string]any{
		"level": level.String(),
		"time":  time.Now().UTC().Format(time.RFC3339Nano),
		"msg":   msg,
	}
	for k, v := range l.fields {
		if _, reserved := line[k]; !reserved {
			line[k] = v
		}
	}
	for k, v := range fields {
		if _, reserved := line[k]; !reserved {
			line[k] = v
		}
	}

	if l.format == FormatHuman {
		l.writeHumanLocked(level, msg, line)
		return
	}

	blob, err := json.Marshal(line)
	if err != nil {
		fmt.Fprintf(l.out, `{"level":"error","msg":"log marshal failed: %v"}`+"\n", err)
		return
	}
	fmt.Fprintf(l.out, "%s\n", blob)
}

// writeHumanLocked renders the console-friendly line (NAVI-328):
// `time LEVEL msg key=value ...` — fields sorted for deterministic
// output, level padded for alignment. Reserved keys (level/time/msg)
// are excluded from the field tail (already rendered).
func (l *Logger) writeHumanLocked(level Level, msg string, line map[string]any) {
	var b strings.Builder
	b.WriteString(line["time"].(string))
	b.WriteByte(' ')
	b.WriteString(fmt.Sprintf("%-5s", strings.ToUpper(level.String())))
	b.WriteByte(' ')
	b.WriteString(msg)
	keys := make([]string, 0, len(line))
	for k := range line {
		switch k {
		case "level", "time", "msg":
			continue // rendered above
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteByte(' ')
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(renderHumanValue(line[k]))
	}
	b.WriteByte('\n')
	fmt.Fprint(l.out, b.String())
}

// renderHumanValue renders a field value in k=v form: plain strings
// unquoted, everything else JSON-encoded (maps, numbers, nested).
func renderHumanValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	blob, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(blob)
}

// Debug logs at debug level.
func (l *Logger) Debug(msg string, fields map[string]any) { l.Log(Debug, msg, fields) }

// Info logs at info level.
func (l *Logger) Info(msg string, fields map[string]any) { l.Log(Info, msg, fields) }

// Warn logs at warn level.
func (l *Logger) Warn(msg string, fields map[string]any) { l.Log(Warn, msg, fields) }

// Error logs at error level.
func (l *Logger) Error(msg string, fields map[string]any) { l.Log(Error, msg, fields) }

// WithFields returns a child logger with additional base fields
// (context injection: thought id, schedule name, etc.). The parent is
// unchanged.
func (l *Logger) WithFields(extra map[string]any) *Logger {
	merged := make(map[string]any, len(l.fields)+len(extra))
	for k, v := range l.fields {
		merged[k] = v
	}
	for k, v := range extra {
		if _, reserved := merged[k]; !reserved {
			merged[k] = v
		}
	}
	return &Logger{out: l.out, min: l.min, fields: merged}
}

// Truncate returns the first n runes of s (for logging prompts and
// tool output without leaking full content).
func Truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// Writer is an alias for io.Writer (sugar for the logging package).
type Writer = io.Writer

// StderrWriter returns a stderr io.Writer.
func StderrWriter() io.Writer { return os.Stderr }

// NewFileWriter returns an append-mode file writer for the given path.
func NewFileWriter(path string) (io.Writer, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}
