package session

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/flintwillow-software/opennavi/internal/agent"
)

// This file bridges the agent runtime and the session store: pausing an
// agent captures its conversation into session entries; resuming
// reconstructs an AgentState from entries for a fresh agent run.

// SyncFromAgent captures the agent's current conversation state into the
// session as entries, replacing any previously captured entries (the
// session is the persistence unit for one conversation; the live agent
// state is authoritative while it runs). The session is persisted and
// its status set to paused.
func (m *SessionManager) SyncFromAgent(s *Session, a *agent.Agent) error {
	state := a.State().Clone()
	entries := EntriesFromAgentState(state)
	s.Entries = append(s.Entries[:0], entries...)
	s.Current = len(s.Entries) - 1
	s.Status = StatusPaused
	s.UpdatedAt = time.Now().UTC()
	if err := m.store.SaveSession(s); err != nil {
		return fmt.Errorf("sync agent state into session %s: %w", s.ID, err)
	}
	return nil
}

// RestoreToAgent reconstructs an AgentState from the session's active
// path and loads it into an idle agent. The restored agent is idle with
// the session's full conversation as its message history; continue it
// with Prompt/Continue as usual.
func (m *SessionManager) RestoreToAgent(s *Session, a *agent.Agent) error {
	state := AgentStateFromEntries(s.GetPathToRoot())
	state.Config = a.Config // keep the target agent's configuration
	if err := a.LoadState(state); err != nil {
		return fmt.Errorf("restore session %s into agent: %w", s.ID, err)
	}
	if s.Status == StatusPaused {
		if _, err := m.ResumeSession(s.ID); err != nil {
			return err
		}
	}
	return nil
}

// EntriesFromAgentState converts an agent's message history into session
// entries. An assistant message carrying tool calls maps to one content
// entry plus one metadata entry per call (mirroring the relational shape
// of the messages table); consecutive assistant tool-call entries are
// merged back into a single message by AgentStateFromEntries.
func EntriesFromAgentState(st *agent.AgentState) []SessionEntry {
	entries := make([]SessionEntry, 0, len(st.Messages))
	for _, msg := range st.Messages {
		switch msg.Role {
		case agent.RoleUser:
			entries = append(entries, NewEntry(RoleUser, msg.Content))
		case agent.RoleAssistant:
			e := NewEntry(RoleAssistant, msg.Content)
			if len(msg.ToolCalls) > 0 {
				e.ToolCallID = msg.ToolCalls[0].ToolCallID
				e.ToolName = msg.ToolCalls[0].Name
				e.ToolInput = append(json.RawMessage(nil), msg.ToolCalls[0].Arguments...)
				entries = append(entries, e)
				for _, tc := range msg.ToolCalls[1:] {
					extra := NewEntry(RoleAssistant, "")
					extra.ToolCallID = tc.ToolCallID
					extra.ToolName = tc.Name
					extra.ToolInput = append(json.RawMessage(nil), tc.Arguments...)
					entries = append(entries, extra)
				}
			} else {
				entries = append(entries, e)
			}
		case agent.RoleTool:
			e := NewEntry(RoleTool, msg.ToolResult)
			e.ToolCallID = msg.ToolCallID
			entries = append(entries, e)
		default: // system or unknown roles
			e := NewEntry(RoleSystem, msg.Content)
			entries = append(entries, e)
		}
	}
	return entries
}

// AgentStateFromEntries reconstructs an AgentState from entries,
// merging consecutive assistant tool-call metadata entries back into
// their message (the inverse of EntriesFromAgentState).
func AgentStateFromEntries(entries []SessionEntry) *agent.AgentState {
	st := &agent.AgentState{Status: agent.StatusIdle}
	if len(entries) == 0 {
		return st
	}

	msgs := make([]agent.AgentMessage, 0, len(entries))
	for _, e := range entries {
		role := roleFromSession(e.Role)
		// Merge consecutive assistant tool-call entries into the
		// previous assistant message.
		if role == agent.RoleAssistant && e.Message == "" && e.ToolCallID != "" && len(msgs) > 0 && msgs[len(msgs)-1].Role == agent.RoleAssistant {
			msgs[len(msgs)-1].ToolCalls = append(msgs[len(msgs)-1].ToolCalls, agent.ToolCall{
				Name:       e.ToolName,
				Arguments:  append(json.RawMessage(nil), e.ToolInput...),
				ToolCallID: e.ToolCallID,
			})
			continue
		}

		msg := agent.AgentMessage{Role: role, Content: e.Message}
		switch role {
		case agent.RoleAssistant:
			if e.ToolCallID != "" || e.ToolName != "" {
				msg.ToolCalls = append(msg.ToolCalls, agent.ToolCall{
					Name:       e.ToolName,
					Arguments:  append(json.RawMessage(nil), e.ToolInput...),
					ToolCallID: e.ToolCallID,
				})
			}
		case agent.RoleTool:
			msg.ToolResult = e.Message
			msg.ToolCallID = e.ToolCallID
		}
		msgs = append(msgs, msg)
	}
	st.Messages = msgs
	return st
}

func roleFromSession(r Role) agent.Role {
	switch r {
	case RoleUser:
		return agent.RoleUser
	case RoleAssistant:
		return agent.RoleAssistant
	case RoleTool:
		return agent.RoleTool
	default:
		return agent.RoleSystem
	}
}
