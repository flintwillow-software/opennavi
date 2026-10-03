package thoughts

import (
	"encoding/json"
	"fmt"
	"testing"
)

// ── dependencies through the tool surface (NAVI-378) ────────────────────

// TestThoughtsCreateExpressesDependencies: the agent's deferral surface
// carries the relation ("this continues t1"), the created record reports
// it, and the derived blocked state clears when the dependency completes.
func TestThoughtsCreateExpressesDependencies(t *testing.T) {
	q := routeQueue(t)
	reg := toolRegistry(t, q)

	firstOut, err := dispatch(t, reg, "thoughts_create", `{"prompt":"stage one"}`)
	if err != nil {
		t.Fatal(err)
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(firstOut), &first); err != nil {
		t.Fatal(err)
	}
	firstID := first["id"].(string)

	secondOut, err := dispatch(t, reg, "thoughts_create", fmt.Sprintf(`{"prompt":"stage two","depends_on":[%q]}`, firstID))
	if err != nil {
		t.Fatal(err)
	}
	var second map[string]any
	if err := json.Unmarshal([]byte(secondOut), &second); err != nil {
		t.Fatal(err)
	}
	secondID := second["id"].(string)

	got, err := q.GetThought(secondID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.DependsOn) != 1 || got.DependsOn[0] != firstID {
		t.Fatalf("persisted depends_on = %v, want [%s]", got.DependsOn, firstID)
	}

	// The creation response carries both the edge and the derived state.
	if deps, ok := second["depends_on"].([]any); !ok || len(deps) != 1 || deps[0] != firstID {
		t.Fatalf("created record depends_on = %v, want [%s]", second["depends_on"], firstID)
	}
	if blocked, ok := second["blocked_by"].([]any); !ok || len(blocked) != 1 || blocked[0] != firstID {
		t.Fatalf("created record blocked_by = %v, want [%s]", second["blocked_by"], firstID)
	}

	// The queue will not hand the dependent out while the predecessor is
	// pending.
	claimed, ok := q.Dequeue()
	if !ok || claimed.ID != firstID {
		t.Fatalf("dequeued %v (ok=%v), want the predecessor %s", claimed, ok, firstID)
	}
	if extra, ok := q.Dequeue(); ok {
		t.Fatalf("dependent %s claimable before its dependency completed", extra.ID)
	}
	if err := q.Complete(firstID, "done"); err != nil {
		t.Fatal(err)
	}

	// thoughts_read reports the edge without a resolved blocked_by once
	// the dependency is complete.
	readOut, err := dispatch(t, reg, "thoughts_read", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range decodeList(t, readOut) {
		if row["id"] != secondID {
			continue
		}
		if deps, ok := row["depends_on"].([]any); !ok || len(deps) != 1 || deps[0] != firstID {
			t.Fatalf("read depends_on = %v, want [%s]", row["depends_on"], firstID)
		}
		if _, blocked := row["blocked_by"]; blocked {
			t.Fatalf("released thought still reported blocked: %v", row["blocked_by"])
		}
		return
	}
	t.Fatalf("thought %s missing from the read snapshot", secondID)
}

// TestThoughtsCreateRejectsInvalidDependencies: bad input and unknown ids
// are refused at the door, not at claim time.
func TestThoughtsCreateRejectsInvalidDependencies(t *testing.T) {
	q := routeQueue(t)
	reg := toolRegistry(t, q)

	if _, err := dispatch(t, reg, "thoughts_create", `{"prompt":"x","depends_on":"nope"}`); err == nil {
		t.Fatal("depends_on must be an array of ids")
	}
	if _, err := dispatch(t, reg, "thoughts_create", `{"prompt":"x","depends_on":["ghost"]}`); err == nil {
		t.Fatal("an unknown dependency must be rejected at the door")
	}
}

// TestThoughtsCreateAcceptsMultipleDependencies: a stage that joins two
// earlier stages waits on both.
func TestThoughtsCreateAcceptsMultipleDependencies(t *testing.T) {
	q := routeQueue(t)
	reg := toolRegistry(t, q)

	ids := make([]string, 0, 2)
	for _, prompt := range []string{"left", "right"} {
		out, err := dispatch(t, reg, "thoughts_create", fmt.Sprintf(`{"prompt":%q}`, prompt))
		if err != nil {
			t.Fatal(err)
		}
		var created map[string]any
		if err := json.Unmarshal([]byte(out), &created); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, created["id"].(string))
	}

	out, err := dispatch(t, reg, "thoughts_create",
		fmt.Sprintf(`{"prompt":"join","depends_on":[%q,%q]}`, ids[0], ids[1]))
	if err != nil {
		t.Fatal(err)
	}
	var join map[string]any
	if err := json.Unmarshal([]byte(out), &join); err != nil {
		t.Fatal(err)
	}
	got, err := q.GetThought(join["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.DependsOn) != 2 || got.DependsOn[0] != ids[0] || got.DependsOn[1] != ids[1] {
		t.Fatalf("join depends_on = %v, want %v", got.DependsOn, ids)
	}
}
