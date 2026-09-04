package sqlite

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// Cipher encrypts and decrypts sensitive column values (message
// content, tool input). The store applies it transparently; a Noop
// cipher keeps values as plaintext.
type Cipher interface {
	Encrypt(plaintext []byte) ([]byte, error)
	Decrypt(ciphertext []byte) ([]byte, error)
}

// NoopCipher is the identity cipher: the default, so tests and
// unconfigured deployments store plaintext. Encryption is opt-in via
// NAVI_SESSION_KEY (or an explicit cipher).
type NoopCipher struct{}

func (NoopCipher) Encrypt(b []byte) ([]byte, error) { return b, nil }
func (NoopCipher) Decrypt(b []byte) ([]byte, error) { return b, nil }

// encPrefix marks encrypted values in TEXT columns. Values are stored
// as enc1:<base64(nonce|ciphertext+tag)>; plaintext values have no
// prefix. Reads are self-describing, so Noop and encrypted values can
// coexist in one column.
const encPrefix = "enc1:"

// AESGCM implements Cipher with AES-256-GCM. Nonce is prepended to the
// ciphertext; the GCM tag is appended by the cipher itself.
type AESGCM struct {
	aead cipher.AEAD
}

// NewAESGCM builds an AES-256-GCM cipher from a 32-byte key.
func NewAESGCM(key []byte) (*AESGCM, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("sqlite: AES-256 key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("sqlite: create cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("sqlite: create GCM: %w", err)
	}
	return &AESGCM{aead: aead}, nil
}

func (c *AESGCM) Encrypt(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("sqlite: nonce: %w", err)
	}
	ct := c.aead.Seal(nonce, nonce, plaintext, nil)
	out := make([]byte, len(encPrefix)+base64.StdEncoding.EncodedLen(len(ct)))
	copy(out, encPrefix)
	base64.StdEncoding.Encode(out[len(encPrefix):], ct)
	return out, nil
}

func (c *AESGCM) Decrypt(data []byte) ([]byte, error) {
	s := string(data)
	if !strings.HasPrefix(s, encPrefix) {
		// Unencrypted value (written before encryption was enabled).
		return data, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, encPrefix))
	if err != nil {
		return nil, fmt.Errorf("sqlite: decode encrypted value: %w", err)
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return nil, fmt.Errorf("sqlite: encrypted value too short")
	}
	pt, err := c.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return nil, fmt.Errorf("sqlite: decrypt: %w", err)
	}
	return pt, nil
}

// KeyFromEnv loads the session encryption key from the environment.
// NAVI_SESSION_KEY must decode (hex or base64) to exactly 32 bytes.
// An unset variable returns nil, nil: the caller uses the NoopCipher.
// Key rotation is not supported.
func KeyFromEnv() ([]byte, error) {
	v := os.Getenv("NAVI_SESSION_KEY")
	if v == "" {
		return nil, nil
	}
	decode := func(decoder func(string) ([]byte, error)) ([]byte, error) {
		b, err := decoder(v)
		if err != nil {
			return nil, err
		}
		if len(b) != 32 {
			return nil, fmt.Errorf("sqlite: NAVI_SESSION_KEY must decode to 32 bytes, got %d", len(b))
		}
		return b, nil
	}
	// Hex first (64 chars), then base64.
	if len(v) == 64 {
		if key, err := decode(hex.DecodeString); err == nil {
			return key, nil
		}
	}
	return decode(base64.StdEncoding.DecodeString)
}

// CipherFromEnv resolves the cipher from the environment: AES-256-GCM
// when NAVI_SESSION_KEY is set, Noop otherwise.
func CipherFromEnv() (Cipher, error) {
	key, err := KeyFromEnv()
	if err != nil {
		return nil, err
	}
	if key == nil {
		return NoopCipher{}, nil
	}
	return NewAESGCM(key)
}
