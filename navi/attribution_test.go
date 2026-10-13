package navi

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/object"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/principal"
	"github.com/flintwillow-software/opennavi/thought"
)

// This file is the attribution test (ADR 0021 decision 12, ADR 0027): the
// interaction record (a thought's originator and lineage) is exposed where a
// portal reads, and the trust ledger records the trust decisions the navi made,
// separately and without ever holding a credential.

// TestTrustLedgerRecordsDecisions pins the ledger's vocabulary: an
// authenticated credential with the principal it resolved, and a refusal with
// the CLAIMED principal, so an operator sees who was turned away.
func TestTrustLedgerRecordsDecisions(t *testing.T) {
	now := time.Now().UTC()
	ref := principal.MustParse("service:cli")
	pub, priv := keypair(t)
	key, err := NewEd25519Key("k1", pub)
	if err != nil {
		t.Fatal(err)
	}
	who, err := NewPrincipal(ref, key)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := NewPrincipals(who)
	if err != nil {
		t.Fatal(err)
	}
	trust, err := NewTrustSources(compiled)
	if err != nil {
		t.Fatal(err)
	}
	auth := NewPortalAuthenticator(trust)

	message := []byte("POST\n/portal/control\n1\nabc")
	cred := portal.Credential{
		Scheme: portal.SchemeEd25519, Principal: ref, KeyID: "k1",
		Message: message, Proof: ed25519.Sign(priv, message), Now: now,
	}
	if _, err := auth.Authenticate(context.Background(), cred); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	bad := cred
	bad.Proof = ed25519.Sign(priv, []byte("another message"))
	if _, err := auth.Authenticate(context.Background(), bad); !errors.Is(err, portal.ErrUnauthenticated) {
		t.Fatalf("bad proof = %v, want ErrUnauthenticated", err)
	}
	absent := cred
	absent.Principal = principal.MustParse("service:absent")
	if _, err := auth.Authenticate(context.Background(), absent); !errors.Is(err, portal.ErrUnauthenticated) {
		t.Fatalf("unknown principal = %v, want ErrUnauthenticated", err)
	}

	events := trust.Ledger().Events()
	if len(events) != 3 {
		t.Fatalf("ledger has %d events, want 3: %+v", len(events), events)
	}
	if events[0].Kind != TrustAuthenticated || events[0].Principal != ref || events[0].Scheme != "ed25519" {
		t.Errorf("first event = %+v, want the authenticated principal", events[0])
	}
	if events[1].Kind != TrustRefused || events[1].Principal != ref {
		t.Errorf("second event = %+v, want a refusal naming the claimed principal", events[1])
	}
	if events[2].Kind != TrustRefused || events[2].Principal != principal.MustParse("service:absent") {
		t.Errorf("third event = %+v, want a refusal naming the unknown claim", events[2])
	}
}

// TestTrustLedgerNeverCarriesACredential is ADR 0027 decision 5: a ledger that
// held the token would be a secret store with the worst retention policy.
func TestTrustLedgerNeverCarriesACredential(t *testing.T) {
	trust, err := NewTrustSources()
	if err != nil {
		t.Fatal(err)
	}
	auth := NewPortalAuthenticator(trust)
	const token = "super-secret-token-value"
	if _, err := auth.Authenticate(context.Background(), portal.Credential{
		Scheme: portal.SchemeBearer, Token: token, Now: time.Now().UTC(),
	}); !errors.Is(err, portal.ErrUnauthenticated) {
		t.Fatalf("a token with no bearer source = %v, want a refusal", err)
	}
	for _, ev := range trust.Ledger().Events() {
		if strings.Contains(ev.Detail, token) {
			t.Fatalf("the ledger recorded the credential: %+v", ev)
		}
	}
}

// TestTrustLedgerIsBounded is ADR 0027 decision 4: the ledger is a ring, so a
// long-lived navi cannot grow an unbounded audit log in memory.
func TestTrustLedgerIsBounded(t *testing.T) {
	l := NewTrustLedger(3)
	for i := 0; i < 5; i++ {
		l.Append(TrustEvent{Kind: TrustAuthenticated, Principal: principal.MustParse(fmt.Sprintf("service:s%d", i))})
	}
	events := l.Events()
	if len(events) != 3 || l.Len() != 3 {
		t.Fatalf("ledger holds %d events (Len %d), want 3", len(events), l.Len())
	}
	if events[0].Principal.Name() != "s2" || events[2].Principal.Name() != "s4" {
		t.Fatalf("ring dropped the wrong end: %+v", events)
	}
}

// TestThoughtExposesAttribution is the interaction-record half: "on whose
// behalf did this happen" is answerable through the object surface, paired with
// the lineage the delegated-authorization slice records.
func TestThoughtExposesAttribution(t *testing.T) {
	n, s := objectNavi(t)
	alice := principal.MustParse("user:alice")
	if err := n.Thoughts.Enqueue(&thought.Thought{
		ID: "t1", Prompt: "start", Status: thought.ThoughtPending, Origin: alice,
	}); err != nil {
		t.Fatal(err)
	}
	if err := n.Thoughts.Enqueue(&thought.Thought{
		ID: "t2", Prompt: "continue", Status: thought.ThoughtPending, DependsOn: []string{"t1"},
	}); err != nil {
		t.Fatal(err)
	}

	obj, err := s.Get(TypeThought, "t2")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if obj.Fields["origin"] != "user:alice" {
		t.Errorf("origin = %v, want user:alice", obj.Fields["origin"])
	}
	lineage, _ := obj.Fields["lineage"].([]string)
	if len(lineage) != 1 || lineage[0] != "t1" {
		t.Errorf("lineage = %v, want [t1]", obj.Fields["lineage"])
	}
	// Attribution is the record, not working state: it is not sensitive, so a
	// principal holding read:thought sees it.
	if _, _, err := s.List(TypeThought, object.Query{}); err != nil {
		t.Fatalf("List: %v", err)
	}
}

// TestTrustEventTypeIsReadable exposes the ledger through the object surface:
// reading it is a capability (read:trust_event), not a bespoke endpoint.
func TestTrustEventTypeIsReadable(t *testing.T) {
	n, s := objectNavi(t)
	n.Trust().Ledger().Append(TrustEvent{
		Kind: TrustAuthenticated, Principal: principal.MustParse("user:alice"), Scheme: "bearer",
	})
	objs, next, err := s.List(TypeTrustEvent, object.Query{})
	if err != nil {
		t.Fatalf("List(%s): %v", TypeTrustEvent, err)
	}
	if next != "" || len(objs) != 1 {
		t.Fatalf("ledger list = %d objects (next %q), want 1", len(objs), next)
	}
	if objs[0].Fields["principal"] != "user:alice" || objs[0].Fields["kind"] != "authenticated" {
		t.Errorf("trust event = %v, want alice/authenticated", objs[0].Fields)
	}
}
