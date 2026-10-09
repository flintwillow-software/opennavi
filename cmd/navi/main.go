// Command navi is the reference navi CLI. It has one host role and three
// client roles, because the difference is real: a client needs no model, no
// datastore, and no queue — it reads a running navi's portal (ADR 0017).
//
//	navi host     this process IS the navi (TUI, or headless with --daemon)
//	navi connect  a portal attached to a running navi
//	navi enqueue  put a thought on a navi's queue
//	navi health   probe a navi
//
// With no role and a terminal, navi hosts: the TUI is its front door. Without
// a terminal it prints this list instead, because an alt-screen UI in a pipe
// is never what the caller wanted.
package main

import (
	"fmt"
	"os"
	"strings"
)

// usage orients rather than enumerating flags: the two roles are the thing to
// learn, and each role documents its own flags.
const usage = `navi — an agent you run as a program

  navi host [flags]      host a navi: queue, workers, and its portals
  navi connect [url]     attach a portal to a running navi (client mode)
  navi enqueue [flags]   put a thought on a navi's queue
  navi health [flags]    probe a navi

  navi host --help       server flags (model, datastore, portal address)
  navi --help            this list

With no role and a terminal, navi hosts — the TUI is its front door.
`

func main() {
	os.Exit(dispatch(os.Args[1:]))
}

// dispatch runs the role a command line names. The routing itself lives in
// commandFor so the table is testable without running anything.
func dispatch(args []string) int {
	name, rest, code := commandFor(args)
	if len(args) > 0 && args[0] == "daemon" {
		fmt.Fprintln(os.Stderr, "navi: 'daemon' is deprecated — use 'host --daemon'")
	}
	switch {
	case code != 0:
		fmt.Fprintf(os.Stderr, "navi: unknown command %q\n\n%s", args[0], usage)
		return code
	case name == "help":
		fmt.Print(usage)
		return 0
	case name == "host":
		return fail(runHost(rest))
	case name == "connect":
		return fail(runConnect(rest))
	case name == "enqueue":
		return fail(runEnqueue(rest))
	case name == "health":
		return fail(runHealth(rest))
	default:
		return defaultRole(rest)
	}
}

// commandFor resolves a command line into a role. An empty name means the
// default role; a non-zero code means "print usage and exit with it".
func commandFor(args []string) (name string, rest []string, code int) {
	if len(args) == 0 {
		return "", nil, 0
	}
	switch args[0] {
	case "host", "run": // `run` is the previous name, kept as an alias
		return "host", args[1:], 0
	case "connect":
		return "connect", args[1:], 0
	case "enqueue":
		return "enqueue", args[1:], 0
	case "health":
		return "health", args[1:], 0
	case "daemon":
		// Deprecated alias: the host role with a headless presentation.
		return "host", append(args[1:], "--daemon"), 0
	case "help", "-h", "--help":
		return "help", nil, 0
	}
	if strings.HasPrefix(args[0], "-") {
		// Flags with no role: the default role owns them, so
		// `navi --daemon` keeps working.
		return "", args, 0
	}
	return "", nil, 2
}

// defaultRole hosts when a human is present, and orients when not: a
// terminal-less `navi` is a script or a service that got the command wrong,
// and starting a UI would be worse than saying so.
func defaultRole(args []string) int {
	if !stdoutIsTTY() {
		fmt.Print(usage)
		return 0
	}
	return fail(runHost(args))
}

// fail reports an error the way the CLI always has, and yields the exit code.
func fail(err error) int {
	if err == nil {
		return 0
	}
	fmt.Fprintln(os.Stderr, "navi:", err)
	return 1
}
