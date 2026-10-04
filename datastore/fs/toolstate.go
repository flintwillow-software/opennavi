package fs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
)

// todoDoc is the frontmatter of todos/<naviID>/<todoID>.md; the body is the
// item's text, so a person reading the file reads the item.
type todoDoc struct {
	ID        string `yaml:"id"`
	Status    string `yaml:"status,omitempty"`
	Priority  int    `yaml:"priority"`
	CreatedAt string `yaml:"created_at,omitempty"`
	UpdatedAt string `yaml:"updated_at,omitempty"`
}

// todoManifest is the frontmatter of todos/<naviID>/manifest.md: the ordered id
// list, and nothing else. It is the batch's atomic anchor — written LAST — so a
// rejected or interrupted ReplaceTodos/UpdateTodos leaves the previous list
// authoritative, and item files the manifest does not reference are invisible
// garbage collected on the next write.
type todoManifest struct {
	IDs []string `yaml:"ids"`
}

// noteDoc is the frontmatter of notes/<naviID>/<noteID>.md; the body is the
// note verbatim.
type noteDoc struct {
	ID        string   `yaml:"id"`
	Title     string   `yaml:"title,omitempty"`
	Tags      []string `yaml:"tags,omitempty"`
	CreatedAt string   `yaml:"created_at,omitempty"`
	UpdatedAt string   `yaml:"updated_at,omitempty"`
}

type toolState struct{ b *Backend }

func (s toolState) todoDir(naviID string) string {
	return filepath.Join(s.b.root, dirTodos, safeName(naviID))
}

func (s toolState) manifestPath(naviID string) string {
	return filepath.Join(s.todoDir(naviID), manifestFile)
}

func (s toolState) todoPath(naviID, todoID string) string {
	return filepath.Join(s.todoDir(naviID), safeName(todoID)+".md")
}

func (s toolState) noteDir(naviID string) string {
	return filepath.Join(s.b.root, dirNotes, safeName(naviID))
}

func (s toolState) notePath(naviID, noteID string) string {
	return filepath.Join(s.noteDir(naviID), safeName(noteID)+".md")
}

// safeName keeps a caller-supplied identity from escaping the state root or
// clashing with the layout, while staying readable for ordinary ids.
func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), ".")
	if out == "" {
		return "_"
	}
	if len(out) > 128 {
		out = out[:128]
	}
	return out
}

// ListTodos reads the manifest, then the items it references.
func (s toolState) ListTodos(naviID string, status datastore.TodoStatus, limit int) ([]datastore.Todo, error) {
	items, err := s.readTodos(naviID)
	if err != nil {
		return nil, err
	}
	out := []datastore.Todo{}
	for _, item := range items {
		if status != "" && item.Status != status {
			continue
		}
		out = append(out, item)
	}
	sortTodos(out)
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}

// ReplaceTodos replaces the navi's whole list, anchoring the new list with the
// manifest written after the item files.
func (s toolState) ReplaceTodos(naviID string, items []datastore.Todo) ([]datastore.Todo, error) {
	now := time.Now().UTC()
	next := make([]datastore.Todo, 0, len(items))
	for i, item := range items {
		if item.Status == "" {
			item.Status = datastore.TodoPending
		}
		if !item.Status.Valid() {
			// Reject before writing anything: a rejected batch must leave the
			// previous list intact.
			return nil, fmt.Errorf("fs: todos for %s: item %d has invalid status %q", naviID, i, item.Status)
		}
		if item.ID == "" {
			item.ID = datastore.NewID()
		}
		item.NaviID = naviID
		item.CreatedAt, item.UpdatedAt = now, now
		next = append(next, item)
	}

	s.b.writeMu.Lock()
	defer s.b.writeMu.Unlock()

	prev, err := s.readTodosLocked(naviID)
	if err != nil {
		return nil, err
	}
	prevByID := indexTodos(prev)
	for i := range next {
		// Keep the creation time of an item that survives the replace.
		if old, ok := prevByID[next[i].ID]; ok {
			next[i].CreatedAt = old.CreatedAt
		}
		if err := s.writeTodoLocked(naviID, next[i]); err != nil {
			return nil, err
		}
	}
	if err := s.writeManifestLocked(naviID, next); err != nil {
		return nil, err
	}
	s.collectTodoGarbageLocked(naviID, next)
	return copyTodos(next), nil
}

// UpdateTodos applies a partial batch. The batch is validated in full before
// anything is written, and the manifest is written last.
func (s toolState) UpdateTodos(naviID string, updates []datastore.TodoUpdate) ([]datastore.Todo, error) {
	if len(updates) == 0 {
		return []datastore.Todo{}, nil
	}

	s.b.writeMu.Lock()
	defer s.b.writeMu.Unlock()

	current, err := s.readTodosLocked(naviID)
	if err != nil {
		return nil, err
	}
	next := copyTodos(current)
	now := time.Now().UTC()
	for i, u := range updates {
		if u.Status != nil && !u.Status.Valid() {
			return nil, fmt.Errorf("fs: todos for %s: item %d has invalid status %q", naviID, i, *u.Status)
		}
		idx := -1
		for j := range next {
			if next[j].ID == u.ID {
				idx = j
				break
			}
		}
		if idx < 0 {
			return nil, fmt.Errorf("fs: todos for %s: %w (id %s)", naviID, datastore.ErrTodoNotFound, u.ID)
		}
		if u.Remove {
			next = append(next[:idx], next[idx+1:]...)
			continue
		}
		if u.Text != nil {
			next[idx].Text = *u.Text
		}
		if u.Status != nil {
			next[idx].Status = *u.Status
		}
		if u.Priority != nil {
			next[idx].Priority = *u.Priority
		}
		next[idx].UpdatedAt = now
	}

	for _, item := range next {
		if err := s.writeTodoLocked(naviID, item); err != nil {
			return nil, err
		}
	}
	if err := s.writeManifestLocked(naviID, next); err != nil {
		return nil, err
	}
	s.collectTodoGarbageLocked(naviID, next)

	out := copyTodos(next)
	sortTodos(out)
	return out, nil
}

// ReadNotes reads the navi's notes, filtering by id, tag, or literal substring
// on title/body, most recently updated first.
func (s toolState) ReadNotes(naviID string, q datastore.NoteQuery) ([]datastore.Note, error) {
	dir := s.noteDir(naviID)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []datastore.Note{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fs: read %s: %w", dir, err)
	}
	out := []datastore.Note{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		note, ok, err := s.readNoteFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if q.ID != "" && note.ID != q.ID {
			continue
		}
		if q.Tag != "" && !hasTag(note.Tags, q.Tag) {
			continue
		}
		if q.Query != "" && !strings.Contains(note.Title, q.Query) && !strings.Contains(note.Body, q.Query) {
			continue
		}
		out = append(out, note)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	if q.Limit > 0 && q.Limit < len(out) {
		out = out[:q.Limit]
	}
	return out, nil
}

// UpsertNote stores a note verbatim, preserving the creation time of one that
// already exists.
func (s toolState) UpsertNote(naviID string, n datastore.Note) (datastore.Note, error) {
	s.b.writeMu.Lock()
	defer s.b.writeMu.Unlock()

	now := time.Now().UTC()
	if n.ID == "" {
		n.ID = datastore.NewID()
	} else if prev, ok, err := s.readNoteFile(s.notePath(naviID, n.ID)); err != nil {
		return datastore.Note{}, err
	} else if ok {
		n.CreatedAt = prev.CreatedAt
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = now
	}
	n.NaviID = naviID
	n.UpdatedAt = now

	doc := noteDoc{
		ID:        n.ID,
		Title:     n.Title,
		Tags:      n.Tags,
		CreatedAt: formatTime(n.CreatedAt),
		UpdatedAt: formatTime(n.UpdatedAt),
	}
	data, err := renderFrontmatter(doc, []byte(n.Body))
	if err != nil {
		return datastore.Note{}, err
	}
	if err := writeFile(s.notePath(naviID, n.ID), data); err != nil {
		return datastore.Note{}, err
	}
	return n, nil
}

// RemoveNote deletes one note; an unknown id is a sentinel, not a silent no-op.
func (s toolState) RemoveNote(naviID, id string) error {
	s.b.writeMu.Lock()
	defer s.b.writeMu.Unlock()

	path := s.notePath(naviID, id)
	if _, ok, err := s.readNoteFile(path); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("fs: notes for %s: %w (id %s)", naviID, datastore.ErrNoteNotFound, id)
	}
	if err := removeFile(path); err != nil {
		return fmt.Errorf("fs: notes for %s: remove %s: %w", naviID, id, err)
	}
	return nil
}

// ── internals ───────────────────────────────────────────────────────────

func (s toolState) readTodos(naviID string) ([]datastore.Todo, error) {
	s.b.writeMu.Lock()
	defer s.b.writeMu.Unlock()
	return s.readTodosLocked(naviID)
}

// readTodosLocked resolves the manifest and the items it references. Caller
// holds writeMu.
func (s toolState) readTodosLocked(naviID string) ([]datastore.Todo, error) {
	path := s.manifestPath(naviID)
	meta, exists, hasMeta, err := readFrontmatter(path)
	if err != nil {
		return nil, err
	}
	if !exists || !hasMeta {
		return []datastore.Todo{}, nil
	}
	var manifest todoManifest
	if err := decodeFrontmatter(path, meta, &manifest); err != nil {
		return nil, err
	}
	out := make([]datastore.Todo, 0, len(manifest.IDs))
	for _, id := range manifest.IDs {
		item, ok, err := s.readTodoFile(s.todoPath(naviID, id))
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("fs: %s: manifest references missing todo %s", path, id)
		}
		out = append(out, item)
	}
	return out, nil
}

func (s toolState) readTodoFile(path string) (datastore.Todo, bool, error) {
	data, exists, err := readFileSafe(path)
	if err != nil {
		return datastore.Todo{}, false, err
	}
	if !exists {
		return datastore.Todo{}, false, nil
	}
	meta, body, ok := splitFrontmatter(data)
	if !ok {
		return datastore.Todo{}, false, nil
	}
	var doc todoDoc
	if err := decodeFrontmatter(path, meta, &doc); err != nil {
		return datastore.Todo{}, false, err
	}
	created, err := parseTime(doc.CreatedAt)
	if err != nil {
		return datastore.Todo{}, false, err
	}
	updated, err := parseTime(doc.UpdatedAt)
	if err != nil {
		return datastore.Todo{}, false, err
	}
	return datastore.Todo{
		ID:        doc.ID,
		Text:      strings.TrimSuffix(string(body), "\n"),
		Status:    datastore.TodoStatus(doc.Status),
		Priority:  doc.Priority,
		CreatedAt: created,
		UpdatedAt: updated,
	}, true, nil
}

func (s toolState) writeTodoLocked(naviID string, item datastore.Todo) error {
	doc := todoDoc{
		ID:        item.ID,
		Status:    string(item.Status),
		Priority:  item.Priority,
		CreatedAt: formatTime(item.CreatedAt),
		UpdatedAt: formatTime(item.UpdatedAt),
	}
	data, err := renderFrontmatter(doc, []byte(item.Text+"\n"))
	if err != nil {
		return err
	}
	return writeFile(s.todoPath(naviID, item.ID), data)
}

func (s toolState) writeManifestLocked(naviID string, items []datastore.Todo) error {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	data, err := renderFrontmatter(todoManifest{IDs: ids}, nil)
	if err != nil {
		return err
	}
	return writeFile(s.manifestPath(naviID), data)
}

// collectTodoGarbageLocked removes item files the manifest no longer
// references. Best-effort: leftovers are invisible to readers and removed on
// the next write.
func (s toolState) collectTodoGarbageLocked(naviID string, items []datastore.Todo) {
	keep := make(map[string]bool, len(items))
	for _, item := range items {
		keep[safeName(item.ID)+".md"] = true
	}
	dir := s.todoDir(naviID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || name == manifestFile || !strings.HasSuffix(name, ".md") || keep[name] {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}

func (s toolState) readNoteFile(path string) (datastore.Note, bool, error) {
	data, exists, err := readFileSafe(path)
	if err != nil {
		return datastore.Note{}, false, err
	}
	if !exists {
		return datastore.Note{}, false, nil
	}
	meta, body, ok := splitFrontmatter(data)
	if !ok {
		return datastore.Note{}, false, nil
	}
	var doc noteDoc
	if err := decodeFrontmatter(path, meta, &doc); err != nil {
		return datastore.Note{}, false, err
	}
	created, err := parseTime(doc.CreatedAt)
	if err != nil {
		return datastore.Note{}, false, err
	}
	updated, err := parseTime(doc.UpdatedAt)
	if err != nil {
		return datastore.Note{}, false, err
	}
	return datastore.Note{
		ID:        doc.ID,
		Title:     doc.Title,
		Body:      string(body),
		Tags:      doc.Tags,
		CreatedAt: created,
		UpdatedAt: updated,
	}, true, nil
}

func sortTodos(items []datastore.Todo) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Priority != items[j].Priority {
			return items[i].Priority < items[j].Priority
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
}

func indexTodos(items []datastore.Todo) map[string]datastore.Todo {
	out := make(map[string]datastore.Todo, len(items))
	for _, item := range items {
		out[item.ID] = item
	}
	return out
}

func copyTodos(in []datastore.Todo) []datastore.Todo {
	return append([]datastore.Todo(nil), in...)
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}
