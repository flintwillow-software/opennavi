package navi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/connector"
	"github.com/flintwillow-software/opennavi/connector/github"
)

// This file proves the composition property ADR 0014 and ADR 0020 rest on: an
// egress tool reaches the model's surface only because the author added it.
//
// The import of a connector *implementation* is test-only on purpose. In
// production the composition root (cmd/navi) chooses components; the test
// needs a real one to show that the tool.Tool a connector hands over survives
// the bridge into the runtime registry.

// TestEgressToolIsAbsentUnlessTheAuthorAddsIt is one half of NAVI-371's
// acceptance criterion: a navi that does not add a connector's egress tool has
// no such capability, however present the connector package is in the module.
func TestEgressToolIsAbsentUnlessTheAuthorAddsIt(t *testing.T) {
	n := catalogNavi(t)
	if err := n.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if err := n.assemble(); err != nil {
		t.Fatalf("assemble: %v", err)
	}
	for _, name := range n.registry.ActiveNames() {
		if strings.HasPrefix(name, "github_") {
			t.Fatalf("tool %q is on the surface without being added", name)
		}
	}
}

// TestEgressToolIsPresentAndGatedWhenAdded is the other half: adding the tool
// puts it on the surface, and it is still gated by the connector's lifecycle —
// so the registry carries the tool AND the gate (ADR 0014/0020).
func TestEgressToolIsPresentAndGatedWhenAdded(t *testing.T) {
	const ref = "GITHUB_TOKEN"
	// Never started: the tool must be present but refuse the call.
	gh := github.New(github.Config{
		Credential: ref,
		Repository: "acme/widgets",
		Env:        func(string) string { return "" },
	})

	n := catalogNavi(t)
	if err := n.Connectors.Add(gh); err != nil {
		t.Fatalf("Connectors.Add: %v", err)
	}
	tools, err := gh.SendTools()
	if err != nil {
		t.Fatalf("SendTools: %v", err)
	}
	for _, tl := range tools {
		if err := n.Tools.Add(tl); err != nil {
			t.Fatalf("Tools.Add: %v", err)
		}
	}
	if err := n.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if err := n.assemble(); err != nil {
		t.Fatalf("assemble: %v", err)
	}

	var active bool
	for _, name := range n.registry.ActiveNames() {
		if name == github.ToolIssueComment {
			active = true
		}
	}
	if !active {
		t.Fatalf("the added egress tool is not on the surface: %v", n.registry.ActiveNames())
	}

	// The registry dispatches it — the tool.Tool bridged into the runtime
	// catalog — and the call fails closed because the connector never started.
	if _, err := n.registry.Dispatch(context.Background(), github.ToolIssueComment,
		json.RawMessage(`{"issue":1,"body":"hi"}`)); !errors.Is(err, connector.ErrNotStarted) {
		t.Fatalf("dispatch = %v, want ErrNotStarted", err)
	}
}
