package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// isInteractive reports whether the test is running attached to a
// terminal. The TUI regression tests exercise the non-TTY paths
// deterministically (CI/piped stdin); on an interactive session the
// TUI would own the terminal, so skip. /dev/null is a char device but
// NOT interactive (the go-test harness provides it as stdin).
func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false // pipe or file: not interactive
	}
	// Char device — distinguish a real TTY from /dev/null.
	if null, nerr := os.Open(os.DevNull); nerr == nil {
		defer null.Close()
		if ni, serr := null.Stat(); serr == nil && os.SameFile(fi, ni) {
			return false // /dev/null
		}
	}
	return true
}

// runNavi executes the freshly built run entry with piped-stdin
// semantics (the operator's repro shape) and returns exit code +
// combined output. Panics surface as exit code 2 with "panic:" in the
// output.
func runNavi(t *testing.T, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command("go", append([]string{"run", "."}, args...)...)
	cmd.Stdin = os.Stdin // /dev/null under the harness (piped EOF)
	out, err := cmd.CombinedOutput()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("go run: %v", err)
	}
	return code, string(out)
}

// TestTUIPipedStdinNoPanic: the operator's repro — `navi host` with
// piped stdin (< /dev/null). Pre-NAVI-332 this panicked with
// close-of-closed-channel (readSession's EOF close racing the TUI-quit
// close). Post-fix there is exactly one closer (the TUI), so the run
// exits cleanly through the TTY-unavailable error path.
func TestTUIPipedStdinNoPanic(t *testing.T) {
	if isInteractive() {
		t.Skip("interactive session — TUI owns the terminal")
	}
	done := make(chan struct{})
	var code int
	var out string
	go func() {
		code, out = runNavi(t, "run", "--model", "mock")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("run did not return within 30s (TUI hung on piped stdin)")
	}
	if strings.Contains(out, "panic:") {
		t.Fatalf("piped-stdin TUI run panicked:\n%s", out)
	}
	if code == 2 {
		t.Fatalf("exit code 2 (panic signature): %s", out)
	}
}

// TestDaemonPipedStdinDrainsAndExits: --daemon keeps the stdin loop
// (NAVI-332 scope) — piped EOF closes sessionDone exactly once (the
// loop is the only closer) and the run drains and exits cleanly.
func TestDaemonPipedStdinDrainsAndExits(t *testing.T) {
	if isInteractive() {
		t.Skip("interactive session — TUI owns the terminal")
	}
	done := make(chan struct{})
	var code int
	var out string
	go func() {
		code, out = runNavi(t, "run", "--daemon", "--model", "mock")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("daemon-mode run did not return within 30s (stdin loop wedged)")
	}
	if strings.Contains(out, "panic:") {
		t.Fatalf("daemon-mode piped-stdin run panicked:\n%s", out)
	}
	if code != 0 {
		t.Fatalf("daemon-mode exit = %d, want 0 (clean drain): %s", code, out)
	}
}

// TestTUIBranchHasNoStdinReader: source-level tripwire for the
// NAVI-332 contention bug — the TUI path must not spawn readSession
// (Bubble Tea owns stdin). The reference navi guards the stdin loop
// behind `if p.daemonMode`, and host.go must not call it at all.
func TestTUIBranchHasNoStdinReader(t *testing.T) {
	src, err := os.ReadFile("host.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "readSession(") {
		t.Fatal("host.go spawns readSession — the reference navi must guard it behind --daemon")
	}

	ref, err := os.ReadFile("reference.go")
	if err != nil {
		t.Fatal(err)
	}
	rs := string(ref)
	// Exactly two references: the definition and one daemon-guarded call.
	if n := strings.Count(rs, "readSession("); n != 2 {
		t.Fatalf("readSession references = %d, want definition + one daemon-mode call", n)
	}
	guard := strings.Index(rs, "if p.daemonMode {")
	call := strings.Index(rs, "readSession(")
	def := strings.Index(rs, "func readSession(")
	if guard < 0 || call < 0 || def < 0 {
		t.Fatal("reference navi session-driver structure not found")
	}
	if call != def && call < guard {
		t.Fatal("readSession is called outside the --daemon guard (stdin contention regression)")
	}
}
