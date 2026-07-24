package postgres

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestAuthDeliveryReportPostgresIsIdempotentAndRetainedSeparately(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	createdAt := time.Unix(123_456, 0).UTC()
	report, err := domain.NewAuthDeliveryReport(
		[8]byte{1, 2, 3, 4, 5, 6, 7, byte(time.Now().UnixNano())},
		time.Now().UnixNano(), "tdesktop", "15550004444",
		"phone-code-hash", 77, "delivery-77",
		domain.AuthCodeDeliverySMS, "46000", createdAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	store := NewAuthDeliveryReportStore(pool)
	stored, created, err := store.CreateAuthDeliveryReport(ctx, report)
	if err != nil || !created || stored.ID <= 0 {
		t.Fatalf("stored=%+v created=%v err=%v", stored, created, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM auth_delivery_reports WHERE id = $1", stored.ID)
	})
	retry, created, err := store.CreateAuthDeliveryReport(ctx, report)
	if err != nil || created || retry.ID != stored.ID ||
		retry.PhoneHash != stored.PhoneHash || retry.CodeHash != stored.CodeHash {
		t.Fatalf("retry=%+v created=%v err=%v", retry, created, err)
	}
	deleted, err := store.DeleteExpiredAuthDeliveryReports(
		ctx, createdAt.Add(time.Second), 10,
	)
	if err != nil || deleted < 1 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	var moderationRows int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM moderation_reports
WHERE reporter_user_id = $1`, report.IssuedUserID).Scan(&moderationRows); err != nil {
		t.Fatal(err)
	}
	if moderationRows != 0 {
		t.Fatalf("auth diagnostic leaked into moderation reports: %d", moderationRows)
	}
}
