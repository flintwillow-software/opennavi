// Command navi is the reference navi CLI: it dispatches subcommands
// (run, enqueue, health; daemon as a deprecated alias for `run
// --daemon`). With no subcommand it runs the navi — the TUI by default,
// headless when `--daemon` is passed.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "daemon":
			// Deprecated alias (NAVI-325): `navi daemon` == `navi run
			// --daemon` — one assembly, headless presentation. Kept this
			// cycle; removed next.
			fmt.Fprintln(os.Stderr, "navi: 'daemon' is deprecated — use 'run --daemon'")
			if err := runInteractive(append(os.Args[2:], "--daemon")); err != nil {
				fmt.Fprintln(os.Stderr, "navi:", err)
				os.Exit(1)
			}
			return
		case "health":
			if err := runHealth(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "navi:", err)
				os.Exit(1)
			}
			return

		case "enqueue":
			if err := runEnqueue(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "navi:", err)
				os.Exit(1)
			}
			return

		case "run":
			if err := runInteractive(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "navi:", err)
				os.Exit(1)
			}
			return
		}
	}

	// Default: run the navi (TUI, or headless with --daemon).
	if err := runInteractive(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "navi:", err)
		os.Exit(1)
	}
}
