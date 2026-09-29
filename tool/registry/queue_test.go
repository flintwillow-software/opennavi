package registry

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/thought"
)

// queueDef builds a complete ToolDefinition for seam tests, following
// the tool/registry test convention.
func queueDef(name string) ToolDefinition {
	schema, _ := ParseSchema(json.RawMessage(`{"type":"object"}`))
	return ToolDefinition{
		Name:          name,
		Description:   "Test tool " + name + " for the " + name + " domain.",
		InputSchema:   schema,
		OutputFormat:  "json",
		Access:        AccessReadOnly,
		Timeout:       10 * time.Second,
		Recovery:      "none",
		Mode:          ToolModeParallel,
		PromptSnippet: "Test tool (" + name + ")",
		SourceInfo:    SourceInfo{Origin: OriginSDK, Path: "tool/registry/queue_test.go"},
	}
}

// handleSeen records the queue handle delivered to a handler.
type handleSeen struct {
	queue *thought.Queue
}

func queueUnderTest(t *testing.T) *thought.Queue {
	t.Helper()
	q, err := thought.NewQueue(thought.Config{MaxConcurrent: 1, PrioritySlots: 0, SlotExhaustion: thought.PolicyQueue}, thought.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestRegisterQueueToolsBindsHandle(t *testing.T) {
	q := queueUnderTest(t)
	seen := &handleSeen{}

	// The tools register into a REAL registry with the normal surface.
	reg := New()
	err := RegisterQueueTools(reg, QueueHandle{Queue: q}, []QueueTool{
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
	if def.Name != "thoughts_read" || def.Access != AccessReadOnly {
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
	q := queueUnderTest(t)
	ok := func(ctx context.Context, args map[string]any, h QueueHandle) (string, error) { return "", nil }

	if err := RegisterQueueTools(nil, QueueHandle{Queue: q}, []QueueTool{{Def: queueDef("thoughts_read"), Handler: ok}}); err == nil {
		t.Error("RegisterQueueTools accepted nil registry")
	}
	if err := RegisterQueueTools(New(), QueueHandle{}, []QueueTool{{Def: queueDef("thoughts_read"), Handler: ok}}); err == nil {
		t.Error("RegisterQueueTools accepted a nil queue handle")
	}
	if err := RegisterQueueTools(New(), QueueHandle{Queue: q}, []QueueTool{{Def: queueDef("thoughts_read"), Handler: nil}}); err == nil {
		t.Error("RegisterQueueTools accepted a nil handler")
	}

	// Governance flows through the registry: an incomplete metadata
	// descriptor must be rejected (like stateful registration).
	bad := queueDef("thoughts_read")
	bad.Description = ""
	if err := RegisterQueueTools(New(), QueueHandle{Queue: q}, []QueueTool{{Def: bad, Handler: ok}}); err == nil {
		t.Error("RegisterQueueTools accepted an incomplete ToolDefinition")
	}

	// Duplicate registration is rejected and keeps the earlier tool.
	reg := New()
	if err := RegisterQueueTools(reg, QueueHandle{Queue: q}, []QueueTool{{Def: queueDef("thoughts_read"), Handler: ok}}); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	if err := RegisterQueueTools(reg, QueueHandle{Queue: q}, []QueueTool{{Def: queueDef("thoughts_read"), Handler: ok}}); err == nil {
		t.Error("duplicate registration accepted")
	}
}
