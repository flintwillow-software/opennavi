// Command navi-release is the release-train's text surgery: it computes the
// next version from a CHANGELOG's Semver hints, drafts a tag message from the
// Unreleased entries, and cuts that section into a versioned one.
//
// It is a thin CLI over internal/release. scripts/release.sh orchestrates the
// release (git, $EDITOR, prompts) and calls this for the parts that touch the
// CHANGELOG, so the logic is testable in Go rather than living in a shell
// heredoc.
//
//	navi-release next   <changelog> <last-tag>            # print the next version
//	navi-release draft  <changelog> <last-tag> <version>  # print the tag-message draft
//	navi-release cut    <changelog> <version> <date>      # rewrite in place
//	navi-release draft-section <changelog> <version>      # draft from a cut section
//
// `next` prints "<version> <bump>" (e.g. "0.6.0 minor").
package main

import (
	"fmt"
	"os"

	"github.com/flintwillow-software/opennavi/internal/release"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "navi-release:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: navi-release next|draft|draft-section|cut ...")
	}

	switch args[0] {
	case "next":
		if len(args) != 3 {
			return fmt.Errorf("usage: navi-release next <changelog> <last-tag>")
		}
		changelog, err := read(args[1])
		if err != nil {
			return err
		}
		entries, err := release.Unreleased(changelog)
		if err != nil {
			return err
		}
		version, bump, err := release.NextVersion(args[2], entries)
		if err != nil {
			return err
		}
		fmt.Printf("%s %s\n", version, bump)
		return nil

	case "draft":
		if len(args) != 4 {
			return fmt.Errorf("usage: navi-release draft <changelog> <last-tag> <version>")
		}
		changelog, err := read(args[1])
		if err != nil {
			return err
		}
		entries, err := release.Unreleased(changelog)
		if err != nil {
			return err
		}
		// 8 titles per section and 30 refs keep the draft readable; the full
		// detail is in the changelog either way.
		fmt.Print(release.Draft(args[3], args[2], entries, 8, 30))
		return nil

	case "draft-section":
		// Draft from an already-cut versioned section. This is what the TAGGING
		// step uses: after the cut, the release's own section is the committed
		// record of what the tag names, so the message cannot drift from it.
		if len(args) != 3 {
			return fmt.Errorf("usage: navi-release draft-section <changelog> <version>")
		}
		changelog, err := read(args[1])
		if err != nil {
			return err
		}
		entries, err := release.Versioned(changelog, args[2])
		if err != nil {
			return err
		}
		fmt.Print(release.Draft(args[2], "the previous release", entries, 8, 30))
		return nil

	case "cut":
		if len(args) != 4 {
			return fmt.Errorf("usage: navi-release cut <changelog> <version> <date>")
		}
		changelog, err := read(args[1])
		if err != nil {
			return err
		}
		out, err := release.Cut(changelog, args[2], args[3])
		if err != nil {
			return err
		}
		return os.WriteFile(args[1], []byte(out), 0o644)

	default:
		return fmt.Errorf("unknown subcommand %q (want next, draft, or cut)", args[0])
	}
}

// read reads a file, reporting a missing one without the raw os error.
func read(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(b), nil
}
