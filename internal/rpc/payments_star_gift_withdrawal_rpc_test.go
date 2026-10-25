package rpc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	appaccount "telesrv/internal/app/account"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
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

// withdrawalRPCAccount accepts any tmp_password or SRP proof so the later
// admission steps stay covered; TestStarGiftWithdrawalTmpPasswordGate runs the
// real tmp_password check and the account service tests run the real SRP check.
type withdrawalRPCAccount struct {
	AccountService
	checks     int
	checked    []byte
	checkedKey [8]byte
	checkErr   error
	srpChecks  int
	srpChecked domain.PasswordCheck
	srpErr     error
	state      domain.RevenueWithdrawalPasswordState
}

func (s *withdrawalRPCAccount) CheckPassword(_ context.Context, _ int64, check domain.PasswordCheck) error {
	s.srpChecks++
	s.srpChecked = check
	return s.srpErr
}

func (s *withdrawalRPCAccount) CheckTmpPassword(_ context.Context, _ int64, authKeyID [8]byte, tmpPassword []byte, _ time.Time) error {
	s.checks++
	s.checked = append([]byte(nil), tmpPassword...)
	s.checkedKey = authKeyID
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

func withdrawalRequest(slug string, tmpPassword []byte) *tg.PaymentsGetStarGiftWithdrawalURLRequest {
	return &tg.PaymentsGetStarGiftWithdrawalURLRequest{
		Stargift:    &tg.InputSavedStarGiftSlug{Slug: slug},
		TmpPassword: tmpPassword,
	}
}

func TestStarGiftWithdrawalNeedsMatchingOwnerAndKnownSlug(t *testing.T) {
	r, userID, gifts, account, _, authKeyID := starGiftWithdrawalRouter(t)
	ctx := WithAuthKeyID(WithUserID(context.Background(), userID), authKeyID)

	if _, err := r.onPaymentsGetStarGiftWithdrawalURL(ctx, withdrawalRequest(
		"owned-by-someone-else", withdrawalTmpPassword(1))); !tgerr.Is(err, "STARGIFT_OWNER_INVALID") {
		t.Fatalf("foreign owner err = %v, want STARGIFT_OWNER_INVALID", err)
	}
	if gifts.calls != 0 {
		t.Fatalf("foreign owner still withdrew: calls=%d", gifts.calls)
	}

	if _, err := r.onPaymentsGetStarGiftWithdrawalURL(ctx, withdrawalRequest(
		"never-minted", withdrawalTmpPassword(1))); !tgerr.Is(err, "STARGIFT_INVALID") {
		t.Fatalf("unknown slug err = %v, want STARGIFT_INVALID", err)
	}
	if gifts.calls != 0 {
		t.Fatalf("unknown slug still withdrew: calls=%d", gifts.calls)
	}

	got, err := r.onPaymentsGetStarGiftWithdrawalURL(ctx, withdrawalRequest(
		"withdrawal-test-gift", withdrawalTmpPassword(9)))
	if err != nil {
		t.Fatalf("owner withdrawal: %v", err)
	}
	if got.URL == "" || gifts.calls != 1 || gifts.req.UserID != userID || gifts.req.Ref.Slug != "withdrawal-test-gift" ||
		gifts.req.Ref.Owner != (domain.Peer{Type: domain.PeerTypeUser, ID: userID}) {
		t.Fatalf("withdrawal = %+v req = %+v", got, gifts.req)
	}
	if account.srpChecks != 0 {
		t.Fatalf("tmp_password withdrawal consulted the SRP check %d times", account.srpChecks)
	}
}

func TestStarGiftWithdrawalRejectsUnproven2FAAndFreshSessions(t *testing.T) {
	r, userID, gifts, account, auth, authKeyID := starGiftWithdrawalRouter(t)
	ctx := WithAuthKeyID(WithUserID(context.Background(), userID), authKeyID)
	slug := "withdrawal-test-gift"

	account.checkErr = domain.ErrTmpPasswordInvalid
	if _, err := r.onPaymentsGetStarGiftWithdrawalURL(ctx, withdrawalRequest(
		slug, withdrawalTmpPassword(9))); !tgerr.Is(err, "TMP_PASSWORD_INVALID") {
		t.Fatalf("wrong tmp password err = %v, want TMP_PASSWORD_INVALID", err)
	}
	if gifts.calls != 0 || !bytes.Equal(account.checked, withdrawalTmpPassword(9)) || account.checkedKey != authKeyID {
		t.Fatalf("wrong tmp password: calls=%d checked=%x on key %x", gifts.calls, account.checked, account.checkedKey)
	}
	account.checkErr = nil

	// An empty tmp_password is no proof: a passwordless account no longer
	// passes the way inputCheckPasswordEmpty used to, with or without bytes.
	account.state = domain.RevenueWithdrawalPasswordState{}
	account.checks = 0
	for _, tmp := range [][]byte{nil, withdrawalTmpPassword(9)} {
		if _, err := r.onPaymentsGetStarGiftWithdrawalURL(ctx, withdrawalRequest(slug, tmp)); !tgerr.Is(err, "PASSWORD_MISSING") {
			t.Fatalf("passwordless tmp %x err = %v, want PASSWORD_MISSING", tmp, err)
		}
	}
	if account.checks != 0 || gifts.calls != 0 {
		t.Fatalf("passwordless account reached tmp check or withdrew: checks=%d calls=%d", account.checks, gifts.calls)
	}

	account.state = domain.RevenueWithdrawalPasswordState{
		HasPassword: true, PasswordChangedAt: time.Now().Add(-time.Hour),
	}
	_, err := r.onPaymentsGetStarGiftWithdrawalURL(ctx, withdrawalRequest(
		slug, withdrawalTmpPassword(9)))
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
		slug, withdrawalTmpPassword(9)))
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
	tmp := withdrawalTmpPassword(9)

	r.botStatus.Store(userID, true)
	if _, err := r.onPaymentsGetStarGiftWithdrawalURL(ctx, withdrawalRequest(slug, tmp)); !tgerr.Is(err, "BOT_METHOD_INVALID") {
		t.Fatalf("bot err = %v, want BOT_METHOD_INVALID", err)
	}
	if gifts.calls != 0 {
		t.Fatalf("bot still withdrew: calls=%d", gifts.calls)
	}
	r.botStatus.Delete(userID)

	gifts.err = domain.ErrStarGiftExportCooldown
	if _, err := r.onPaymentsGetStarGiftWithdrawalURL(ctx, withdrawalRequest(slug, tmp)); !tgerr.Is(err, "STARGIFT_WITHDRAWAL_UNAVAILABLE") {
		t.Fatalf("cooldown err = %v, want STARGIFT_WITHDRAWAL_UNAVAILABLE", err)
	}
	gifts.err = nil

	gifts.err = domain.ErrStarGiftTransferUnavailable
	if _, err := r.onPaymentsGetStarGiftWithdrawalURL(ctx, withdrawalRequest(slug, tmp)); !tgerr.Is(err, "STARGIFT_INVALID") {
		t.Fatalf("closed export err = %v, want STARGIFT_INVALID", err)
	}
}

// withdrawalRPCLifecycle supplies only the durable 2FA facts the production
// account service reads for the tmp_password binding.
type withdrawalRPCLifecycle struct {
	store.AccountLifecycleStore
	snapshot domain.AccountDeletionSnapshot
}

func (s *withdrawalRPCLifecycle) AccountDeletionSnapshot(context.Context, int64) (domain.AccountDeletionSnapshot, bool, error) {
	return s.snapshot, true, nil
}

func TestStarGiftWithdrawalTmpPasswordGate(t *testing.T) {
	r, userID, gifts, _, _, authKeyID := starGiftWithdrawalRouter(t)
	ctx := WithAuthKeyID(WithUserID(context.Background(), userID), authKeyID)
	bgctx := context.Background()
	changedAt := time.Now().Add(-48 * time.Hour)
	auths := memory.NewAuthorizationStore()
	otherKey := [8]byte{9, 9, 9}
	for _, key := range [][8]byte{authKeyID, otherKey} {
		if err := auths.Bind(bgctx, domain.Authorization{AuthKeyID: key, UserID: userID}); err != nil {
			t.Fatalf("bind authorization: %v", err)
		}
	}
	login, _, _ := auths.ByAuthKey(bgctx, authKeyID)
	grants := memory.NewTmpPasswordStore()
	r.deps.Account = appaccount.NewService(memory.NewPasswordStore(),
		appaccount.WithAccountLifecycle(&withdrawalRPCLifecycle{snapshot: domain.AccountDeletionSnapshot{
			User: domain.User{ID: userID}, HasPassword: true, PasswordUpdatedAt: changedAt,
		}}),
		appaccount.WithTmpPasswords(grants, auths))

	// Stands in for the account.getTmpPassword grant of this session; the
	// issuance path itself is covered by the account service tests.
	secret := withdrawalTmpPassword(7)
	if err := grants.PutTmpPassword(bgctx, domain.TmpPasswordGrant{
		AuthKeyID: authKeyID, UserID: userID, AuthorizationCreatedAt: login.CreatedAt,
		PasswordChangedAt: changedAt, SecretHash: sha256.Sum256(secret), ValidUntil: time.Now().Add(10 * time.Minute),
	}); err != nil {
		t.Fatalf("put grant: %v", err)
	}

	// Without the issued value nothing reaches the slug, freshness or export
	// checks: not an empty value, not arbitrary bytes, not this secret from
	// another session of the same account.
	otherCtx := WithAuthKeyID(WithUserID(context.Background(), userID), otherKey)
	for _, slug := range []string{"withdrawal-test-gift", "never-minted"} {
		for _, tmp := range [][]byte{nil, {}, withdrawalTmpPassword(9)} {
			if _, err := r.onPaymentsGetStarGiftWithdrawalURL(ctx, withdrawalRequest(slug, tmp)); !tgerr.Is(err, "TMP_PASSWORD_INVALID") {
				t.Fatalf("%s tmp %x err = %v, want TMP_PASSWORD_INVALID", slug, tmp, err)
			}
		}
		if _, err := r.onPaymentsGetStarGiftWithdrawalURL(otherCtx, withdrawalRequest(slug, secret)); !tgerr.Is(err, "TMP_PASSWORD_INVALID") {
			t.Fatalf("%s secret from another session err = %v, want TMP_PASSWORD_INVALID", slug, err)
		}
	}
	if gifts.calls != 0 {
		t.Fatalf("unverified tmp_password withdrew: calls=%d", gifts.calls)
	}

	got, err := r.onPaymentsGetStarGiftWithdrawalURL(ctx, withdrawalRequest("withdrawal-test-gift", secret))
	if err != nil || got.URL == "" || gifts.calls != 1 {
		t.Fatalf("issued tmp_password withdrawal = %+v err=%v calls=%d", got, err, gifts.calls)
	}
	// The grant is reusable until valid_until, as TDesktop caches it.
	if _, err := r.onPaymentsGetStarGiftWithdrawalURL(ctx, withdrawalRequest("withdrawal-test-gift", secret)); err != nil || gifts.calls != 2 {
		t.Fatalf("reused tmp_password err=%v calls=%d", err, gifts.calls)
	}
}

func legacyWithdrawalRequest(slug string, password tg.InputCheckPasswordSRPClass) *tg.PaymentsGetStarGiftWithdrawalURLLegacyRequest {
	return &tg.PaymentsGetStarGiftWithdrawalURLLegacyRequest{
		Stargift: &tg.InputSavedStarGiftSlug{Slug: slug},
		Password: password,
	}
}

func withdrawalSRPProof() *tg.InputCheckPasswordSRP {
	return &tg.InputCheckPasswordSRP{SRPID: 5, A: withdrawalTmpPassword(2), M1: withdrawalTmpPassword(3)}
}

// Exact 225-231 clients export with the historical SRP constructor. The
// legacy handler checks that proof as SRP and then runs exactly the gates of
// the Layer 232 tmp_password path; it never consults a tmp_password grant.
func TestStarGiftLegacyWithdrawalSRPGate(t *testing.T) {
	r, userID, gifts, account, auth, authKeyID := starGiftWithdrawalRouter(t)
	ctx := WithAuthKeyID(WithUserID(context.Background(), userID), authKeyID)
	slug := "withdrawal-test-gift"
	proof := withdrawalSRPProof()

	r.botStatus.Store(userID, true)
	if _, err := r.onPaymentsGetStarGiftWithdrawalURLLegacy(ctx, legacyWithdrawalRequest(slug, proof)); !tgerr.Is(err, "BOT_METHOD_INVALID") {
		t.Fatalf("bot err = %v, want BOT_METHOD_INVALID", err)
	}
	r.botStatus.Delete(userID)

	// A passwordless account has nothing to prove: neither an SRP answer nor
	// inputCheckPasswordEmpty passes the way the pre-232 handler let it.
	account.state = domain.RevenueWithdrawalPasswordState{}
	for _, password := range []tg.InputCheckPasswordSRPClass{proof, &tg.InputCheckPasswordEmpty{}} {
		if _, err := r.onPaymentsGetStarGiftWithdrawalURLLegacy(ctx, legacyWithdrawalRequest(slug, password)); !tgerr.Is(err, "PASSWORD_MISSING") {
			t.Fatalf("passwordless %T err = %v, want PASSWORD_MISSING", password, err)
		}
	}
	account.state = domain.RevenueWithdrawalPasswordState{
		HasPassword: true, PasswordChangedAt: time.Now().Add(-48 * time.Hour),
	}
	if _, err := r.onPaymentsGetStarGiftWithdrawalURLLegacy(ctx, legacyWithdrawalRequest(slug, &tg.InputCheckPasswordEmpty{})); !tgerr.Is(err, "PASSWORD_HASH_INVALID") {
		t.Fatalf("empty check with 2FA err = %v, want PASSWORD_HASH_INVALID", err)
	}
	if account.srpChecks != 0 || gifts.calls != 0 {
		t.Fatalf("unproven request reached the SRP check or withdrew: checks=%d calls=%d", account.srpChecks, gifts.calls)
	}

	for _, test := range []struct {
		err  error
		want string
	}{
		{domain.ErrPasswordHashInvalid, "PASSWORD_HASH_INVALID"},
		{domain.ErrSRPIDInvalid, "SRP_ID_INVALID"},
	} {
		account.srpErr = test.err
		// The failed proof hides whether the slug exists or is owned.
		for _, target := range []string{slug, "never-minted", "owned-by-someone-else"} {
			if _, err := r.onPaymentsGetStarGiftWithdrawalURLLegacy(ctx, legacyWithdrawalRequest(target, proof)); !tgerr.Is(err, test.want) {
				t.Fatalf("%s with %v err = %v, want %s", target, test.err, err, test.want)
			}
		}
	}
	account.srpErr = nil
	if gifts.calls != 0 || account.srpChecked.Empty || account.srpChecked.SRPID != proof.SRPID ||
		!bytes.Equal(account.srpChecked.A, proof.A) || !bytes.Equal(account.srpChecked.M1, proof.M1) {
		t.Fatalf("SRP check got %+v, calls=%d", account.srpChecked, gifts.calls)
	}

	account.state.PasswordChangedAt = time.Now().Add(-time.Hour)
	if _, err := r.onPaymentsGetStarGiftWithdrawalURLLegacy(ctx, legacyWithdrawalRequest(slug, proof)); !tgerr.Is(err, "PASSWORD_TOO_FRESH") {
		t.Fatalf("fresh password err = %v, want PASSWORD_TOO_FRESH", err)
	}
	account.state.PasswordChangedAt = time.Now().Add(-48 * time.Hour)
	auth.authorization.CreatedAt = time.Now().Add(-time.Minute)
	if _, err := r.onPaymentsGetStarGiftWithdrawalURLLegacy(ctx, legacyWithdrawalRequest(slug, proof)); !tgerr.Is(err, "SESSION_TOO_FRESH") {
		t.Fatalf("fresh session err = %v, want SESSION_TOO_FRESH", err)
	}
	auth.authorization.CreatedAt = time.Now().Add(-48 * time.Hour)
	auth.authorization.PasswordPending = true
	if _, err := r.onPaymentsGetStarGiftWithdrawalURLLegacy(ctx, legacyWithdrawalRequest(slug, proof)); !tgerr.Is(err, "AUTH_KEY_UNREGISTERED") {
		t.Fatalf("password-pending authorization err = %v, want AUTH_KEY_UNREGISTERED", err)
	}
	auth.authorization.PasswordPending = false

	if _, err := r.onPaymentsGetStarGiftWithdrawalURLLegacy(ctx, legacyWithdrawalRequest("owned-by-someone-else", proof)); !tgerr.Is(err, "STARGIFT_OWNER_INVALID") {
		t.Fatalf("foreign owner err = %v, want STARGIFT_OWNER_INVALID", err)
	}
	if _, err := r.onPaymentsGetStarGiftWithdrawalURLLegacy(ctx, legacyWithdrawalRequest("never-minted", proof)); !tgerr.Is(err, "STARGIFT_INVALID") {
		t.Fatalf("unknown slug err = %v, want STARGIFT_INVALID", err)
	}
	if gifts.calls != 0 {
		t.Fatalf("gated legacy requests withdrew: calls=%d", gifts.calls)
	}

	got, err := r.onPaymentsGetStarGiftWithdrawalURLLegacy(ctx, legacyWithdrawalRequest(slug, proof))
	if err != nil || got.URL == "" || gifts.calls != 1 || gifts.req.UserID != userID || gifts.req.Ref.Slug != slug {
		t.Fatalf("legacy withdrawal = %+v err=%v calls=%d req=%+v", got, err, gifts.calls, gifts.req)
	}
	if account.checks != 0 {
		t.Fatalf("legacy withdrawal consulted tmp_password %d times", account.checks)
	}

	gifts.err = domain.ErrStarGiftExportCooldown
	if _, err := r.onPaymentsGetStarGiftWithdrawalURLLegacy(ctx, legacyWithdrawalRequest(slug, proof)); !tgerr.Is(err, "STARGIFT_WITHDRAWAL_UNAVAILABLE") {
		t.Fatalf("cooldown err = %v, want STARGIFT_WITHDRAWAL_UNAVAILABLE", err)
	}
}

func layer231WithdrawalWire(password bin.Encoder) []byte {
	// payments.getStarGiftWithdrawalUrl#d06e93a8 stargift:InputSavedStarGift
	// password:InputCheckPasswordSRP, as every 225-231 client still sends it.
	var b bin.Buffer
	b.PutID(0xd06e93a8)
	if err := (&tg.InputSavedStarGiftSlug{Slug: "withdrawal-test-gift"}).Encode(&b); err != nil {
		panic(err)
	}
	if err := password.Encode(&b); err != nil {
		panic(err)
	}
	return b.Copy()
}

func TestStarGiftWithdrawalExactLayerAdmission(t *testing.T) {
	r, userID, gifts, account, _, authKeyID := starGiftWithdrawalRouter(t)
	ctx := WithAuthKeyID(WithUserID(context.Background(), userID), authKeyID)
	request := withdrawalRequest("withdrawal-test-gift", withdrawalTmpPassword(9))
	wire := encodeExactLayerRPC(t, tlprofile.Profile232, request)
	if id := binary.LittleEndian.Uint32(wire.Buf); id != 0xc8ca817b {
		t.Fatalf("Layer 232 withdrawal wire id = %#x", id)
	}
	decodeWire := bin.Buffer{Buf: append([]byte(nil), wire.Buf...)}
	decoded, err := tlprofile.DecodeObject(tlprofile.Profile232, &decodeWire, tlprofile.Limits{})
	if got, ok := decoded.(*tg.PaymentsGetStarGiftWithdrawalURLRequest); err != nil || !ok ||
		!bytes.Equal(got.TmpPassword, request.TmpPassword) {
		t.Fatalf("Layer 232 withdrawal decoded = %#v, %v", decoded, err)
	}
	admitted, err := r.AdmitLayer(tlprofile.Profile232, &wire, tlprofile.Limits{})
	if err != nil {
		t.Fatalf("admit Layer 232 withdrawal: %v", err)
	}
	if _, method, _ := tlprofile.SemanticName(admitted.Call().Method()); method != "payments.getStarGiftWithdrawalUrl" ||
		!r.dispatcher.Has(admitted.Call().Method()) {
		t.Fatalf("Layer 232 withdrawal admitted as %q, registered=%v", method, r.dispatcher.Has(admitted.Call().Method()))
	}

	// td admits the historical SRP constructor as its own legacy method, so the
	// proof reaches the SRP handler intact; it is never an empty tmp_password.
	for profile := tlprofile.Profile225; profile <= tlprofile.Profile231; profile++ {
		for _, password := range []tg.InputCheckPasswordSRPClass{withdrawalSRPProof(), &tg.InputCheckPasswordEmpty{}} {
			raw := layer231WithdrawalWire(password)
			legacy := bin.Buffer{Buf: append([]byte(nil), raw...)}
			admitted, err := r.AdmitLayer(profile, &legacy, tlprofile.Limits{})
			if err != nil {
				t.Fatalf("Layer %d %T withdrawal admission: %v", profile, password, err)
			}
			if _, method, _ := tlprofile.SemanticName(admitted.Call().Method()); method != "payments.getStarGiftWithdrawalUrlLegacy" ||
				admitted.Call().WireID() != 0xd06e93a8 || !r.dispatcher.Has(admitted.Call().Method()) {
				t.Fatalf("Layer %d withdrawal admitted as %q/%#x, registered=%v",
					profile, method, admitted.Call().WireID(), r.dispatcher.Has(admitted.Call().Method()))
			}
			before := account.srpChecks
			calls := gifts.calls
			result, err := r.dispatcher.Dispatch(ctx, admitted)
			if _, empty := password.(*tg.InputCheckPasswordEmpty); empty {
				if !tgerr.Is(err, "PASSWORD_HASH_INVALID") || account.srpChecks != before || gifts.calls != calls {
					t.Fatalf("Layer %d empty check err = %v (srp checks %d->%d, calls %d->%d)",
						profile, err, before, account.srpChecks, calls, gifts.calls)
				}
				continue
			}
			if err != nil || account.srpChecks != before+1 || gifts.calls != calls+1 {
				t.Fatalf("Layer %d SRP withdrawal err = %v (srp checks %d->%d, calls %d->%d)",
					profile, err, before, account.srpChecks, calls, gifts.calls)
			}
			var out bin.Buffer
			if err := result.Encode(&out); err != nil {
				t.Fatalf("Layer %d withdrawal result encode: %v", profile, err)
			}
		}
	}
	if account.checks != 0 {
		t.Fatalf("historical withdrawals consulted tmp_password %d times", account.checks)
	}

	// Each exact historical profile still admits only its own wire ID.
	modern := encodeExactLayerRPC(t, tlprofile.Profile232, request)
	raw := append([]byte(nil), modern.Buf...)
	if _, err := r.AdmitLayer(tlprofile.Profile231, &modern, tlprofile.Limits{}); !errors.Is(err, tlprofile.ErrUnknownRPCMethod) ||
		!bytes.Equal(modern.Raw(), raw) {
		t.Fatalf("Layer 231 admitted the Layer 232 withdrawal: %v", err)
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

func withdrawalTmpPassword(fill byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = fill
	}
	return out
}
