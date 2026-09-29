package sqlite

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/flintwillow-software/opennavi/store"
)

const timeFormat = time.RFC3339Nano

// ImportJSONL reads a JSONL file written by ExportJSONL (header line,
// then one message per line) and inserts it as a new session within a
// single transaction (— atomic import). If the header
// carries a session ID that already exists, the import fails rather
// than overwriting; use a fresh export or delete the target first.
func (s *Store) ImportJSONL(path string) (*store.Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("sqlite: import open %s: %w", path, err)
	}
	defer f.Close()

	var header exportHeaderLine
	var entries []store.SessionEntry
	scanner := bufio.NewScanner(f)
	// Messages can exceed the 64KB default token size (shell_exec caps
	// at 100KB, file_read at 512KB), so raise the scan ceiling.
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		if line == 1 {
			if err := json.Unmarshal(raw, &header); err != nil {
				return nil, fmt.Errorf("sqlite: import %s: header line: %w", path, err)
			}
			if header.SessionID == "" {
				return nil, fmt.Errorf("sqlite: import %s: header missing session_id", path)
			}
			continue
		}
		var m exportMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("sqlite: import %s: line %d: %w", path, line, err)
		}
		e := store.NewEntry(store.Role(m.Role), m.Content)
		e.ToolName = m.ToolName
		e.ToolCallID = m.ToolCallID
		if m.ToolInput != "" {
			e.ToolInput = json.RawMessage(m.ToolInput)
		}
		if ts, err := time.Parse(timeFormat, m.Timestamp); err == nil {
			e.Time = ts
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: import %s: scan: %w", path, err)
	}
	if header.SessionID == "" {
		return nil, fmt.Errorf("sqlite: import %s: empty file", path)
	}

	created := time.Now().UTC()
	if header.CreatedAt != "" {
		if ts, err := time.Parse(timeFormat, header.CreatedAt); err == nil {
			created = ts
		}
	}
	sess := &store.Session{
		ID:        header.SessionID,
		Name:      header.Name,
		AgentID:   header.AgentID,
		Profile:   header.Profile,
		WorkDir:   header.WorkDir,
		ParentID:  header.ParentID,
		BranchID:  header.BranchID,
		Status:    store.StatusActive,
		Entries:   entries,
		Current:   len(entries) - 1,
		CreatedAt: created,
		UpdatedAt: created,
	}

	// Import is atomic: everything or nothing. Refuse to overwrite an
	// existing session — imports restore or migrate, they don't merge.
	if _, err := s.LoadSession(header.SessionID); err == nil {
		return nil, fmt.Errorf("sqlite: import %s: session %s already exists", path, header.SessionID)
	} else if !errors.Is(err, store.ErrSessionNotFound) {
		return nil, fmt.Errorf("sqlite: import %s: check existing session: %w", path, err)
	}
	if err := s.SaveSession(sess); err != nil {
		return nil, fmt.Errorf("sqlite: import %s: %w", path, err)
	}
	return sess, nil
}
