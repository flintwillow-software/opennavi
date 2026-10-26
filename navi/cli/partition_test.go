package cli

import (
	"flag"
	"reflect"
	"testing"
)

// These tests pin the shared argument partition (NAVI-566): a role's
// flags and its positional argument may appear in ANY order, and the set of
// value-taking flags is DERIVED from the FlagSet rather than listed by hand.
//
// The bug this exists to prevent: Go's flag.FlagSet.Parse stops at the first
// non-flag argument, so `connect <url> --key k.pem` parsed the URL and silently
// dropped the credential, leaving the client unauthenticated with no error.
// `enqueue` had solved this with a hand-written partition; `connect` had not.

// connectFlagSet mirrors connect's declared flags exactly.
func connectFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.String("token", "", "")
	fs.String("principal", "", "")
	fs.String("key", "", "")
	fs.String("key-id", "", "")
	return fs
}

// enqueueFlagSet mirrors enqueue's declared flags exactly, including the Int.
func enqueueFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("enqueue", flag.ContinueOnError)
	fs.Int("priority", 4, "")
	fs.String("db", "", "")
	fs.String("provider", "", "")
	fs.String("model", "", "")
	fs.String("mode", "", "")
	fs.String("portal", "", "")
	return fs
}

// TestPartitionArgsConnectCredentialAfterURL is the regression: the credential
// written after the address must still reach the flag parser. Before the fix
// this produced flags=[] and positional=["<url>", "--key", "k.pem"], so the
// client connected with no credential and no error.
func TestPartitionArgsConnectCredentialAfterURL(t *testing.T) {
	fs := connectFlagSet()
	flags, positional := PartitionArgs([]string{"10.0.0.51:8090", "--key", "k.pem", "--principal", "service:cli", "--key-id", "k1"}, fs)

	wantPos := []string{"10.0.0.51:8090"}
	if !reflect.DeepEqual(positional, wantPos) {
		t.Fatalf("positional = %v, want %v", positional, wantPos)
	}
	if err := fs.Parse(flags); err != nil {
		t.Fatalf("Parse(%v): %v", flags, err)
	}
	// The credential must be READABLE, not merely present in the slice — this
	// is the assertion the old behaviour failed.
	if got := fs.Lookup("key").Value.String(); got != "k.pem" {
		t.Fatalf("--key = %q, want k.pem (the credential was dropped)", got)
	}
	if got := fs.Lookup("principal").Value.String(); got != "service:cli" {
		t.Fatalf("--principal = %q, want service:cli", got)
	}
	if got := fs.Lookup("key-id").Value.String(); got != "k1" {
		t.Fatalf("--key-id = %q, want k1", got)
	}
}

// TestPartitionArgsBothOrders proves the two orders are equivalent, for both
// roles that have a positional argument. These are the connect counterparts of
// TestRunEnqueuePromptFirst / TestRunEnqueueFlagsFirst, which pinned the same
// property for enqueue before this helper existed.
func TestPartitionArgsBothOrders(t *testing.T) {
	t.Run("connect flags first", func(t *testing.T) {
		fs := connectFlagSet()
		flags, positional := PartitionArgs([]string{"--token", "tok", "10.0.0.51:8090"}, fs)
		if err := fs.Parse(flags); err != nil {
			t.Fatal(err)
		}
		if got := fs.Lookup("token").Value.String(); got != "tok" {
			t.Fatalf("--token = %q, want tok", got)
		}
		if !reflect.DeepEqual(positional, []string{"10.0.0.51:8090"}) {
			t.Fatalf("positional = %v", positional)
		}
	})

	t.Run("connect url first", func(t *testing.T) {
		fs := connectFlagSet()
		flags, positional := PartitionArgs([]string{"10.0.0.51:8090", "--token", "tok"}, fs)
		if err := fs.Parse(flags); err != nil {
			t.Fatal(err)
		}
		if got := fs.Lookup("token").Value.String(); got != "tok" {
			t.Fatalf("--token = %q, want tok", got)
		}
		if !reflect.DeepEqual(positional, []string{"10.0.0.51:8090"}) {
			t.Fatalf("positional = %v", positional)
		}
	})

	t.Run("enqueue prompt first", func(t *testing.T) {
		fs := enqueueFlagSet()
		// Separate words, as a shell would deliver them: PartitionArgs returns
		// them individually and Enqueue joins them into the prompt.
		flags, positional := PartitionArgs([]string{"hello", "second", "--db", "/tmp/x.db"}, fs)
		if err := fs.Parse(flags); err != nil {
			t.Fatal(err)
		}
		if got := fs.Lookup("db").Value.String(); got != "/tmp/x.db" {
			t.Fatalf("--db = %q, want /tmp/x.db", got)
		}
		// PartitionArgs returns the words; Enqueue joins them into the prompt.
		if !reflect.DeepEqual(positional, []string{"hello", "second"}) {
			t.Fatalf("positional = %v, want the two prompt words", positional)
		}
	})

	t.Run("enqueue flags first", func(t *testing.T) {
		fs := enqueueFlagSet()
		flags, positional := PartitionArgs([]string{"-priority", "7", "-db", "/tmp/x.db", "the", "prompt"}, fs)
		if err := fs.Parse(flags); err != nil {
			t.Fatal(err)
		}
		if got := fs.Lookup("priority").Value.String(); got != "7" {
			t.Fatalf("--priority = %q, want 7 (the Int flag must consume its value)", got)
		}
		if !reflect.DeepEqual(positional, []string{"the", "prompt"}) {
			t.Fatalf("positional = %v, want the two prompt words", positional)
		}
	})
}

// TestPartitionArgsDerivesValueFlags is the property the design turns on: the
// value-taking set comes from the FlagSet, so a flag a role declares is handled
// without being listed anywhere. A hand-maintained list would be the original
// defect one level up.
func TestPartitionArgsDerivesValueFlags(t *testing.T) {
	// A flag set declared inline — nothing about it is known to the helper.
	fs := flag.NewFlagSet("late", flag.ContinueOnError)
	fs.String("brand-new-flag", "", "")
	flags, positional := PartitionArgs([]string{"positional-here", "--brand-new-flag", "v"}, fs)
	if err := fs.Parse(flags); err != nil {
		t.Fatal(err)
	}
	if got := fs.Lookup("brand-new-flag").Value.String(); got != "v" {
		t.Fatalf("--brand-new-flag = %q, want v — the helper must derive arity from the FlagSet, not a list", got)
	}
	if !reflect.DeepEqual(positional, []string{"positional-here"}) {
		t.Fatalf("positional = %v", positional)
	}
}

// TestPartitionArgsBooleanFlagDoesNotEatPositional is the case the derived
// design handles and a hand-written list could not: a boolean flag takes no
// value, so it must NOT consume the following token. The flag package's own
// contract (flag.Value's IsBoolFlag) is what decides this.
//
// No role in this package declares a boolean flag today; this test exists so
// the first one to add it finds the behaviour already correct rather than
// discovering that the helper ate its argument.
func TestPartitionArgsBooleanFlagDoesNotEatPositional(t *testing.T) {
	fs := flag.NewFlagSet("withbool", flag.ContinueOnError)
	fs.Bool("verbose", false, "")
	fs.String("db", "", "")

	flags, positional := PartitionArgs([]string{"--verbose", "--db", "/tmp/x.db", "prompt"}, fs)
	if err := fs.Parse(flags); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(positional, []string{"prompt"}) {
		t.Fatalf("positional = %v, want [prompt] — a boolean flag must not consume the next token", positional)
	}
	if got := fs.Lookup("db").Value.String(); got != "/tmp/x.db" {
		t.Fatalf("--db = %q, want /tmp/x.db", got)
	}
}

// TestPartitionArgsInlineValue covers -flag=value, which carries its own value
// and must not consume the following token.
func TestPartitionArgsInlineValue(t *testing.T) {
	fs := connectFlagSet()
	flags, positional := PartitionArgs([]string{"--token=tok", "10.0.0.51:8090"}, fs)
	if err := fs.Parse(flags); err != nil {
		t.Fatal(err)
	}
	if got := fs.Lookup("token").Value.String(); got != "tok" {
		t.Fatalf("--token = %q, want tok", got)
	}
	if !reflect.DeepEqual(positional, []string{"10.0.0.51:8090"}) {
		t.Fatalf("positional = %v, want the URL (an inline value must not eat it)", positional)
	}
}

// TestPartitionArgsLoneDashIsPositional pins the stdin convention: a bare "-"
// is a positional argument, not a flag.
func TestPartitionArgsLoneDashIsPositional(t *testing.T) {
	fs := connectFlagSet()
	flags, positional := PartitionArgs([]string{"-"}, fs)
	if len(flags) != 0 {
		t.Fatalf("flags = %v, want none (a lone dash is positional)", flags)
	}
	if !reflect.DeepEqual(positional, []string{"-"}) {
		t.Fatalf("positional = %v, want [-]", positional)
	}
}

// TestPartitionArgsMissingValueIsNotInvented pins the end-of-args case: a
// trailing value-taking flag with nothing after it must not panic or fabricate
// a value. The flag package reports the missing value, which is the right error
// to surface.
func TestPartitionArgsMissingValueIsNotInvented(t *testing.T) {
	fs := connectFlagSet()
	flags, positional := PartitionArgs([]string{"10.0.0.51:8090", "--key"}, fs)
	if len(positional) != 1 {
		t.Fatalf("positional = %v, want just the URL", positional)
	}
	if err := fs.Parse(flags); err == nil {
		t.Fatal("a trailing --key with no value must be an error, not a silently empty credential")
	}
}

// TestConnectFlagSetHasNoBooleans documents an invariant the derived design
// relies on today and would handle correctly if it changed: every flag the
// client roles declare takes a value. It is asserted rather than assumed so the
// first boolean flag added is a deliberate change to this test, not a silent
// behaviour shift.
func TestConnectFlagSetHasNoBooleans(t *testing.T) {
	for name, fs := range map[string]*flag.FlagSet{
		"connect": connectFlagSet(),
		"enqueue": enqueueFlagSet(),
	} {
		fs.VisitAll(func(f *flag.Flag) {
			if !flagTakesValue(f) {
				t.Errorf("%s: flag %q takes no value; the helper handles this, but the roles' comments assume all flags take one", name, f.Name)
			}
		})
	}
}
