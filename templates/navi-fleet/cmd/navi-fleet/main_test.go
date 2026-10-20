package main

import (
	"path/filepath"
	"testing"
)

// TestComposeValidates assembles the whole navi without serving it: every
// connector, tool, model, and portal is declared and the composition passes
// validation. It is the guard that the full-fat template stays buildable and
// coherent as the connectors change.
func TestComposeValidates(t *testing.T) {
	t.Setenv("NAVI_CONFIG", "")
	t.Setenv("NAVI_DB", filepath.Join(t.TempDir(), "navi-fleet.db"))
	t.Setenv("HULY_WORKSPACE", "flintwillowsoftware")
	t.Setenv("NAVI_HULY_PROJECT", "NAVI")

	n, store, err := compose()
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	defer store.Close()

	if err := n.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := n.Connectors.Len(); got != 3 {
		t.Fatalf("connectors = %d, want 3 (huly, forgejo, github)", got)
	}
	if n.Tools.Len() == 0 {
		t.Fatal("no tools declared")
	}
	if got := n.Portals.Len(); got != 1 {
		t.Fatalf("portals = %d, want 1", got)
	}
}

// TestGithubIngressOptIn pins the template's ingress behaviour: off unless a
// login and a repository are given, and a declared mention subscription acting
// as the navi otherwise.
func TestGithubIngressOptIn(t *testing.T) {
	if got := githubIngress("acme/widgets", "", "navi-fleet"); got != nil {
		t.Fatalf("no mention should mean no ingress, got %+v", got)
	}
	if got := githubIngress("", "alice", "navi-fleet"); got != nil {
		t.Fatalf("no repository should mean no ingress, got %+v", got)
	}
	got := githubIngress("acme/widgets", " alice ", "navi-fleet")
	if len(got) != 1 {
		t.Fatalf("want one subscription, got %d", len(got))
	}
	if s := got[0]; s.Mention != "alice" || s.Principal.String() != "navi:navi-fleet" {
		t.Fatalf("unexpected subscription: %+v", s)
	}
}

// TestParseGitRemote pins the remote parser for both spellings, for any host.
func TestParseGitRemote(t *testing.T) {
	cases := map[string]string{
		"git@github.com:acme/widgets.git":               "acme/widgets",
		"https://github.com/acme/widgets":               "acme/widgets",
		"https://git.flintwillow.software/acme/widgets": "acme/widgets",
		"git@git.flintwillow.software:acme/widgets.git": "acme/widgets",
		"https://example.test/":                         "",
		"":                                              "",
	}
	for in, want := range cases {
		if got := parseGitRemote(in); got != want {
			t.Errorf("parseGitRemote(%q) = %q, want %q", in, got, want)
		}
	}
}
