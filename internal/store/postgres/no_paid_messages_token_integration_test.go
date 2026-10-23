package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestNoPaidMessagesTokenStorePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	owner, err := NewUserStore(pool).Create(ctx, domain.User{AccessHash: 61, Phone: "+1998" + suffix + "01", FirstName: "FreeLinkOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", owner.ID) })
	s := NewNoPaidMessagesTokenStore(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)

	perm, err := s.ExportNoPaidMessagesToken(ctx, owner.ID, false, false, "perm-token-"+suffix+"-aaaa", now)
	if err != nil {
		t.Fatal(err)
	}
	if perm.Temporary || !perm.ExpiresAt.IsZero() || perm.OwnerUserID != owner.ID {
		t.Fatalf("permanent = %+v", perm)
	}
	again, err := s.ExportNoPaidMessagesToken(ctx, owner.ID, false, false, "unused-candidate-"+suffix, now)
	if err != nil || again.Token != perm.Token {
		t.Fatalf("re-export = %+v, %v; want active token", again, err)
	}
	temp, err := s.ExportNoPaidMessagesToken(ctx, owner.ID, true, false, "temp-token-"+suffix+"-bbbb", now)
	if err != nil {
		t.Fatal(err)
	}
	if !temp.Temporary || !temp.ExpiresAt.Equal(now.Add(domain.NoPaidMessagesTokenTemporaryTTL)) {
		t.Fatalf("temporary = %+v", temp)
	}

	if got, ok, err := s.ResolveNoPaidMessagesToken(ctx, owner.ID, perm.Token, now); err != nil || !ok || got.Token != perm.Token {
		t.Fatalf("resolve permanent = %+v, %v, %v", got, ok, err)
	}
	if _, ok, err := s.ResolveNoPaidMessagesToken(ctx, owner.ID+1, perm.Token, now); err != nil || ok {
		t.Fatalf("resolve for another owner = %v, %v", ok, err)
	}
	if _, ok, _ := s.ResolveNoPaidMessagesToken(ctx, owner.ID, temp.Token, now.Add(domain.NoPaidMessagesTokenTemporaryTTL)); ok {
		t.Fatal("expired temporary token resolves")
	}

	// An expired temporary token is replaced on the next export.
	later := now.Add(domain.NoPaidMessagesTokenTemporaryTTL + time.Minute)
	fresh, err := s.ExportNoPaidMessagesToken(ctx, owner.ID, true, false, "temp-token-"+suffix+"-cccc", later)
	if err != nil || fresh.Token == temp.Token {
		t.Fatalf("export after expiry = %+v, %v", fresh, err)
	}

	revoked, err := s.ExportNoPaidMessagesToken(ctx, owner.ID, false, true, "perm-token-"+suffix+"-dddd", now)
	if err != nil || revoked.Token == perm.Token {
		t.Fatalf("revoke = %+v, %v", revoked, err)
	}
	if _, ok, _ := s.ResolveNoPaidMessagesToken(ctx, owner.ID, perm.Token, now); ok {
		t.Fatal("revoked token resolves")
	}

	// Concurrent revokes keep exactly one active permanent token.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = s.ExportNoPaidMessagesToken(ctx, owner.ID, false, true, "race-"+suffix+"-"+string(rune('a'+i))+"-token", now)
		}(i)
	}
	wg.Wait()
	var active int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM no_paid_messages_tokens WHERE owner_user_id=$1 AND NOT temporary AND revoked_at IS NULL`, owner.ID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("active permanent tokens = %d, want 1", active)
	}
}
