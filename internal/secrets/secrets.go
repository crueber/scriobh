// Package secrets seals the few credentials Scríobh must keep: Git deploy keys
// and tokens today, AI provider keys later.
//
// Values are sealed with AES-256-GCM under a key derived (HKDF-SHA-256, with a
// label of its own) from the instance key, SCRIOBH_SECRET_KEY or the 0600 key
// file, so the key that signs sessions is never used directly to encrypt. Every
// sealed value is bound to a context string (the table, row and field it
// belongs to) as additional authenticated data: a ciphertext copied into
// another row does not open.
//
// A Box without a key refuses to seal. Credentials are never stored in the
// clear as a fallback.
//
// Nothing here logs. Callers must not put an opened secret in an error, a log
// line, an audit entry or a response; Redact scrubs known values from text that
// comes from a library the caller does not control.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

// label separates this use of the instance key from every other.
const label = "scriobh/secrets/v1"

// prefix marks a sealed value and its format version.
const prefix = "v1."

// Errors. Their messages are safe to show to a person.
var (
	// ErrNoKey means the instance has no secret key, so nothing can be sealed.
	ErrNoKey = errors.New("no secret key is configured, so credentials cannot be stored; set SCRIOBH_SECRET_KEY or let the server create its key file")
	// ErrCannotOpen means the value is not sealed under this instance's key
	// (the key changed, the data was restored without its key, or the value was
	// altered or moved to another row).
	ErrCannotOpen = errors.New("the stored credential cannot be read with this installation's secret key")
)

// Box seals and opens values under one instance key.
type Box struct {
	aead cipher.AEAD
}

// New returns a Box for the instance key. An empty key gives a disabled Box:
// Enabled is false and Seal returns ErrNoKey.
func New(instanceKey []byte) *Box {
	if len(instanceKey) == 0 {
		return &Box{}
	}
	k, err := hkdf.Key(sha256.New, instanceKey, nil, label, 32)
	if err != nil {
		return &Box{}
	}
	blk, err := aes.NewCipher(k)
	if err != nil {
		return &Box{}
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return &Box{}
	}
	return &Box{aead: g}
}

// Enabled reports whether the Box has a key.
func (b *Box) Enabled() bool { return b != nil && b.aead != nil }

// Seal encrypts plain for the given context and returns text that is safe to
// store. The context is not secret; it must be the same at Open.
func (b *Box) Seal(plain []byte, context string) (string, error) {
	if !b.Enabled() {
		return "", ErrNoKey
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := b.aead.Seal(nonce, nonce, plain, []byte(context))
	return prefix + base64.RawURLEncoding.EncodeToString(out), nil
}

// Open decrypts a value sealed for context. Any failure is ErrCannotOpen; the
// reason is deliberately not distinguished.
func (b *Box) Open(sealed, context string) ([]byte, error) {
	if !b.Enabled() {
		return nil, ErrNoKey
	}
	if !strings.HasPrefix(sealed, prefix) {
		return nil, ErrCannotOpen
	}
	raw, err := base64.RawURLEncoding.DecodeString(sealed[len(prefix):])
	if err != nil || len(raw) < b.aead.NonceSize()+b.aead.Overhead() {
		return nil, ErrCannotOpen
	}
	n := b.aead.NonceSize()
	plain, err := b.aead.Open(nil, raw[:n], raw[n:], []byte(context))
	if err != nil {
		return nil, ErrCannotOpen
	}
	return plain, nil
}

// CanOpen reports whether sealed opens under this key, without keeping the
// plain text.
func (b *Box) CanOpen(sealed, context string) bool {
	p, err := b.Open(sealed, context)
	clear(p)
	return err == nil
}

// Redact replaces every occurrence of each secret of four or more characters
// in s. Use it on text produced by libraries (transport errors can echo a URL
// or a header).
func Redact(s string, secret ...string) string {
	for _, v := range secret {
		if len(v) >= 4 {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	return s
}
