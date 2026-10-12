// Package principal is the identity-reference contract (ADR 0021).
//
// A principal is an identity with authenticators attached. The model itself —
// the identity, the keys it holds, the trust sources that resolve it, and the
// compiled policy that decides what it may do — is core navi, not a component
// contract (ADR 0021, "Consequences"). What crosses a contract boundary is
// only a *reference*: a datastore keys a memory entry by the principal that
// owns it, and a connector declares the service principal its inbound work
// acts as. This package carries that reference — a validated (kind, name)
// pair — and nothing else, so the contract layer gains a typed, validated
// identity value without gaining the security engine.
//
// Kinds are open (ADR 0021 decision 1). The constants below name the kinds the
// record names; a kind not listed is still valid, because the set must not
// need a contract change to grow. A group is a principal kind (a named
// collection of principals, optionally authenticatable itself); a role is
// *not* a principal and has no kind here (ADR 0021 decision 2).
//
// # Migration
//
// Before this type existed, the runtime keyed short-term memory by the navi's
// bare name (navi/runtime.go wrote n.name). ParseLegacy reads such an
// unqualified stored value as navi:<name> — the only subject the runtime ever
// wrote — so pre-change records stay visible; new writes always store the
// qualified form. Persisting backends (datastore/fs, datastore/sqlite) apply
// ParseLegacy on read and, for SQL, match the bare legacy form in the subject
// filter.
package principal

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Errors reported by this package. Callers test with errors.Is.
var (
	// ErrEmptyRef is the zero reference: no principal at all. A zero Ref is
	// the absence of a principal, and an action with no principal is
	// refused, never defaulted open (ADR 0021, ADR 0022 decision 9).
	ErrEmptyRef = errors.New("principal: empty principal reference")
	// ErrInvalidRef is a reference that is not "kind:name" — an empty kind,
	// an empty name, or a name containing the separator.
	ErrInvalidRef = errors.New("principal: invalid principal reference")
)

// Kind is the open kind of a principal. Kinds are strings, not a closed enum:
// ADR 0021 decision 1 makes the set open, so a future kind must not require a
// contract change. The constants name the kinds the record names.
type Kind string

const (
	// KindUser is a human principal. Humans authenticate through an OIDC
	// session (ADR 0021 decision 8).
	KindUser Kind = "user"
	// KindService is a service principal — an integration or a peer
	// process. Services authenticate with an Ed25519 keypair.
	KindService Kind = "service"
	// KindConnector is a connector's own principal (ADR 0023 decision 2).
	KindConnector Kind = "connector"
	// KindNavi is a navi's service principal, navi:<name> (ADR 0021
	// decision 10). System-originated work acts as it.
	KindNavi Kind = "navi"
	// KindGroup is a group: a named collection of principals, optionally
	// authenticatable itself as a shared identity (ADR 0021 decision 2).
	KindGroup Kind = "group"
)

// Ref is a validated reference to a principal: an open kind and a non-empty
// name, written "kind:name". A Ref is produced by New or Parse, so the zero
// Ref is the absence of a principal and every non-zero Ref is well-formed.
// Ref is comparable and usable as a map key, and marshals as its string form.
type Ref struct {
	kind Kind
	name string
}

// New builds a reference from a kind and a name. Both must be non-empty and
// neither may contain the ":" separator; surrounding space is trimmed.
func New(kind Kind, name string) (Ref, error) {
	k := Kind(strings.TrimSpace(string(kind)))
	n := strings.TrimSpace(name)
	switch {
	case k == "":
		return Ref{}, fmt.Errorf("%w: empty kind", ErrInvalidRef)
	case n == "":
		return Ref{}, fmt.Errorf("%w: empty name for kind %q", ErrInvalidRef, k)
	case strings.Contains(string(k), ":"):
		return Ref{}, fmt.Errorf("%w: kind %q contains the separator", ErrInvalidRef, k)
	case strings.Contains(n, ":"):
		return Ref{}, fmt.Errorf("%w: name %q contains the separator", ErrInvalidRef, n)
	}
	return Ref{kind: k, name: n}, nil
}

// Parse parses a qualified reference, "kind:name". An empty string is
// ErrEmptyRef; anything without exactly one non-empty kind and name is
// ErrInvalidRef.
func Parse(s string) (Ref, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return Ref{}, ErrEmptyRef
	}
	kind, name, ok := strings.Cut(t, ":")
	if !ok {
		return Ref{}, fmt.Errorf("%w: %q (want kind:name)", ErrInvalidRef, s)
	}
	return New(Kind(kind), name)
}

// ParseLegacy parses a stored reference, accepting the pre-principal form: an
// unqualified name is read as navi:<name> (see the package doc's Migration
// section). A qualified value parses exactly as Parse. Empty input is
// ErrEmptyRef.
func ParseLegacy(s string) (Ref, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return Ref{}, ErrEmptyRef
	}
	if !strings.Contains(t, ":") {
		return New(KindNavi, t)
	}
	return Parse(t)
}

// MustParse is Parse for a compile-time constant. It panics on a malformed
// literal, which is a programming error rather than a runtime input; use Parse
// or ParseLegacy for anything that comes from outside the binary.
func MustParse(s string) Ref {
	r, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return r
}

// Kind returns the reference's kind; empty for the zero Ref.
func (r Ref) Kind() Kind { return r.kind }

// Name returns the reference's name; empty for the zero Ref.
func (r Ref) Name() string { return r.name }

// IsZero reports whether r names no principal.
func (r Ref) IsZero() bool { return r == Ref{} }

// Validate reports whether r names a principal. Every non-zero Ref is valid
// by construction, so this only rejects the zero Ref; it exists for values
// that arrive from outside the package (a decoded or zero-initialized field)
// and to state the invariant at a boundary.
func (r Ref) Validate() error {
	if r.IsZero() {
		return ErrEmptyRef
	}
	return nil
}

// String renders the reference as "kind:name"; "" for the zero Ref.
func (r Ref) String() string {
	if r.IsZero() {
		return ""
	}
	return string(r.kind) + ":" + r.name
}

// MarshalText implements encoding.TextMarshaler.
func (r Ref) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler. It is strict: an
// unqualified value is rejected rather than migrated. Use ParseLegacy to read
// pre-principal persisted state.
func (r *Ref) UnmarshalText(b []byte) error {
	parsed, err := Parse(string(b))
	if err != nil {
		return err
	}
	*r = parsed
	return nil
}

// MarshalJSON implements json.Marshaler, so a Ref renders as its string form
// rather than as an object with unexported fields.
func (r Ref) MarshalJSON() ([]byte, error) { return json.Marshal(r.String()) }

// UnmarshalJSON implements json.Unmarshaler. Like UnmarshalText it is strict;
// "" decodes to the zero Ref.
func (r *Ref) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if strings.TrimSpace(s) == "" {
		*r = Ref{}
		return nil
	}
	parsed, err := Parse(s)
	if err != nil {
		return err
	}
	*r = parsed
	return nil
}
