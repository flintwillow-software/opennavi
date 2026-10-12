package main

import (
	"testing"

	"github.com/flintwillow-software/opennavi/connector/github"
)

// TestGithubIngressOptIn pins the template's ingress behaviour: off unless a
// login is given, never without a repository, and a declared mention
// subscription otherwise.
func TestGithubIngressOptIn(t *testing.T) {
	if got := githubIngress("acme/widgets", ""); got != nil {
		t.Fatalf("no mention should mean no ingress, got %+v", got)
	}
	if got := githubIngress("", "alice"); got != nil {
		t.Fatalf("no repository should mean no ingress, got %+v", got)
	}
	if got := githubIngress("acme/widgets", "   "); got != nil {
		t.Fatalf("blank mention should mean no ingress, got %+v", got)
	}

	got := githubIngress("acme/widgets", " alice ")
	if len(got) != 1 {
		t.Fatalf("want one subscription, got %d", len(got))
	}
	s := got[0]
	if s.Name != "mentions" || s.Repository != "acme/widgets" || s.Mention != "alice" {
		t.Fatalf("unexpected subscription: %+v", s)
	}
	if s.Principal.String() != "navi:navi-code" {
		t.Fatalf("principal = %q, want navi:navi-code", s.Principal)
	}
	if len(s.Events) != 1 || s.Events[0] != github.EventIssueMention {
		t.Fatalf("unexpected events: %+v", s.Events)
	}
}
