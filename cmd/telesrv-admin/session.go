package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const sessionCookieName = "telesrv_admin_session"

// csrfCookieName is the double-submit cookie. It is deliberately NOT HttpOnly:
// the panel's own JavaScript has to read it back to echo it in the X-CSRF-Token
// header, which is the whole mechanism.
const csrfCookieName = "telesrv_admin_csrf"

// csrfHeaderName is the header the panel echoes the cookie in.
const csrfHeaderName = "X-CSRF-Token"

type sessionClaims struct {
	Actor string `json:"actor"`
	Exp   int64  `json:"exp"`
	Nonce string `json:"nonce"`
	// Permissions is the right set granted to this session, taken from
	// TELESRV_ADMIN_UI_PERMISSIONS at login. It travels inside the signed cookie
	// rather than being re-read per request, so a session keeps the rights it was
	// issued with, and it cannot be edited by the browser: the HMAC covers it.
	Permissions []string `json:"permissions,omitempty"`
	// CSRF is the double-submit token bound to this session. Binding it into the
	// signed claims is what makes the cookie/header pair unforgeable by a sibling
	// origin that can only *write* cookies (a subdomain, say): such an attacker
	// can set both the cookie and the header to a value they know, but they cannot
	// produce a session cookie that agrees with it.
	CSRF string `json:"csrf,omitempty"`
}

func signSession(key []byte, claims sessionClaims) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encPayload := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(encPayload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encPayload + "." + sig, nil
}

func verifySession(key []byte, value string, now time.Time) (sessionClaims, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return sessionClaims{}, false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(parts[0]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(parts[1]), []byte(want)) != 1 {
		return sessionClaims{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return sessionClaims{}, false
	}
	var claims sessionClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return sessionClaims{}, false
	}
	if claims.Actor == "" || claims.Exp <= now.Unix() {
		return sessionClaims{}, false
	}
	return claims, true
}

// newCSRFToken mints a fresh double-submit token.
func newCSRFToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// setCSRFCookie publishes the token to the browser.
func setCSRFCookie(w http.ResponseWriter, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:   csrfCookieName,
		Value:  token,
		Path:   "/",
		MaxAge: int(ttl.Seconds()),
		// Readable by the panel's script on purpose; see csrfCookieName.
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
	})
}
