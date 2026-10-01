// Package auth handles PIN hashing and a signed, stateless session cookie
// (no session storage is needed on Vercel).
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// CookieName is the session cookie name.
const CookieName = "karaoke_session"

// Auth issues and verifies session tokens. The token only says who the user
// is; their role is read again from the store on every request.
type Auth struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// New creates an Auth. secret must be long and random (32+ bytes).
func New(secret string, ttl time.Duration) (*Auth, error) {
	if len(secret) < 32 {
		return nil, errors.New("SESSION_SECRET minimal 32 karakter")
	}
	return &Auth{secret: []byte(secret), ttl: ttl, now: time.Now}, nil
}

// Token returns "<username b64>.<expiry-unix>.<signature>".
func (a *Auth) Token(username string) string {
	msg := base64.RawURLEncoding.EncodeToString([]byte(username)) + "." +
		strconv.FormatInt(a.now().Add(a.ttl).Unix(), 10)
	return msg + "." + a.sign(msg)
}

// Username returns the user of a valid, unexpired token.
func (a *Auth) Username(token string) (string, bool) {
	i := strings.LastIndexByte(token, '.')
	if i < 0 || !hmac.Equal([]byte(token[i+1:]), []byte(a.sign(token[:i]))) {
		return "", false
	}
	userB64, exp, ok := strings.Cut(token[:i], ".")
	if !ok {
		return "", false
	}
	unix, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || a.now().Unix() >= unix {
		return "", false
	}
	user, err := base64.RawURLEncoding.DecodeString(userB64)
	if err != nil || len(user) == 0 {
		return "", false
	}
	return string(user), true
}

// SetCookie writes the session cookie for username.
func (a *Auth) SetCookie(w http.ResponseWriter, username string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    a.Token(username),
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

// RequestUsername returns the username from the request's session cookie.
func (a *Auth) RequestUsername(r *http.Request) (string, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return "", false
	}
	return a.Username(c.Value)
}

func (a *Auth) sign(msg string) string {
	m := hmac.New(sha256.New, a.secret)
	m.Write([]byte(msg))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
