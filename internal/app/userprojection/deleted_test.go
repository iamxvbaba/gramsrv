package userprojection

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

func TestDeletedUserProjectionCannotReintroducePII(t *testing.T) {
	in := domain.User{ID: 42, AccessHash: 99, Deleted: true, Phone: "stale", FirstName: "Stale", PhotoID: 123, Contact: true}
	got, err := New().One(context.Background(), 7, in)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Deleted || got.ID != 42 || got.Phone != "" || got.FirstName != "" || got.PhotoID != 0 || got.Contact {
		t.Fatalf("deleted projection leaked PII: %+v", got)
	}
}
