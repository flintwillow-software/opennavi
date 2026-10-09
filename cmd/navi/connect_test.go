package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// TestCommandForRoutesRoles pins the role table, which is the whole CLI
// surface: one host role, three client roles, and the aliases that keep
// existing command lines working.
func TestCommandForRoutesRoles(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
		rest []string
		code int
	}{
		{"host", []string{"host", "--model", "m"}, "host", []string{"--model", "m"}, 0},
		{"run is an alias", []string{"run", "--daemon"}, "host", []string{"--daemon"}, 0},
		{"daemon is host --daemon", []string{"daemon", "-db", "x"}, "host", []string{"-db", "x", "--daemon"}, 0},
		{"connect", []string{"connect", "127.0.0.1:8090"}, "connect", []string{"127.0.0.1:8090"}, 0},
		{"enqueue", []string{"enqueue", "--db", "x"}, "enqueue", []string{"--db", "x"}, 0},
		{"health", []string{"health"}, "health", nil, 0},
		{"help", []string{"--help"}, "help", nil, 0},
		{"bare", nil, "", nil, 0},
		{"flags with no role", []string{"--daemon"}, "", []string{"--daemon"}, 0},
		{"unknown", []string{"frobnicate"}, "", nil, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name, rest, code := commandFor(tc.args)
			if name != tc.want || code != tc.code {
				t.Fatalf("commandFor(%v) = (%q, %v, %d), want (%q, _, %d)", tc.args, name, rest, code, tc.want, tc.code)
			}
			if len(rest) != len(tc.rest) {
				t.Fatalf("commandFor(%v) rest = %v, want %v", tc.args, rest, tc.rest)
			}
			for i := range rest {
				if rest[i] != tc.rest[i] {
					t.Fatalf("commandFor(%v) rest = %v, want %v", tc.args, rest, tc.rest)
				}
			}
		})
	}
}

// TestDefaultRoleOrientsWithoutATerminal is the bare-`navi` behaviour: a test
// process has no terminal on stdout, so the default role must orient instead
// of starting a UI.
func TestDefaultRoleOrientsWithoutATerminal(t *testing.T) {
	out, code := captureStdout(t, func() int { return defaultRole(nil) })
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (nothing went wrong)", code)
	}
	for _, want := range []string{"navi host", "navi connect", "navi enqueue", "navi health"} {
		if !strings.Contains(out, want) {
			t.Fatalf("usage missing %q:\n%s", want, out)
		}
	}
}

// captureStdout runs fn with stdout redirected and returns what it printed.
func captureStdout(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	code := fn()
	_ = w.Close()
	os.Stdout = old
	blob, _ := io.ReadAll(r)
	return string(blob), code
}

// TestConnectTargetResolution pins the endpoint precedence: the positional
// argument wins, $NAVI_PORTAL (the endpoint `navi enqueue` also uses) is the
// fallback, and neither is an error naming the role.
func TestConnectTargetResolution(t *testing.T) {
	t.Setenv("NAVI_PORTAL", "http://env-host:8090")
	got, err := connectTarget("")
	if err != nil || got != "http://env-host:8090" {
		t.Fatalf("connectTarget(empty) = %q (%v), want the env endpoint", got, err)
	}
	got, err = connectTarget("127.0.0.1:9000")
	if err != nil || got != "http://127.0.0.1:9000" {
		t.Fatalf("connectTarget(arg) = %q (%v), want the argument to win", got, err)
	}

	t.Setenv("NAVI_PORTAL", "")
	if _, err := connectTarget("  "); err == nil {
		t.Fatal("a blank endpoint must be refused rather than dialed")
	} else if !strings.Contains(err.Error(), "connect") {
		t.Fatalf("error = %v, want it to name the role", err)
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
		got, err := normalizePortalURL(tc.in)
		if err != nil {
			t.Fatalf("normalizePortalURL(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("normalizePortalURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	if _, err := normalizePortalURL("   "); err == nil {
		t.Fatal("a blank portal URL must be refused")
	}
}
