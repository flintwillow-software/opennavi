package sqlite

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/flintwillow-software/opennavi/store"
)

// exportMessage is one line/element of the export format (spec §07.2).
type exportMessage struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	Timestamp  string `json:"timestamp"`
	ToolName   string `json:"tool_name,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolInput  string `json:"tool_input,omitempty"`
}

// exportDocument is the top-level JSON export structure (spec §07.2).
type exportDocument struct {
	SessionID string          `json:"session_id"`
	Name      string          `json:"name"`
	AgentID   string          `json:"agent_id"`
	CreatedAt string          `json:"created_at"`
	Messages  []exportMessage `json:"messages"`
}

// exportHeaderLine is the first line of a JSONL export: the session
// metadata the import path needs to reconstruct the session row.
type exportHeaderLine struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
	AgentID   string `json:"agent_id"`
	Profile   string `json:"profile"`
	WorkDir   string `json:"work_dir"`
	ParentID  string `json:"parent_id,omitempty"`
	BranchID  string `json:"branch_id,omitempty"`
	CreatedAt string `json:"created_at"`
}

func exportMessages(sess *store.Session) ([]exportMessage, error) {
	out := make([]exportMessage, 0, len(sess.Entries))
	for _, e := range sess.Entries {
		m := exportMessage{
			Role:      string(e.Role),
			Content:   e.Message,
			Timestamp: e.Time.UTC().Format(timeFormat),
		}
		if e.ToolName != "" {
			m.ToolName = e.ToolName
		}
		if e.ToolCallID != "" {
			m.ToolCallID = e.ToolCallID
		}
		if len(e.ToolInput) > 0 {
			m.ToolInput = string(e.ToolInput)
		}
		out = append(out, m)
	}
	return out, nil
}

// ExportJSON writes the session to path in the machine-readable JSON
// format from spec §07.2.
func (s *Store) ExportJSON(sess *store.Session, path string) error {
	msgs, err := exportMessages(sess)
	if err != nil {
		return err
	}
	doc := exportDocument{
		SessionID: sess.ID,
		Name:      sess.Name,
		AgentID:   sess.AgentID,
		CreatedAt: sess.CreatedAt.UTC().Format(timeFormat),
		Messages:  msgs,
	}
	blob, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("sqlite: encode export %s: %w", sess.ID, err)
	}
	if err := os.WriteFile(path, blob, 0o644); err != nil {
		return fmt.Errorf("sqlite: write export %s: %w", path, err)
	}
	return nil
}

// ExportJSONL writes the session as newline-delimited JSON: line one is
// the session header, then one message object per line (spec §01.2 —
// the human-readable, portable backup format that ImportJSONL reads).
func (s *Store) ExportJSONL(sess *store.Session, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("sqlite: create jsonl %s: %w", path, err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	header := exportHeaderLine{
		SessionID: sess.ID,
		Name:      sess.Name,
		AgentID:   sess.AgentID,
		Profile:   sess.Profile,
		WorkDir:   sess.WorkDir,
		ParentID:  sess.ParentID,
		BranchID:  sess.BranchID,
		CreatedAt: sess.CreatedAt.UTC().Format(timeFormat),
	}
	if err := enc.Encode(header); err != nil {
		return fmt.Errorf("sqlite: encode header %s: %w", sess.ID, err)
	}

	msgs, err := exportMessages(sess)
	if err != nil {
		return err
	}
	for _, m := range msgs {
		if err := enc.Encode(m); err != nil {
			return fmt.Errorf("sqlite: encode message in %s: %w", sess.ID, err)
		}
	}
	return nil
}
