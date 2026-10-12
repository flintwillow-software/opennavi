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
	"github.com/flintwillow-software/opennavi/principal"
)

// memoryDoc is the frontmatter of memory/<scope>/<id>.md; the body is the
// record's content, so a person can read what the navi remembers.
type memoryDoc struct {
	ID         string   `yaml:"id"`
	Scope      string   `yaml:"scope,omitempty"`
	Subject    string   `yaml:"subject,omitempty"`
	Tags       []string `yaml:"tags,omitempty"`
	Importance float64  `yaml:"importance,omitempty"`
	ExpiresAt  string   `yaml:"expires_at,omitempty"`
	CreatedAt  string   `yaml:"created_at,omitempty"`
	UpdatedAt  string   `yaml:"updated_at,omitempty"`
}

type memory struct{ b *Backend }

func (m memory) scopeDir(scope datastore.Scope) string {
	name := safeName(string(scope))
	if strings.TrimSpace(string(scope)) == "" {
		name = "_"
	}
	return filepath.Join(m.b.root, dirMemory, name)
}

func (m memory) path(scope datastore.Scope, id string) string {
	return filepath.Join(m.scopeDir(scope), safeName(id)+".md")
}

// Remember stores a record, preserving the creation time of one that already
// exists (and moving it if its scope changed).
func (m memory) Remember(rec datastore.MemoryRecord) (datastore.MemoryRecord, error) {
	m.b.writeMu.Lock()
	defer m.b.writeMu.Unlock()

	now := time.Now().UTC()
	var previousPath string
	if rec.ID == "" {
		rec.ID = datastore.NewID()
	} else if path, prev, ok, err := m.locate(rec.ID); err != nil {
		return datastore.MemoryRecord{}, err
	} else if ok {
		rec.CreatedAt = prev.CreatedAt
		previousPath = path
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now

	doc := memoryDoc{
		ID:         rec.ID,
		Scope:      string(rec.Scope),
		Subject:    rec.Subject.String(),
		Tags:       rec.Tags,
		Importance: rec.Importance,
		ExpiresAt:  formatTime(rec.ExpiresAt),
		CreatedAt:  formatTime(rec.CreatedAt),
		UpdatedAt:  formatTime(rec.UpdatedAt),
	}
	data, err := renderFrontmatter(doc, []byte(rec.Content))
	if err != nil {
		return datastore.MemoryRecord{}, err
	}
	path := m.path(rec.Scope, rec.ID)
	if err := writeFile(path, data); err != nil {
		return datastore.MemoryRecord{}, err
	}
	if previousPath != "" && previousPath != path {
		// The record moved scope: the old file would otherwise be a second
		// copy of the same id.
		if err := removeFile(previousPath); err != nil {
			return datastore.MemoryRecord{}, fmt.Errorf("fs: memory %s: remove %s: %w", rec.ID, previousPath, err)
		}
	}
	return rec, nil
}

// Recall returns records matching the query, most relevant first.
func (m memory) Recall(q datastore.RecallQuery) ([]datastore.MemoryRecord, error) {
	root := filepath.Join(m.b.root, dirMemory)
	scopes, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []datastore.MemoryRecord{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fs: read %s: %w", root, err)
	}
	out := []datastore.MemoryRecord{}
	for _, scope := range scopes {
		if !scope.IsDir() {
			continue
		}
		dir := filepath.Join(root, scope.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("fs: read %s: %w", dir, err)
		}
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".md") {
				continue
			}
			rec, ok, err := m.readFile(filepath.Join(dir, file.Name()))
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			if q.Scope != "" && rec.Scope != q.Scope {
				continue
			}
			if !q.Subject.IsZero() && rec.Subject != q.Subject {
				continue
			}
			if q.Query != "" && !strings.Contains(strings.ToLower(rec.Content), strings.ToLower(q.Query)) {
				continue
			}
			if !hasAllTags(rec.Tags, q.Tags) {
				continue
			}
			out = append(out, rec)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Importance != out[j].Importance {
			return out[i].Importance > out[j].Importance
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if q.Limit > 0 && q.Limit < len(out) {
		out = out[:q.Limit]
	}
	return out, nil
}

// Forget deletes a record; an unknown id is a sentinel, not a silent no-op.
func (m memory) Forget(id string) error {
	m.b.writeMu.Lock()
	defer m.b.writeMu.Unlock()

	path, _, ok, err := m.locate(id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("fs: memory %s: %w", id, datastore.ErrMemoryNotFound)
	}
	if err := removeFile(path); err != nil {
		return fmt.Errorf("fs: memory %s: remove %s: %w", id, path, err)
	}
	return nil
}

// locate finds a record by id across scope directories (the directory is
// derived from the scope the record was written with).
func (m memory) locate(id string) (string, datastore.MemoryRecord, bool, error) {
	root := filepath.Join(m.b.root, dirMemory)
	scopes, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return "", datastore.MemoryRecord{}, false, nil
	}
	if err != nil {
		return "", datastore.MemoryRecord{}, false, fmt.Errorf("fs: read %s: %w", root, err)
	}
	for _, scope := range scopes {
		if !scope.IsDir() {
			continue
		}
		path := filepath.Join(root, scope.Name(), safeName(id)+".md")
		rec, ok, err := m.readFile(path)
		if err != nil {
			return "", datastore.MemoryRecord{}, false, err
		}
		if ok {
			return path, rec, true, nil
		}
	}
	return "", datastore.MemoryRecord{}, false, nil
}

func (m memory) readFile(path string) (datastore.MemoryRecord, bool, error) {
	data, exists, err := readFileSafe(path)
	if err != nil {
		return datastore.MemoryRecord{}, false, err
	}
	if !exists {
		return datastore.MemoryRecord{}, false, nil
	}
	meta, body, ok := splitFrontmatter(data)
	if !ok {
		return datastore.MemoryRecord{}, false, nil
	}
	var doc memoryDoc
	if err := decodeFrontmatter(path, meta, &doc); err != nil {
		return datastore.MemoryRecord{}, false, err
	}
	created, err := parseTime(doc.CreatedAt)
	if err != nil {
		return datastore.MemoryRecord{}, false, err
	}
	updated, err := parseTime(doc.UpdatedAt)
	if err != nil {
		return datastore.MemoryRecord{}, false, err
	}
	expires, err := parseTime(doc.ExpiresAt)
	if err != nil {
		return datastore.MemoryRecord{}, false, err
	}
	// Migration: a record written before the principal reference carries the
	// navi's bare name; ParseLegacy reads it as navi:<name> so it stays
	// visible under the qualified query.
	var subject principal.Ref
	if strings.TrimSpace(doc.Subject) != "" {
		if subject, err = principal.ParseLegacy(doc.Subject); err != nil {
			return datastore.MemoryRecord{}, false, err
		}
	}
	return datastore.MemoryRecord{
		ID:         doc.ID,
		Scope:      datastore.Scope(doc.Scope),
		Subject:    subject,
		Content:    string(body),
		Tags:       doc.Tags,
		Importance: doc.Importance,
		ExpiresAt:  expires,
		CreatedAt:  created,
		UpdatedAt:  updated,
	}, true, nil
}

func hasAllTags(have, want []string) bool {
	for _, w := range want {
		if !hasTag(have, w) {
			return false
		}
	}
	return true
}
