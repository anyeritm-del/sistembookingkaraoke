package auth

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// pinIterations is the PBKDF2 cost. A PIN has few possible values, so the
// real protection is the pepper: it lives only in the server environment,
// so a copy of the sheet alone is not enough to guess PINs.
const pinIterations = 100_000

// PINHasher hashes PINs as "v1$<iterations>$<salt>$<hash>" with
// PBKDF2-SHA256 over HMAC-SHA256(pepper, pin).
type PINHasher struct {
	pepper []byte
}

// NewPINHasher needs a random pepper of at least 32 characters (PIN_PEPPER).
// Changing the pepper makes every saved PIN invalid.
func NewPINHasher(pepper string) (*PINHasher, error) {
	if len(pepper) < 32 {
		return nil, errors.New("PIN_PEPPER minimal 32 karakter")
	}
	return &PINHasher{pepper: []byte(pepper)}, nil
}

// Hash returns a new salted hash of pin.
func (h *PINHasher) Hash(pin string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := h.derive(pin, salt, pinIterations)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("v1$%d$%s$%s", pinIterations, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// Verify reports whether pin matches hash.
func (h *PINHasher) Verify(hash, pin string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != "v1" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 || iter > 10_000_000 {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[2])
	want, err2 := enc.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false
	}
	got, err := h.derive(pin, salt, iter)
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

func (h *PINHasher) derive(pin string, salt []byte, iter int) ([]byte, error) {
	m := hmac.New(sha256.New, h.pepper)
	m.Write([]byte(pin))
	return pbkdf2.Key(sha256.New, string(m.Sum(nil)), salt, iter, 32)
}
