package rpc

import (
	"errors"
	"testing"

	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/internal/store"
)

func TestContactErrBlocklistConflictIsRetryable(t *testing.T) {
	for name, err := range map[string]error{
		"snapshot":      store.ErrBlocklistConflict,
		"serialize":     &pgconn.PgError{Code: "40001", Message: "could not serialize access"},
		"deadlock":      &pgconn.PgError{Code: "40P01", Message: "deadlock detected"},
		"wrapped-store": store.ErrBlocklistConflict,
	} {
		t.Run(name, func(t *testing.T) {
			if name == "wrapped-store" {
				err = &wrapError{err: err}
			}
			got := contactErr(err)
			var te *tgerr.Error
			if !errors.As(got, &te) || te.Code != 400 || te.Type != "BLOCKLIST_CONFLICT" {
				t.Fatalf("contactErr(%q) = %v, want 400 BLOCKLIST_CONFLICT", err, got)
			}
		})
	}
}

func TestContactErrUnknownIsInternal(t *testing.T) {
	got := contactErr(errors.New("boom"))
	var te *tgerr.Error
	if !errors.As(got, &te) || te.Code != 500 {
		t.Fatalf("contactErr(boom) = %v, want 500", got)
	}
}

type wrapError struct{ err error }

func (w *wrapError) Error() string { return w.err.Error() }
func (w *wrapError) Unwrap() error { return w.err }