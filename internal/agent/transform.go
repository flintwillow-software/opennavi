package agent

// TransformContext converts agent messages to LLM-facing format.
// TODO: inject system prompt, context files, provider-specific transforms.
func TransformContext(config AgentConfig, messages []AgentMessage) []Message {
	out := make([]Message, 0, len(messages)+1)

	// Prepend system prompt if configured
	if config.SystemPrompt != "" {
		out = append(out, Message{
			Role:    "system",
			Content: []Content{{Type: "text", Text: config.SystemPrompt}},
		})
	}

	// Convert each message
	for _, m := range messages {
		role := "user"
		if m.Role == 1 {
			role = "assistant"
		} else if m.Role == 2 {
			role = "tool"
		}
		out = append(out, Message{
			Role:    role,
			Content: []Content{{Type: "text", Text: m.Content}},
		})
	}
	return out
}
