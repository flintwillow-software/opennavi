package agent

// ── Ollama Provider Stub ─────────────────────────────────────────────────

// OllamaProvider implements ProviderInterface using a local Ollama server.
type OllamaProvider struct {
	BaseURL string // e.g. "http://localhost:11434"
}

// Stream will send a chat completion request to Ollama and return events.
// TODO: implement actual HTTP streaming to Ollama at /api/chat
func (p *OllamaProvider) Stream(messages []Message, tools []AgentTool, config AgentConfig) (chan Event, error) {
	out := make(chan Event, 32)
	_ = messages
	_ = tools
	_ = config
	close(out)
	return out, nil
}

// Close cleans up provider resources.
func (p *OllamaProvider) Close() error {
	return nil
}
