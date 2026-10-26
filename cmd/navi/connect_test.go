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
