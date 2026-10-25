package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestTmpPasswordStoreFollowsAuthorizationPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	userID := createRevokeTestUser(t, ctx, pool, "tmp-password")
	otherUserID := createRevokeTestUser(t, ctx, pool, "tmp-password-other")
	keys := NewAuthKeyStore(pool)
	auths := NewAuthorizationStore(pool)
	grants := NewTmpPasswordStore(pool)
	perm := revokeTestAuthKeyID(0xc1)
	pending := revokeTestAuthKeyID(0xc2)
	unbound := revokeTestAuthKeyID(0xc3)
	for _, key := range [][8]byte{perm, pending, unbound} {
		saveRevokeTestAuthKey(t, ctx, keys, key, 0)
	}
	bind := func(key [8]byte, user int64, passwordPending bool) domain.Authorization {
		t.Helper()
		if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: key, UserID: user, PasswordPending: passwordPending}); err != nil {
			t.Fatalf("bind %x: %v", key, err)
		}
		a, found, err := auths.ByAuthKey(ctx, key)
		if err != nil || !found {
			t.Fatalf("load authorization %x found=%v err=%v", key, found, err)
		}
		return a
	}
	login := bind(perm, userID, false)
	pendingLogin := bind(pending, userID, true)
	snapshot, found, err := NewAccountLifecycleStore(pool, testAllocatorsFor(pool).boxIDs).AccountDeletionSnapshot(ctx, userID)
	if err != nil || !found {
		t.Fatalf("password snapshot found=%v err=%v", found, err)
	}
	grant := domain.TmpPasswordGrant{
		AuthKeyID: perm, UserID: userID, AuthorizationCreatedAt: login.CreatedAt,
		PasswordChangedAt: snapshot.PasswordUpdatedAt, SecretHash: sha256.Sum256([]byte("first")),
		ValidUntil: time.Unix(time.Now().Unix()+600, 0).UTC(),
	}
	if err := grants.PutTmpPassword(ctx, grant); err != nil {
		t.Fatalf("put grant: %v", err)
	}
	// The version tokens must survive the round trip exactly; CheckTmpPassword
	// compares them with Equal against the live rows.
	got, found, err := grants.TmpPasswordByAuthKey(ctx, perm)
	if err != nil || !found || got.UserID != userID || got.SecretHash != grant.SecretHash ||
		!got.AuthorizationCreatedAt.Equal(login.CreatedAt) || !got.PasswordChangedAt.Equal(snapshot.PasswordUpdatedAt) ||
		!got.ValidUntil.Equal(grant.ValidUntil) {
		t.Fatalf("grant = %+v found=%v err=%v, want %+v", got, found, err, grant)
	}

	replacement := grant
	replacement.SecretHash = sha256.Sum256([]byte("second"))
	if err := grants.PutTmpPassword(ctx, replacement); err != nil {
		t.Fatalf("replace grant: %v", err)
	}
	if got, _, _ := grants.TmpPasswordByAuthKey(ctx, perm); got.SecretHash != replacement.SecretHash {
		t.Fatal("re-issuing did not replace the authorization's grant")
	}
	assertRevokeTestTableCount(t, ctx, pool, "account_tmp_passwords", "auth_key_id", authKeyIDToInt64(perm), 1)

	stale := []struct {
		name  string
		grant domain.TmpPasswordGrant
	}{
		{"older login", withGrant(grant, func(g *domain.TmpPasswordGrant) { g.AuthorizationCreatedAt = login.CreatedAt.Add(-time.Second) })},
		{"other user", withGrant(grant, func(g *domain.TmpPasswordGrant) { g.UserID = otherUserID })},
		{"password pending", withGrant(grant, func(g *domain.TmpPasswordGrant) {
			g.AuthKeyID, g.AuthorizationCreatedAt = pending, pendingLogin.CreatedAt
		})},
		{"no authorization", withGrant(grant, func(g *domain.TmpPasswordGrant) { g.AuthKeyID = unbound })},
	}
	for _, c := range stale {
		if err := grants.PutTmpPassword(ctx, c.grant); !errors.Is(err, store.ErrAuthorizationStateChanged) {
			t.Fatalf("%s: put err = %v, want ErrAuthorizationStateChanged", c.name, err)
		}
	}
	if got, _, _ := grants.TmpPasswordByAuthKey(ctx, perm); got.SecretHash != replacement.SecretHash {
		t.Fatal("a rejected put changed the live grant")
	}
	assertRevokeTestTableCount(t, ctx, pool, "account_tmp_passwords", "auth_key_id", authKeyIDToInt64(pending), 0)
	assertRevokeTestTableCount(t, ctx, pool, "account_tmp_passwords", "auth_key_id", authKeyIDToInt64(unbound), 0)

	// A re-login moves the login version, so the stored grant no longer
	// matches the live authorization even though its row is still present.
	relogin := bind(perm, userID, false)
	if relogin.CreatedAt.Equal(login.CreatedAt) {
		t.Fatal("re-login kept authorizations.created_at")
	}
	if got, found, _ := grants.TmpPasswordByAuthKey(ctx, perm); !found || got.AuthorizationCreatedAt.Equal(relogin.CreatedAt) {
		t.Fatalf("grant after re-login = %+v found=%v, want the old login version", got, found)
	}

	if err := auths.Delete(ctx, perm); err != nil {
		t.Fatalf("delete authorization: %v", err)
	}
	if _, found, err := grants.TmpPasswordByAuthKey(ctx, perm); err != nil || found {
		t.Fatalf("grant after logout found=%v err=%v, want cascaded away", found, err)
	}
}

func withGrant(grant domain.TmpPasswordGrant, change func(*domain.TmpPasswordGrant)) domain.TmpPasswordGrant {
	change(&grant)
	return grant
}
