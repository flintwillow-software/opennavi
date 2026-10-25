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
	t.Setenv("NAVI_PORTAL_ADDR", "")
	t.Setenv("NAVI_DAEMON", "")

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
	// Under `go test` stdout is not a terminal and no portal address is
	// configured, so the presentation is correctly empty: a navi in a pipe
	// gets no alt-screen program. TestPresentation pins the choices.
	if got := n.Portals.Len(); got != 0 {
		t.Fatalf("portals = %d, want 0 (no TTY, no gateway)", got)
	}
}

// TestPresentation pins the presentation rules that make the navi runnable as a
// service (NAVI-407): the gateway is declared when an address is configured
// (that is how a headless navi is driven), the TUI only when a terminal is
// watching, and NAVI_DAEMON forces headless regardless.
func TestPresentation(t *testing.T) {
	base := func(t *testing.T) {
		t.Setenv("NAVI_CONFIG", "")
		t.Setenv("NAVI_DB", filepath.Join(t.TempDir(), "navi-fleet.db"))
		t.Setenv("HULY_WORKSPACE", "flintwillowsoftware")
		t.Setenv("NAVI_PORTAL_ADDR", "")
		t.Setenv("NAVI_DAEMON", "")
	}
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
		base(t)
		t.Setenv("NAVI_PORTAL_ADDR", "127.0.0.1:0")
		// control + gateway, and no TUI because stdout is not a terminal here.
		if got := composePortals(t); got != 2 {
			t.Fatalf("portals = %d, want 2 (control + gateway)", got)
		}
	})

	t.Run("headless is empty without an address", func(t *testing.T) {
		base(t)
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

// TestTrustOptions pins the trust-source wiring that makes a headless gateway
// usable (NAVI-407): a store reference and/or an OIDC issuer become declared
// trust sources, an absent pair declares none (fail closed), and a malformed
// issuer is a composition error rather than a silent no-trust navi.
func TestTrustOptions(t *testing.T) {
	t.Run("none declared", func(t *testing.T) {
		t.Setenv("NAVI_TRUST_STORE", "")
		t.Setenv("NAVI_OIDC_ISSUER", "")
		opts, err := trustOptions()
		if err != nil {
			t.Fatalf("trustOptions: %v", err)
		}
		if len(opts) != 0 {
			t.Fatalf("got %d options, want 0 (no trust source declared)", len(opts))
		}
	})

	t.Run("a trust store reference", func(t *testing.T) {
		t.Setenv("NAVI_TRUST_STORE", filepath.Join(t.TempDir(), "trust.yaml"))
		t.Setenv("NAVI_OIDC_ISSUER", "")
		opts, err := trustOptions()
		if err != nil {
			t.Fatalf("trustOptions: %v", err)
		}
		if len(opts) != 1 {
			t.Fatalf("got %d options, want 1", len(opts))
		}
	})

	t.Run("a malformed OIDC issuer is an error", func(t *testing.T) {
		t.Setenv("NAVI_TRUST_STORE", "")
		t.Setenv("NAVI_OIDC_ISSUER", "not a url")
		if _, err := trustOptions(); err == nil {
			t.Fatal("malformed issuer: got nil error, want a composition error")
		}
	})
}
