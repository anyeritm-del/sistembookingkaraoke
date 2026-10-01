package auth

import (
	"strings"
	"testing"
	"time"
)

const secret = "0123456789abcdef0123456789abcdef"

func TestTokenRoundTrip(t *testing.T) {
	a, err := New("123456", secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	a.now = func() time.Time { return now }

	tok := a.Token()
	if !a.Valid(tok) {
		t.Fatal("fresh token should be valid")
	}
	exp, sig, _ := strings.Cut(tok, ".")
	if a.Valid(exp + "9." + sig) {
		t.Error("changed expiry must not be valid")
	}
	if a.Valid("garbage") || a.Valid("") {
		t.Error("garbage must not be valid")
	}

	other, _ := New("123456", strings.Repeat("x", 32), time.Hour)
	other.now = a.now
	if other.Valid(tok) {
		t.Error("token from another secret must not be valid")
	}

	now = now.Add(2 * time.Hour)
	if a.Valid(tok) {
		t.Error("expired token must not be valid")
	}
}

func TestCheckPIN(t *testing.T) {
	a, _ := New("123456", secret, time.Hour)
	if !a.CheckPIN("123456") || a.CheckPIN("12345") || a.CheckPIN("") {
		t.Error("CheckPIN wrong")
	}
}

func TestNewValidates(t *testing.T) {
	if _, err := New("12", secret, time.Hour); err == nil {
		t.Error("short PIN should fail")
	}
	if _, err := New("123456", "short", time.Hour); err == nil {
		t.Error("short secret should fail")
	}
}
