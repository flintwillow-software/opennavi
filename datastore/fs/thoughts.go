package fs

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/thought"
)

// thoughtDoc is the frontmatter of one thought.md: the thought row plus the
// transcript's metadata projection, so TranscriptInfo needs no body read.
//
// The row exists iff ID is non-empty, and the transcript exists iff
// HasTranscript — which is what lets DeleteThought and DeleteTranscript stay
// independent even though the ADR's layout keeps both in one file.
type thoughtDoc struct {
	ID         string            `yaml:"id,omitempty"`
	Prompt     string            `yaml:"prompt,omitempty"`
	Context    string            `yaml:"context,omitempty"`
	Result     string            `yaml:"result,omitempty"`
	Metadata   map[string]string `yaml:"metadata,omitempty"`
	Priority   int               `yaml:"priority"`
	Status     string            `yaml:"status,omitempty"`
	Provider   string            `yaml:"provider,omitempty"`
	Model      string            `yaml:"model,omitempty"`
	Mode       string            `yaml:"mode,omitempty"`
	Skills     []string          `yaml:"skills,omitempty"`
	DependsOn  []string          `yaml:"depends_on,omitempty"`
	Assembled  string            `yaml:"assembled,omitempty"`
	DeferUntil string            `yaml:"defer_until,omitempty"`
	LogPath    string            `yaml:"log_path,omitempty"`
	Checkpoint string            `yaml:"checkpoint,omitempty"` // base64
	CreatedAt  string            `yaml:"created_at,omitempty"`
	UpdatedAt  string            `yaml:"updated_at,omitempty"`

	HasTranscript       bool   `yaml:"has_transcript,omitempty"`
	Messages            int    `yaml:"messages,omitempty"`
	TranscriptUpdatedAt string `yaml:"transcript_updated_at,omitempty"`
}

type thoughts struct{ b *Backend }

func (t thoughts) dir(thoughtID string) string {
	return filepath.Join(t.b.root, dirThoughts, thoughtID)
}

func (t thoughts) path(thoughtID string) string {
	return filepath.Join(t.dir(thoughtID), thoughtFile)
}

// SaveThought upserts the thought row. A row update rewrites the file, so the
// existing transcript body is copied forward untouched rather than parsed.
func (t thoughts) SaveThought(th *thought.Thought) error {
	if strings.TrimSpace(th.ID) == "" {
		return fmt.Errorf("fs: SaveThought: thought id is required")
	}
	t.b.writeMu.Lock()
	defer t.b.writeMu.Unlock()

	path := t.path(th.ID)
	var doc thoughtDoc
	var body []byte
	prev, exists, err := readFileSafe(path)
	if err != nil {
		return err
	}
	if exists {
		if meta, rest, ok := splitFrontmatter(prev); ok {
			if err := decodeFrontmatter(path, meta, &doc); err != nil {
				return err
			}
			body = rest
		}
	}

	row, err := rowDoc(th)
	if err != nil {
		return err
	}
	// The transcript half of the file belongs to the transcript methods.
	row.HasTranscript = doc.HasTranscript
	row.Messages = doc.Messages
	row.TranscriptUpdatedAt = doc.TranscriptUpdatedAt

	data, err := renderFrontmatter(row, body)
	if err != nil {
		return err
	}
	return writeFile(path, data)
}

// LoadThought reads one thought row from frontmatter only.
func (t thoughts) LoadThought(id string) (*thought.Thought, error) {
	path := t.path(id)
	meta, exists, hasMeta, err := readFrontmatter(path)
	if err != nil {
		return nil, err
	}
	if !exists || !hasMeta {
		return nil, thought.ErrThoughtNotFound
	}
	var doc thoughtDoc
	if err := decodeFrontmatter(path, meta, &doc); err != nil {
		return nil, err
	}
	if doc.ID == "" {
		return nil, thought.ErrThoughtNotFound // row deleted, transcript kept
	}
	return docThought(path, &doc)
}

// LoadThoughts enumerates every thought row, reading frontmatter only — the
// property that keeps a cold start over a large state tree affordable.
func (t thoughts) LoadThoughts() ([]*thought.Thought, error) {
	dir := filepath.Join(t.b.root, dirThoughts)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("fs: read %s: %w", dir, err)
	}
	out := []*thought.Thought{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := t.path(entry.Name())
		meta, exists, hasMeta, err := readFrontmatter(path)
		if err != nil {
			return nil, err
		}
		if !exists || !hasMeta {
			continue
		}
		var doc thoughtDoc
		if err := decodeFrontmatter(path, meta, &doc); err != nil {
			return nil, err
		}
		if doc.ID == "" {
			continue
		}
		th, err := docThought(path, &doc)
		if err != nil {
			return nil, err
		}
		out = append(out, th)
	}
	return out, nil
}

// DeleteThought clears the row, keeping any transcript in the same file.
func (t thoughts) DeleteThought(id string) error {
	t.b.writeMu.Lock()
	defer t.b.writeMu.Unlock()

	path := t.path(id)
	prev, exists, err := readFileSafe(path)
	if err != nil {
		return err
	}
	if !exists {
		return thought.ErrThoughtNotFound
	}
	var doc thoughtDoc
	var body []byte
	if meta, rest, ok := splitFrontmatter(prev); ok {
		if err := decodeFrontmatter(path, meta, &doc); err != nil {
			return err
		}
		body = rest
	}
	if doc.ID == "" {
		return thought.ErrThoughtNotFound
	}

	// Drop the row's fields; keep the transcript half.
	doc.ID, doc.Prompt, doc.Context, doc.Result = "", "", "", ""
	doc.Metadata, doc.Skills, doc.DependsOn = nil, nil, nil
	doc.Priority, doc.Status, doc.Provider, doc.Model, doc.Mode = 0, "", "", "", ""
	doc.Assembled, doc.DeferUntil, doc.LogPath, doc.Checkpoint = "", "", "", ""
	doc.CreatedAt, doc.UpdatedAt = "", ""

	if !doc.HasTranscript {
		if err := removeFile(path); err != nil {
			return fmt.Errorf("fs: delete %s: %w", path, err)
		}
		return nil
	}
	data, err := renderFrontmatter(doc, body)
	if err != nil {
		return err
	}
	return writeFile(path, data)
}

// SaveTranscript replaces the transcript for thoughtID, writing the body as a
// unit and refreshing the frontmatter's metadata projection.
func (t thoughts) SaveTranscript(thoughtID string, entries []datastore.Entry) error {
	t.b.writeMu.Lock()
	defer t.b.writeMu.Unlock()

	path := t.path(thoughtID)
	var doc thoughtDoc
	prev, exists, err := readFileSafe(path)
	if err != nil {
		return err
	}
	if exists {
		if meta, _, ok := splitFrontmatter(prev); ok {
			if err := decodeFrontmatter(path, meta, &doc); err != nil {
				return err
			}
		}
	}
	doc.HasTranscript = true
	doc.Messages = len(entries)
	doc.TranscriptUpdatedAt = formatTime(latestEntryTime(entries))

	data, err := renderFrontmatter(doc, renderTranscript(entries))
	if err != nil {
		return err
	}
	return writeFile(path, data)
}

// LoadTranscript reads the conversation. This is the only method that reads
// (and parses) a transcript body.
func (t thoughts) LoadTranscript(thoughtID string) ([]datastore.Entry, error) {
	path := t.path(thoughtID)
	data, exists, err := readFileSafe(path)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, datastore.ErrTranscriptNotFound
	}
	meta, body, ok := splitFrontmatter(data)
	if !ok {
		return nil, datastore.ErrTranscriptNotFound
	}
	var doc thoughtDoc
	if err := decodeFrontmatter(path, meta, &doc); err != nil {
		return nil, err
	}
	if !doc.HasTranscript {
		return nil, datastore.ErrTranscriptNotFound
	}
	t.b.noteBodyRead()
	return parseTranscript(body)
}

// TranscriptInfos enumerates every persisted transcript's metadata in one
// pass: one frontmatter read per thought directory, never a body parse
// (ADR 0011 decision 5).
func (t thoughts) TranscriptInfos() ([]datastore.TranscriptInfo, error) {
	dir := filepath.Join(t.b.root, dirThoughts)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []datastore.TranscriptInfo{}, nil
		}
		return nil, fmt.Errorf("fs: read %s: %w", dir, err)
	}
	out := []datastore.TranscriptInfo{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := t.path(entry.Name())
		meta, exists, hasMeta, err := readFrontmatter(path)
		if err != nil {
			return nil, err
		}
		if !exists || !hasMeta {
			continue
		}
		var doc thoughtDoc
		if err := decodeFrontmatter(path, meta, &doc); err != nil {
			return nil, err
		}
		if !doc.HasTranscript {
			continue
		}
		updated, err := parseTime(doc.TranscriptUpdatedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, datastore.TranscriptInfo{
			ThoughtID:    entry.Name(),
			MessageCount: doc.Messages,
			UpdatedAt:    updated,
		})
	}
	return out, nil
}

// TranscriptInfo answers from frontmatter alone: the message count and update
// time are maintained on every transcript write.
func (t thoughts) TranscriptInfo(thoughtID string) (datastore.TranscriptInfo, error) {
	path := t.path(thoughtID)
	meta, exists, hasMeta, err := readFrontmatter(path)
	if err != nil {
		return datastore.TranscriptInfo{}, err
	}
	if !exists || !hasMeta {
		return datastore.TranscriptInfo{}, datastore.ErrTranscriptNotFound
	}
	var doc thoughtDoc
	if err := decodeFrontmatter(path, meta, &doc); err != nil {
		return datastore.TranscriptInfo{}, err
	}
	if !doc.HasTranscript {
		return datastore.TranscriptInfo{}, datastore.ErrTranscriptNotFound
	}
	updated, err := parseTime(doc.TranscriptUpdatedAt)
	if err != nil {
		return datastore.TranscriptInfo{}, err
	}
	return datastore.TranscriptInfo{
		ThoughtID:    thoughtID,
		MessageCount: doc.Messages,
		UpdatedAt:    updated,
	}, nil
}

// DeleteTranscript clears the transcript, keeping any thought row in the same
// file. Deleting an absent transcript is not an error.
func (t thoughts) DeleteTranscript(thoughtID string) error {
	t.b.writeMu.Lock()
	defer t.b.writeMu.Unlock()

	path := t.path(thoughtID)
	prev, exists, err := readFileSafe(path)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	var doc thoughtDoc
	if meta, _, ok := splitFrontmatter(prev); ok {
		if err := decodeFrontmatter(path, meta, &doc); err != nil {
			return err
		}
	} else {
		return nil // not a record we wrote
	}
	doc.HasTranscript, doc.Messages, doc.TranscriptUpdatedAt = false, 0, ""

	if doc.ID == "" {
		if err := removeFile(path); err != nil {
			return fmt.Errorf("fs: delete transcript %s: %w", path, err)
		}
		return nil
	}
	data, err := renderFrontmatter(doc, nil)
	if err != nil {
		return err
	}
	return writeFile(path, data)
}

// rowDoc projects a thought row into frontmatter.
func rowDoc(th *thought.Thought) (thoughtDoc, error) {
	doc := thoughtDoc{
		ID:         th.ID,
		Prompt:     th.Prompt,
		Context:    th.Context,
		Result:     th.Result,
		Metadata:   th.Metadata,
		Priority:   th.Priority,
		Status:     string(th.Status),
		Provider:   th.Provider,
		Model:      th.Model,
		Mode:       th.Mode,
		Skills:     th.Skills,
		DependsOn:  th.DependsOn,
		Assembled:  th.Assembled,
		DeferUntil: formatTime(th.DeferUntil),
		LogPath:    th.LogPath,
		CreatedAt:  formatTime(th.CreatedAt),
		UpdatedAt:  formatTime(th.UpdatedAt),
	}
	if len(th.Checkpoint) > 0 {
		doc.Checkpoint = base64.StdEncoding.EncodeToString(th.Checkpoint)
	}
	return doc, nil
}

// docThought rebuilds a thought row from frontmatter.
func docThought(path string, doc *thoughtDoc) (*thought.Thought, error) {
	th := &thought.Thought{
		ID:        doc.ID,
		Prompt:    doc.Prompt,
		Context:   doc.Context,
		Result:    doc.Result,
		Metadata:  doc.Metadata,
		Priority:  doc.Priority,
		Status:    thought.ThoughtStatus(doc.Status),
		Provider:  doc.Provider,
		Model:     doc.Model,
		Mode:      doc.Mode,
		Skills:    doc.Skills,
		DependsOn: doc.DependsOn,
		Assembled: doc.Assembled,
		LogPath:   doc.LogPath,
	}
	var err error
	if th.DeferUntil, err = parseTime(doc.DeferUntil); err != nil {
		return nil, fmt.Errorf("fs: %s: defer_until: %w", path, err)
	}
	if th.CreatedAt, err = parseTime(doc.CreatedAt); err != nil {
		return nil, fmt.Errorf("fs: %s: created_at: %w", path, err)
	}
	if th.UpdatedAt, err = parseTime(doc.UpdatedAt); err != nil {
		return nil, fmt.Errorf("fs: %s: updated_at: %w", path, err)
	}
	if doc.Checkpoint != "" {
		decoded, err := base64.StdEncoding.DecodeString(doc.Checkpoint)
		if err != nil {
			return nil, fmt.Errorf("fs: %s: checkpoint is not valid base64: %w", path, err)
		}
		th.Checkpoint = decoded
	}
	return copyThought(th), nil
}
