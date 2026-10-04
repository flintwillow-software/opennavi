// Package fs is the markdown + frontmatter datastore backend: a navi's state
// as a directory tree a person can read, diff, and back up with ordinary
// tools. ADR 0011 makes it the first shipped backend and the compiled
// default; `datastore/inmem` stays the non-durable reference backend.
//
// # Layout
//
// Everything lives under one per-navi root (the `path` datastore parameter):
//
//	thoughts/<thoughtID>/thought.md   frontmatter = the thought row, body = its transcript
//	todos/<naviID>/manifest.md        the ordered todo id list (the atomic anchor)
//	todos/<naviID>/<todoID>.md        frontmatter = the item, body = its text
//	notes/<naviID>/<noteID>.md        frontmatter = title/tags/timestamps, body verbatim
//	memory/<scope>/<id>.md            frontmatter = scope/subject/tags/importance/expiry, body = content
//
// # Two structural properties
//
// Metadata without bodies. Frontmatter carries everything a cold start
// enumerates — the thought row and the transcript's message count and update
// time — and the reader stops at the closing delimiter, so LoadThoughts,
// LoadThought, and TranscriptInfo never parse a conversation. Only
// LoadTranscript does, which the conformance suite checks through
// conformance.ReadReporter.
//
// Atomicity. Every write goes to a temp file in the same directory and is
// renamed into place, so a reader sees the old file or the new one and never a
// half-written one. A todo batch is anchored by its manifest, written last: an
// interrupted batch leaves the previous list authoritative and any leftover
// files are unreferenced garbage, collected on the next write. One root is one
// writer — Open takes an advisory lock on `<root>/.lock` and fails loudly if
// another process holds it, so two copies of a binary pointed at the same root
// cannot silently interleave.
package fs

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/thought"
)

// ParamPath is the runtime-config datastore parameter naming the per-navi
// state root. It is required: a backend with no root would have to guess a
// location, and guessing where to write a navi's memory is worse than failing
// (ADR 0009's stance).
const ParamPath = "path"

// Directory and file names of the layout above.
const (
	dirThoughts  = "thoughts"
	dirTodos     = "todos"
	dirNotes     = "notes"
	dirMemory    = "memory"
	thoughtFile  = "thought.md"
	manifestFile = "manifest.md"
	lockFile     = ".lock"
)

// Candidate returns the backend descriptor. It is marked default: ADR 0011
// makes `fs` the first shipped backend and the compiled default, and exactly
// one candidate in a compiled set may carry the flag.
func Candidate() datastore.Candidate {
	return datastore.Candidate{
		Name:    "fs",
		Default: true,
		Open:    Open,
	}
}

// Open builds the backend for the configured root (Params["path"]).
func Open(p datastore.Params) (datastore.Backend, error) {
	root := strings.TrimSpace(p[ParamPath])
	if root == "" {
		return nil, fmt.Errorf("fs: datastore parameter %q is required (the per-navi state root)", ParamPath)
	}
	return New(root)
}

// CandidateAt returns the descriptor with a compiled-in root, for a
// composition root that knows its state directory at declaration (from a flag,
// say) and has no runtime config file. A runtime config `path` parameter still
// wins when present: config narrows or overrides, it never has to restate a
// compiled default.
func CandidateAt(root string) datastore.Candidate {
	c := Candidate()
	inner := c.Open
	c.Open = func(p datastore.Params) (datastore.Backend, error) {
		if strings.TrimSpace(p[ParamPath]) == "" && strings.TrimSpace(root) != "" {
			p = datastore.Params{ParamPath: root}
		}
		return inner(p)
	}
	return c
}

// Backend is the markdown + frontmatter backend. It implements
// datastore.Backend.
type Backend struct {
	root string
	lock *os.File

	// writeMu serializes mutations. Files are replaced by rename, so readers
	// need no lock, but a mutation that preserves the part it does not own
	// (a thought row preserving its transcript body, an entry preserving its
	// creation time) is a read-modify-write and must not interleave.
	writeMu sync.Mutex

	// bodyReads counts transcript body loads, for the conformance suite's
	// metadata-without-bodies check (conformance.ReadReporter).
	bodyReads int64
}

var _ datastore.Backend = (*Backend)(nil)

// New creates the backend rooted at root, creating the tree and taking the
// root's advisory lock. The caller owns Close.
func New(root string) (*Backend, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("fs: resolve root %q: %w", root, err)
	}
	for _, dir := range []string{
		abs,
		filepath.Join(abs, dirThoughts),
		filepath.Join(abs, dirTodos),
		filepath.Join(abs, dirNotes),
		filepath.Join(abs, dirMemory),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("fs: create %s: %w", dir, err)
		}
	}
	b := &Backend{root: abs}
	if err := b.acquireLock(); err != nil {
		return nil, err
	}
	return b, nil
}

// Root returns the backend's absolute state root.
func (b *Backend) Root() string { return b.root }

// Close releases the root lock. Nothing else is held open between calls.
func (b *Backend) Close() error {
	if b.lock == nil {
		return nil
	}
	err := b.releaseLock()
	b.lock = nil
	return err
}

// ResetReads clears the transcript read counter (conformance.ReadReporter).
func (b *Backend) ResetReads() { atomic.StoreInt64(&b.bodyReads, 0) }

// BodyReads reports transcript body loads since the last ResetReads. Reading
// frontmatter is deliberately not a body read: cold-start enumeration must not
// touch a conversation (ADR 0011 decision 5).
func (b *Backend) BodyReads() int { return int(atomic.LoadInt64(&b.bodyReads)) }

func (b *Backend) noteBodyRead() { atomic.AddInt64(&b.bodyReads, 1) }

// ── aggregates ──────────────────────────────────────────────────────────

func (b *Backend) Thoughts() datastore.Thoughts   { return thoughts{b} }
func (b *Backend) ToolState() datastore.ToolState { return toolState{b} }
func (b *Backend) Memory() datastore.Memory       { return memory{b} }

// ── files ───────────────────────────────────────────────────────────────

// writeFile replaces path atomically: a temp file in the same directory is
// written, flushed, and renamed over the target, so a concurrent reader never
// observes a partial file, and an interrupted write leaves the previous file
// in place.
func writeFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("fs: create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("fs: temp file in %s: %w", dir, err)
	}
	name := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(name)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("fs: write %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("fs: sync %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("fs: close %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return fmt.Errorf("fs: rename into %s: %w", path, err)
	}
	return nil
}

// removeFile deletes path, tolerating absence.
func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// readFileSafe reads a file, reporting absence distinctly so callers can map it
// onto their aggregate's sentinel error.
func readFileSafe(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("fs: read %s: %w", path, err)
	}
	return data, true, nil
}

// ── frontmatter ─────────────────────────────────────────────────────────

const frontmatterDelim = "---"

// renderFrontmatter writes the delimited YAML block plus the body.
func renderFrontmatter(meta any, body []byte) ([]byte, error) {
	yamlBytes, err := yaml.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("fs: encode frontmatter: %w", err)
	}
	var b bytes.Buffer
	b.WriteString(frontmatterDelim)
	b.WriteString("\n")
	b.Write(yamlBytes)
	b.WriteString(frontmatterDelim)
	b.WriteString("\n")
	b.Write(body)
	return b.Bytes(), nil
}

// splitFrontmatter separates a file into its YAML block and its body.
func splitFrontmatter(data []byte) (meta, body []byte, ok bool) {
	rest, found := bytes.CutPrefix(data, []byte(frontmatterDelim+"\n"))
	if !found {
		return nil, data, false
	}
	end := bytes.Index(rest, []byte("\n"+frontmatterDelim+"\n"))
	if end < 0 {
		return nil, data, false
	}
	return rest[:end], rest[end+len("\n"+frontmatterDelim+"\n"):], true
}

// readFrontmatter reads ONLY the frontmatter block of a file: it stops at the
// closing delimiter instead of slurping the body, which is what makes cold-start
// enumeration over a large state tree affordable. It returns the parsed YAML
// bytes, whether the file exists, and whether it carried a frontmatter block.
func readFrontmatter(path string) (meta []byte, exists, hasMeta bool, err error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, false, nil
	}
	if err != nil {
		return nil, false, false, fmt.Errorf("fs: open %s: %w", path, err)
	}
	defer f.Close()

	r := bufio.NewReader(f)
	first, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, true, false, fmt.Errorf("fs: read %s: %w", path, err)
	}
	if strings.TrimRight(first, "\n") != frontmatterDelim {
		return nil, true, false, nil // no frontmatter: not a record we wrote
	}
	var buf bytes.Buffer
	for {
		line, err := r.ReadString('\n')
		if strings.TrimRight(line, "\n") == frontmatterDelim {
			return buf.Bytes(), true, true, nil
		}
		buf.WriteString(line)
		if err != nil {
			// Unterminated frontmatter: treat the file as unreadable rather
			// than silently accepting a partial record.
			if errors.Is(err, io.EOF) {
				return nil, true, false, fmt.Errorf("fs: %s: unterminated frontmatter", path)
			}
			return nil, true, false, fmt.Errorf("fs: read %s: %w", path, err)
		}
	}
}

// decodeFrontmatter parses a frontmatter block into out, naming the file in
// any error so a hand-edited file is diagnosable.
func decodeFrontmatter(path string, meta []byte, out any) error {
	if err := yaml.Unmarshal(meta, out); err != nil {
		return fmt.Errorf("fs: %s: parse frontmatter: %w", path, err)
	}
	return nil
}

// formatTime renders a timestamp for frontmatter: RFC 3339 with nanoseconds,
// readable and lossless.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// parseTime reads a frontmatter timestamp; empty means the zero time.
func parseTime(s string) (time.Time, error) {
	if strings.TrimSpace(s) == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("fs: invalid timestamp %q: %w", s, err)
	}
	return t.UTC(), nil
}

// copyThought deep-copies a thought so a backend never aliases caller memory.
func copyThought(th *thought.Thought) *thought.Thought {
	out := *th
	if th.Metadata != nil {
		out.Metadata = make(map[string]string, len(th.Metadata))
		for k, v := range th.Metadata {
			out.Metadata[k] = v
		}
	}
	out.Skills = append([]string(nil), th.Skills...)
	out.DependsOn = append([]string(nil), th.DependsOn...)
	out.Checkpoint = append([]byte(nil), th.Checkpoint...)
	return &out
}
