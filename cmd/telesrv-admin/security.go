package main

import (
	"context"
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Panel session authorisation and CSRF.
//
// The panel authenticates with a cookie, which is what makes it a CSRF target:
// a request forged by any other origin arrives with the operator's session
// attached. Two independent checks close that.
//
// 1. Double-submit token. At login the server mints a random token, publishes it
//    in a readable cookie (telesrv_admin_csrf) and requires the same value in the
//    X-CSRF-Token header of every mutating request. A cross-origin page can make
//    the browser *send* the cookie but cannot read it, so it cannot produce the
//    header. Double-submit is the right shape here specifically because this
//    process keeps no server-side session store: the session lives entirely in a
//    signed cookie, so there is nowhere to park a per-session token, and the
//    stateless variant is the one that survives a restart and a second replica.
//    The token is additionally bound into the signed session claims, so a
//    cookie-writing neighbour (a sibling subdomain) cannot supply a matching
//    cookie/header pair of its own choosing either.
//
// 2. Origin agreement. When the browser states an Origin, it must be this host.
//    That catches a forged request from a page that somehow does hold a token.
//
// Both comparisons are constant time, for the same reason the session MAC is.

// Panel permission names. They match the strings an operator configures in
// TELESRV_ADMIN_UI_PERMISSIONS and the ones the admin API enforces.
const (
	permissionAll                = "*"
	permissionVerificationReview = "verification.review"
	permissionVerificationRevoke = "verification.revoke"
	// Third-party bot verification. Deliberately not implied by the official
	// verification rights above: the two are separate mechanisms over separate
	// tables, so a session trusted with one queue is not thereby trusted with the
	// other. review reads and decides applications; manage appoints verifiers,
	// curates the icon catalogue and strips granted marks.
	permissionBotVerificationReview = "botverification.review"
	permissionBotVerificationManage = "botverification.manage"
)

type permissionsKey struct{}

// requireAuthAPI is the gate on every authenticated API route: a valid session,
// and -- for a mutating request -- a valid CSRF token.
func (s *server) requireAuthAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			writeAPIError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		claims, ok := verifySession(s.cfg.SessionKey, cookie.Value, time.Now())
		if !ok {
			clearSessionCookie(w)
			writeAPIError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		if !checkMutationSafety(w, r, claims) {
			return
		}
		ctx := context.WithValue(r.Context(), actorKey{}, claims.Actor)
		ctx = context.WithValue(ctx, permissionsKey{}, newPanelPermissions(claims.Permissions))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requirePermission refuses a session that was not granted the right, before the
// request ever reaches the admin API. The panel is the only caller that can be
// driven by a browser, so the check belongs here as well as upstream: a 403 from
// this process costs no round trip and cannot be confused with a domain failure.
func (s *server) requirePermission(permission string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !permissionsFromContext(r.Context()).Has(permission) {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error":      "permission " + permission + " is required",
				"code":       "FORBIDDEN",
				"permission": permission,
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// checkMutationSafety enforces the CSRF contract on a mutating request.
func checkMutationSafety(w http.ResponseWriter, r *http.Request, claims sessionClaims) bool {
	if !mutatingMethod(r.Method) {
		return true
	}
	if !sameOriginRequest(r) {
		writeAPIError(w, http.StatusForbidden, "origin is not allowed")
		return false
	}
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil || cookie.Value == "" {
		writeAPIError(w, http.StatusForbidden, "missing "+csrfCookieName+" cookie; sign in again")
		return false
	}
	header := strings.TrimSpace(r.Header.Get(csrfHeaderName))
	if header == "" {
		writeAPIError(w, http.StatusForbidden, "missing "+csrfHeaderName+" header")
		return false
	}
	if subtle.ConstantTimeCompare([]byte(header), []byte(cookie.Value)) != 1 {
		writeAPIError(w, http.StatusForbidden, csrfHeaderName+" does not match the "+csrfCookieName+" cookie")
		return false
	}
	// The signed session is the third leg: it pins the pair to the session this
	// server issued. A session minted before the token existed carries no CSRF
	// claim and is refused, which forces one re-login rather than leaving a
	// half-protected session running.
	if claims.CSRF == "" || subtle.ConstantTimeCompare([]byte(header), []byte(claims.CSRF)) != 1 {
		writeAPIError(w, http.StatusForbidden, "csrf token is not bound to this session; sign in again")
		return false
	}
	return true
}

// mutatingMethod reports whether the method changes state. GET/HEAD/OPTIONS are
// the safe ones; everything else has to carry a token.
func mutatingMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

// sameOriginRequest checks the Origin header against the request host.
//
// An absent Origin is accepted: browsers omit it on same-origin requests and
// non-browser callers (curl, tests) never send it, so requiring it would break
// the panel without adding protection the token does not already give. A present
// Origin must be this host -- including the literal "null" a sandboxed or
// privacy-stripped context sends, which is by definition not this host.
//
// This compares against r.Host, so a reverse proxy in front of the panel has to
// preserve it (nginx: proxy_set_header Host $host).
func sameOriginRequest(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}

// panelPermissions is a resolved session permission set.
type panelPermissions struct {
	all   bool
	names map[string]struct{}
	list  []string
}

func newPanelPermissions(permissions []string) panelPermissions {
	set := panelPermissions{names: make(map[string]struct{}, len(permissions))}
	for _, permission := range permissions {
		permission = strings.TrimSpace(permission)
		if permission == "" {
			continue
		}
		if _, dup := set.names[permission]; dup {
			continue
		}
		if permission == permissionAll {
			set.all = true
		}
		set.names[permission] = struct{}{}
		set.list = append(set.list, permission)
	}
	return set
}

// Has reports whether the session was granted the permission.
func (p panelPermissions) Has(permission string) bool {
	if p.all {
		return true
	}
	_, ok := p.names[permission]
	return ok
}

// List is what the panel is told about itself, so the UI can hide a section the
// session may not use instead of rendering it into a 403.
func (p panelPermissions) List() []string {
	if p.list == nil {
		return []string{}
	}
	return p.list
}

func permissionsFromContext(ctx context.Context) panelPermissions {
	if permissions, ok := ctx.Value(permissionsKey{}).(panelPermissions); ok {
		return permissions
	}
	return panelPermissions{}
}
