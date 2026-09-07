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

// Logger writes structured JSON log lines. Safe for concurrent use.
type Logger struct {
	mu     sync.Mutex
	out    io.Writer
	min    Level
	fields map[string]any
}

// New creates a logger writing to w at the given minimum level, with
// optional base fields injected into every line (context injection:
// navi name, host, etc.).
func New(w io.Writer, min Level, baseFields map[string]any) *Logger {
	return &Logger{out: w, min: min, fields: baseFields}
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

	blob, err := json.Marshal(line)
	if err != nil {
		fmt.Fprintf(l.out, `{"level":"error","msg":"log marshal failed: %v"}`+"\n", err)
		return
	}
	fmt.Fprintf(l.out, "%s\n", blob)
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
