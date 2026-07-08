package postgres

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

func TestPasswordStoreLoginEmailUniqueCaseInsensitivePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	passwords := NewPasswordStore(pool)
	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	u1, err := users.Create(ctx, domain.User{AccessHash: 101, Phone: "+1665" + suffix + "01", FirstName: "EmailOne"})
	if err != nil {
		t.Fatalf("create user1: %v", err)
	}
	u2, err := users.Create(ctx, domain.User{AccessHash: 102, Phone: "+1665" + suffix + "02", FirstName: "EmailTwo"})
	if err != nil {
		t.Fatalf("create user2: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM account_passwords WHERE user_id = ANY($1::bigint[])", []int64{u1.ID, u2.ID})
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{u1.ID, u2.ID})
	})

	if err := passwords.Save(ctx, u1.ID, domain.PasswordSettings{LoginEmail: "Owner@Example.Test"}); err != nil {
		t.Fatalf("save user1 email: %v", err)
	}
	ownerID, found, err := passwords.LoginEmailOwner(ctx, "owner@example.test")
	if err != nil || !found || ownerID != u1.ID {
		t.Fatalf("LoginEmailOwner = id %d found %v err %v, want user1", ownerID, found, err)
	}
	if err := passwords.Save(ctx, u2.ID, domain.PasswordSettings{LoginEmail: "owner@example.test"}); !errors.Is(err, domain.ErrEmailOccupied) {
		t.Fatalf("save duplicate email err = %v, want ErrEmailOccupied", err)
	}
}
