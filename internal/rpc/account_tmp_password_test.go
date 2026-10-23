package rpc

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

func TestAccountGetTmpPasswordValidatesPeriodAndPassword(t *testing.T) {
	r, userID, _, account, _, authKeyID := starGiftWithdrawalRouter(t)
	ctx := WithAuthKeyID(WithUserID(context.Background(), userID), authKeyID)
	srp := &tg.InputCheckPasswordSRP{SRPID: 1, A: withdrawalSRPProof(9), M1: withdrawalSRPProof(9)}

	for _, period := range []int{0, 59, 86401} {
		if _, err := r.onAccountGetTmpPassword(ctx, &tg.AccountGetTmpPasswordRequest{Password: srp, Period: period}); !tgerr.Is(err, "PERIOD_INVALID") {
			t.Fatalf("period %d err = %v, want PERIOD_INVALID", period, err)
		}
	}
	if account.checks != 0 {
		t.Fatalf("invalid period still checked password: checks=%d", account.checks)
	}

	account.checkErr = domain.ErrPasswordHashInvalid
	if _, err := r.onAccountGetTmpPassword(ctx, &tg.AccountGetTmpPasswordRequest{Password: srp, Period: 60}); !tgerr.Is(err, "PASSWORD_HASH_INVALID") {
		t.Fatalf("wrong password err = %v, want PASSWORD_HASH_INVALID", err)
	}
	account.checkErr = nil

	before := time.Now()
	got, err := r.onAccountGetTmpPassword(ctx, &tg.AccountGetTmpPasswordRequest{Password: srp, Period: 600})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.TmpPassword) != tmpPasswordLen || int64(got.ValidUntil) < before.Add(599*time.Second).Unix() {
		t.Fatalf("tmp password = %x valid_until=%d", got.TmpPassword, got.ValidUntil)
	}
}

func TestStarGiftWithdrawalAcceptsLayer232TmpPassword(t *testing.T) {
	r, userID, gifts, account, _, authKeyID := starGiftWithdrawalRouter(t)
	ctx := WithAuthKeyID(WithUserID(context.Background(), userID), authKeyID)
	srp := &tg.InputCheckPasswordSRP{SRPID: 1, A: withdrawalSRPProof(9), M1: withdrawalSRPProof(9)}
	tmp, err := r.onAccountGetTmpPassword(ctx, &tg.AccountGetTmpPasswordRequest{Password: srp, Period: 600})
	if err != nil {
		t.Fatal(err)
	}
	checks := account.checks

	got, err := r.onPaymentsGetStarGiftWithdrawalURL(ctx, &tg.PaymentsGetStarGiftWithdrawalURLRequest{
		Stargift:    &tg.InputSavedStarGiftSlug{Slug: "withdrawal-test-gift"},
		TmpPassword: tmp.TmpPassword,
	})
	if err != nil {
		t.Fatalf("withdrawal with tmp_password: %v", err)
	}
	if got.URL == "" || gifts.calls != 1 || gifts.req.UserID != userID {
		t.Fatalf("withdrawal = %+v req = %+v", got, gifts.req)
	}
	if account.checks != checks {
		t.Fatalf("tmp_password re-ran the SRP check: %d -> %d", checks, account.checks)
	}
}

func TestStarGiftWithdrawalRejectsInvalidTmpPasswords(t *testing.T) {
	r, userID, gifts, account, _, authKeyID := starGiftWithdrawalRouter(t)
	ctx := WithAuthKeyID(WithUserID(context.Background(), userID), authKeyID)
	srp := &tg.InputCheckPasswordSRP{SRPID: 1, A: withdrawalSRPProof(9), M1: withdrawalSRPProof(9)}
	tmp, err := r.onAccountGetTmpPassword(ctx, &tg.AccountGetTmpPasswordRequest{Password: srp, Period: 600})
	if err != nil {
		t.Fatal(err)
	}
	withdraw := func(ctx context.Context, token []byte) error {
		_, err := r.onPaymentsGetStarGiftWithdrawalURL(ctx, &tg.PaymentsGetStarGiftWithdrawalURLRequest{
			Stargift:    &tg.InputSavedStarGiftSlug{Slug: "withdrawal-test-gift"},
			TmpPassword: token,
		})
		return err
	}

	tampered := bytes.Clone(tmp.TmpPassword)
	tampered[len(tampered)-1] ^= 1
	malformedLegacy := withdrawalRequest("x", srp).TmpPassword
	malformedLegacy = malformedLegacy[:len(malformedLegacy)-1]
	for name, token := range map[string][]byte{
		"empty":            nil,
		"garbage":          []byte("not-a-token"),
		"tampered":         tampered,
		"malformed legacy": malformedLegacy,
	} {
		if err := withdraw(ctx, token); !tgerr.Is(err, "TMP_PASSWORD_INVALID") {
			t.Fatalf("%s token err = %v, want TMP_PASSWORD_INVALID", name, err)
		}
	}

	// Bound to the device that requested it.
	otherKey := WithAuthKeyID(WithUserID(context.Background(), userID), [8]byte{9, 9, 9, 9, 9, 9, 9, 9})
	if err := withdraw(otherKey, tmp.TmpPassword); !tgerr.Is(err, "TMP_PASSWORD_INVALID") {
		t.Fatalf("other auth key err = %v, want TMP_PASSWORD_INVALID", err)
	}

	// Changing the 2FA password revokes every issued token.
	account.state.PasswordChangedAt = account.state.PasswordChangedAt.Add(time.Second)
	if err := withdraw(ctx, tmp.TmpPassword); !tgerr.Is(err, "TMP_PASSWORD_INVALID") {
		t.Fatalf("token after password change err = %v, want TMP_PASSWORD_INVALID", err)
	}
	if gifts.calls != 0 {
		t.Fatalf("invalid token still withdrew: calls=%d", gifts.calls)
	}
}

func TestTmpPasswordSignerExpiresAndBindsUser(t *testing.T) {
	var signer tmpPasswordSigner
	now := time.Unix(1_800_000_000, 0)
	changed := now.Add(-time.Hour)
	key := [8]byte{1}
	token, err := signer.issue(7, key, now.Add(time.Minute).Unix(), changed)
	if err != nil {
		t.Fatal(err)
	}
	if !signer.valid(token, 7, key, now, changed) {
		t.Fatal("fresh token rejected")
	}
	if signer.valid(token, 7, key, now.Add(time.Minute), changed) {
		t.Fatal("expired token accepted")
	}
	if signer.valid(token, 8, key, now, changed) {
		t.Fatal("token accepted for another user")
	}
	var other tmpPasswordSigner
	if other.valid(token, 7, key, now, changed) {
		t.Fatal("token accepted by a signer with another key")
	}
}
