package main

import (
	"path/filepath"
	"testing"

	"github.com/flintwillow-software/opennavi/navi"
	"github.com/flintwillow-software/opennavi/principal"
)

// baseEnv clears the deployment knobs so each test starts from the compiled
// defaults, and points the database at a temp file.
func baseEnv(t *testing.T) {
	t.Helper()
	t.Setenv("NAVI_CONFIG", "")
	t.Setenv("NAVI_DB", filepath.Join(t.TempDir(), "navi-standard.db"))
	t.Setenv("NAVI_NAME", "")
	t.Setenv("NAVI_PORTAL_ADDR", "")
	t.Setenv("NAVI_DAEMON", "")
	t.Setenv("NAVI_WEBHOOK_ADDR", "")
	t.Setenv("HULY_WORKSPACE", "")
	t.Setenv("HULY_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OLLAMA_HOST", "")
}

// TestComposeValidates assembles the whole navi without serving it: every
// option, tool, connector, model, mode, role, and group is declared and the
// composition — INCLUDING the compiled policy (ADR 0033) — passes validation.
// This is the guard that the template stays buildable and coherent as the SDK
// changes.
func TestComposeValidates(t *testing.T) {
	baseEnv(t)

	n, store, err := compose()
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	defer store.Close()

	// Validate compiles the policy as its final step (ADR 0033 decision 2),
	// so a role naming an undeclared tool would fail HERE rather than at the
	// first request.
	if err := n.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	// Every section is present.
	if got := n.Models.Len(); got != 2 {
		t.Fatalf("models = %d, want 2 (ollama, openrouter)", got)
	}
	if got := n.Connectors.Len(); got != 2 {
		t.Fatalf("connectors = %d, want 2 (huly, github)", got)
	}
	if got := n.Modes.Len(); got != 3 {
		t.Fatalf("modes = %d, want 3 (plan, execute, research)", got)
	}
	if got := n.Roles.Len(); got != 2 {
		t.Fatalf("roles = %d, want 2 (operator, reader)", got)
	}
	if got := n.Groups.Len(); got != 1 {
		t.Fatalf("groups = %d, want 1 (navi-peers)", got)
	}
	if n.Tools.Len() == 0 {
		t.Fatal("no tools declared")
	}
	if n.Policy() == nil {
		t.Fatal("Validate did not compile a policy")
	}
}

// TestOperatorCanCallEveryDeclaredTool pins the acceptance criterion of
// NAVI-565 (ADR 0033 decision 2): the operator role grants every tool the
// navi declares, asserted against the COMPILED policy rather than the declared
// grant list. A grant the ceiling rejects (a `call:*` wildcard) fails here.
func TestOperatorCanCallEveryDeclaredTool(t *testing.T) {
	baseEnv(t)

	n, store, err := compose()
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	defer store.Close()
	if err := n.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	who, err := navi.NewPrincipal(principal.MustParse("user:operator"))
	if err != nil {
		t.Fatalf("principal: %v", err)
	}
	for _, tl := range n.Tools.All() {
		d := n.Policy().Decide(navi.AccessRequest{
			Principal:  who,
			Capability: navi.Call(tl.Def.Name),
		})
		if !d.Allowed {
			t.Errorf("operator cannot call %s: %s", tl.Def.Name, d.Reason)
		}
	}
}

// TestExternalWritesDefaultToDenied pins the operator's stated direction: a
// reader may read external systems but not write them. The reader role must be
// able to call the read tools and refused the ones that leave the machine.
func TestExternalWritesDefaultToDenied(t *testing.T) {
	baseEnv(t)

	n, store, err := compose()
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	defer store.Close()
	if err := n.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	// Group membership comes from the trust source (ADR 0021 decision 4), not
	// from the group declaration, so the test supplies a principal the source
	// has placed in group:readers — which is what makes the reader role apply.
	who, err := navi.NewResolvedPrincipal(
		principal.MustParse("user:reader-example"),
		nil,
		[]principal.Ref{principal.MustParse("group:readers")},
	)
	if err != nil {
		t.Fatalf("principal: %v", err)
	}
	allowed := []string{"huly_issue_list", "huly_issue_search", "huly_issue_description", "file_read"}
	for _, name := range allowed {
		if d := n.Policy().Decide(navi.AccessRequest{Principal: who, Capability: navi.Call(name)}); !d.Allowed {
			t.Errorf("reader refused read tool %s: %s", name, d.Reason)
		}
	}
	denied := []string{"huly_issue_comment", "huly_issue_update", "huly_issue_create", "github_issue_comment", "shell_exec", "stm_write"}
	for _, name := range denied {
		if d := n.Policy().Decide(navi.AccessRequest{Principal: who, Capability: navi.Call(name)}); d.Allowed {
			t.Errorf("reader allowed external/write tool %s, want refused", name)
		}
	}
}

// TestPresentation pins the presentation rules. The local playground runs the
// TUI in-process when a terminal is watching; a headless run with a gateway
// address declares control + gateway; a pipe gets no alt-screen program.
func TestPresentation(t *testing.T) {
	composePortals := func(t *testing.T) int {
		t.Helper()
		n, store, err := compose()
		if err != nil {
			t.Fatalf("compose: %v", err)
		}
		defer store.Close()
		return n.Portals.Len()
	}

	t.Run("gateway when an address is configured", func(t *testing.T) {
		baseEnv(t)
		t.Setenv("NAVI_PORTAL_ADDR", "127.0.0.1:0")
		// control + gateway, and no TUI because stdout is not a terminal here.
		if got := composePortals(t); got != 2 {
			t.Fatalf("portals = %d, want 2 (control + gateway)", got)
		}
	})

	t.Run("headless is empty without an address", func(t *testing.T) {
		baseEnv(t)
		t.Setenv("NAVI_DAEMON", "1")
		if got := composePortals(t); got != 0 {
			t.Fatalf("portals = %d, want 0 (headless, no gateway)", got)
		}
	})
}

// TestHeadless pins the NAVI_DAEMON spellings.
func TestHeadless(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "on", " on "} {
		t.Setenv("NAVI_DAEMON", v)
		if !headless() {
			t.Errorf("headless(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "off", "maybe"} {
		t.Setenv("NAVI_DAEMON", v)
		if headless() {
			t.Errorf("headless(%q) = true, want false", v)
		}
	}
}

// TestEmbedderIsLocal pins that the embedder points at Ollama and needs no
// external service — the playground's whole premise.
func TestEmbedderIsLocal(t *testing.T) {
	baseEnv(t)
	n, store, err := compose()
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	defer store.Close()
	if err := n.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// TestEnvHelpers pins the small env parsers, since every knob in the template
// goes through them.
func TestEnvHelpers(t *testing.T) {
	t.Setenv("NAVI_TEST_STR", "  hello  ")
	if got := envOr("NAVI_TEST_STR", "def"); got != "hello" {
		t.Errorf("envOr = %q, want hello", got)
	}
	t.Setenv("NAVI_TEST_STR", "   ")
	if got := envOr("NAVI_TEST_STR", "def"); got != "def" {
		t.Errorf("envOr blank = %q, want def", got)
	}
	t.Setenv("NAVI_TEST_INT", "7")
	if got := envInt("NAVI_TEST_INT", 1); got != 7 {
		t.Errorf("envInt = %d, want 7", got)
	}
	t.Setenv("NAVI_TEST_INT", "not-a-number")
	if got := envInt("NAVI_TEST_INT", 1); got != 1 {
		t.Errorf("envInt bad = %d, want default 1", got)
	}
	t.Setenv("NAVI_TEST_DUR", "90s")
	if got := envDuration("NAVI_TEST_DUR", 0); got.String() != "1m30s" {
		t.Errorf("envDuration = %s, want 1m30s", got)
	}
}
