package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestClientTelemetryStoreIdempotencyRateLimitAndRetention(t *testing.T) {
	ctx := context.Background()
	store := NewClientTelemetryStore()
	now := time.Unix(1_750_000_000, 0).UTC()
	newEvent := func(subject int64, at time.Time) domain.ClientTelemetryEvent {
		event, err := domain.NewClientTelemetryEvent(
			71, domain.ClientTelemetryMessageDelivery,
			domain.Peer{Type: domain.PeerTypeUser, ID: 72},
			[]int64{subject}, map[string]any{"push": true}, at,
		)
		if err != nil {
			t.Fatal(err)
		}
		return event
	}
	first := newEvent(1, now)
	stored, created, err := store.CreateClientTelemetry(ctx, first)
	if err != nil || !created || stored.ID <= 0 {
		t.Fatalf("first=%+v created=%v err=%v", stored, created, err)
	}
	retry, created, err := store.CreateClientTelemetry(ctx, first)
	if err != nil || created || retry.ID != stored.ID {
		t.Fatalf("retry=%+v created=%v err=%v", retry, created, err)
	}
	for i := 1; i < domain.MaxClientTelemetryEventsPerHour; i++ {
		if _, created, err := store.CreateClientTelemetry(
			ctx, newEvent(int64(i+1), now),
		); err != nil || !created {
			t.Fatalf("create %d created=%v err=%v", i, created, err)
		}
	}
	if got, created, err := store.CreateClientTelemetry(ctx, first); err != nil ||
		created || got.ID != stored.ID {
		t.Fatalf("retry at limit got=%+v created=%v err=%v", got, created, err)
	}
	if _, _, err := store.CreateClientTelemetry(
		ctx, newEvent(domain.MaxClientTelemetryEventsPerHour+1, now),
	); !errors.Is(err, domain.ErrClientTelemetryRateLimited) {
		t.Fatalf("overflow err=%v", err)
	}
	deleted, err := store.DeleteExpiredClientTelemetry(
		ctx, now.Add(time.Second), domain.MaxClientTelemetryEventsPerHour+1,
	)
	if err != nil || deleted != domain.MaxClientTelemetryEventsPerHour {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	recreated, created, err := store.CreateClientTelemetry(ctx, first)
	if err != nil || !created || recreated.ID == stored.ID {
		t.Fatalf("recreated=%+v created=%v err=%v", recreated, created, err)
	}
}
