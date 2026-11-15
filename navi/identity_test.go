package navi

import (
	"crypto/ed25519"
	"crypto/rand"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/principal"
)

// This file gates the navi's own identity (NAVI-629, ADR 0031 decision 2):
// the keypair it derives its principal reference and libp2p PeerID from.

// TestLoadOrGenerateIdentityCreatesAStableKey pins the generate-on-first-run
// path: a missing file yields a valid identity, the file is written 0600, and
// a second load of the same path returns the SAME identity — so a navi's
// PeerID is stable across restarts rather than changing every boot.
func TestLoadOrGenerateIdentityCreatesAStableKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.pem")

	first, err := LoadOrGenerateIdentity("bastilli", path)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if first.Ref() != principal.MustParse("navi:bastilli") {
		t.Fatalf("ref = %s, want navi:bastilli", first.Ref())
	}
	if len(first.PublicKey()) != ed25519.PublicKeySize {
		t.Fatalf("public key is %d bytes", len(first.PublicKey()))
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("key file missing: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("key file mode = %o, want 600", perm)
	}

	second, err := LoadOrGenerateIdentity("bastilli", path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if first.PeerID() != second.PeerID() {
		t.Fatalf("PeerID changed across reload: %s -> %s", first.PeerID(), second.PeerID())
	}
	if !first.PublicKey().Equal(second.PublicKey()) {
		t.Fatal("public key changed across reload")
	}
}

// TestIdentityPeerIDMatchesTheLibp2pEncoding pins the PeerID derivation
// against the libp2p spec's own byte layout, decoded independently here. A
// self-referential assertion (encode then compare to the same encoder) would
// pass with any layout, so this decodes the string back to bytes and checks
// the structure the spec mandates:
//
//	0x00 0x24              identity multihash, length 36
//	0x08 0x01 0x12 0x20    protobuf: field 1 (type) = Ed25519, field 2 = 32 bytes
//	<32-byte public key>
//
// It also pins the human-visible prefix: an Ed25519 PeerID always starts
// "12D3KooW", which is what an operator sees and what a libp2p host would
// report for the same key.
func TestIdentityPeerIDMatchesTheLibp2pEncoding(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id, err := NewIdentity("peer-check", priv)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := base58Decode(id.PeerID())
	if err != nil {
		t.Fatalf("PeerID is not valid base58btc: %v", err)
	}
	if len(raw) != 38 {
		t.Fatalf("decoded PeerID is %d bytes, want 38 (2 multihash + 4 protobuf + 32 key)", len(raw))
	}
	wantPrefix := []byte{0x00, 0x24, 0x08, 0x01, 0x12, 0x20}
	for i, b := range wantPrefix {
		if raw[i] != b {
			t.Fatalf("byte %d = %#x, want %#x", i, raw[i], b)
		}
	}
	embedded := raw[6:]
	if !ed25519.PublicKey(embedded).Equal(id.PublicKey()) {
		t.Fatal("the PeerID does not embed the identity's public key")
	}
	if !strings.HasPrefix(id.PeerID(), "12D3KooW") {
		t.Fatalf("Ed25519 PeerID = %q, want the 12D3KooW prefix", id.PeerID())
	}
}

// TestIdentityPeerIDCrossChecksLibp2p pins the derivation against a REAL
// libp2p implementation, not just the spec text. For the deterministic seed
// 0x00..0x1f, go-libp2p's peer.IDFromPrivateKey reports
// 12D3KooWA4Xop1JaT3MHxwYMkCepYsv4iPVopMXwCz5iHYdBfeSB; this asserts the
// hand-rolled encoding produces the identical string, which is the check that
// matters — a spec reading can be subtly wrong, a byte-for-byte match with the
// reference implementation cannot.
func TestIdentityPeerIDCrossChecksLibp2p(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	id, err := NewIdentity("crosscheck", ed25519.NewKeyFromSeed(seed))
	if err != nil {
		t.Fatal(err)
	}
	const want = "12D3KooWA4Xop1JaT3MHxwYMkCepYsv4iPVopMXwCz5iHYdBfeSB"
	if got := id.PeerID(); got != want {
		t.Fatalf("PeerID = %s, want %s (the go-libp2p reference)", got, want)
	}
}

// TestIdentityPeerIDIsDistinctPerKey pins that two navis get two PeerIDs —
// the property the peer roster depends on.
func TestIdentityPeerIDIsDistinctPerKey(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadOrGenerateIdentity("a", filepath.Join(dir, "a.pem"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrGenerateIdentity("b", filepath.Join(dir, "b.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if a.PeerID() == b.PeerID() {
		t.Fatal("two generated identities share a PeerID")
	}
}

// TestLoadOrGenerateNeverOverwrites pins the fail-closed rule: a malformed
// key file is an error, not a reason to mint a new identity and silently
// change the navi's PeerID.
func TestLoadOrGenerateNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.pem")
	if err := os.WriteFile(path, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrGenerateIdentity("x", path); err == nil {
		t.Fatal("a malformed key file must be an error, not regenerated")
	}
	// The file is untouched.
	got, _ := os.ReadFile(path)
	if string(got) != "not a pem" {
		t.Fatalf("the key file was overwritten: %q", got)
	}
}

// TestWithIdentityIsOptional pins the default: a navi with no WithIdentity
// assembles with no identity, exactly as before this existed.
func TestWithIdentityIsOptional(t *testing.T) {
	clearProviderEnv(t)
	n := testNavi(t)
	if err := n.assemble(); err != nil {
		t.Fatal(err)
	}
	if n.Identity() != nil {
		t.Fatal("a navi with no WithIdentity must have no identity")
	}
}

// TestWithIdentityAssemblesTheDeclaredKey pins the wiring: WithIdentity(path)
// loads (or generates) the key at assembly and exposes it.
func TestWithIdentityAssemblesTheDeclaredKey(t *testing.T) {
	clearProviderEnv(t)
	path := filepath.Join(t.TempDir(), "navi.pem")
	n := testNavi(t, WithIdentity(path))
	if err := n.assemble(); err != nil {
		t.Fatal(err)
	}
	if n.Identity() == nil {
		t.Fatal("WithIdentity did not assemble an identity")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the key file was not created: %v", err)
	}
}

// TestWithIdentityFailsOnAMalformedKey pins that assembly fails rather than
// serving without the identity the author declared.
func TestWithIdentityFailsOnAMalformedKey(t *testing.T) {
	clearProviderEnv(t)
	path := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	n := testNavi(t, WithIdentity(path))
	if err := n.assemble(); err == nil {
		t.Fatal("assembly must fail on a malformed identity key")
	}
}

// base58Decode is an independent base58btc decoder for the test: it proves the
// PeerID is the encoding the spec requires rather than reusing the encoder
// under test.
func base58Decode(s string) ([]byte, error) {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	x := new(big.Int)
	base := big.NewInt(58)
	for _, r := range s {
		idx := strings.IndexRune(alphabet, r)
		if idx < 0 {
			return nil, errInvalidBase58
		}
		x.Mul(x, base)
		x.Add(x, big.NewInt(int64(idx)))
	}
	out := x.Bytes()
	// Restore leading zero bytes (each leading '1' is a zero byte).
	for i := 0; i < len(s) && s[i] == '1'; i++ {
		out = append([]byte{0}, out...)
	}
	return out, nil
}

type base58Error string

func (e base58Error) Error() string { return string(e) }

const errInvalidBase58 = base58Error("invalid base58btc")
