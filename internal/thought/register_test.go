package thought

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/tools/registry"
)

// queueDef builds a complete ToolDefinition for seam tests, following
// the tools/registry test convention.
func queueDef(name string) registry.ToolDefinition {
	schema, _ := registry.ParseSchema(json.RawMessage(`{"type":"object"}`))
	return registry.ToolDefinition{
		Name:          name,
		Description:   "Test tool " + name + " for the " + name + " domain.",
		InputSchema:   schema,
		OutputFormat:  "json",
		Access:        registry.AccessReadOnly,
		Timeout:       10 * time.Second,
		Recovery:      "none",
		Mode:          registry.ToolModeParallel,
		PromptSnippet: "Test tool (" + name + ")",
		SourceInfo:    registry.SourceInfo{Origin: registry.OriginSDK, Path: "internal/thought/register_test.go"},
	}
}

// handleSeen records the queue handle delivered to a handler.
type handleSeen struct {
	queue *ThoughtQueue
}

func TestRegisterQueueToolsBindsHandle(t *testing.T) {
	q, err := NewQueue(Config{MaxConcurrent: 1, PrioritySlots: 0, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	seen := &handleSeen{}

	// The tools register into a REAL registry with the normal surface.
	reg := registry.New()
	err = RegisterQueueTools(reg, QueueHandle{Queue: q}, []QueueTool{
		{Def: queueDef("thoughts_read"), Handler: func(ctx context.Context, args map[string]any, h QueueHandle) (string, error) {
			seen.queue = h.Queue
			return "[]", nil
		}},
	})
	if err != nil {
		t.Fatalf("RegisterQueueTools: %v", err)
	}
	if err := reg.SetActive([]string{"thoughts_read"}); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	def, ok := reg.Get("thoughts_read")
	if !ok {
		t.Fatal("thoughts_read not registered")
	}
	if def.Name != "thoughts_read" || def.Access != registry.AccessReadOnly {
		t.Errorf("definition round-trip mismatch: %+v", def)
	}

	// Dispatch delivers the bound queue handle to the handler.
	out, err := reg.Dispatch(context.Background(), "thoughts_read", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if out != "[]" {
		t.Errorf("output = %q", out)
	}
	if seen.queue != q {
		t.Error("handler did not receive the bound queue handle")
	}
}

func TestRegisterQueueToolsValidation(t *testing.T) {
	q, _ := NewQueue(Config{MaxConcurrent: 1, PrioritySlots: 0, SlotExhaustion: PolicyQueue}, NewMemoryStore())
	ok := func(ctx context.Context, args map[string]any, h QueueHandle) (string, error) { return "", nil }

	if err := RegisterQueueTools(nil, QueueHandle{Queue: q}, []QueueTool{{Def: queueDef("thoughts_read"), Handler: ok}}); err == nil {
		t.Error("RegisterQueueTools accepted nil registry")
	}
	if err := RegisterQueueTools(registry.New(), QueueHandle{}, []QueueTool{{Def: queueDef("thoughts_read"), Handler: ok}}); err == nil {
		t.Error("RegisterQueueTools accepted a nil queue handle")
	}
	if err := RegisterQueueTools(registry.New(), QueueHandle{Queue: q}, []QueueTool{{Def: queueDef("thoughts_read"), Handler: nil}}); err == nil {
		t.Error("RegisterQueueTools accepted a nil handler")
	}

	// Governance flows through the registry: an incomplete metadata
	// descriptor must be rejected (like stateful registration).
	bad := queueDef("thoughts_read")
	bad.Description = ""
	if err := RegisterQueueTools(registry.New(), QueueHandle{Queue: q}, []QueueTool{{Def: bad, Handler: ok}}); err == nil {
		t.Error("RegisterQueueTools accepted an incomplete ToolDefinition")
	}

	// Duplicate registration is rejected and keeps the earlier tool.
	reg := registry.New()
	if err := RegisterQueueTools(reg, QueueHandle{Queue: q}, []QueueTool{{Def: queueDef("thoughts_read"), Handler: ok}}); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	if err := RegisterQueueTools(reg, QueueHandle{Queue: q}, []QueueTool{{Def: queueDef("thoughts_read"), Handler: ok}}); err == nil {
		t.Error("duplicate registration accepted")
	}
}
