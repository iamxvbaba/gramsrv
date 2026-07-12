package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

func TestTranslationSettingsOwnerPeerScopedRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner := createTestUser(t, ctx, users, "+1980"+suffix+"01", "Translate", "Owner")
	other := createTestUser(t, ctx, users, "+1980"+suffix+"02", "Translate", "Other")
	peerUser := createTestUser(t, ctx, users, "+1980"+suffix+"03", "Translate", "Peer")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, other.ID, peerUser.ID})
	})
	store := NewDialogStore(pool)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: peerUser.ID}

	if changed, err := store.SetTranslationDisabled(ctx, owner.ID, peer, true); err != nil || !changed {
		t.Fatalf("disable = %v/%v", changed, err)
	}
	if changed, err := store.SetTranslationDisabled(ctx, owner.ID, peer, true); err != nil || changed {
		t.Fatalf("duplicate disable = %v/%v", changed, err)
	}
	if disabled, err := store.TranslationDisabled(ctx, owner.ID, peer); err != nil || !disabled {
		t.Fatalf("owner disabled = %v/%v", disabled, err)
	}
	if disabled, err := store.TranslationDisabled(ctx, other.ID, peer); err != nil || disabled {
		t.Fatalf("other disabled = %v/%v", disabled, err)
	}
	if changed, err := store.SetTranslationDisabled(ctx, owner.ID, peer, false); err != nil || !changed {
		t.Fatalf("enable = %v/%v", changed, err)
	}
	if disabled, err := store.TranslationDisabled(ctx, owner.ID, peer); err != nil || disabled {
		t.Fatalf("enabled read = %v/%v", disabled, err)
	}
}
