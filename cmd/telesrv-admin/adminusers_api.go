package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/admin"
)

// Operator accounts are written here rather than through callAdminAPI like the
// domain mutations are, on purpose. They are not a Telegram entity: they are
// the console's own authentication, and routing them through the domain API
// would mean the console cannot fix its own locked-out operators whenever that
// service is unreachable -- exactly when you need to. Reads already go straight
// to Postgres for the same reason, so this keeps one owner for one table.
//
// They do follow the panel's command convention: every mutation is a
// /api/actions/* route that takes a reason, runs as a dry run first and returns
// an admin.CommandResult. Granting somebody the run of the console deserves the
// same "here is what this will do, confirm it" step as freezing an account.
// Executions go through runOperatorCommand (admin_command_runner.go), which
// binds the mutation, the admin_commands row and the audit row into one
// transaction, makes command_id idempotent and serialises the last-manager
// guard, so a command cannot half-land in the trail.

// requireAdminsManage is the single gate for every operator-account route, so
// none of them can be registered without it by accident.
func (s *server) requireAdminsManage(next http.Handler) http.Handler {
	return s.scopedRoute(permissionAdminsManage, next)
}

// errAdminUsernameTaken maps the unique-index violation to something the panel
// can show, without leaking the constraint name.
var errAdminUsernameTaken = errors.New("username is already taken")

// errLastManagerStanding guards against a named operator removing the final
// enabled admins.manage grant. The break-glass login may deliberately recover
// or replace that grant, but an ordinary session must never be mistaken for it.
var errLastManagerStanding = errors.New("this would leave no enabled account able to manage operators")

// errUsernameReserved guards the break-glass name, which authentication
// resolves before the table is consulted.
var errUsernameReserved = errors.New("this username is reserved for the built-in operator")

// createAdminConsoleUser inserts a new operator. token_epoch starts at 1; there
// are no sessions to invalidate yet. Kept for the store-level callers and
// integration tests; the routed mutations execute the same write inside
// runOperatorCommand's transaction.
func (s *server) createAdminConsoleUser(ctx context.Context, username, password string, permissions []string, enabled bool) (AdminConsoleUser, error) {
	var q pgxRunner
	if s != nil && s.read != nil {
		q = s.read.pool
	}
	return createAdminConsoleUserOn(ctx, q, username, password, permissions, enabled)
}

// updateAdminConsoleUser changes permissions and/or enabled state.
//
// It deliberately does NOT move token_epoch. currentSessionPermissions re-reads
// this row on every request, so a narrowed permission set applies from the
// operator's next request and a disabled account is refused outright -- both
// without ending a session. Bumping the epoch here would only sign someone out
// mid-task to achieve what the re-read already achieves.
func (s *server) updateAdminConsoleUser(ctx context.Context, id int64, permissions []string, enabled bool) (AdminConsoleUser, error) {
	var q pgxRunner
	if s != nil && s.read != nil {
		q = s.read.pool
	}
	return updateAdminConsoleUserOn(ctx, q, id, permissions, enabled)
}

// setAdminConsoleUserPassword replaces the hash and bumps the epoch, so a
// password change signs out whoever was using the old one -- which is the
// point of changing it after a suspected compromise.
func (s *server) setAdminConsoleUserPassword(ctx context.Context, id int64, password string) error {
	var q pgxRunner
	if s != nil && s.read != nil {
		q = s.read.pool
	}
	return setAdminConsoleUserPasswordOn(ctx, q, id, password)
}

// normalisePermissions trims, de-duplicates and collapses to the wildcard when
// it is present, so "*" plus a list cannot be stored as something that reads
// narrower than it is. The wildcard itself is refused by validatePermissions
// for named accounts; this keeps legacy rows and the break-glass path working.
func normalisePermissions(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if p == permissionAll {
			return []string{permissionAll}
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// --- HTTP surface -----------------------------------------------------------

// adminUserActionRequest carries the panel's usual command envelope alongside
// the operator fields. ID is absent when creating.
type adminUserActionRequest struct {
	CommandID   string   `json:"command_id"`
	Reason      string   `json:"reason"`
	Confirm     bool     `json:"confirm"`
	ID          int64    `json:"id"`
	Username    string   `json:"username"`
	Password    string   `json:"password"`
	Permissions []string `json:"permissions"`
	Enabled     *bool    `json:"enabled"`
}

func (s *server) handleListAdminUsersAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	users, err := s.read.ListAdminConsoleUsers(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		// The built-in operator has no database row, so it would otherwise be
		// invisible here -- a list of who can sign in that omits the account
		// with the most rights is worse than no list. It is reported first and
		// flagged as system; the panel renders it read-only, and every mutation
		// below refuses it anyway.
		"system": map[string]any{
			"username":    breakGlassUsername,
			"permissions": newPanelPermissions(s.cfg.Permissions).List(),
			"enabled":     true,
			"system":      true,
		},
		"rows": users,
		// The vocabulary the panel offers when editing an account, so the list
		// of assignable rights lives in one place instead of being duplicated
		// in the frontend and drifting from what the routes actually check.
		"available_permissions": assignablePermissions(),
	})
}

// handleCreateAdminUserAPI runs as a dry run unless confirmed.
func (s *server) handleCreateAdminUserAPI(w http.ResponseWriter, r *http.Request) {
	var body adminUserActionRequest
	if !s.decodeAdminUserAction(w, r, &body) {
		return
	}
	meta := s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "admin-operator-create")
	const action = "create-admin-operator"
	enabled := body.Enabled == nil || *body.Enabled
	permissions := normalisePermissions(body.Permissions)
	username := strings.TrimSpace(body.Username)
	// Credential-free params: the password is bound into the request envelope
	// only as the fingerprint computed by runOperatorCommand.
	params := map[string]any{"username": username, "permissions": permissions, "enabled": enabled}

	fn := func(ctx context.Context, tx pgx.Tx) (admin.CommandResult, error) {
		// Validate on the dry run too, so "this will fail" is discovered before
		// the operator is asked to confirm rather than after.
		if err := validateAdminUsername(username); err != nil {
			return admin.CommandResult{}, err
		}
		if strings.EqualFold(username, breakGlassUsername) {
			return admin.CommandResult{}, errUsernameReserved
		}
		if err := validateAdminPassword(body.Password); err != nil {
			return admin.CommandResult{}, err
		}
		if err := validatePermissions(permissions); err != nil {
			return admin.CommandResult{}, err
		}
		if meta.DryRun {
			return admin.CommandResult{
				Status: "ok",
				DryRun: true,
				Message: fmt.Sprintf("Would create operator %q with %d permission(s), %s.",
					username, len(permissions), enabledWord(enabled)),
				Details: params,
			}, nil
		}
		user, err := createAdminConsoleUserOn(ctx, tx, username, body.Password, permissions, enabled)
		if err != nil {
			return admin.CommandResult{}, err
		}
		return admin.CommandResult{
			Status:  "ok",
			Message: fmt.Sprintf("Created operator %q.", user.Username),
			Details: map[string]any{"id": user.ID, "username": user.Username, "permissions": user.Permissions, "enabled": user.Enabled},
		}, nil
	}

	result, err := s.runOperatorCommand(r.Context(), meta, action, params, body.Password, fn)
	writeCommandResultAPI(w, result, err)
}

// handleUpdateAdminUserAPI changes rights and/or enabled state, dry run first.
func (s *server) handleUpdateAdminUserAPI(w http.ResponseWriter, r *http.Request) {
	var body adminUserActionRequest
	if !s.decodeAdminUserAction(w, r, &body) {
		return
	}
	meta := s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "admin-operator-access")
	const action = "set-admin-operator-access"
	enabled := body.Enabled == nil || *body.Enabled
	permissions := normalisePermissions(body.Permissions)
	// Who is running this edit. 0 is the break-glass login, which has no row
	// and therefore cannot be the account being edited -- an important
	// distinction for the last-manager guard below.
	actingID := operatorIDFromContext(r.Context())
	params := map[string]any{"id": body.ID, "permissions": permissions, "enabled": enabled}

	fn := func(ctx context.Context, tx pgx.Tx) (admin.CommandResult, error) {
		if body.ID <= 0 {
			return admin.CommandResult{}, errAdminUserNotFound
		}
		if err := validatePermissions(permissions); err != nil {
			return admin.CommandResult{}, err
		}
		// The guard runs for the dry run too, and inside the command transaction
		// with the advisory lock when confirmed. Passing the identity, rather
		// than only whether this is a self-edit, keeps a named request that was
		// demoted while waiting on the lock distinct from break-glass.
		if err := guardManagerRemovalTx(ctx, tx, body.ID, permissions, enabled, actingID); err != nil {
			return admin.CommandResult{}, err
		}
		if meta.DryRun {
			return admin.CommandResult{
				Status: "ok",
				DryRun: true,
				Message: fmt.Sprintf("Would set operator #%d to %d permission(s), %s. Takes effect on their next request.",
					body.ID, len(permissions), enabledWord(enabled)),
				Details: params,
			}, nil
		}
		user, err := updateAdminConsoleUserOn(ctx, tx, body.ID, permissions, enabled)
		if err != nil {
			return admin.CommandResult{}, err
		}
		return admin.CommandResult{
			Status:  "ok",
			Message: fmt.Sprintf("Updated %q. The new access applies from their next request.", user.Username),
			Details: map[string]any{"id": user.ID, "username": user.Username, "permissions": user.Permissions, "enabled": user.Enabled},
		}, nil
	}

	result, err := s.runOperatorCommand(r.Context(), meta, action, params, body.Password, fn)
	writeCommandResultAPI(w, result, err)
}

// handleSetAdminUserPasswordAPI resets a password, dry run first.
func (s *server) handleSetAdminUserPasswordAPI(w http.ResponseWriter, r *http.Request) {
	var body adminUserActionRequest
	if !s.decodeAdminUserAction(w, r, &body) {
		return
	}
	meta := s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "admin-operator-password")
	const action = "set-admin-operator-password"
	params := map[string]any{"id": body.ID}

	fn := func(ctx context.Context, tx pgx.Tx) (admin.CommandResult, error) {
		if body.ID <= 0 {
			return admin.CommandResult{}, errAdminUserNotFound
		}
		if err := validateAdminPassword(body.Password); err != nil {
			return admin.CommandResult{}, err
		}
		if meta.DryRun {
			return admin.CommandResult{
				Status:  "ok",
				DryRun:  true,
				Message: fmt.Sprintf("Would set a new password for operator #%d. Their existing sessions would be signed out.", body.ID),
				// The password itself is never echoed, not even back to the
				// operator who just typed it.
				Details: params,
			}, nil
		}
		if err := setAdminConsoleUserPasswordOn(ctx, tx, body.ID, body.Password); err != nil {
			return admin.CommandResult{}, err
		}
		return admin.CommandResult{
			Status:  "ok",
			Message: fmt.Sprintf("Password changed for operator #%d. Their existing sessions are signed out.", body.ID),
			Details: params,
		}, nil
	}

	result, err := s.runOperatorCommand(r.Context(), meta, action, params, body.Password, fn)
	writeCommandResultAPI(w, result, err)
}

// decodeAdminUserAction shares the store check, body decode and reason
// requirement across the three mutations.
func (s *server) decodeAdminUserAction(w http.ResponseWriter, r *http.Request, body *adminUserActionRequest) bool {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return false
	}
	if err := decodeJSON(r, body); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return false
	}
	if strings.TrimSpace(body.Reason) == "" {
		writeAPIError(w, http.StatusBadRequest, "a reason is required")
		return false
	}
	return true
}

func enabledWord(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

// guardManagerRemoval is the pool-level last-manager fence kept for the
// store-level callers and integration tests. The routed mutations run the
// transaction-shaped guardManagerRemovalTx instead (admin_command_runner.go),
// which serialises the count-and-edit under the advisory lock. actingID is 0
// only for the built-in break-glass login.
func (s *server) guardManagerRemoval(ctx context.Context, id int64, permissions []string, enabled bool, actingID int64) error {
	var q pgxRunner
	if s != nil && s.read != nil {
		q = s.read.pool
	}
	if q == nil {
		return fmt.Errorf("read store is not configured")
	}
	return guardManagerRemovalOn(ctx, q, id, permissions, enabled, actingID)
}
