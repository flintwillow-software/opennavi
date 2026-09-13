// External consumer proof for NAVI-276/277: another repo pulls in the
// public SDK surface — agent runtime, provider layer, session store,
// sdk types, and the tool registry — without touching internal/.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/flintwillow-software/opennavi/agent"
	"github.com/flintwillow-software/opennavi/llm"
	"github.com/flintwillow-software/opennavi/sdk"
	"github.com/flintwillow-software/opennavi/store"
	"github.com/flintwillow-software/opennavi/store/sqlite"
	"github.com/flintwillow-software/opennavi/tools/registry"
)

// scriptedProvider implements agent.ProviderInterface — external
// authors can implement custom providers against the public surface.
type scriptedProvider struct{}

func (scriptedProvider) Stream(ctx context.Context, messages []agent.Message, tools []agent.AgentTool, cfg agent.AgentConfig) (<-chan agent.Event, error) {
	ch := make(chan agent.Event, 2)
	ch <- agent.Event{Type: string(agent.EventContentDelta), Content: "hello from the SDK"}
	close(ch)
	return ch, nil
}

func (scriptedProvider) Close() error { return nil }

func main() {
	// 1. Provider layer (public llm/): fallback chain + retry policy.
	primary := llm.NewMockProvider()
	fallback := llm.NewMockProvider()
	if _, err := llm.NewProviderFallback(primary, fallback, nil); err != nil {
		panic(err)
	}
	fmt.Println("llm fallback chain ok; token count:", primary.TokenCount(nil))

	// 2. Agent runtime (public agent/) with the public runtime API.
	a := agent.NewAgent(agent.AgentConfig{
		Provider:     "custom",
		Model:        "test-model",
		SystemPrompt: "You are a consumer-side navi.",
		WorkDir:      ".",
	})
	a.Provider = scriptedProvider{}
	a.Prompt("say hi", nil)
	a.WaitForIdle()
	st := a.State()
	fmt.Println("agent state after prompt:", st.Status, "| messages:", len(st.Messages))

	// 3. Session persistence (public store/ + store/sqlite/).
	mem := store.NewMemoryStore()
	sess := store.NewSession("consumer-navi", ".")
	sess.AppendEntry(store.NewEntry(store.RoleUser, "persist me"))
	if err := mem.SaveSession(sess); err != nil {
		panic(err)
	}
	back, err := mem.LoadSession(sess.ID)
	if err != nil || len(back.Entries) != 1 {
		panic(fmt.Sprintf("memory store roundtrip: %v", err))
	}
	dir, _ := os.MkdirTemp("", "navi-consumer")
	sqlStore, err := sqlite.Open(dir+"/s.db", sqlite.Options{})
	if err != nil {
		panic(err)
	}
	defer sqlStore.Close()
	if err := sqlStore.SaveSession(sess); err != nil {
		panic(err)
	}
	loaded, err := sqlStore.LoadSession(sess.ID)
	if err != nil || loaded.ID != sess.ID {
		panic(fmt.Sprintf("sqlite roundtrip: %v", err))
	}
	fmt.Println("store roundtrip ok:", loaded.ID)

	// 4. Tool registry (public tools/registry) with an author tool.
	reg := registry.New()
	schema, _ := registry.ParseSchema(json.RawMessage(`{"type":"object"}`))
	err = reg.Register(registry.ToolDefinition{
		Name:          "consumer_ping",
		Description:   "Author tool from an external repo",
		InputSchema:   schema,
		OutputFormat:  "text",
		Access:        registry.AccessReadOnly,
		Timeout:       5 * time.Second,
		Recovery:      "retry_once",
		Mode:          registry.ToolModeSequential,
		SourceInfo:    registry.SourceInfo{Origin: registry.OriginAuthor, Path: "main.go"},
		PromptSnippet: "ping the consumer navi",
	}, func(ctx context.Context, args map[string]any) (string, error) {
		return "pong", nil
	})
	if err != nil {
		panic(err)
	}
	if err := reg.SetActive([]string{"consumer_ping"}); err != nil {
		panic(err)
	}
	fmt.Println("registry active tools:", reg.ActiveNames())

	// 5. sdk.Build entry point (public sdk/).
	err = sdk.Build(sdk.NaviDef{
		Name:        "consumer-navi",
		Version:     "0.1.0",
		Personality: "Built entirely against the public SDK surface.",
	})
	fmt.Println("sdk.Build:", err) // expected: ErrBuilderUnavailable (S000300 pending)
}
