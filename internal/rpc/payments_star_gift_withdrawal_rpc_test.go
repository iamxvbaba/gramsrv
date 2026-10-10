package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

type withdrawalRPCGifts struct {
	GiftsService
	uniques map[string]domain.UniqueStarGift
	info    domain.StarGiftValueInfo
	req     domain.StarGiftWithdrawalRequest
	calls   int
	err     error
}

func (s *withdrawalRPCGifts) UniqueBySlug(_ context.Context, slug string) (domain.UniqueStarGift, bool, error) {
	gift, found := s.uniques[slug]
	return gift, found, nil
}

func (s *withdrawalRPCGifts) ValueInfo(context.Context, int64) (domain.StarGiftValueInfo, error) {
	return s.info, nil
}

func (s *withdrawalRPCGifts) Withdraw(_ context.Context, req domain.StarGiftWithdrawalRequest) (domain.StarGiftWithdrawal, error) {
	s.calls++
	s.req = req
	if s.err != nil {
		return domain.StarGiftWithdrawal{}, s.err
	}
	return domain.StarGiftWithdrawal{URL: "https://links.example.test/gift-withdrawal/token"}, nil
}

type withdrawalRPCAccount struct {
	AccountService
	checks   int
	checkErr error
	state    domain.RevenueWithdrawalPasswordState
}

func (s *withdrawalRPCAccount) CheckPassword(context.Context, int64, domain.PasswordCheck) error {
	s.checks++
	return s.checkErr
}

func (s *withdrawalRPCAccount) RevenueWithdrawalPasswordState(context.Context, int64) (domain.RevenueWithdrawalPasswordState, error) {
	return s.state, nil
}

type withdrawalRPCAuth struct {
	AuthService
	authorization domain.Authorization
}

func (s *withdrawalRPCAuth) Authorization(context.Context, [8]byte) (domain.Authorization, bool, error) {
	return s.authorization, true, nil
}

func starGiftWithdrawalRouter(t *testing.T) (
	*Router,
	int64,
	*withdrawalRPCGifts,
	*withdrawalRPCAccount,
	*withdrawalRPCAuth,
	[8]byte) {
	t.Helper()
	r, sender, _, _ := starGiftTestRouter(t)
	now := time.Now()
	authKeyID := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	gifts := &withdrawalRPCGifts{
		uniques: map[string]domain.UniqueStarGift{
			"owned-by-someone-else": {ID: 71, Slug: "owned-by-someone-else",
				Owner: domain.Peer{Type: domain.PeerTypeChannel, ID: 4242}},
			"withdrawal-test-gift": {ID: 72, Slug: "withdrawal-test-gift",
				Owner: domain.Peer{Type: domain.PeerTypeUser, ID: sender.ID}},
		},
		info: domain.StarGiftValueInfo{Currency: "TON", Value: 1200000000},
	}
	account := &withdrawalRPCAccount{state: domain.RevenueWithdrawalPasswordState{
		HasPassword: true, PasswordChangedAt: now.Add(-48 * time.Hour),
	}}
	auth := &withdrawalRPCAuth{authorization: domain.Authorization{
		AuthKeyID: authKeyID, UserID: sender.ID, CreatedAt: now.Add(-48 * time.Hour),
	}}
	r.deps.Gifts = gifts
	r.deps.Account = account
	r.deps.Auth = auth
	return r, sender.ID, gifts, account, auth, authKeyID
}

func withdrawalRequest(
	slug string,
	password tg.InputCheckPasswordSRPClass) *tg.PaymentsGetStarGiftWithdrawalURLRequest {
	req := &tg.PaymentsGetStarGiftWithdrawalURLRequest{
		Stargift: &tg.InputSavedStarGiftSlug{Slug: slug},
	}
	if password != nil {
		req.Password = password
	}
	return req
}

func TestStarGiftWithdrawalNeedsMatchingOwnerAndKnownSlug(t *testing.T) {
	r, userID, gifts, _, _, authKeyID := starGiftWithdrawalRouter(t)
	ctx := WithAuthKeyID(WithUserID(context.Background(), userID), authKeyID)

	if _, err := r.onPaymentsGetStarGiftWithdrawalURL(ctx, withdrawalRequest(
		"owned-by-someone-else", &tg.InputCheckPasswordSRP{SRPID: 1})); !tgerr.Is(err, "STARGIFT_OWNER_INVALID") {
		t.Fatalf("foreign owner err = %v, want STARGIFT_OWNER_INVALID", err)
	}
	if gifts.calls != 0 {
		t.Fatalf("foreign owner still withdrew: calls=%d", gifts.calls)
	}

	if _, err := r.onPaymentsGetStarGiftWithdrawalURL(ctx, withdrawalRequest(
		"never-minted", &tg.InputCheckPasswordSRP{SRPID: 1})); !tgerr.Is(err, "STARGIFT_INVALID") {
		t.Fatalf("unknown slug err = %v, want STARGIFT_INVALID", err)
	}
	if gifts.calls != 0 {
		t.Fatalf("unknown slug still withdrew: calls=%d", gifts.calls)
	}

	got, err := r.onPaymentsGetStarGiftWithdrawalURL(ctx, withdrawalRequest(
		"withdrawal-test-gift", &tg.InputCheckPasswordSRP{SRPID: 1, A: withdrawalSRPProof(9), M1: withdrawalSRPProof(9)}))
	if err != nil {
		t.Fatalf("owner withdrawal: %v", err)
	}
	if got.URL == "" || gifts.calls != 1 || gifts.req.UserID != userID || gifts.req.Ref.Slug != "withdrawal-test-gift" ||
		gifts.req.Ref.Owner != (domain.Peer{Type: domain.PeerTypeUser, ID: userID}) {
		t.Fatalf("withdrawal = %+v req = %+v", got, gifts.req)
	}
}

func TestStarGiftWithdrawalRejectsUnproven2FAAndFreshSessions(t *testing.T) {
	r, userID, gifts, account, auth, authKeyID := starGiftWithdrawalRouter(t)
	ctx := WithAuthKeyID(WithUserID(context.Background(), userID), authKeyID)
	slug := "withdrawal-test-gift"

	account.checkErr = domain.ErrPasswordHashInvalid
	if _, err := r.onPaymentsGetStarGiftWithdrawalURL(ctx, withdrawalRequest(
		slug, &tg.InputCheckPasswordSRP{SRPID: 1, A: withdrawalSRPProof(9), M1: withdrawalSRPProof(9)})); !tgerr.Is(err, "PASSWORD_HASH_INVALID") {
		t.Fatalf("wrong password err = %v, want PASSWORD_HASH_INVALID", err)
	}
	if gifts.calls != 0 {
		t.Fatalf("wrong password still withdrew: calls=%d", gifts.calls)
	}
	account.checkErr = nil

	account.checks = 0
	if _, err := r.onPaymentsGetStarGiftWithdrawalURL(ctx, withdrawalRequest(
		slug, &tg.InputCheckPasswordEmpty{})); err != nil {
		t.Fatalf("empty check on passwordless account: %v", err)
	}
	if account.checks != 1 {
		t.Fatalf("empty check skipped CheckPassword: checks=%d", account.checks)
	}

	account.state = domain.RevenueWithdrawalPasswordState{
		HasPassword: true, PasswordChangedAt: time.Now().Add(-time.Hour),
	}
	_, err := r.onPaymentsGetStarGiftWithdrawalURL(ctx, withdrawalRequest(
		slug, &tg.InputCheckPasswordSRP{SRPID: 1, A: withdrawalSRPProof(9), M1: withdrawalSRPProof(9)}))
	if !tgerr.Is(err, "PASSWORD_TOO_FRESH") {
		t.Fatalf("fresh password err = %v, want PASSWORD_TOO_FRESH", err)
	}
	rpcErr, ok := tgerr.As(err)
	if !ok || rpcErr.Argument <= 0 || rpcErr.Argument > 24*60*60 {
		t.Fatalf("PASSWORD_TOO_FRESH wait = %+v", rpcErr)
	}

	account.state = domain.RevenueWithdrawalPasswordState{
		HasPassword: true, PasswordChangedAt: time.Now().Add(-48 * time.Hour),
	}
	auth.authorization.CreatedAt = time.Now().Add(-time.Minute)
	_, err = r.onPaymentsGetStarGiftWithdrawalURL(ctx, withdrawalRequest(
		slug, &tg.InputCheckPasswordSRP{SRPID: 1, A: withdrawalSRPProof(9), M1: withdrawalSRPProof(9)}))
	if !tgerr.Is(err, "SESSION_TOO_FRESH") {
		t.Fatalf("fresh session err = %v, want SESSION_TOO_FRESH", err)
	}
	rpcErr, ok = tgerr.As(err)
	if !ok || rpcErr.Argument <= 0 || rpcErr.Argument > 24*60*60 {
		t.Fatalf("SESSION_TOO_FRESH wait = %+v", rpcErr)
	}
}

func TestStarGiftWithdrawalRejectsBotsAndMapsCooldown(t *testing.T) {
	r, userID, gifts, _, _, authKeyID := starGiftWithdrawalRouter(t)
	ctx := WithAuthKeyID(WithUserID(context.Background(), userID), authKeyID)
	slug := "withdrawal-test-gift"
	srp := &tg.InputCheckPasswordSRP{SRPID: 1, A: withdrawalSRPProof(9), M1: withdrawalSRPProof(9)}

	r.botStatus.Store(userID, true)
	if _, err := r.onPaymentsGetStarGiftWithdrawalURL(ctx, withdrawalRequest(slug, srp)); !tgerr.Is(err, "BOT_METHOD_INVALID") {
		t.Fatalf("bot err = %v, want BOT_METHOD_INVALID", err)
	}
	if gifts.calls != 0 {
		t.Fatalf("bot still withdrew: calls=%d", gifts.calls)
	}
	r.botStatus.Delete(userID)

	gifts.err = domain.ErrStarGiftExportCooldown
	if _, err := r.onPaymentsGetStarGiftWithdrawalURL(ctx, withdrawalRequest(slug, srp)); !tgerr.Is(err, "STARGIFT_WITHDRAWAL_UNAVAILABLE") {
		t.Fatalf("cooldown err = %v, want STARGIFT_WITHDRAWAL_UNAVAILABLE", err)
	}
	gifts.err = nil

	gifts.err = domain.ErrStarGiftTransferUnavailable
	if _, err := r.onPaymentsGetStarGiftWithdrawalURL(ctx, withdrawalRequest(slug, srp)); !tgerr.Is(err, "STARGIFT_INVALID") {
		t.Fatalf("closed export err = %v, want STARGIFT_INVALID", err)
	}
}

func TestUniqueStarGiftValueInfoUsesSlugError(t *testing.T) {
	r, userID, gifts, _, _, authKeyID := starGiftWithdrawalRouter(t)
	ctx := WithAuthKeyID(WithUserID(context.Background(), userID), authKeyID)

	if _, err := r.onPaymentsGetUniqueStarGiftValueInfo(ctx,
		&tg.PaymentsGetUniqueStarGiftValueInfoRequest{Slug: "never-minted"}); !tgerr.Is(err, "STARGIFT_SLUG_INVALID") {
		t.Fatalf("unknown slug err = %v, want STARGIFT_SLUG_INVALID", err)
	}

	got, err := r.onPaymentsGetUniqueStarGiftValueInfo(ctx,
		&tg.PaymentsGetUniqueStarGiftValueInfoRequest{Slug: " WITHDRAWAL-TEST-GIFT "})
	if err != nil || got.Currency != "TON" || got.Value != gifts.info.Value {
		t.Fatalf("value info = %+v err = %v", got, err)
	}

	r.botStatus.Store(userID, true)
	if _, err := r.onPaymentsGetUniqueStarGiftValueInfo(ctx,
		&tg.PaymentsGetUniqueStarGiftValueInfoRequest{Slug: "withdrawal-test-gift"}); !tgerr.Is(err, "BOT_METHOD_INVALID") {
		t.Fatalf("bot value info err = %v, want BOT_METHOD_INVALID", err)
	}
}

func withdrawalSRPProof(fill byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = fill
	}
	return out
}
