// Package auth handles staff login with a shared PIN and a signed,
// stateless session cookie (no session storage is needed on Vercel).
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// CookieName is the session cookie name.
const CookieName = "karaoke_session"

// Auth checks the PIN and issues and verifies session tokens.
type Auth struct {
	pin    []byte
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// New creates an Auth. secret must be long and random (32+ bytes).
func New(pin, secret string, ttl time.Duration) (*Auth, error) {
	if len(pin) < 4 {
		return nil, errors.New("ADMIN_PIN minimal 4 karakter")
	}
	if len(secret) < 32 {
		return nil, errors.New("SESSION_SECRET minimal 32 karakter")
	}
	return &Auth{pin: []byte(pin), secret: []byte(secret), ttl: ttl, now: time.Now}, nil
}

// CheckPIN compares the PIN in constant time.
func (a *Auth) CheckPIN(pin string) bool {
	return subtle.ConstantTimeCompare([]byte(pin), a.pin) == 1
}

// Token returns a token "<expiry-unix>.<signature>".
func (a *Auth) Token() string {
	exp := strconv.FormatInt(a.now().Add(a.ttl).Unix(), 10)
	return exp + "." + a.sign(exp)
}

// Valid reports whether the token is signed by us and not expired.
func (a *Auth) Valid(token string) bool {
	exp, sig, ok := strings.Cut(token, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(a.sign(exp))) {
		return false
	}
	unix, err := strconv.ParseInt(exp, 10, 64)
	return err == nil && a.now().Unix() < unix
}

// SetCookie writes the session cookie.
func (a *Auth) SetCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    a.Token(),
		Path:     "/",
		MaxAge:   int(a.ttl / time.Second),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

// ClearCookie removes the session cookie.
func ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
}

// LoggedIn reports whether the request has a valid session cookie.
func (a *Auth) LoggedIn(r *http.Request) bool {
	c, err := r.Cookie(CookieName)
	return err == nil && a.Valid(c.Value)
}

func (a *Auth) sign(msg string) string {
	m := hmac.New(sha256.New, a.secret)
	m.Write([]byte(msg))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
