package adminapi

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
)

// Admin API authorisation.
//
// Every request arrives with a bearer token, and the token decides which
// permissions the request carries:
//
//   - TELESRV_ADMIN_API_TOKEN is the master token and carries every permission.
//     This is what keeps the existing surface working unchanged: all the routes
//     that predate permissions stay mounted through authenticated(), which is
//     defined as "requires every permission", so the master token reaches them
//     exactly as before.
//   - A scoped token from TELESRV_ADMIN_SCOPED_TOKENS carries only the
//     permissions its entry lists. A scoped token is therefore *not* a weaker
//     master token: it authenticates successfully and is then refused with 403 on
//     anything outside its list, including every legacy route. Widening a scoped
//     token to the legacy surface would be a silent privilege escalation, so the
//     wildcard has to be spelled out in configuration to get it.
//
// The two-step answer matters for diagnosis: 401 means "I do not know this
// token", 403 means "I know you and you may not do this".

// Permission names. They are the same strings the operator writes into
// TELESRV_ADMIN_UI_PERMISSIONS / TELESRV_ADMIN_SCOPED_TOKENS.
const (
	// PermissionAll is the wildcard: a principal carrying it passes every check.
	PermissionAll = "*"
	// PermissionVerificationReview guards the whole official-verification review
	// surface: the queue, one application, the counters, and the claim/approve/
	// reject decisions.
	PermissionVerificationReview = "verification.review"
	// PermissionVerificationRevoke is required *in addition* to
	// PermissionVerificationReview to clear a badge that was already granted.
	// Taking a badge away is visible to every client of a public peer, so it is
	// deliberately not implied by the right to review new applications.
	PermissionVerificationRevoke = "verification.revoke"
)

// CodeForbidden is the stable code for a permission failure, so the panel can
// tell an authorisation refusal apart from a domain refusal.
const CodeForbidden = "FORBIDDEN"

// ScopedToken is one bearer token restricted to a permission set. It mirrors
// config.AdminScopedToken; the adminapi package keeps its own shape so it does
// not depend on the configuration loader.
type ScopedToken struct {
	// Name is the audit identity of actions performed with this token.
	Name        string
	Token       string
	Permissions []string
}

// permissionSet is a resolved permission list.
type permissionSet struct {
	all   bool
	names map[string]struct{}
}

func newPermissionSet(permissions []string) permissionSet {
	set := permissionSet{names: make(map[string]struct{}, len(permissions))}
	for _, permission := range permissions {
		permission = strings.TrimSpace(permission)
		if permission == "" {
			continue
		}
		if permission == PermissionAll {
			set.all = true
			continue
		}
		set.names[permission] = struct{}{}
	}
	return set
}

// Has reports whether the set grants the permission.
func (p permissionSet) Has(permission string) bool {
	if p.all {
		return true
	}
	_, ok := p.names[permission]
	return ok
}

// principal is the authenticated caller.
type principal struct {
	// name is the scoped token's audit identity, or "" for the master token,
	// whose actions are attributed by the actor the caller states in the body.
	name        string
	permissions permissionSet
}

type principalKey struct{}

// principalName returns the scoped-token identity behind the request, or "" when
// the request came in on the master token.
func principalName(ctx context.Context) string {
	if p, ok := ctx.Value(principalKey{}).(principal); ok {
		return p.name
	}
	return ""
}

// principalFor resolves the bearer token to a principal.
//
// Every configured token is compared, and every comparison is constant time and
// unconditional: returning as soon as one matches would leak, through timing,
// which token position a guess collided with.
func (s *Server) principalFor(r *http.Request) (principal, bool) {
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if got == "" {
		return principal{}, false
	}
	matched := false
	resolved := principal{}
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1 && s.token != "" {
		matched = true
		resolved = principal{permissions: permissionSet{all: true}}
	}
	for _, scoped := range s.scoped {
		if subtle.ConstantTimeCompare([]byte(got), []byte(scoped.Token)) == 1 && scoped.Token != "" && !matched {
			matched = true
			resolved = principal{name: scoped.Name, permissions: newPermissionSet(scoped.Permissions)}
		}
	}
	return resolved, matched
}

// authenticated guards a route that requires unrestricted rights.
//
// This is every route that predates the permission model. Keeping them here is
// the documented behaviour: the master token carries every permission, so nothing
// about the existing surface changes, while a bounded scoped token cannot use one
// of them as a side door.
func (s *Server) authenticated(next http.HandlerFunc) http.HandlerFunc {
	return s.authorized(PermissionAll, next)
}

// authorized guards a route behind one permission.
func (s *Server) authorized(permission string, next http.HandlerFunc) http.HandlerFunc {
	return s.authorizedAll([]string{permission}, next)
}

// authorizedAll guards a route behind every listed permission. Revocation uses it
// to require the review right and the revoke right together, so the revoke right
// alone cannot be handed out as a way into the review surface.
func (s *Server) authorizedAll(permissions []string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller, ok := s.principalFor(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		for _, permission := range permissions {
			if !caller.permissions.Has(permission) {
				writeForbidden(w, permission)
				return
			}
		}
		next(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, caller)))
	}
}

// writeForbidden names the missing permission, so an operator configuring a
// scoped token is told what to add instead of having to guess.
func writeForbidden(w http.ResponseWriter, permission string) {
	writeJSON(w, http.StatusForbidden, map[string]string{
		"error":      "permission " + permission + " is required",
		"code":       CodeForbidden,
		"permission": permission,
	})
}
