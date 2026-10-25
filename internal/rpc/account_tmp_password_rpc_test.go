package rpc

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

type tmpPasswordRPCAccount struct {
	AccountService
	calls    int
	userID   int64
	authKey  [8]byte
	check    domain.PasswordCheck
	period   int
	now      time.Time
	issued   domain.TmpPassword
	issueErr error
}

func (s *tmpPasswordRPCAccount) IssueTmpPassword(_ context.Context, userID int64, authKeyID [8]byte, check domain.PasswordCheck, period int, now time.Time) (domain.TmpPassword, error) {
	s.calls++
	s.userID, s.authKey, s.check, s.period, s.now = userID, authKeyID, check, period, now
	return s.issued, s.issueErr
}

func TestAccountGetTmpPasswordIssuesForThisAuthorization(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	account := &tmpPasswordRPCAccount{issued: domain.TmpPassword{
		Secret: bytes.Repeat([]byte{4}, 32), ValidUntil: now.Add(600 * time.Second),
	}}
	r := New(Config{}, Deps{Account: account}, zaptest.NewLogger(t), fixedClock{now: now})
	authKeyID := [8]byte{1, 2, 3}
	ctx := WithAuthKeyID(WithUserID(context.Background(), 77), authKeyID)
	srp := &tg.InputCheckPasswordSRP{SRPID: 5, A: []byte{1}, M1: []byte{2}}

	got, err := r.onAccountGetTmpPassword(ctx, &tg.AccountGetTmpPasswordRequest{Password: srp, Period: 600})
	if err != nil {
		t.Fatalf("account.getTmpPassword: %v", err)
	}
	if !bytes.Equal(got.TmpPassword, account.issued.Secret) || got.ValidUntil != int(now.Unix())+600 {
		t.Fatalf("result = %+v, want issued secret valid until now+600", got)
	}
	if account.userID != 77 || account.authKey != authKeyID || account.period != 600 || !account.now.Equal(now) ||
		account.check.SRPID != 5 || account.check.Empty {
		t.Fatalf("issue args = user %d key %x period %d now %v check %+v", account.userID, account.authKey, account.period, account.now, account.check)
	}

	if _, err := r.onAccountGetTmpPassword(ctx, &tg.AccountGetTmpPasswordRequest{Password: &tg.InputCheckPasswordEmpty{}, Period: 600}); err != nil {
		t.Fatalf("empty check is the service's decision: %v", err)
	}
	if !account.check.Empty {
		t.Fatalf("inputCheckPasswordEmpty reached the service as %+v", account.check)
	}
}

func TestAccountGetTmpPasswordErrors(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cases := []struct {
		err  error
		want string
	}{
		{domain.ErrPasswordMissing, "PASSWORD_MISSING"},
		{domain.ErrTmpPasswordPeriod, "PERIOD_INVALID"},
		{domain.ErrPasswordHashInvalid, "PASSWORD_HASH_INVALID"},
		{domain.ErrSRPIDInvalid, "SRP_ID_INVALID"},
		{domain.ErrSRPPasswordChanged, "SRP_PASSWORD_CHANGED"},
		{store.ErrAuthorizationStateChanged, "AUTH_KEY_UNREGISTERED"},
	}
	for _, c := range cases {
		account := &tmpPasswordRPCAccount{issueErr: c.err}
		r := New(Config{}, Deps{Account: account}, zaptest.NewLogger(t), fixedClock{now: now})
		ctx := WithAuthKeyID(WithUserID(context.Background(), 77), [8]byte{1})
		if _, err := r.onAccountGetTmpPassword(ctx, &tg.AccountGetTmpPasswordRequest{Password: &tg.InputCheckPasswordEmpty{}, Period: 600}); !tgerr.Is(err, c.want) {
			t.Errorf("%v -> %v, want %s", c.err, err, c.want)
		}
	}

	account := &tmpPasswordRPCAccount{}
	r, user, _, _ := starGiftTestRouter(t)
	r.deps.Account = account
	request := &tg.AccountGetTmpPasswordRequest{Password: &tg.InputCheckPasswordEmpty{}, Period: 600}
	if _, err := r.onAccountGetTmpPassword(WithUserID(context.Background(), user.ID), request); !tgerr.Is(err, "AUTH_KEY_UNREGISTERED") {
		t.Fatalf("no auth key err = %v, want AUTH_KEY_UNREGISTERED", err)
	}
	r.botStatus.Store(user.ID, true)
	if _, err := r.onAccountGetTmpPassword(WithAuthKeyID(WithUserID(context.Background(), user.ID), [8]byte{1}), request); !tgerr.Is(err, "BOT_METHOD_INVALID") {
		t.Fatalf("bot err = %v, want BOT_METHOD_INVALID", err)
	}
	if account.calls != 0 {
		t.Fatalf("rejected callers reached issuance: calls=%d", account.calls)
	}
}

// account.getTmpPassword#449e0b51 is the same constructor in every exact
// profile; it is not a Layer 232 addition and must dispatch everywhere.
func TestAccountGetTmpPasswordExactLayerAdmission(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1_800_000_000, 0)})
	request := &tg.AccountGetTmpPasswordRequest{Password: &tg.InputCheckPasswordEmpty{}, Period: 600}
	for profile := tlprofile.Profile225; profile <= tlprofile.Profile232; profile++ {
		wire := encodeExactLayerRPC(t, profile, request)
		admitted, err := r.AdmitLayer(profile, &wire, tlprofile.Limits{})
		if err != nil {
			t.Fatalf("Layer %d admit: %v", profile, err)
		}
		if _, method, _ := tlprofile.SemanticName(admitted.Call().Method()); method != "account.getTmpPassword" ||
			!r.dispatcher.Has(admitted.Call().Method()) {
			t.Fatalf("Layer %d admitted as %q, registered=%v", profile, method, r.dispatcher.Has(admitted.Call().Method()))
		}
	}
}
