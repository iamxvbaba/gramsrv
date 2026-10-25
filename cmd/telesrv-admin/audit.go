package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"telesrv/internal/admin"
)

// Panel-owned commands are written straight to Postgres here rather than
// through the upstream Admin API, for the same reason operator accounts are
// (see adminusers_api.go): these are the console's own mutations, and recording
// them should work even when the domain service is down. The rows land in the
// same two tables the domain writes, so the whole audit trail is one table and
// one shape no matter which side performed the action.

// recordAgentCommand writes one finished admin_command and appends its audit
// row. It runs best-effort: the mutation it describes has already happened, so
// a storage failure must not fail the request -- but it is worth a loud log
// line, because a hole in the audit trail is how abuse goes unnoticed.
//
// The result's Details are expected not to contain credentials; the operator-
// account handlers above never put the password in them (see adminusers_api.go).
func (s *server) recordAgentCommand(r *http.Request, meta admin.CommandMeta, action, status string, result *admin.CommandResult, cmdErr error) {
	logf := log.Printf
	if s == nil || s.read == nil || s.read.pool == nil {
		// Only reachable when an admin-user route is used without a read store,
		// which the handlers refuse before this is called. Logging keeps the
		// guard honest instead of failing silently.
		logf("audit: skipping record for %s: no read store", meta.CommandID)
		return
	}
	requestJSON, _ := json.Marshal(meta)
	resultJSON := []byte("{}")
	if result != nil {
		resultJSON, _ = json.Marshal(result)
	}
	errorText := ""
	if cmdErr != nil {
		errorText = cmdErr.Error()
	}

	ctx := r.Context()
	tx, err := s.read.pool.Begin(ctx)
	if err != nil {
		logf("audit: begin tx for %s: %v", meta.CommandID, err)
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	// request::jsonb stores the CommandMeta, so the stored row carries the same
	// "provided via the signed-in operator, at which command id, with which
	// reason" envelope a domain command does. No password travels in the request
	// envelope, so none can leak into either table.
	//
	// target_user_id / target_peer_type / target_peer_id are omitted on purpose:
	// panel commands have no domain target (CommandMeta has none), and the
	// columns are NOT NULL DEFAULT 0/'' -- an explicit NULL would abort with
	// SQLSTATE 23502 on real PostgreSQL. Defaults keep the row well-formed.
	if _, err := tx.Exec(ctx, `
INSERT INTO admin_commands (
	command_id, actor, action,
	dry_run, reason, request, result, status, error, created_at
) VALUES (
	$1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, $8, $9, now()
) ON CONFLICT (command_id) DO NOTHING`,
		meta.CommandID, meta.Actor, action, meta.DryRun, meta.Reason,
		string(requestJSON), string(resultJSON), status, errorText,
	); err != nil {
		logf("audit: insert admin_command %s: %v", meta.CommandID, err)
		return
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
		logf("audit: append audit log for %s: %v", meta.CommandID, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		logf("audit: commit for %s: %v", meta.CommandID, err)
		return
	}
	committed = true
}

// handleAuditLogsAPI lists the global action trail for whoever holds audit.read.
// Filters are optional and combined: actor, action and a status of completed or
// failed, plus a limit that is capped so a spent month of commands cannot be
// dumped into the browser.
func (s *server) handleAuditLogsAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	q := r.URL.Query()
	limit, err := parseBoundedLimit(q.Get("limit"), 200)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	rows, err := s.read.listAuditLogs(r.Context(), q.Get("actor"), q.Get("action"), q.Get("status"), limit)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

// parseBoundedLimit parses an optional "limit" query value within 1..max.
func parseBoundedLimit(raw string, max int) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return max, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 {
		return 0, fmt.Errorf("limit must be a positive integer")
	}
	if n > max {
		n = max
	}
	return n, nil
}

type auditLogAPIEntry struct {
	ID         int64     `json:"id"`
	CommandID  string    `json:"command_id"`
	Actor      string    `json:"actor"`
	Action     string    `json:"action"`
	DryRun     bool      `json:"dry_run"`
	Reason     string    `json:"reason"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
	Result     string    `json:"result,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	TargetType string    `json:"target_type,omitempty"`
	TargetID   int64     `json:"target_id,omitempty"`
}

// listAuditLogs is the global counterpart of auditLogs/channelAuditLogs in
// readstore.go: it walks the whole table instead of one target's slice of it.
func (s *readStore) listAuditLogs(ctx context.Context, actor, action, status string, limit int) ([]auditLogAPIEntry, error) {
	actor = strings.TrimSpace(actor)
	action = strings.TrimSpace(action)
	status = strings.TrimSpace(status)
	rows, err := s.pool.Query(ctx, `
SELECT id, command_id, actor, action, dry_run, reason, status, error, result, created_at,
	target_peer_type, target_peer_id
FROM admin_audit_logs
WHERE ($1 = '' OR actor = $1)
  AND ($2 = '' OR action = $2)
  AND ($3 = '' OR status = $3)
ORDER BY id DESC
LIMIT $4`, actor, action, status, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit logs: %w", err)
	}
	defer rows.Close()
	out := make([]auditLogAPIEntry, 0, limit)
	for rows.Next() {
		var e auditLogAPIEntry
		var result []byte
		var targetType *string
		if err := rows.Scan(&e.ID, &e.CommandID, &e.Actor, &e.Action, &e.DryRun, &e.Reason,
			&e.Status, &e.Error, &result, &e.CreatedAt, &targetType, &e.TargetID); err != nil {
			return nil, err
		}
		if targetType != nil {
			e.TargetType = *targetType
		}
		if len(result) > 0 && string(result) != "{}" {
			e.Result = prettyJSON(result)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
