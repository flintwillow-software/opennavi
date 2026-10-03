package datastore

import (
	"fmt"
	"sort"
	"strings"
)

// Aggregate names one persistence aggregate. ADR 0011 decision 12 routes
// every aggregate to the active datastore by default; multi-backend
// routing (transcripts on fs, memory on sqlite) is NAVI-366.
type Aggregate string

const (
	// AggregateThoughts is the thought/transcript aggregate: the thought
	// rows the queue persists and the conversation each thought produced.
	AggregateThoughts Aggregate = "thoughts"
	// AggregateToolState is stateful tool state (todos, notes).
	AggregateToolState Aggregate = "tool_state"
	// AggregateMemory is the navi's short- and long-term memory.
	AggregateMemory Aggregate = "memory"
)

// Aggregates lists every aggregate the contract defines, in declaration
// order.
func Aggregates() []Aggregate {
	return []Aggregate{AggregateThoughts, AggregateToolState, AggregateMemory}
}

// Params carries a backend's runtime parameters (path, dsn, ...) from
// config to Candidate.Open. Values are strings; a backend parses what it
// needs. No credentials belong in a Candidate (ADR 0008) — parameters
// arrive at runtime.
type Params map[string]string

// Candidate declares one compiled-in backend: the name runtime config
// selects it by, whether it is the compiled default, and how to open it.
// It is the model.Provider shape (ADR 0009): a stateless descriptor
// declared at build time.
type Candidate struct {
	// Name identifies the candidate in runtime config
	// (datastore.backend). It is the persistence layer's name ("inmem",
	// "fs", "sqlite"), not an aggregate's.
	Name string
	// Default marks the candidate selected when config names none.
	// Exactly one of several candidates must be marked default.
	Default bool
	// Open builds the backend for the selected candidate. Params come
	// from runtime config.
	Open func(Params) (Backend, error)
}

// Config is the runtime layer of the selection (ADR 0011 decision 3):
// which compiled candidate is active, its parameters, and the optional
// aggregate→backend routing map.
type Config struct {
	// Backend names the active candidate. Empty selects the compiled
	// default.
	Backend string
	// Params are handed to the active candidate's Open.
	Params Params
	// Routing maps an aggregate to the candidate serving it. Empty routes
	// every aggregate to the active backend. Multi-backend routing is
	// NAVI-366; it ships here as a validated seam.
	Routing map[Aggregate]string
}

// Backend is what a persistence implementation provides: one view per
// aggregate. Implementations are ordinary components in their own
// packages and satisfy this interface structurally.
type Backend interface {
	Thoughts() Thoughts
	ToolState() ToolState
	Memory() Memory
}

// Resolve selects the active candidate from the compiled set and the
// runtime config. Selection order: config names one, else the candidate
// marked default, else the only candidate. Anything ambiguous is an
// error, never a guess (ADR 0009's stance).
func Resolve(candidates []Candidate, cfg Config) (Candidate, error) {
	if len(candidates) == 0 {
		return Candidate{}, fmt.Errorf("datastore: no candidates declared")
	}

	active, err := selectCandidate(candidates, cfg)
	if err != nil {
		return Candidate{}, err
	}
	if err := validateRouting(cfg.Routing, active, candidates); err != nil {
		return Candidate{}, err
	}
	return active, nil
}

func selectCandidate(candidates []Candidate, cfg Config) (Candidate, error) {
	if name := strings.TrimSpace(cfg.Backend); name != "" {
		for _, c := range candidates {
			if strings.EqualFold(c.Name, name) {
				return c, nil
			}
		}
		return Candidate{}, fmt.Errorf("datastore: backend %q is not compiled in (candidates: %s)",
			name, strings.Join(candidateNames(candidates), ", "))
	}

	var defaults []Candidate
	for _, c := range candidates {
		if c.Default {
			defaults = append(defaults, c)
		}
	}
	switch {
	case len(defaults) == 1:
		return defaults[0], nil
	case len(defaults) > 1:
		return Candidate{}, fmt.Errorf("datastore: %d candidates marked default, want one; candidates: %s",
			len(defaults), strings.Join(candidateNames(candidates), ", "))
	case len(candidates) == 1:
		return candidates[0], nil
	default:
		return Candidate{}, fmt.Errorf(
			"datastore: %d candidates compiled but none marked default and config names none; candidates: %s",
			len(candidates), strings.Join(candidateNames(candidates), ", "))
	}
}

// validateRouting checks the routing seam: every aggregate names a
// compiled candidate, and nothing is routed away from the active backend
// yet (multi-backend routing is NAVI-366).
func validateRouting(routing map[Aggregate]string, active Candidate, candidates []Candidate) error {
	aggs := make([]string, 0, len(routing))
	for agg := range routing {
		aggs = append(aggs, string(agg))
	}
	sort.Strings(aggs)

	for _, a := range aggs {
		agg := Aggregate(a)
		name := routing[agg]
		if !compiled(candidates, name) {
			return fmt.Errorf("datastore: routing aggregate %q names backend %q, which is not compiled in",
				agg, name)
		}
		if !strings.EqualFold(name, active.Name) {
			return fmt.Errorf(
				"datastore: routing aggregate %q to backend %q: multi-backend routing is not implemented (NAVI-366)",
				agg, name)
		}
	}
	return nil
}

func compiled(candidates []Candidate, name string) bool {
	for _, c := range candidates {
		if strings.EqualFold(c.Name, name) {
			return true
		}
	}
	return false
}

func candidateNames(candidates []Candidate) []string {
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, c.Name)
	}
	return out
}
