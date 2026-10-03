package datastore

import (
	"strings"
	"testing"
)

func cand(name string, def bool) Candidate {
	return Candidate{
		Name:    name,
		Default: def,
		Open:    func(Params) (Backend, error) { return nil, nil },
	}
}

func TestResolveNoCandidates(t *testing.T) {
	if _, err := Resolve(nil, Config{}); err == nil {
		t.Fatal("resolving an empty candidate set must fail")
	}
}

func TestResolveConfigNamesUncompiledBackend(t *testing.T) {
	_, err := Resolve([]Candidate{cand("inmem", true)}, Config{Backend: "postgres"})
	if err == nil {
		t.Fatal("config naming an uncompiled backend must fail")
	}
	if !strings.Contains(err.Error(), "postgres") || !strings.Contains(err.Error(), "inmem") {
		t.Fatalf("error should name the requested backend and the compiled set: %v", err)
	}
}

func TestResolveSingleCandidateWinsWithoutDefault(t *testing.T) {
	got, err := Resolve([]Candidate{cand("inmem", false)}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "inmem" {
		t.Fatalf("got %q, want inmem", got.Name)
	}
}

func TestResolveDefaultCandidateWins(t *testing.T) {
	got, err := Resolve([]Candidate{cand("a", false), cand("b", true)}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "b" {
		t.Fatalf("got %q, want the default b", got.Name)
	}
}

func TestResolveAmbiguousSetFails(t *testing.T) {
	if _, err := Resolve([]Candidate{cand("a", false), cand("b", false)}, Config{}); err == nil {
		t.Fatal("several candidates with no default and no config must fail")
	}
	if _, err := Resolve([]Candidate{cand("a", true), cand("b", true)}, Config{}); err == nil {
		t.Fatal("several defaults must fail")
	}
}

func TestResolveConfigBeatsDefault(t *testing.T) {
	got, err := Resolve([]Candidate{cand("a", true), cand("b", false)}, Config{Backend: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "b" {
		t.Fatalf("got %q, want the config-selected b", got.Name)
	}
}

func TestResolveRoutingIdentityIsAllowed(t *testing.T) {
	routing := map[Aggregate]string{AggregateMemory: "inmem"}
	if _, err := Resolve([]Candidate{cand("inmem", true)}, Config{Routing: routing}); err != nil {
		t.Fatalf("routing an aggregate to the active backend must be allowed: %v", err)
	}
}

func TestResolveRoutingAwayFromActiveIsNotImplemented(t *testing.T) {
	routing := map[Aggregate]string{AggregateMemory: "other"}
	_, err := Resolve([]Candidate{cand("inmem", true), cand("other", false)}, Config{Routing: routing})
	if err == nil {
		t.Fatal("routing an aggregate to a different backend must fail until NAVI-366")
	}
	if !strings.Contains(err.Error(), "NAVI-366") {
		t.Fatalf("error should point at the deferred work: %v", err)
	}
}

func TestResolveRoutingUnknownBackendFails(t *testing.T) {
	routing := map[Aggregate]string{AggregateThoughts: "nope"}
	if _, err := Resolve([]Candidate{cand("inmem", true)}, Config{Routing: routing}); err == nil {
		t.Fatal("routing to an uncompiled backend must fail")
	}
}

func TestAggregatesCoverTheContract(t *testing.T) {
	got := Aggregates()
	want := []Aggregate{AggregateThoughts, AggregateToolState, AggregateMemory}
	if len(got) != len(want) {
		t.Fatalf("Aggregates() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Aggregates()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
