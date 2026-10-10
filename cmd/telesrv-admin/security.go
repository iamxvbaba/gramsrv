package main

import (
	"context"
	"crypto/subtle"
	"fmt"
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
// TELESRV_ADMIN_UI_PERMISSIONS and the ones the admin routes enforce.
const (
	permissionAll           = "*"
	permissionPremiumManage = "premium.manage"
	// permissionDonationsManage gates the crypto donations admin page:
	// wallet status, chain config (RPC/WS endpoint, enabled flag,
	// confirmation depth, pricing) and the deposit/transaction history.
	// Never exposes the wallet mnemonic or a private key -- there is no
	// admin route that returns either.
	permissionDonationsManage = "donations.manage"
	// permissionPricesManage gates the shop pricing page: the effective
	// product prices, the product kill switches and the stars rate the bot
	// bills star purchases at.
	permissionPricesManage       = "prices.manage"
	permissionBotTokenRead       = "bots.token.read"
	permissionVerificationReview = "verification.review"
	permissionVerificationRevoke = "verification.revoke"
	// Third-party bot verification. Deliberately not implied by the official
	// verification rights above: the two are separate mechanisms over separate
	// tables, so a session trusted with one queue is not thereby trusted with the
	// other. review reads and decides applications; manage appoints verifiers,
	// curates the icon catalogue and strips granted marks.
	permissionBotVerificationReview = "botverification.review"
	permissionBotVerificationManage = "botverification.manage"

	// permissionAdminsManage gates the operator accounts themselves: creating
	// them, editing their rights, disabling them, resetting their passwords.
	//
	// It is the one right that can grant every other right, so it is never
	// implied by anything else. guardManagerRemoval additionally refuses the
	// edit that would leave nobody holding it.
	permissionAdminsManage = "admins.manage"

	// permissionAuditRead gates the global action log. It is deliberately NOT
	// implied by the read rights of the sections it covers: seeing who did what
	// is a supervisory job, not a working-level one.
	permissionAuditRead = "audit.read"

	// Section rights, in read/manage pairs that follow the sidebar. Reading a
	// section and changing it are separate grants because most of the people
	// who need to look at this data never need to alter it.
	permissionAccountsRead     = "accounts.read"
	permissionAccountsManage   = "accounts.manage"
	permissionChannelsRead     = "channels.read"
	permissionChannelsManage   = "channels.manage"
	permissionBotsRead         = "bots.read"
	permissionBotsManage       = "bots.manage"
	permissionMessagesRead     = "messages.read"
	permissionMessagesManage   = "messages.manage"
	permissionModerationReview = "moderation.review"
	permissionBroadcastsRead   = "broadcasts.read"
	permissionBroadcastsSend   = "broadcasts.send"
	permissionStorageRead      = "storage.read"
	permissionStorageManage    = "storage.manage"
	// Sticker packs, emoji packs and the GIF catalogue: one section as far as
	// the panel is concerned, so one pair of rights.
	permissionContentRead     = "content.read"
	permissionContentManage   = "content.manage"
	permissionUsernamesRead   = "usernames.read"
	permissionUsernamesManage = "usernames.manage"
	// Collectible phone numbers are their own section with their own pair.
	permissionPhonesRead   = "phones.read"
	permissionPhonesManage = "phones.manage"
	// Star gifts, give-gifts and auctions/drops are one "gifts" section.
	permissionGiftsRead   = "gifts.read"
	permissionGiftsManage = "gifts.manage"
	// Account-ratings review and adjustment.
	permissionRatingsRead   = "ratings.read"
	permissionRatingsManage = "ratings.manage"
	// The Stars ledger is an audit surface in its own right: balances and the
	// signed per-account history. Read-only, so a session that only has to
	// investigate (or clean up after) a balance bug never needs a management
	// right to look.
	permissionStarsRead     = "stars.read"
	permissionDashboardRead = "dashboard.read"

	// permissionServerManage gates the whole Server Settings surface: reading
	// and editing the server identity (name/description/icon), the 777000
	// login-notification templates, the .env file, and the status page. One
	// right rather than a read/manage pair because the section is small and
	// every screen mixes read and write -- splitting them would just be two
	// buttons the operator has to tick to get one section working.
	permissionServerManage = "server.manage"

	// permissionSessionOnly marks the handful of routes that need a session but
	// no right: reading who you are, and signing out. It is not a grantable
	// name -- scopedRoute treats it as "authenticated is enough" -- so it can
	// never be typed into an account's permission list by mistake.
	permissionSessionOnly = ""
)

// assignablePermissions is the vocabulary the operator-accounts screen offers.
//
// The wildcard is deliberately absent: it is meaningful in
// TELESRV_ADMIN_UI_PERMISSIONS for the break-glass login, but handing "*" to a
// named account through a UI is how least privilege quietly stops being a
// thing. An operator who genuinely needs everything gets every entry ticked,
// which at least leaves a legible record of what was granted.
func assignablePermissions() []string {
	return []string{
		permissionAccountsRead,
		permissionAccountsManage,
		permissionChannelsRead,
		permissionChannelsManage,
		permissionBotsRead,
		permissionBotsManage,
		permissionMessagesRead,
		permissionMessagesManage,
		permissionModerationReview,
		permissionBroadcastsRead,
		permissionBroadcastsSend,
		permissionContentRead,
		permissionContentManage,
		permissionUsernamesRead,
		permissionUsernamesManage,
		permissionPhonesRead,
		permissionPhonesManage,
		permissionGiftsRead,
		permissionGiftsManage,
		permissionRatingsRead,
		permissionRatingsManage,
		permissionStarsRead,
		permissionStorageRead,
		permissionStorageManage,
		permissionDashboardRead,
		permissionPremiumManage,
		permissionDonationsManage,
		permissionPricesManage,
		permissionBotTokenRead,
		permissionVerificationReview,
		permissionVerificationRevoke,
		permissionBotVerificationReview,
		permissionBotVerificationManage,
		permissionAuditRead,
		permissionAdminsManage,
		permissionServerManage,
	}
}

// validatePermissions refuses anything the operator-accounts routes cannot
// grant: the wildcard (reserved for the break-glass login set in the
// environment) and names outside the assignable vocabulary. Being strict here
// is what makes a permission list stored against a named account a legible
// record of least privilege instead of a marker that silently means everything.
func validatePermissions(permissions []string) error {
	assignable := assignablePermissions()
	for _, p := range permissions {
		if p == permissionAll {
			return fmt.Errorf("%s is not assignable to a named operator account", permissionAll)
		}
		found := false
		for _, allowed := range assignable {
			if p == allowed {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unknown permission %q", p)
		}
	}
	return nil
}

// scopedRoute is the only way an API route should be registered. Requiring the
// permission as an argument is what makes the panel deny-by-default: a route
// cannot be added without someone stating which right it belongs to, so the
// failure mode of forgetting is a compile error rather than an endpoint that
// quietly answers to everyone.
//
// permissionSessionOnly is the deliberate exception, spelled out at each use.
func (s *server) scopedRoute(permission string, handler http.Handler) http.Handler {
	if permission == permissionSessionOnly {
		return s.requireAuthAPI(handler)
	}
	return s.requireAuthAPI(s.requirePermission(permission, handler))
}

// scopedRouteAll is scopedRoute for a route that needs more than one right at
// once -- taking a granted verification badge away needs both the right to work
// the queue and the separate right to revoke. Every permission must be held;
// they are requirements, not alternatives.
func (s *server) scopedRouteAll(permissions []string, handler http.Handler) http.Handler {
	if len(permissions) == 0 {
		// Refusing outright beats silently degrading to "any session": an empty
		// list here is a mistake at the call site, not a way to open a route.
		panic("scopedRouteAll: no permissions given")
	}
	wrapped := handler
	for i := len(permissions) - 1; i >= 0; i-- {
		wrapped = s.requirePermission(permissions[i], wrapped)
	}
	return s.requireAuthAPI(wrapped)
}

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
		// Rights inside the cookie are a 12-hour snapshot; the account they
		// belong to may have been disabled, demoted or had its password changed
		// since. Re-read it and use what the database says now, so revocation
		// takes effect on the next request rather than at session expiry.
		permissions, ok := s.currentSessionPermissions(r.Context(), claims)
		if !ok {
			clearSessionCookie(w)
			writeAPIError(w, http.StatusUnauthorized, "session is no longer valid")
			return
		}
		ctx := context.WithValue(r.Context(), actorKey{}, claims.Actor)
		ctx = context.WithValue(ctx, permissionsKey{}, permissions)
		ctx = context.WithValue(ctx, operatorIDKey{}, claims.UserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// currentSessionPermissions resolves the rights this request actually gets.
//
// The break-glass operator (UserID 0) has no database row and keeps the
// configured set -- that login exists precisely for when the database cannot
// be consulted, so it must not depend on one.
//
// A named account is re-read every request. Anything that moved its token
// epoch invalidates the session; anything that narrowed its permissions
// narrows this request. A read failure is treated as a refusal rather than as
// permission, so a database outage cannot silently widen access.
func (s *server) currentSessionPermissions(ctx context.Context, claims sessionClaims) (panelPermissions, bool) {
	if claims.UserID == 0 {
		return newPanelPermissions(claims.Permissions), true
	}
	if s.read == nil {
		return panelPermissions{}, false
	}
	enabled, epoch, permissions, err := s.read.AdminConsoleSessionState(ctx, claims.UserID)
	if err != nil || !enabled || epoch != claims.Epoch {
		return panelPermissions{}, false
	}
	return newPanelPermissions(permissions), true
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
