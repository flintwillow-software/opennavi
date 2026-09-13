package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TransformContext converts agent state messages into the LLM-facing
// format via the context transformation pipeline (spec section 06):
//
//  1. Convert agent messages to LLM messages
//  2. Filter UI-only messages
//  3. Prepend the system prompt
//  4. Inject context files
//  5. Apply provider-specific transforms
func TransformContext(config AgentConfig, messages []AgentMessage) []Message {
	out := convertToLLM(messages)
	out = filterUIMessages(out)

	// NOTE: compaction summaries land as system-role messages
	// mid-conversation (internal/agent/compaction.go). Ollama accepts
	// system messages anywhere, but providers requiring system-at-top
	// must move them in applyProviderTransform — that hook exists for
	// exactly this kind of provider-specific reshaping.
	if config.SystemPrompt != "" {
		sys := Message{
			Role:    "system",
			Content: []Content{TextContent(config.SystemPrompt)},
		}
		out = append([]Message{sys}, out...)
	}

	out = injectContextFiles(config, out)
	out = applyProviderTransform(config.Provider, out)
	return out
}

// convertToLLM maps agent messages to LLM messages, carrying tool calls
// on assistant messages and tool results on tool messages.
func convertToLLM(messages []AgentMessage) []Message {
	out := make([]Message, 0, len(messages))
	for _, m := range messages {
		text := m.Content
		if m.Role == RoleTool && m.ToolResult != "" {
			text = m.ToolResult
		}
		lm := Message{
			Role:    m.Role.String(),
			Content: []Content{TextContent(text)},
		}
		if m.Role == RoleAssistant && len(m.ToolCalls) > 0 {
			lm.ToolCalls = append(lm.ToolCalls, m.ToolCalls...)
		}
		out = append(out, lm)
	}
	return out
}

// filterUIMessages drops messages with no text and no tool calls
// (UI-only placeholders such as empty notifications).
func filterUIMessages(messages []Message) []Message {
	out := make([]Message, 0, len(messages))
	for _, m := range messages {
		if FlattenContent(m.Content) == "" && len(m.ToolCalls) == 0 {
			continue
		}
		out = append(out, m)
	}
	return out
}

// injectContextFiles appends each configured context file as a user
// message containing its path and fenced content. Paths are resolved
// against config.WorkDir when relative. Unreadable files are noted
// rather than fatal.
func injectContextFiles(config AgentConfig, messages []Message) []Message {
	if len(config.ContextFiles) == 0 {
		return messages
	}
	for _, path := range config.ContextFiles {
		resolved := resolveWorkPath(config.WorkDir, path)
		data, err := os.ReadFile(resolved)
		body := string(data)
		if err != nil {
			body = fmt.Sprintf("(unable to read file: %v)", err)
		}
		messages = append(messages, Message{
			Role:    "user",
			Content: []Content{TextContent(fmt.Sprintf("File: %s\n```\n%s\n```", path, body))},
		})
	}
	return messages
}

// applyProviderTransform applies provider-specific message
// transformations as the final pipeline step.
func applyProviderTransform(provider string, messages []Message) []Message {
	switch strings.ToLower(provider) {
	case "ollama", "lmstudio":
		// These providers accept a system message as-is; no changes needed.
		return messages
	default:
		return messages
	}
}

// resolveWorkPath resolves p against workDir when p is relative.
func resolveWorkPath(workDir, p string) string {
	if p == "" {
		return p
	}
	if filepath.IsAbs(p) || workDir == "" {
		return p
	}
	return filepath.Join(workDir, p)
}

// jsonSchema marshals a schema map to raw JSON for tool descriptors.
func jsonSchema(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{"type":"object"}`)
	}
	return data
}

// FlattenContent concatenates the text of all content blocks.
func FlattenContent(blocks []Content) string {
	var sb strings.Builder
	for _, b := range blocks {
		if b.Text != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}
