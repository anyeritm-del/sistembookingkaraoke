package auth

import (
	"strings"
	"testing"
	"time"
)

const secret = "0123456789abcdef0123456789abcdef"

func TestTokenRoundTrip(t *testing.T) {
	a, err := New(secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	a.now = func() time.Time { return now }

	tok := a.Token("budi.s")
	if u, ok := a.Username(tok); !ok || u != "budi.s" {
		t.Fatalf("fresh token: %q %v", u, ok)
	}
	// Swap the username part: signature must fail.
	parts := strings.SplitN(tok, ".", 2)
	forged := "YWRtaW4." + parts[1] // base64("admin")
	if _, ok := a.Username(forged); ok {
		t.Error("forged username must not be valid")
	}
	for _, bad := range []string{"", "garbage", "a.b", "a.b.c"} {
		if _, ok := a.Username(bad); ok {
			t.Errorf("%q must not be valid", bad)
		}
	}

	other, _ := New(strings.Repeat("x", 32), time.Hour)
	other.now = a.now
	if _, ok := other.Username(tok); ok {
		t.Error("token from another secret must not be valid")
	}

	now = now.Add(2 * time.Hour)
	if _, ok := a.Username(tok); ok {
		t.Error("expired token must not be valid")
	}
}

func TestNewValidates(t *testing.T) {
	if _, err := New("short", time.Hour); err == nil {
		t.Error("short secret should fail")
	}
	if _, err := NewPINHasher("short"); err == nil {
		t.Error("short pepper should fail")
	}
}

func TestPINHash(t *testing.T) {
	h, err := NewPINHasher(strings.Repeat("p", 32))
	if err != nil {
		t.Fatal(err)
	}
	hash, err := h.Hash("482915")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "v1$100000$") || strings.Contains(hash, "482915") {
		t.Errorf("hash format: %s", hash)
	}
	if !h.Verify(hash, "482915") {
		t.Error("right PIN must verify")
	}
	if h.Verify(hash, "482916") || h.Verify(hash, "") {
		t.Error("wrong PIN must not verify")
	}
	again, _ := h.Hash("482915")
	if again == hash {
		t.Error("salt must make hashes differ")
	}
	other, _ := NewPINHasher(strings.Repeat("q", 32))
	if other.Verify(hash, "482915") {
		t.Error("another pepper must not verify")
	}
	for _, bad := range []string{"", "v1$x$y$z", "v2$1$AA$AA", "v1$0$AA$AA"} {
		if h.Verify(bad, "482915") {
			t.Errorf("malformed hash %q must not verify", bad)
		}
	}
}
