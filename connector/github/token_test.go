package github

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGH writes an executable standing in for the GitHub CLI, so the source is
// tested against a real subprocess rather than a stubbed exec.
func fakeGH(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestGHCLITokenReadsTheCLICredential is the point of the source: a developer
// who is already logged in needs no exported variable.
func TestGHCLITokenReadsTheCLICredential(t *testing.T) {
	bin := fakeGH(t, "#!/bin/sh\necho tok-from-gh-cli\n")
	tok, err := (&GHCLIToken{Path: bin}).Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "tok-from-gh-cli" {
		t.Fatalf("token = %q, want the CLI's output", tok)
	}
}

// TestGHCLITokenAsksOnceWithinTheTTL pins the caching the source owns: gh is a
// subprocess, so asking per request would spawn one for every egress call.
func TestGHCLITokenAsksOnceWithinTheTTL(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "count")
	bin := fakeGH(t, fmt.Sprintf("#!/bin/sh\necho x >> %s\necho tok\n", counter))

	src := &GHCLIToken{Path: bin}
	for i := 0; i < 3; i++ {
		if _, err := src.Token(context.Background()); err != nil {
			t.Fatalf("Token: %v", err)
		}
	}
	if got := countLines(t, counter); got != 1 {
		t.Fatalf("gh was run %d times, want 1 within the TTL", got)
	}

	// A negative TTL disables caching, so the next call asks again — the escape
	// hatch for a token that was revoked or replaced mid-run.
	src.TTL = -1
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got := countLines(t, counter); got != 2 {
		t.Fatalf("gh was run %d times, want 2 once caching is disabled", got)
	}
}

// TestGHCLITokenHostArgument pins that an enterprise host is asked about
// explicitly, and that the default host is not passed a redundant flag.
func TestGHCLITokenHostArgument(t *testing.T) {
	args := filepath.Join(t.TempDir(), "args")
	bin := fakeGH(t, fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %s\necho tok\n", args))

	if _, err := (&GHCLIToken{Path: bin}).Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got := readFile(t, args); got != "auth token" {
		t.Fatalf("args = %q, want no hostname flag for github.com", got)
	}

	if _, err := (&GHCLIToken{Path: bin, Host: "ghe.example.com"}).Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got := readFile(t, args); !strings.Contains(got, "--hostname ghe.example.com") {
		t.Fatalf("args = %q, want the hostname flag for a non-default host", got)
	}
}

// TestGHCLITokenReportsFailuresWithoutLeaking checks the failure paths an
// operator will actually hit, and that none of them can quote stdout (where the
// token goes).
func TestGHCLITokenReportsFailuresWithoutLeaking(t *testing.T) {
	t.Run("not logged in", func(t *testing.T) {
		bin := fakeGH(t, "#!/bin/sh\necho 'To get started with GitHub CLI, please run: gh auth login' >&2\nexit 1\n")
		_, err := (&GHCLIToken{Path: bin}).Token(context.Background())
		if err == nil || !strings.Contains(err.Error(), "gh auth login") {
			t.Fatalf("Token = %v, want gh's own guidance", err)
		}
	})

	t.Run("empty output", func(t *testing.T) {
		bin := fakeGH(t, "#!/bin/sh\nexit 0\n")
		_, err := (&GHCLIToken{Path: bin}).Token(context.Background())
		if err == nil || !strings.Contains(err.Error(), "printed no token") {
			t.Fatalf("Token = %v, want an empty-output error", err)
		}
	})

	t.Run("binary missing from PATH", func(t *testing.T) {
		// A bare name is resolved through PATH, so this is the "gh is not
		// installed" case.
		_, err := (&GHCLIToken{Path: "navi-no-such-binary-xyz"}).Token(context.Background())
		if err == nil || !strings.Contains(err.Error(), "not found on PATH") {
			t.Fatalf("Token = %v, want a not-on-PATH error", err)
		}
	})

	t.Run("configured path does not exist", func(t *testing.T) {
		// An absolute path bypasses PATH lookup, so it fails differently and
		// the message has to say so — the fix is GHCLIToken.Path, not an install.
		_, err := (&GHCLIToken{Path: filepath.Join(t.TempDir(), "absent")}).Token(context.Background())
		if err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("Token = %v, want a does-not-exist error", err)
		}
	})

	t.Run("a token on stdout is not quoted in a failure", func(t *testing.T) {
		// The command fails *after* writing something token-shaped to stdout.
		bin := fakeGH(t, "#!/bin/sh\necho tok-must-not-appear\nexit 1\n")
		_, err := (&GHCLIToken{Path: bin}).Token(context.Background())
		if err == nil {
			t.Fatal("want a failure")
		}
		if strings.Contains(err.Error(), "tok-must-not-appear") {
			t.Fatalf("stdout leaked into the error: %v", err)
		}
	})
}

// TestChainTokenPrefersTheEarlierSource pins the order a navi expresses with
// it: an explicit reference wins, and the developer's gh login is the fallback.
func TestChainTokenPrefersTheEarlierSource(t *testing.T) {
	bin := fakeGH(t, "#!/bin/sh\necho tok-from-gh\n")
	explicit := StaticToken{Ref: "GITHUB_TOKEN", Env: envWith("GITHUB_TOKEN", "tok-explicit")}

	got, err := ChainToken(explicit, &GHCLIToken{Path: bin}).Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got != "tok-explicit" {
		t.Fatalf("token = %q, want the explicit reference to win", got)
	}

	// With nothing exported, it falls through to the CLI.
	got, err = ChainToken(StaticToken{Ref: "GITHUB_TOKEN", Env: envWith("OTHER", "x")}, &GHCLIToken{Path: bin}).Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got != "tok-from-gh" {
		t.Fatalf("token = %q, want the gh fallback", got)
	}
}

// TestChainTokenNamesEveryAttempt pins the error an operator sees when nothing
// is configured: every place a credential could have come from, not just the
// last one tried.
func TestChainTokenNamesEveryAttempt(t *testing.T) {
	bin := fakeGH(t, "#!/bin/sh\necho 'not logged in' >&2\nexit 1\n")
	_, err := ChainToken(
		StaticToken{Ref: "GITHUB_TOKEN", Env: envWith("OTHER", "x")},
		&GHCLIToken{Path: bin},
	).Token(context.Background())
	if err == nil {
		t.Fatal("want a failure when no source can supply a token")
	}
	for _, want := range []string{"GITHUB_TOKEN", "not logged in"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}

	if _, err := ChainToken().Token(context.Background()); err == nil {
		t.Fatal("an empty chain must fail rather than return an empty token")
	}
}

// TestSourcesSatisfyTokenSource is the compile-time contract: every source the
// package offers, and the composite, can be used wherever a source is.
func TestSourcesSatisfyTokenSource(t *testing.T) {
	var _ TokenSource = ChainToken()
	var _ TokenSource = &GHCLIToken{}
	var _ TokenSource = StaticToken{}
	var _ TokenSource = &AppInstallation{}
	// The optional extension that makes a rejected minted token recoverable.
	var _ TokenInvalidator = &AppInstallation{}
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return len(strings.Fields(string(data)))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.TrimSpace(string(data))
}
