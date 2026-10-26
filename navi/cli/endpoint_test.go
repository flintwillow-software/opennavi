package cli

import (
	"strings"
	"testing"
)

// These tests moved here with the endpoint helpers: resolving where a navi is
// and normalizing what an operator types are the client roles' shared
// plumbing, so they belong with it rather than in the reference CLI.

// TestResolveEndpoint pins the endpoint precedence: the positional argument
// wins, $NAVI_PORTAL (the endpoint every client role reads) is the fallback,
// and neither is an error.
//
// The error names the variable rather than the calling role, because the
// helper is shared by connect/enqueue/health and cannot know which one asked.
func TestResolveEndpoint(t *testing.T) {
	t.Setenv("NAVI_PORTAL", "http://env-host:8090")
	got, err := ResolveEndpoint("")
	if err != nil || got != "http://env-host:8090" {
		t.Fatalf("ResolveEndpoint(empty) = %q (%v), want the env endpoint", got, err)
	}
	got, err = ResolveEndpoint("127.0.0.1:9000")
	if err != nil || got != "http://127.0.0.1:9000" {
		t.Fatalf("ResolveEndpoint(arg) = %q (%v), want the argument to win", got, err)
	}

	t.Setenv("NAVI_PORTAL", "")
	if _, err := ResolveEndpoint("  "); err == nil {
		t.Fatal("a blank endpoint must be refused rather than dialed")
	} else if !strings.Contains(err.Error(), PortalEnv) {
		t.Fatalf("error = %v, want it to name %s", err, PortalEnv)
	}
}

// TestNormalizePortalURL accepts what an operator types and refuses to dial
// nothing.
func TestNormalizePortalURL(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"127.0.0.1:8090", "http://127.0.0.1:8090"},
		{"http://navi.internal:8090", "http://navi.internal:8090"},
		{"https://navi.internal", "https://navi.internal"},
		{"http://navi.internal:8090/", "http://navi.internal:8090"},
		{"  navi.internal:8090  ", "http://navi.internal:8090"},
	} {
		got, err := NormalizeURL(tc.in)
		if err != nil {
			t.Fatalf("NormalizeURL(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("NormalizeURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	if _, err := NormalizeURL("   "); err == nil {
		t.Fatal("a blank portal URL must be refused")
	}
}
