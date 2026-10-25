package account

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

const tmpPasswordTestPassword = "correct horse"

type tmpPasswordFixture struct {
	svc       *Service
	passwords *memory.PasswordStore
	auths     *memory.AuthorizationStore
	grants    *memory.TmpPasswordStore
	lifecycle *fakeAccountLifecycleStore
	userID    int64
	authKey   [8]byte
	now       time.Time
}

func newTmpPasswordFixture(t *testing.T) *tmpPasswordFixture {
	t.Helper()
	ctx := context.Background()
	f := &tmpPasswordFixture{
		passwords: memory.NewPasswordStore(),
		auths:     memory.NewAuthorizationStore(),
		grants:    memory.NewTmpPasswordStore(),
		userID:    42,
		authKey:   [8]byte{7, 7, 7},
		now:       time.Unix(1_800_000_000, 0).UTC(),
	}
	f.lifecycle = &fakeAccountLifecycleStore{snapshot: domain.AccountDeletionSnapshot{
		User:        domain.User{ID: f.userID},
		HasPassword: true, PasswordUpdatedAt: f.now.Add(-48 * time.Hour),
	}}
	settings := defaultPasswordSettings()
	algo := settings.NewAlgo
	algo.Salt1 = append(append([]byte(nil), algo.Salt1...), bytes.Repeat([]byte{0x5A}, 32)...)
	settings.HasPassword = true
	settings.CurrentAlgo = &algo
	settings.SRPVerifier = verifierForPassword(algo, []byte(tmpPasswordTestPassword))
	if err := f.passwords.Save(ctx, f.userID, settings); err != nil {
		t.Fatalf("save password: %v", err)
	}
	if err := f.auths.Bind(ctx, domain.Authorization{AuthKeyID: f.authKey, UserID: f.userID}); err != nil {
		t.Fatalf("bind authorization: %v", err)
	}
	f.svc = NewService(f.passwords, WithAccountLifecycle(f.lifecycle), WithTmpPasswords(f.grants, f.auths))
	return f
}

func (f *tmpPasswordFixture) proof(t *testing.T, password string) domain.PasswordCheck {
	t.Helper()
	challenge, err := f.svc.GetPassword(context.Background(), f.userID)
	if err != nil {
		t.Fatalf("GetPassword: %v", err)
	}
	return clientPasswordCheck(t, challenge, []byte(password))
}

func (f *tmpPasswordFixture) issue(t *testing.T, period int) domain.TmpPassword {
	t.Helper()
	tmp, err := f.svc.IssueTmpPassword(context.Background(), f.userID, f.authKey, f.proof(t, tmpPasswordTestPassword), period, f.now)
	if err != nil {
		t.Fatalf("IssueTmpPassword(period=%d): %v", period, err)
	}
	return tmp
}

func (f *tmpPasswordFixture) check(secret []byte, at time.Time) error {
	return f.svc.CheckTmpPassword(context.Background(), f.userID, f.authKey, secret, at)
}

func TestTmpPasswordIssuedSecretIsValidUntilItsDeadline(t *testing.T) {
	f := newTmpPasswordFixture(t)
	tmp := f.issue(t, 600)
	if len(tmp.Secret) != tmpPasswordSecretSize || !tmp.ValidUntil.Equal(f.now.Add(600*time.Second)) {
		t.Fatalf("issued = %d-byte secret valid until %v, want %d bytes until %v",
			len(tmp.Secret), tmp.ValidUntil, tmpPasswordSecretSize, f.now.Add(600*time.Second))
	}
	grant, found, err := f.grants.TmpPasswordByAuthKey(context.Background(), f.authKey)
	if err != nil || !found || grant.SecretHash != sha256.Sum256(tmp.Secret) || grant.UserID != f.userID ||
		!grant.PasswordChangedAt.Equal(f.lifecycle.snapshot.PasswordUpdatedAt) {
		t.Fatalf("stored grant = %+v found=%v err=%v, want only the secret's digest bound to user and password", grant, found, err)
	}
	if err := f.check(tmp.Secret, f.now); err != nil {
		t.Fatalf("check at issue time: %v", err)
	}
	if err := f.check(tmp.Secret, f.now.Add(599*time.Second)); err != nil {
		t.Fatalf("check one second before deadline: %v", err)
	}
	if err := f.check(tmp.Secret, f.now.Add(600*time.Second)); !errors.Is(err, domain.ErrTmpPasswordInvalid) {
		t.Fatalf("check at deadline = %v, want ErrTmpPasswordInvalid", err)
	}
}

func TestTmpPasswordCheckAcceptsOnlyThisAuthorizationsSecret(t *testing.T) {
	ctx := context.Background()
	f := newTmpPasswordFixture(t)
	tmp := f.issue(t, 600)
	otherKey := [8]byte{8, 8, 8}
	if err := f.auths.Bind(ctx, domain.Authorization{AuthKeyID: otherKey, UserID: f.userID}); err != nil {
		t.Fatalf("bind second authorization: %v", err)
	}
	cases := map[string]struct {
		userID  int64
		authKey [8]byte
		secret  []byte
	}{
		"empty":                  {f.userID, f.authKey, nil},
		"zero length":            {f.userID, f.authKey, []byte{}},
		"truncated":              {f.userID, f.authKey, tmp.Secret[:16]},
		"different secret":       {f.userID, f.authKey, bytes.Repeat([]byte{9}, tmpPasswordSecretSize)},
		"other session":          {f.userID, otherKey, tmp.Secret},
		"other user same key":    {f.userID + 1, f.authKey, tmp.Secret},
		"zero auth key":          {f.userID, [8]byte{}, tmp.Secret},
		"zero user":              {0, f.authKey, tmp.Secret},
		"secret with extra tail": {f.userID, f.authKey, append(append([]byte(nil), tmp.Secret...), 0)},
	}
	for name, c := range cases {
		if err := f.svc.CheckTmpPassword(ctx, c.userID, c.authKey, c.secret, f.now); !errors.Is(err, domain.ErrTmpPasswordInvalid) {
			t.Errorf("%s: err = %v, want ErrTmpPasswordInvalid", name, err)
		}
	}
	if err := f.check(tmp.Secret, f.now); err != nil {
		t.Fatalf("issuing session check after foreign attempts: %v", err)
	}
}

func TestTmpPasswordRevokedWhenLoginOrPasswordChanges(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		mutate func(t *testing.T, f *tmpPasswordFixture)
	}{
		{"password changed", func(t *testing.T, f *tmpPasswordFixture) {
			f.lifecycle.snapshot.PasswordUpdatedAt = f.now.Add(-time.Hour)
		}},
		{"2FA disabled", func(t *testing.T, f *tmpPasswordFixture) {
			f.lifecycle.snapshot.HasPassword = false
		}},
		{"logged out", func(t *testing.T, f *tmpPasswordFixture) {
			if err := f.auths.Delete(ctx, f.authKey); err != nil {
				t.Fatalf("delete authorization: %v", err)
			}
		}},
		{"re-login on the same auth key", func(t *testing.T, f *tmpPasswordFixture) {
			before, _, _ := f.auths.ByAuthKey(ctx, f.authKey)
			time.Sleep(time.Millisecond)
			if err := f.auths.Bind(ctx, domain.Authorization{AuthKeyID: f.authKey, UserID: f.userID}); err != nil {
				t.Fatalf("rebind: %v", err)
			}
			if after, _, _ := f.auths.ByAuthKey(ctx, f.authKey); after.CreatedAt.Equal(before.CreatedAt) {
				t.Fatal("rebind kept created_at; the login version did not move")
			}
		}},
		{"auth key rebound to another account", func(t *testing.T, f *tmpPasswordFixture) {
			if err := f.auths.Bind(ctx, domain.Authorization{AuthKeyID: f.authKey, UserID: f.userID + 1}); err != nil {
				t.Fatalf("rebind other user: %v", err)
			}
		}},
		{"authorization back to password pending", func(t *testing.T, f *tmpPasswordFixture) {
			if err := f.auths.Bind(ctx, domain.Authorization{AuthKeyID: f.authKey, UserID: f.userID, PasswordPending: true}); err != nil {
				t.Fatalf("rebind pending: %v", err)
			}
		}},
		{"issued again", func(t *testing.T, f *tmpPasswordFixture) {
			next := f.issue(t, 600)
			if err := f.check(next.Secret, f.now); err != nil {
				t.Fatalf("replacement secret: %v", err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newTmpPasswordFixture(t)
			tmp := f.issue(t, 600)
			c.mutate(t, f)
			if err := f.check(tmp.Secret, f.now); !errors.Is(err, domain.ErrTmpPasswordInvalid) {
				t.Fatalf("check after %s = %v, want ErrTmpPasswordInvalid", c.name, err)
			}
		})
	}
}

func TestTmpPasswordIssueAdmission(t *testing.T) {
	ctx := context.Background()

	t.Run("period bounds", func(t *testing.T) {
		f := newTmpPasswordFixture(t)
		for _, period := range []int{-1, 0, domain.TmpPasswordMinPeriod - 1, domain.TmpPasswordMaxPeriod + 1} {
			if _, err := f.svc.IssueTmpPassword(ctx, f.userID, f.authKey, f.proof(t, tmpPasswordTestPassword), period, f.now); !errors.Is(err, domain.ErrTmpPasswordPeriod) {
				t.Fatalf("period %d err = %v, want ErrTmpPasswordPeriod", period, err)
			}
		}
		for _, period := range []int{domain.TmpPasswordMinPeriod, domain.TmpPasswordMaxPeriod} {
			if tmp := f.issue(t, period); !tmp.ValidUntil.Equal(f.now.Add(time.Duration(period) * time.Second)) {
				t.Fatalf("period %d valid until %v", period, tmp.ValidUntil)
			}
		}
	})

	t.Run("passwordless account", func(t *testing.T) {
		f := newTmpPasswordFixture(t)
		f.lifecycle.snapshot.HasPassword = false
		for _, check := range []domain.PasswordCheck{{Empty: true}, f.proof(t, tmpPasswordTestPassword)} {
			if _, err := f.svc.IssueTmpPassword(ctx, f.userID, f.authKey, check, 600, f.now); !errors.Is(err, domain.ErrPasswordMissing) {
				t.Fatalf("passwordless issue err = %v, want ErrPasswordMissing", err)
			}
		}
		assertNoTmpPasswordGrant(t, f)
	})

	t.Run("wrong or empty proof", func(t *testing.T) {
		f := newTmpPasswordFixture(t)
		for _, check := range []domain.PasswordCheck{{Empty: true}, f.proof(t, "wrong horse")} {
			if _, err := f.svc.IssueTmpPassword(ctx, f.userID, f.authKey, check, 600, f.now); !errors.Is(err, domain.ErrPasswordHashInvalid) {
				t.Fatalf("bad proof err = %v, want ErrPasswordHashInvalid", err)
			}
		}
		assertNoTmpPasswordGrant(t, f)
	})

	t.Run("authorization not this user's full login", func(t *testing.T) {
		f := newTmpPasswordFixture(t)
		pending := [8]byte{9, 9, 9}
		if err := f.auths.Bind(ctx, domain.Authorization{AuthKeyID: pending, UserID: f.userID, PasswordPending: true}); err != nil {
			t.Fatalf("bind pending: %v", err)
		}
		foreign := [8]byte{10, 10, 10}
		if err := f.auths.Bind(ctx, domain.Authorization{AuthKeyID: foreign, UserID: f.userID + 1}); err != nil {
			t.Fatalf("bind foreign: %v", err)
		}
		for _, key := range [][8]byte{{}, {11, 11, 11}, pending, foreign} {
			if _, err := f.svc.IssueTmpPassword(ctx, f.userID, key, f.proof(t, tmpPasswordTestPassword), 600, f.now); !errors.Is(err, store.ErrAuthorizationStateChanged) {
				t.Fatalf("auth key %x err = %v, want ErrAuthorizationStateChanged", key, err)
			}
		}
		assertNoTmpPasswordGrant(t, f)
	})

	t.Run("missing dependencies fail closed", func(t *testing.T) {
		svc := NewService(memory.NewPasswordStore())
		if _, err := svc.IssueTmpPassword(ctx, 42, [8]byte{1}, domain.PasswordCheck{Empty: true}, 600, time.Now()); !errors.Is(err, errTmpPasswordUnavailable) {
			t.Fatalf("issue without stores err = %v, want errTmpPasswordUnavailable", err)
		}
		if err := svc.CheckTmpPassword(ctx, 42, [8]byte{1}, bytes.Repeat([]byte{1}, tmpPasswordSecretSize), time.Now()); !errors.Is(err, errTmpPasswordUnavailable) {
			t.Fatalf("check without stores err = %v, want errTmpPasswordUnavailable", err)
		}
	})
}

func assertNoTmpPasswordGrant(t *testing.T, f *tmpPasswordFixture) {
	t.Helper()
	if _, found, _ := f.grants.TmpPasswordByAuthKey(context.Background(), f.authKey); found {
		t.Fatal("a rejected issuance wrote a grant")
	}
}
