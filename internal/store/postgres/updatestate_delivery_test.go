package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/internal/domain"
)

type captureUpdateStateDB struct {
	sql  string
	args []any
}

func (d *captureUpdateStateDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	d.sql = sql
	d.args = append([]any(nil), args...)
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (*captureUpdateStateDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("unexpected Query")
}

func (*captureUpdateStateDB) QueryRow(context.Context, string, ...any) pgx.Row {
	panic("unexpected QueryRow")
}

func TestCommitDeliveredStateUsesOneAtomicBaselineUpsert(t *testing.T) {
	db := &captureUpdateStateDB{}
	store := NewUpdateStateStore(db)
	state := domain.UpdateState{Pts: 9, Qts: 2, Date: 90, Seq: 3}
	if err := store.CommitDeliveredState(context.Background(), [8]byte{4}, 1004, state, domain.UpdateStateCommitDeliveredAndObservedBaseline); err != nil {
		t.Fatalf("commit baseline: %v", err)
	}
	if len(db.args) != 7 || db.args[6] != true {
		t.Fatalf("baseline args = %#v, want final true mode", db.args)
	}
	for _, fragment := range []string{
		"pts = GREATEST(update_states.pts, EXCLUDED.pts)",
		"WHEN $7 THEN GREATEST(update_states.observed_pts, EXCLUDED.observed_pts)",
	} {
		if !strings.Contains(db.sql, fragment) {
			t.Fatalf("atomic commit SQL missing %q:\n%s", fragment, db.sql)
		}
	}
}

func TestCommitDeliveredOnlyLeavesObservedUntouched(t *testing.T) {
	db := &captureUpdateStateDB{}
	store := NewUpdateStateStore(db)
	if err := store.CommitDeliveredState(context.Background(), [8]byte{5}, 1005, domain.UpdateState{Pts: 7}, domain.UpdateStateCommitDeliveredOnly); err != nil {
		t.Fatalf("commit delivered-only: %v", err)
	}
	if len(db.args) != 7 || db.args[6] != false {
		t.Fatalf("delivered-only args = %#v, want final false mode", db.args)
	}
	if !strings.Contains(db.sql, "ELSE update_states.observed_pts") {
		t.Fatalf("delivered-only SQL can overwrite observed:\n%s", db.sql)
	}
}

func TestObserveClientStateDoesNotFabricateConfirmedCursor(t *testing.T) {
	db := &captureUpdateStateDB{}
	store := NewUpdateStateStore(db)
	if err := store.ObserveClientState(context.Background(), [8]byte{6}, 1006, domain.UpdateState{Pts: 11, Qts: 4, Date: 110, Seq: 2}); err != nil {
		t.Fatalf("observe request: %v", err)
	}
	if !strings.Contains(db.sql, "VALUES ($1, $2, 0, 0, 0, 0, $3)") {
		t.Fatalf("observed-only insert fabricated confirmed values:\n%s", db.sql)
	}
	if len(db.args) != 3 || db.args[2] != 11 {
		t.Fatalf("observed-only args = %#v, want auth/user/pts", db.args)
	}
}
