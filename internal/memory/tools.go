package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/datastore"
	"github.com/flintwillow-software/opennavi/tool"
)

// Tools returns the built-in memory tools. memory_write is the deliberate
// write surface for the always-loaded tier; reads need no tool because the
// notes are injected into every thought.
func Tools(s *Service) ([]tool.Tool, error) {
	schema, err := tool.ParseSchema(json.RawMessage(`{
		"type": "object",
		"properties": {
			"content": {
				"type": "string",
				"description": "The note to remember, written as a short self-contained statement."
			},
			"scope": {
				"type": "string",
				"enum": ["user", "session"],
				"description": "user = notes about the human (preferences, style, lessons); session = working state for the current task. Defaults to session."
			},
			"id": {
				"type": "string",
				"description": "Replace an existing entry by id, or name the entry to remove."
			},
			"remove": {
				"type": "boolean",
				"description": "Delete the entry with this id instead of writing."
			}
		},
		"additionalProperties": false
	}`))
	if err != nil {
		return nil, err
	}

	return []tool.Tool{{
		Def: tool.Def{
			Name:          "memory_write",
			Description:   "Add, replace, or remove one short-term memory note. Short-term memory is injected into every thought's context; write deliberately, only what a later thought needs. Use user scope for notes about the human, session scope for working state. Pass an id to replace an existing entry, or remove=true with an id to delete it.",
			InputSchema:   schema,
			OutputFormat:  "text",
			Access:        tool.AccessWrite,
			Timeout:       5 * time.Second,
			Recovery:      "none",
			Mode:          tool.ToolModeSequential,
			Tags:          []string{"memory", "context", "session"},
			Version:       "0.1.0",
			PromptSnippet: "memory_write — add, replace, or remove a short-term memory note (user or session scope)",
			PromptGuidelines: []string{
				"Short-term memory is already in your context; write only what is worth carrying to a later thought, and consolidate rather than accumulating near-duplicates.",
			},
			SourceInfo: tool.SourceInfo{Origin: tool.OriginBuiltin},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return handleWrite(ctx, s, args)
		},
	}}, nil
}

func handleWrite(ctx context.Context, s *Service, args map[string]any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	id, _ := args["id"].(string)
	if remove, _ := args["remove"].(bool); remove {
		if id == "" {
			return "", fmt.Errorf("memory_write: remove requires an id")
		}
		if err := s.Remove(id); err != nil {
			return "", err
		}
		return fmt.Sprintf("removed %s", id), nil
	}

	content, _ := args["content"].(string)
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("memory_write: content is required")
	}

	scope := datastore.ScopeSession
	if raw, ok := args["scope"].(string); ok && raw != "" {
		scope = datastore.Scope(raw)
	}

	rec, err := s.Write(datastore.MemoryRecord{ID: id, Scope: scope, Content: content})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("remembered %s (%s)", rec.ID, rec.Scope), nil
}
