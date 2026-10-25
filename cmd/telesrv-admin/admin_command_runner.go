package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/internal/admin"
)

// Operator-account mutations are commands like every other panel action: they
// run dry first, land in admin_commands and leave an audit row. Unlike the
// domain service they also touch the console's own account table, which the
// domain service does not own, so the mutation, the command row and the audit
// row must be one transaction here -- a crash between "password updated" and
// "audit stored" would be exactly the undetectable hole the trail exists to
// close.
//
// The runner also makes command_id strictly idempotent, which the earlier
// write-then-audit path was not: the same command_id always produces the same
// outcome, a replayed command returns the stored result with already_executed
// set, and a command_id reused for a DIFFERENT request is refused outright
// instead of silently re-running the mutation.
//
// A credential-free fingerprint binds the password into the stored request
// envelope: the password is part of what the fingerprint covers, so a reused
// command_id with a different secret disagrees on it and conflicts, while the
// raw secret still never reaches either admin table.

const (
	maxOperatorCommandIDLength = 128
	maxOperatorActorLength     = 128
	maxOperatorReasonLength    = 1000
	// managerGuardAdvisoryKey serializes last-manager guard + mutate across
	// concurrent operators so two parallel demotions cannot both count each
	// other and both proceed.
	managerGuardAdvisoryKey int64 = 0x7465_6C73_7276_3101
)

// pgxRunner is the smallest surface both *pgxpool.Pool and a pgx.Tx satisfy,
// so the store writes below run identically inside the runner's transaction
// and against the plain pool.
type pgxRunner interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// operatorCommandRequest is the request payload stored on the command. Params
// is the credential-free operation parameters; Fingerprint binds the password
// (and the rest of the request) without embedding it.
type operatorCommandRequest struct {
	CommandID   string          `json:"command_id"`
	Actor       string          `json:"actor"`
	Reason      string          `json:"reason"`
	DryRun      bool            `json:"dry_run"`
	Fingerprint string          `json:"fingerprint"`
	Params      json.RawMessage `json:"params"`
}

// operatorCommandFingerprint returns a stable hex digest over everything that
// makes an operator-account operation what it is: the action, the canonical
// parameters and the password. Two executions that are truly the same request
// share a fingerprint; anything else (a different password most of all) does
// not, so command_id can never be re-bound to a different request.
func operatorCommandFingerprint(action string, params []byte, password string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(action))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(params)
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(password))
	return hex.EncodeToString(h.Sum(nil))
}

type storedOperatorCommand struct {
	CommandID   string
	Actor       string
	Action      string
	DryRun      bool
	Reason      string
	RequestJSON []byte
	ResultJSON  []byte
	Status      string
	Error       string
}

const storedOperatorCommandColumns = `command_id, actor, action, dry_run, reason, request, result, status, error`

// runOperatorCommand pre-claims the command row for meta.CommandID, farms the
// mutation out to fn inside the same transaction, and only reports success once
// the mutation, the finished command row and the audit row have all committed
// together. A storage failure mid-way rolls the mutation back.
//
// fn receives the open transaction so it can run the guard and the mutation on
// the very connection that owns the command row. It must not commit or roll
// back the transaction itself.
func (s *server) runOperatorCommand(ctx context.Context, meta admin.CommandMeta, action string, params any, password string, fn func(context.Context, pgx.Tx) (admin.CommandResult, error)) (admin.CommandResult, error) {
	if s == nil || s.read == nil || s.read.pool == nil {
		return admin.CommandResult{}, fmt.Errorf("read store is not configured")
	}
	meta.CommandID = strings.TrimSpace(meta.CommandID)
	meta.Actor = strings.TrimSpace(meta.Actor)
	meta.Reason = strings.TrimSpace(meta.Reason)
	if meta.CommandID == "" || len(meta.CommandID) > maxOperatorCommandIDLength {
		return admin.CommandResult{}, fmt.Errorf("command_id is required and must be <= %d bytes", maxOperatorCommandIDLength)
	}
	if meta.Actor == "" || len(meta.Actor) > maxOperatorActorLength {
		return admin.CommandResult{}, fmt.Errorf("actor is required and must be <= %d bytes", maxOperatorActorLength)
	}
	if meta.Reason == "" || len(meta.Reason) > maxOperatorReasonLength {
		return admin.CommandResult{}, fmt.Errorf("reason is required and must be <= %d bytes", maxOperatorReasonLength)
	}
	// Params must be JSON-marshallable and credential-free; the caller builds
	// it without the password. The fingerprint is where the password lives.
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return admin.CommandResult{}, fmt.Errorf("marshal operator request: %w", err)
	}
	envelope := operatorCommandRequest{
		CommandID:   meta.CommandID,
		Actor:       meta.Actor,
		Reason:      meta.Reason,
		DryRun:      meta.DryRun,
		Fingerprint: operatorCommandFingerprint(action, paramsJSON, password),
		Params:      paramsJSON,
	}
	requestJSON, err := json.Marshal(envelope)
	if err != nil {
		return admin.CommandResult{}, fmt.Errorf("marshal operator request envelope: %w", err)
	}

	tx, err := s.read.pool.Begin(ctx)
	if err != nil {
		return admin.CommandResult{}, fmt.Errorf("begin operator command tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	// Pre-claim. ON CONFLICT DO NOTHING keeps the first run's row: a returning
	// conflict row would overwrite an executed outcome on replay.
	cmd, err := scanStoredOperatorCommand(tx.QueryRow(ctx, `
INSERT INTO admin_commands (command_id, actor, action, dry_run, reason, request, result, status, error, created_at)
VALUES ($1, $2, $3, $4, $5, $6::jsonb, '{}'::jsonb, 'running', '', now())
ON CONFLICT (command_id) DO NOTHING
RETURNING `+storedOperatorCommandColumns,
		meta.CommandID, meta.Actor, action, meta.DryRun, meta.Reason, string(requestJSON)))
	if errors.Is(err, pgx.ErrNoRows) {
		existing, err := scanStoredOperatorCommand(tx.QueryRow(ctx, `
SELECT `+storedOperatorCommandColumns+`
FROM admin_commands
WHERE command_id = $1`, meta.CommandID))
		if err != nil {
			return admin.CommandResult{}, fmt.Errorf("load existing operator command: %w", err)
		}
		if existing.Action != action || existing.DryRun != meta.DryRun || !sameOperatorRequest(existing.RequestJSON, requestJSON) {
			return admin.CommandResult{
				CommandID: meta.CommandID,
				Action:    action,
				Status:    "failed",
				Error:     "COMMAND_ID_CONFLICT",
				Message:   "command_id is already bound to a different request",
			}, fmt.Errorf("COMMAND_ID_CONFLICT")
		}
		return operatorCommandReplay(existing), nil
	}
	if err != nil {
		return admin.CommandResult{}, fmt.Errorf("claim operator command: %w", err)
	}
	_ = cmd

	result, opErr := fn(ctx, tx)
	result.CommandID = meta.CommandID
	result.Action = action
	result.DryRun = meta.DryRun
	status := "completed"
	if opErr != nil {
		status = "failed"
		result.Status = status
		result.Error = opErr.Error()
		if result.Message == "" {
			result.Message = "operator command failed"
		}
	} else {
		result.Status = status
	}
	resultJSON, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return result, fmt.Errorf("marshal operator result: %w", marshalErr)
	}
	errorText := ""
	if opErr != nil {
		errorText = opErr.Error()
	}

	// Finish the row and append the audit entry inside the same transaction. If
	// either fails the rollback also undoes the mutation, so a command is never
	// reported done unless the trail proves it.
	if _, err := tx.Exec(ctx, `
UPDATE admin_commands
SET status = $2, result = $3::jsonb, error = $4, completed_at = now()
WHERE command_id = $1`, meta.CommandID, status, string(resultJSON), errorText); err != nil {
		return result, fmt.Errorf("finish operator command: %w", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO admin_audit_logs (
	command_id, actor, action, target_user_id, target_peer_type, target_peer_id,
	dry_run, reason, request, result, status, error, created_at
)
SELECT command_id, actor, action, target_user_id, target_peer_type, target_peer_id,
	dry_run, reason, request, result, status, error, now()
FROM admin_commands
WHERE command_id = $1
ON CONFLICT (command_id) DO NOTHING`, meta.CommandID); err != nil {
		return result, fmt.Errorf("append operator audit log: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("commit operator command tx: %w", err)
	}
	committed = true
	return result, opErr
}

// operatorCommandReplay rebuilds the stored outcome for a replayed command_id.
// The stored request matched, so the operation has already happened and must
// not happen again: the caller gets the original result back, flagged.
func operatorCommandReplay(cmd storedOperatorCommand) admin.CommandResult {
	var result admin.CommandResult
	if len(cmd.ResultJSON) > 0 {
		if err := json.Unmarshal(cmd.ResultJSON, &result); err == nil {
			result.AlreadyExecuted = true
			return result
		}
	}
	return admin.CommandResult{
		CommandID:       cmd.CommandID,
		Action:          cmd.Action,
		Status:          cmd.Status,
		AlreadyExecuted: true,
		DryRun:          cmd.DryRun,
		Message:         "command already exists",
		Error:           cmd.Error,
	}
}

func scanStoredOperatorCommand(row pgx.Row) (storedOperatorCommand, error) {
	var cmd storedOperatorCommand
	var status string
	err := row.Scan(
		&cmd.CommandID, &cmd.Actor, &cmd.Action, &cmd.DryRun, &cmd.Reason,
		&cmd.RequestJSON, &cmd.ResultJSON, &status, &cmd.Error)
	if err != nil {
		return storedOperatorCommand{}, err
	}
	cmd.Status = status
	return cmd, nil
}

// sameOperatorRequest mirrors the domain runner's semantic request comparison:
// byte-different JSON that decodes equal (key order, whitespace) counts as the
// same request, so a replay of an executed command is recognised.
func sameOperatorRequest(a, b []byte) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return string(a) == string(b)
	}
	return reflect.DeepEqual(left, right)
}

// --- transaction-capable store writes ----------------------------------------

// createAdminConsoleUserOn is the create-operator store write. It runs against
// whatever runner is handed in, so the runner can keep it inside the command
// transaction while integration tests drive it straight off the pool.
//
// Validation runs first so a bad username or password is refused without
// touching q at all (and without spending a bcrypt budget).
func createAdminConsoleUserOn(ctx context.Context, q pgxRunner, username, password string, permissions []string, enabled bool) (AdminConsoleUser, error) {
	if err := validateAdminUsername(username); err != nil {
		return AdminConsoleUser{}, err
	}
	// authenticateLogin resolves this name to the environment credential before
	// it ever reaches the table, so a row by this name could never be logged
	// into. Refuse it rather than storing an account that silently does nothing.
	if strings.EqualFold(strings.TrimSpace(username), breakGlassUsername) {
		return AdminConsoleUser{}, errUsernameReserved
	}
	hash, err := hashAdminPassword(password)
	if err != nil {
		return AdminConsoleUser{}, err
	}
	if q == nil {
		return AdminConsoleUser{}, fmt.Errorf("read store is not configured")
	}
	permissions = normalisePermissions(permissions)

	var u AdminConsoleUser
	err = q.QueryRow(ctx, `
INSERT INTO admin_console_users (username, password_hash, permissions, enabled)
VALUES ($1, $2, $3, $4)
RETURNING `+adminConsoleUserColumns,
		strings.TrimSpace(username), hash, permissions, enabled).
		Scan(&u.ID, &u.Username, &u.Permissions, &u.Enabled, &u.TokenEpoch,
			&u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return AdminConsoleUser{}, errAdminUsernameTaken
	}
	if err != nil {
		return AdminConsoleUser{}, fmt.Errorf("create admin console user: %w", err)
	}
	if u.Permissions == nil {
		u.Permissions = []string{}
	}
	return u, nil
}

// updateAdminConsoleUserOn changes permissions and/or enabled state without
// moving token_epoch: currentSessionPermissions re-reads the row on every
// request, so the narrower grant applies from the operator's next request.
func updateAdminConsoleUserOn(ctx context.Context, q pgxRunner, id int64, permissions []string, enabled bool) (AdminConsoleUser, error) {
	if q == nil {
		return AdminConsoleUser{}, fmt.Errorf("read store is not configured")
	}
	permissions = normalisePermissions(permissions)

	var u AdminConsoleUser
	err := q.QueryRow(ctx, `
UPDATE admin_console_users
SET permissions = $2,
    enabled     = $3,
    updated_at  = now()
WHERE id = $1
RETURNING `+adminConsoleUserColumns,
		id, permissions, enabled).
		Scan(&u.ID, &u.Username, &u.Permissions, &u.Enabled, &u.TokenEpoch,
			&u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AdminConsoleUser{}, errAdminUserNotFound
	}
	if err != nil {
		return AdminConsoleUser{}, fmt.Errorf("update admin console user: %w", err)
	}
	if u.Permissions == nil {
		u.Permissions = []string{}
	}
	return u, nil
}

// setAdminConsoleUserPasswordOn replaces the hash and bumps token_epoch so a
// password change signs out whoever was using the old one.
func setAdminConsoleUserPasswordOn(ctx context.Context, q pgxRunner, id int64, password string) error {
	if q == nil {
		return fmt.Errorf("read store is not configured")
	}
	hash, err := hashAdminPassword(password)
	if err != nil {
		return err
	}
	tag, err := q.Exec(ctx, `
UPDATE admin_console_users
SET password_hash = $2, token_epoch = token_epoch + 1, updated_at = now()
WHERE id = $1`, id, hash)
	if err != nil {
		return fmt.Errorf("set admin console user password: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errAdminUserNotFound
	}
	return nil
}

// countEnabledAdminConsoleUsersWithOn counts enabled operators besides
// excludeID who hold permission, counting the '*' wildcard as holding
// everything. It backs the last-manager guard.
func countEnabledAdminConsoleUsersWithOn(ctx context.Context, q pgxRunner, permission string, excludeID int64) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `
SELECT count(*)::int FROM admin_console_users
WHERE enabled
  AND id <> $2
  AND (permissions @> ARRAY[$1]::text[] OR permissions @> ARRAY['*']::text[])`,
		permission, excludeID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count admin console users with permission: %w", err)
	}
	return n, nil
}

// guardManagerRemovalOn refuses an edit that would leave nobody able to manage
// operators. stillManages short-circuits the count for edits that keep the
// capability.
//
// A named acting session that still manages operators is included in others
// whenever it edits a different row. Therefore, if no other manager is visible
// after the advisory lock, a named actor is either editing itself or was
// demoted while its request waited for the lock. Only UserID 0, the built-in
// break-glass login with no database row, may deliberately leave zero named
// managers.
func guardManagerRemovalOn(ctx context.Context, q pgxRunner, id int64, permissions []string, enabled bool, actingID int64) error {
	stillManages := enabled && newPanelPermissions(permissions).Has(permissionAdminsManage)
	if stillManages {
		return nil
	}
	others, err := countEnabledAdminConsoleUsersWithOn(ctx, q, permissionAdminsManage, id)
	if err != nil {
		return err
	}
	if others > 0 {
		return nil
	}
	if actingID == 0 {
		return nil
	}
	return errLastManagerStanding
}

// guardManagerRemovalTx runs the guard inside the runner's transaction after
// serialising on the advisory lock, so the count and the mutation that follows
// cannot interleave with a concurrent demotion of the same last manager.
func guardManagerRemovalTx(ctx context.Context, tx pgx.Tx, id int64, permissions []string, enabled bool, actingID int64) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, managerGuardAdvisoryKey); err != nil {
		return fmt.Errorf("serialise last-manager guard: %w", err)
	}
	return guardManagerRemovalOn(ctx, tx, id, permissions, enabled, actingID)
}
