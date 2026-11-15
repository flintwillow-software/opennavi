package navi

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"

	"github.com/flintwillow-software/opennavi/principal"
)

// This file is the navi's own identity (ADR 0031 decision 2): the Ed25519
// keypair that IS the navi, from which both its principal reference
// (navi:<name>) and its libp2p PeerID derive. ADR 0021 made a navi a
// principal with a keypair and a peer navi a principal; this is the missing
// half — until now the navi only ever loaded OTHER principals' PUBLIC keys
// (navi/trust.go), so it had no key of its own and therefore no network
// identity.
//
// The private half never enters the principal model (navi/principal.go's
// Ed25519Key is public-only by design: verification is all a trust source
// needs). Identity is the separate thing that holds the private half, and it
// exposes only the public facts — the Ref, the public key, the PeerID — plus
// a Sign method. Nothing else in the navi reads the private key.

// ErrNoIdentity is reported when an operation needs the navi's own identity
// and none was declared (WithIdentity). It is the fail-closed answer: a navi
// with no identity is exactly what it was before this existed.
var ErrNoIdentity = errors.New("navi: no identity declared")

// Identity is the navi's own keypair and the identities derived from it.
// It is immutable once built.
type Identity struct {
	priv ed25519.PrivateKey
	ref  principal.Ref
}

// NewIdentity builds an Identity from a private key and the navi's name. The
// name is the principal's name, so the reference is `navi:<name>` — the same
// reference the navi acts under as a service principal (ADR 0021 decision 10).
func NewIdentity(name string, priv ed25519.PrivateKey) (*Identity, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%w: ed25519 private key is %d bytes, want %d",
			ErrInvalidAuthenticator, len(priv), ed25519.PrivateKeySize)
	}
	ref, err := principal.New(principal.KindNavi, name)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidPrincipal, err)
	}
	return &Identity{priv: priv, ref: ref}, nil
}

// Ref is the navi's principal reference (`navi:<name>`).
func (i *Identity) Ref() principal.Ref { return i.ref }

// PublicKey is the navi's Ed25519 public key. It is the same key a trust
// store holds for this navi (navi/trust.go), so a peer can verify it.
func (i *Identity) PublicKey() ed25519.PublicKey {
	return i.priv.Public().(ed25519.PublicKey)
}

// Sign signs a message with the navi's private key. It is the operation the
// peer transport will use to prove this identity (ADR 0031 decision 7).
func (i *Identity) Sign(message []byte) []byte {
	return ed25519.Sign(i.priv, message)
}

// PeerID is the navi's libp2p PeerID (ADR 0031 decision 2), derived from the
// public key. It is a pure function of the key, so it is stable across
// restarts and equal to what a libp2p host built from the same key would
// report — which is the point: the navi's principal key IS its PeerID, with
// no second identity system.
//
// The derivation is stdlib-only on purpose. A libp2p Ed25519 PeerID is
// base58btc of an identity multihash over the protobuf-encoded public key:
//
//	0x00            identity multihash code
//	0x24            length (36 = 4-byte protobuf prefix + 32-byte key)
//	0x08 0x01 0x12 0x20   protobuf: field 1 (type) = 1 (Ed25519), field 2
//	                      (data) = 32-byte bytes
//	<32-byte key>
//
// This is a fixed 36-byte layout for Ed25519, so no protobuf or multihash
// library is needed — and pulling go-libp2p in just to format a string would
// invert ADR 0004's dependency caution for the whole navi.
func (i *Identity) PeerID() string {
	buf := make([]byte, 0, 38)
	buf = append(buf, 0x00, 0x24)             // identity multihash, length 36
	buf = append(buf, 0x08, 0x01, 0x12, 0x20) // protobuf: Ed25519, 32 bytes
	buf = append(buf, i.PublicKey()...)
	return base58Encode(buf)
}

// LoadIdentity reads an Ed25519 private key from a PKCS#8 PEM file and builds
// the navi's identity from it. The format is the one the portal client
// already uses (portal/gateway.LoadEd25519Key), so one key file serves both.
func LoadIdentity(name, path string) (*Identity, error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("navi: read identity key %s: %w", path, err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("navi: identity key %s is not PEM", path)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("navi: identity key %s: %w", path, err)
	}
	priv, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("navi: identity key %s is %T, not an Ed25519 private key", path, parsed)
	}
	return NewIdentity(name, priv)
}

// LoadOrGenerateIdentity returns the navi's identity, generating and
// persisting a new keypair when the file does not exist. Generation is what
// makes a navi addressable without an operator hand-crafting a key, and it is
// safe because the file is written 0600 and the PeerID is stable from then on
// — a second process loading the same path gets the same identity.
//
// It never overwrites an existing file: an unreadable or malformed key is an
// error, not a reason to mint a new identity and silently change the navi's
// PeerID.
func LoadOrGenerateIdentity(name, path string) (*Identity, error) {
	_, statErr := os.Stat(path)
	switch {
	case statErr == nil:
		return LoadIdentity(name, path)
	case !errors.Is(statErr, os.ErrNotExist):
		return nil, fmt.Errorf("navi: identity key %s: %w", path, statErr)
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("navi: generate identity key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("navi: marshal identity key: %w", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("navi: identity key directory %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		return nil, fmt.Errorf("navi: write identity key %s: %w", path, err)
	}
	return NewIdentity(name, priv)
}

// WithIdentity declares the navi's own identity: the Ed25519 keypair at
// keyPath, from which its principal reference (navi:<name>) and its libp2p
// PeerID derive (ADR 0031 decision 2). The file is a PKCS#8 PEM private key,
// the same format the portal client uses (portal/gateway.LoadEd25519Key), and
// it is GENERATED on first run when absent — so a navi is addressable without
// an operator hand-crafting a key. An existing file is never overwritten.
//
// An empty path declares no identity, which is the default and exactly what a
// navi was before this existed. The key is loaded at assembly, so a malformed
// or unreadable key fails the run rather than surfacing later.
func WithIdentity(keyPath string) Option {
	return func(n *Navi) { n.identityPath = keyPath }
}

// Identity returns the navi's own identity, or nil when none was declared.
// Nil before Run has assembled the navi.
func (n *Navi) Identity() *Identity { return n.identity }

// assembleIdentity resolves the declared identity key (WithIdentity). It is a
// no-op when no path was declared, so a navi without an identity is unchanged.
func (n *Navi) assembleIdentity() error {
	path := strings.TrimSpace(n.identityPath)
	if path == "" {
		return nil
	}
	id, err := LoadOrGenerateIdentity(n.name, path)
	if err != nil {
		return err
	}
	n.identity = id
	return nil
}

// base58Encode encodes bytes in base58btc (the Bitcoin alphabet), the
// encoding libp2p uses for PeerID strings. It is here rather than a
// dependency because it is the only base58 use in the navi.
func base58Encode(input []byte) string {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	x := new(big.Int).SetBytes(input)
	base := big.NewInt(58)
	zero := big.NewInt(0)
	mod := new(big.Int)

	var out []byte
	for x.Cmp(zero) > 0 {
		x.DivMod(x, base, mod)
		out = append(out, alphabet[mod.Int64()])
	}
	// Leading zero bytes encode as '1'.
	for _, b := range input {
		if b != 0 {
			break
		}
		out = append(out, alphabet[0])
	}
	// Reverse.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}
