package navi

import (
	"sync"
	"time"

	"github.com/flintwillow-software/opennavi/principal"
)

// This file is the trust ledger of ADR 0021 decision 12 and ADR 0027: an
// append-only, bounded record of the trust decisions the navi makes, kept
// SEPARATE from the navi-authored interaction record (the thought, its
// originator and lineage, and its transcript). The ledger is operator-facing;
// the interaction record is the navi's own state.
//
// The ledger records what the navi witnesses. A grant or revocation happens
// outside it — a compiled declaration, or an operator editing a trust store —
// so there is no author to name; the events here are the decisions the navi
// itself makes: a credential it authenticated, and a credential it refused. A
// future operator grant/revocation path appends granted/revoked events with the
// acting operator as By.

// TrustEventKind is the closed set of trust events the ledger records.
type TrustEventKind string

const (
	// TrustAuthenticated is a credential that resolved to a principal.
	TrustAuthenticated TrustEventKind = "authenticated"
	// TrustRefused is a credential that did not resolve. A refusal is recorded
	// as visibly as a success: an operator auditing trust wants to see who was
	// turned away, not only who got in.
	TrustRefused TrustEventKind = "refused"
)

// TrustEvent is one trust decision. It carries identity, mechanism, and
// outcome — never a credential: no token, signature, or key material enters the
// ledger (ADR 0008, ADR 0027 decision 5).
type TrustEvent struct {
	// At is when the decision was made (UTC).
	At time.Time
	// Kind is the decision.
	Kind TrustEventKind
	// Principal is who was trusted. Zero on a refusal, where no principal was
	// established.
	Principal principal.Ref
	// Scheme is how the credential was presented: "bearer" or "ed25519". Empty
	// when no credential was presented at all.
	Scheme string
	// Detail explains a refusal. It is a reason, never the presented material.
	Detail string
}

// DefaultTrustLedgerCap bounds the ledger. It is a ring: the oldest event is
// dropped when the cap is reached, so a long-lived navi cannot grow an
// unbounded audit log in memory (ADR 0027 decision 4).
const DefaultTrustLedgerCap = 1024

// TrustLedger is an append-only, bounded record of trust decisions. It is safe
// for concurrent use. Reading it is a capability (`read:trust_event`), not a
// bespoke endpoint: the ledger is exposed through the object surface.
type TrustLedger struct {
	mu   sync.Mutex
	cap  int
	ring []TrustEvent
	next int
	full bool
}

// NewTrustLedger builds a ledger with the given capacity (0 selects the
// default). A negative capacity is treated as the default rather than an
// unbounded ledger.
func NewTrustLedger(capacity int) *TrustLedger {
	if capacity <= 0 {
		capacity = DefaultTrustLedgerCap
	}
	return &TrustLedger{cap: capacity, ring: make([]TrustEvent, capacity)}
}

// Append records one event, dropping the oldest when the ring is full.
func (l *TrustLedger) Append(ev TrustEvent) {
	if l == nil {
		return
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ring[l.next] = ev
	l.next = (l.next + 1) % l.cap
	if l.next == 0 {
		l.full = true
	}
}

// Events returns a copy of the ledger, oldest first.
func (l *TrustLedger) Events() []TrustEvent {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	n := l.next
	if l.full {
		n = l.cap
	}
	out := make([]TrustEvent, 0, n)
	if l.full {
		out = append(out, l.ring[l.next:]...)
	}
	out = append(out, l.ring[:l.next]...)
	return out
}

// Len reports how many events the ledger holds.
func (l *TrustLedger) Len() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.full {
		return l.cap
	}
	return l.next
}
