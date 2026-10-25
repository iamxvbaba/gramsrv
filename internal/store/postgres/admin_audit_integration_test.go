package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// ListRecentCommands reads the append-only admin_audit_logs journal; every
// snapshot arrives no earlier than its command's creation time and carries no
// completed_at column, so the listing must surface a nil completion timestamp
// instead of failing on a column that does not exist there.
func TestAdminAuditLogListingRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	store := NewAdminStore(tx)
	inserted, ok, err := store.BeginCommand(ctx, domain.AdminCommand{
		CommandID:    "audit-list-test",
		Actor:        "777",
		Action:       "mint_username",
		TargetUserID: 1999999881,
		DryRun:       true,
		Reason:       "review grants",
		RequestJSON:  []byte(`{"username":"audit"}`),
		Status:       domain.AdminCommandRunning,
		TargetPeer:   domain.Peer{Type: domain.PeerTypeUser},
	})
	if err != nil || !ok {
		t.Fatalf("BeginCommand ok=%v err=%v", ok, err)
	}
	_ = inserted
	finished, err := store.FinishCommand(ctx, "audit-list-test", domain.AdminCommandFailed, []byte(`{}`), "peer limit reached")
	if err != nil {
		t.Fatalf("FinishCommand: %v", err)
	}
	if finished.Status != domain.AdminCommandFailed || finished.CompletedAt == nil {
		t.Fatalf("admin_commands row finished = %+v, want failed with CompletedAt", finished)
	}

	audited, err := store.ListRecentCommands(ctx, 50, "777")
	if err != nil {
		t.Fatalf("ListRecentCommands: %v", err)
	}
	if len(audited) != 1 {
		t.Fatalf("ListRecentCommands = %d rows, want the single audit log", len(audited))
	}
	row := audited[0]
	if row.CommandID != "audit-list-test" || row.Action != "mint_username" ||
		row.Actor != "777" || row.TargetUserID != 1999999881 || !row.DryRun ||
		row.Status != domain.AdminCommandFailed || row.Reason != "review grants" || row.Error != "peer limit reached" {
		t.Fatalf("audit row = %+v", row)
	}
	if row.CompletedAt != nil {
		t.Fatalf("audit log row carries a completed_at it cannot know: %+v", row)
	}
	if len(row.ResultJSON) == 0 || len(row.RequestJSON) == 0 {
		t.Fatalf("audit row lost request/result payloads: %+v", row)
	}

	other, err := store.ListRecentCommands(ctx, 50, "nobody")
	if err != nil {
		t.Fatalf("ListRecentCommands other actor: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("ListRecentCommands actor filter leaked %d rows", len(other))
	}
}
