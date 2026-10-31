package auth

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	mtcrypto "github.com/iamxvbaba/td/crypto"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

func TestBindTempAuthKeyValidatesEncryptedMessage(t *testing.T) {
	ctx := context.Background()
	keys := memory.NewAuthKeyStore()
	tempBindings := memory.NewTempAuthKeyBindingStore(keys)
	permKey := testAuthKey(0x11)
	tempKey := testAuthKey(0x55)
	expiresAt := int(time.Now().Add(time.Hour).Unix())
	saveAuthKey(t, keys, permKey)
	saveAuthKeyWithExpiry(t, keys, tempKey, expiresAt)

	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), keys, tempBindings, "12345")

	const (
		nonce     = int64(0x12345678)
		sessionID = int64(0x1020304050)
		msgID     = int64(0x0102030405060708)
	)
	encrypted, err := mtcrypto.EncryptBindMessage(
		bytes.NewReader(bytes.Repeat([]byte{0xCD}, 128)),
		permKey,
		msgID,
		&mtcrypto.BindAuthKeyInner{
			Nonce:         nonce,
			TempAuthKeyID: tempKey.IntID(),
			PermAuthKeyID: permKey.IntID(),
			TempSessionID: sessionID,
			ExpiresAt:     expiresAt,
		},
	)
	if err != nil {
		t.Fatalf("encrypt bind message: %v", err)
	}

	_, err = svc.BindTempAuthKey(ctx, sessionID, domain.TempAuthKeyBinding{
		TempAuthKeyID:    tempKey.ID,
		PermAuthKeyID:    permKey.IntID(),
		Nonce:            nonce,
		ExpiresAt:        expiresAt,
		EncryptedMessage: encrypted,
	})
	if err != nil {
		t.Fatalf("BindTempAuthKey valid message: %v", err)
	}

	_, err = svc.BindTempAuthKey(ctx, sessionID+1, domain.TempAuthKeyBinding{
		TempAuthKeyID:    tempKey.ID,
		PermAuthKeyID:    permKey.IntID(),
		Nonce:            nonce,
		ExpiresAt:        expiresAt,
		EncryptedMessage: encrypted,
	})
	if !errors.Is(err, ErrEncryptedMessageInvalid) {
		t.Fatalf("BindTempAuthKey wrong session err = %v, want ErrEncryptedMessageInvalid", err)
	}

	// TDesktop intentionally adds a 30-second bind grace to the expiry it
	// derived from p_q_inner_data_temp. The request is valid, but the durable
	// binding must be normalized back to the server handshake expiry.
	extendedExpiry := expiresAt + 30
	extendedEncrypted, err := mtcrypto.EncryptBindMessage(
		bytes.NewReader(bytes.Repeat([]byte{0xCE}, 128)),
		permKey,
		msgID+4,
		&mtcrypto.BindAuthKeyInner{
			Nonce:         nonce,
			TempAuthKeyID: tempKey.IntID(),
			PermAuthKeyID: permKey.IntID(),
			TempSessionID: sessionID,
			ExpiresAt:     extendedExpiry,
		},
	)
	if err != nil {
		t.Fatalf("encrypt extended bind message: %v", err)
	}
	_, err = svc.BindTempAuthKey(ctx, sessionID, domain.TempAuthKeyBinding{
		TempAuthKeyID:    tempKey.ID,
		PermAuthKeyID:    permKey.IntID(),
		Nonce:            nonce,
		ExpiresAt:        extendedExpiry,
		EncryptedMessage: extendedEncrypted,
	})
	if err != nil {
		t.Fatalf("BindTempAuthKey TDesktop grace expiry: %v", err)
	}
	stored, found, getErr := tempBindings.GetByTemp(ctx, tempKey.ID)
	if getErr != nil || !found || stored.ExpiresAt != expiresAt {
		t.Fatalf("stored binding after extension attempt = %+v found=%v err=%v", stored, found, getErr)
	}
}

func TestBindTempAuthKeyClassifiesExpiryWithoutDestroyingPermanentKey(t *testing.T) {
	ctx := context.Background()
	keys := memory.NewAuthKeyStore()
	tempBindings := memory.NewTempAuthKeyBindingStore(keys)
	permKey := testAuthKey(0x31)
	tempKey := testAuthKey(0x32)
	saveAuthKey(t, keys, permKey)
	saveAuthKeyWithExpiry(t, keys, tempKey, int(time.Now().Add(-time.Second).Unix()))
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), keys, tempBindings, "12345")

	request := domain.TempAuthKeyBinding{
		TempAuthKeyID: tempKey.ID,
		PermAuthKeyID: permKey.IntID(),
		ExpiresAt:     int(time.Now().Add(time.Hour).Unix()),
	}
	if _, err := svc.BindTempAuthKey(ctx, 1001, request); !errors.Is(err, ErrTempAuthKeyEmpty) {
		t.Fatalf("expired protocol temp key err = %v, want ErrTempAuthKeyEmpty", err)
	}

	request.TempAuthKeyID = testAuthKey(0x33).ID
	if _, err := svc.BindTempAuthKey(ctx, 1001, request); !errors.Is(err, ErrTempAuthKeyEmpty) {
		t.Fatalf("missing protocol temp key err = %v, want ErrTempAuthKeyEmpty", err)
	}

	request.ExpiresAt = int(time.Now().Add(-time.Second).Unix())
	if _, err := svc.BindTempAuthKey(ctx, 1001, request); !errors.Is(err, ErrExpiresAtInvalid) {
		t.Fatalf("expired request proof err = %v, want ErrExpiresAtInvalid", err)
	}
}

func TestUpdateAuthKeyClientInfoConvergesMemoryAuthorizationToAuthKeyLayerAuthority(t *testing.T) {
	ctx := context.Background()
	keys := memory.NewAuthKeyStore()
	authz := memory.NewAuthorizationStore()
	key := testAuthKey(0x23)
	saveAuthKey(t, keys, key)
	if err := authz.Bind(ctx, domain.Authorization{
		AuthKeyID: key.ID,
		UserID:    1780243200,
		Layer:     220,
		Platform:  "unknown",
	}); err != nil {
		t.Fatalf("bind authorization: %v", err)
	}
	svc := NewService(memory.NewUserStore(), authz, memory.NewCodeStore(), keys, nil, "12345")

	info := domain.AuthKeyClientInfo{
		Layer:         227,
		DeviceModel:   "iPhone Simulator",
		Platform:      "ios",
		SystemVersion: "26.5",
		APIID:         1,
		AppVersion:    "12.8 (10000)",
	}
	if err := svc.UpdateAuthKeyClientInfo(ctx, key.ID, info); err != nil {
		t.Fatalf("update auth key client info: %v", err)
	}

	storedKey, found, err := keys.Get(ctx, key.ID)
	if err != nil || !found {
		t.Fatalf("get auth key: found=%v err=%v", found, err)
	}
	storedAuth, found, err := authz.ByAuthKey(ctx, key.ID)
	if err != nil || !found {
		t.Fatalf("get authorization: found=%v err=%v", found, err)
	}
	if storedKey.Platform != "ios" || storedAuth.Platform != "ios" ||
		storedKey.Layer != info.Layer || storedAuth.Layer != info.Layer ||
		storedKey.DeviceModel != info.DeviceModel || storedAuth.DeviceModel != info.DeviceModel ||
		storedKey.AppVersion != info.AppVersion || storedAuth.AppVersion != info.AppVersion {
		t.Fatalf("client metadata did not converge: key=%+v authorization=%+v", storedKey, storedAuth)
	}
}

func TestResolveAuthKeyUsesValidTempBinding(t *testing.T) {
	ctx := context.Background()
	keys := memory.NewAuthKeyStore()
	tempBindings := memory.NewTempAuthKeyBindingStore(keys)
	permKey := testAuthKey(0x11)
	tempKey := testAuthKey(0x55)
	expiresAt := int(time.Now().Add(time.Hour).Unix())
	saveAuthKey(t, keys, permKey)
	saveAuthKeyWithExpiry(t, keys, tempKey, expiresAt)
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), keys, tempBindings, "12345")

	if err := tempBindings.Save(ctx, domain.TempAuthKeyBinding{
		TempAuthKeyID: tempKey.ID,
		PermAuthKeyID: permKey.IntID(),
		ExpiresAt:     expiresAt,
	}); err != nil {
		t.Fatalf("save temp binding: %v", err)
	}

	got, ok, err := svc.ResolveAuthKey(ctx, tempKey.ID)
	if err != nil {
		t.Fatalf("ResolveAuthKey: %v", err)
	}
	if !ok || got != permKey.ID {
		t.Fatalf("resolved = %x ok=%v, want perm %x", got, ok, permKey.ID)
	}
}

func TestResolveAuthKeyAllowsExpiredTempBindingForAuthorizedPermKey(t *testing.T) {
	ctx := context.Background()
	keys := memory.NewAuthKeyStore()
	tempBindings := memory.NewTempAuthKeyBindingStore(keys)
	authz := memory.NewAuthorizationStore()
	permKey := testAuthKey(0x21)
	tempKey := testAuthKey(0x65)
	expiresAt := int(time.Now().Add(-time.Minute).Unix())
	saveAuthKey(t, keys, permKey)
	saveAuthKeyWithExpiry(t, keys, tempKey, expiresAt)
	svc := NewService(memory.NewUserStore(), authz, memory.NewCodeStore(), keys, tempBindings, "12345")

	if err := authz.Bind(ctx, domain.Authorization{AuthKeyID: permKey.ID, UserID: 1000000001}); err != nil {
		t.Fatalf("bind authorization: %v", err)
	}
	if err := tempBindings.Save(ctx, domain.TempAuthKeyBinding{
		TempAuthKeyID: tempKey.ID,
		PermAuthKeyID: permKey.IntID(),
		ExpiresAt:     expiresAt,
	}); err != nil {
		t.Fatalf("save temp binding: %v", err)
	}

	got, ok, err := svc.ResolveAuthKey(ctx, tempKey.ID)
	if err != nil {
		t.Fatalf("ResolveAuthKey: %v", err)
	}
	if !ok || got != permKey.ID {
		t.Fatalf("resolved = %x ok=%v, want authorized perm %x", got, ok, permKey.ID)
	}
}

func TestResolveAuthKeyKeepsExpiredBindingCanonicalWithoutAuthorization(t *testing.T) {
	ctx := context.Background()
	keys := memory.NewAuthKeyStore()
	tempBindings := memory.NewTempAuthKeyBindingStore(keys)
	permKey := testAuthKey(0x31)
	tempKey := testAuthKey(0x75)
	expiresAt := int(time.Now().Add(-time.Minute).Unix())
	saveAuthKey(t, keys, permKey)
	saveAuthKeyWithExpiry(t, keys, tempKey, expiresAt)
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), keys, tempBindings, "12345")

	if err := tempBindings.Save(ctx, domain.TempAuthKeyBinding{
		TempAuthKeyID: tempKey.ID,
		PermAuthKeyID: permKey.IntID(),
		ExpiresAt:     expiresAt,
	}); err != nil {
		t.Fatalf("save temp binding: %v", err)
	}

	got, ok, err := svc.ResolveAuthKey(ctx, tempKey.ID)
	if err != nil {
		t.Fatalf("ResolveAuthKey: %v", err)
	}
	if !ok || got != permKey.ID {
		t.Fatalf("resolved = %x ok=%v, want canonical perm %x even while logged out", got, ok, permKey.ID)
	}
}

func TestExpiredTempLogoutReloginNeverAuthorizesRawTempKey(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	authz := memory.NewAuthorizationStore()
	keys := memory.NewAuthKeyStore()
	tempBindings := memory.NewTempAuthKeyBindingStore(keys)
	permKey := testAuthKey(0x41)
	tempKey := testAuthKey(0x81)
	expiresAt := int(time.Now().Add(-time.Minute).Unix())
	if err := keys.Save(ctx, store.AuthKeyData{ID: permKey.ID}); err != nil {
		t.Fatalf("save perm key: %v", err)
	}
	if err := keys.Save(ctx, store.AuthKeyData{
		ID: tempKey.ID, ExpiresAt: expiresAt,
	}); err != nil {
		t.Fatalf("save temp key: %v", err)
	}
	if err := tempBindings.Save(ctx, domain.TempAuthKeyBinding{
		TempAuthKeyID: tempKey.ID,
		PermAuthKeyID: permKey.IntID(),
		ExpiresAt:     expiresAt,
	}); err != nil {
		t.Fatalf("save temp binding: %v", err)
	}
	bob, err := users.Create(ctx, domain.User{Phone: "15550008101", FirstName: "Bob"})
	if err != nil {
		t.Fatalf("create Bob: %v", err)
	}
	alice, err := users.Create(ctx, domain.User{Phone: "15550008102", FirstName: "Alice"})
	if err != nil {
		t.Fatalf("create Alice: %v", err)
	}
	if err := authz.Bind(ctx, domain.Authorization{AuthKeyID: permKey.ID, UserID: bob.ID}); err != nil {
		t.Fatalf("authorize Bob: %v", err)
	}

	svc := NewService(users, authz, memory.NewCodeStore(), keys, tempBindings, "12345")
	if err := svc.LogOut(ctx, permKey.ID); err != nil {
		t.Fatalf("logout Bob: %v", err)
	}
	resolved, ok, err := svc.ResolveAuthKey(ctx, tempKey.ID)
	if err != nil || !ok || resolved != permKey.ID {
		t.Fatalf("resolve after logout = %x/%v/%v, want perm", resolved, ok, err)
	}
	if _, err := svc.BindVerifiedLogin(ctx, domain.Authorization{AuthKeyID: resolved}, alice.ID); err != nil {
		t.Fatalf("relogin Alice on canonical perm: %v", err)
	}
	if a, found, err := authz.ByAuthKey(ctx, permKey.ID); err != nil || !found || a.UserID != alice.ID {
		t.Fatalf("perm authorization = %+v found=%v err=%v, want Alice", a, found, err)
	}
	if a, found, err := authz.ByAuthKey(ctx, tempKey.ID); err != nil || found {
		t.Fatalf("temp authorization = %+v found=%v err=%v, want absent", a, found, err)
	}
}

func TestAuthorizationBindRejectsTemporaryProtocolKey(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	authz := memory.NewAuthorizationStore()
	keys := memory.NewAuthKeyStore()
	tempKey := testAuthKey(0x82)
	if err := keys.Save(ctx, store.AuthKeyData{
		ID: tempKey.ID, ExpiresAt: int(time.Now().Add(time.Hour).Unix()),
	}); err != nil {
		t.Fatalf("save temp key: %v", err)
	}
	u, err := users.Create(ctx, domain.User{Phone: "15550008201", FirstName: "Alice"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	svc := NewService(users, authz, memory.NewCodeStore(), keys, memory.NewTempAuthKeyBindingStore(keys), "12345")
	if _, err := svc.BindVerifiedLogin(ctx, domain.Authorization{AuthKeyID: tempKey.ID}, u.ID); !errors.Is(err, ErrAuthKeyPermEmpty) {
		t.Fatalf("bind temp authorization err = %v, want ErrAuthKeyPermEmpty", err)
	}
	if _, found, err := authz.ByAuthKey(ctx, tempKey.ID); err != nil || found {
		t.Fatalf("temp authorization found=%v err=%v, want absent", found, err)
	}
}

func TestDeletedUserCannotCrossAuthorizationBoundaries(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	authz := memory.NewAuthorizationStore()
	deleted, err := users.Create(ctx, domain.User{
		Deleted:        true,
		DeletedAt:      time.Now().Unix(),
		DeletionSource: domain.AccountDeletionManual,
	})
	if err != nil {
		t.Fatalf("create deleted user: %v", err)
	}
	svc := NewService(users, authz, memory.NewCodeStore(), nil, nil, "12345")

	passkeyAuthKeyID := [8]byte{0x91}
	if _, err := svc.BindVerifiedLogin(ctx, domain.Authorization{AuthKeyID: passkeyAuthKeyID}, deleted.ID); !errors.Is(err, ErrSystemUserLoginForbidden) {
		t.Fatalf("BindVerifiedLogin deleted user err = %v, want ErrSystemUserLoginForbidden", err)
	}
	if _, found, err := authz.ByAuthKey(ctx, passkeyAuthKeyID); err != nil || found {
		t.Fatalf("deleted passkey authorization found=%v err=%v, want absent", found, err)
	}

	qrAuthKeyID := [8]byte{0x92}
	if _, err := svc.AcceptLoginToken(ctx, domain.Authorization{AuthKeyID: qrAuthKeyID}, deleted.ID); !errors.Is(err, ErrSystemUserLoginForbidden) {
		t.Fatalf("AcceptLoginToken deleted user err = %v, want ErrSystemUserLoginForbidden", err)
	}
	if _, found, err := authz.ByAuthKey(ctx, qrAuthKeyID); err != nil || found {
		t.Fatalf("deleted QR authorization found=%v err=%v, want absent", found, err)
	}

	staleAuthKeyID := [8]byte{0x93}
	if err := authz.Bind(ctx, domain.Authorization{AuthKeyID: staleAuthKeyID, UserID: deleted.ID}); err != nil {
		t.Fatalf("seed stale authorization: %v", err)
	}
	if userID, found, err := svc.UserID(ctx, staleAuthKeyID); err != nil || found || userID != 0 {
		t.Fatalf("UserID stale tombstone = %d found=%v err=%v, want unauthorized", userID, found, err)
	}
	if _, found, err := authz.ByAuthKey(ctx, staleAuthKeyID); err != nil || found {
		t.Fatalf("stale tombstone authorization found=%v err=%v, want retired", found, err)
	}

	pendingAuthKeyID := [8]byte{0x94}
	if err := authz.Bind(ctx, domain.Authorization{AuthKeyID: pendingAuthKeyID, UserID: deleted.ID, PasswordPending: true}); err != nil {
		t.Fatalf("seed stale pending authorization: %v", err)
	}
	if err := svc.CompletePasswordSignIn(ctx, pendingAuthKeyID, deleted.ID); !errors.Is(err, ErrSystemUserLoginForbidden) {
		t.Fatalf("CompletePasswordSignIn deleted user err = %v, want ErrSystemUserLoginForbidden", err)
	}
	if _, found, err := authz.ByAuthKey(ctx, pendingAuthKeyID); err != nil || found {
		t.Fatalf("stale pending tombstone authorization found=%v err=%v, want retired", found, err)
	}
}

func TestPhoneCodeAcceptsTDesktopDigitsOnlySignIn(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	authz := memory.NewAuthorizationStore()
	svc := NewService(users, authz, memory.NewCodeStore(), nil, nil, "12345")

	hash, err := svc.SendCode(ctx, "+15550004310")
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}

	_, _, needSignUp, err := svc.SignIn(ctx, domain.Authorization{}, "15550004310", hash, "12345")
	if err != nil {
		t.Fatalf("SignIn with digits-only phone: %v", err)
	}
	if !needSignUp {
		t.Fatal("SignIn needSignUp = false, want true")
	}

	u, _, err := svc.SignUp(ctx, domain.Authorization{}, "+1 555 000 4310", hash, "Test", "User")
	if err != nil {
		t.Fatalf("SignUp with formatted phone: %v", err)
	}
	if u.Phone != "15550004310" {
		t.Fatalf("created phone = %q, want normalized digits", u.Phone)
	}
	if u.ID != domain.UserIDSequenceBase {
		t.Fatalf("created user id = %d, want base %d", u.ID, domain.UserIDSequenceBase)
	}
}

func TestVirtual888PhoneRegistersAndSignsIn(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	authz := memory.NewAuthorizationStore()
	delivery := &captureLoginCodeDelivery{}
	svc := NewService(
		users,
		authz,
		memory.NewCodeStore(),
		nil,
		nil,
		"12345",
		WithLoginCodeDelivery(delivery),
	)
	const (
		formatted = "+888 12-34"
		canonical = "8881234"
	)

	firstHash, err := svc.SendCode(ctx, formatted)
	if err != nil {
		t.Fatalf("SendCode virtual phone: %v", err)
	}
	verifyCodeForSignUp(t, svc, canonical, firstHash, "12345")
	created, _, err := svc.SignUp(ctx, domain.Authorization{AuthKeyID: [8]byte{0x54}}, formatted, firstHash, "Virtual", "User")
	if err != nil {
		t.Fatalf("SignUp virtual phone: %v", err)
	}
	if created.Phone != canonical {
		t.Fatalf("created phone = %q, want %q", created.Phone, canonical)
	}

	secondHash, err := svc.SendCode(ctx, canonical)
	if err != nil {
		t.Fatalf("SendCode existing virtual phone: %v", err)
	}
	signedIn, _, needSignUp, err := svc.SignIn(ctx, domain.Authorization{AuthKeyID: [8]byte{0x55}}, formatted, secondHash, "12345")
	if err != nil {
		t.Fatalf("SignIn virtual phone: %v", err)
	}
	if needSignUp || signedIn.ID != created.ID {
		t.Fatalf("SignIn user=%d needSignUp=%v, want existing user %d", signedIn.ID, needSignUp, created.ID)
	}
}

func TestIranNationalTrunkVariantsShareOneAccountIdentity(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	authz := memory.NewAuthorizationStore()
	delivery := &captureLoginCodeDelivery{}
	svc := NewService(
		users,
		authz,
		memory.NewCodeStore(),
		nil,
		nil,
		"12345",
		WithLoginCodeDelivery(delivery),
	)
	const (
		withNationalTrunk = "+98 0998 167 9461"
		international     = "989981679461"
		canonical         = "989981679461"
	)

	firstHash, err := svc.SendCode(ctx, withNationalTrunk)
	if err != nil {
		t.Fatalf("SendCode trunk variant: %v", err)
	}
	verifyCodeForSignUp(t, svc, international, firstHash, "12345")
	created, _, err := svc.SignUp(ctx, domain.Authorization{AuthKeyID: [8]byte{1}}, international, firstHash, "Iran", "User")
	if err != nil {
		t.Fatalf("SignUp international variant: %v", err)
	}
	if created.Phone != canonical {
		t.Fatalf("created phone = %q, want %q", created.Phone, canonical)
	}

	secondHash, err := svc.SendCode(ctx, international)
	if err != nil {
		t.Fatalf("SendCode existing international variant: %v", err)
	}
	signedIn, _, needSignUp, err := svc.SignIn(ctx, domain.Authorization{AuthKeyID: [8]byte{2}}, withNationalTrunk, secondHash, "12345")
	if err != nil {
		t.Fatalf("SignIn trunk variant: %v", err)
	}
	if needSignUp || signedIn.ID != created.ID {
		t.Fatalf("SignIn user=%d needSignUp=%v, want existing user %d", signedIn.ID, needSignUp, created.ID)
	}
	if got, found, err := users.ByPhone(ctx, canonical); err != nil || !found || got.ID != created.ID {
		t.Fatalf("canonical lookup user=%+v found=%v err=%v", got, found, err)
	}
}

func verifyCodeForSignUp(t *testing.T, svc *Service, phone, hash, code string) {
	t.Helper()
	got, msg, needSignUp, err := svc.SignIn(context.Background(), domain.Authorization{}, phone, hash, code)
	if err != nil || !needSignUp || got.ID != 0 || msg.ID != 0 {
		t.Fatalf("SignIn before SignUp user=%+v message=%+v needSignUp=%v err=%v, want empty/empty/true/nil", got, msg, needSignUp, err)
	}
}

func TestSystemUserPhoneCannotLoginOrSignUp(t *testing.T) {
	ctx := context.Background()
	codes := memory.NewCodeStore()
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), codes, nil, nil, "12345")
	phone := domain.OfficialSystemUser().Phone

	if _, err := svc.SendCode(ctx, phone); !errors.Is(err, ErrSystemUserLoginForbidden) {
		t.Fatalf("SendCode official system phone err = %v, want ErrSystemUserLoginForbidden", err)
	}

	if err := codes.Set(ctx, "system-signin", store.PhoneCode{Phone: phone, Code: "12345"}, time.Minute); err != nil {
		t.Fatalf("seed sign-in code: %v", err)
	}
	if _, _, _, err := svc.SignIn(ctx, domain.Authorization{}, phone, "system-signin", "12345"); !errors.Is(err, ErrSystemUserLoginForbidden) {
		t.Fatalf("SignIn official system phone err = %v, want ErrSystemUserLoginForbidden", err)
	}

	if err := codes.Set(ctx, "system-email", store.PhoneCode{Phone: phone, Code: "12345"}, time.Minute); err != nil {
		t.Fatalf("seed email code: %v", err)
	}
	if _, _, _, err := svc.SignInWithEmail(ctx, domain.Authorization{}, phone, "system-email", "anything"); !errors.Is(err, ErrSystemUserLoginForbidden) {
		t.Fatalf("SignInWithEmail official system phone err = %v, want ErrSystemUserLoginForbidden", err)
	}

	if err := codes.Set(ctx, "system-signup", store.PhoneCode{Phone: phone, Code: "12345"}, time.Minute); err != nil {
		t.Fatalf("seed sign-up code: %v", err)
	}
	if _, _, err := svc.SignUp(ctx, domain.Authorization{}, phone, "system-signup", "System", "User"); !errors.Is(err, ErrSystemUserLoginForbidden) {
		t.Fatalf("SignUp official system phone err = %v, want ErrSystemUserLoginForbidden", err)
	}
}

func TestSystemUserAuthorizationIsRejectedAndRevoked(t *testing.T) {
	ctx := context.Background()
	authz := memory.NewAuthorizationStore()
	svc := NewService(memory.NewUserStore(), authz, memory.NewCodeStore(), nil, nil, "12345")

	authKeyID := [8]byte{0x71}
	if err := authz.Bind(ctx, domain.Authorization{AuthKeyID: authKeyID, UserID: domain.OfficialSystemUserID}); err != nil {
		t.Fatalf("bind system authorization: %v", err)
	}
	if got, found, err := svc.UserID(ctx, authKeyID); err != nil || found || got != 0 {
		t.Fatalf("UserID(system auth) = %d found=%v err=%v, want not found", got, found, err)
	}
	if _, found, err := authz.ByAuthKey(ctx, authKeyID); err != nil || found {
		t.Fatalf("system authorization after UserID found=%v err=%v, want deleted", found, err)
	}

	pendingAuthKeyID := [8]byte{0x72}
	if err := authz.Bind(ctx, domain.Authorization{AuthKeyID: pendingAuthKeyID, UserID: domain.OfficialSystemUserID, PasswordPending: true}); err != nil {
		t.Fatalf("bind pending system authorization: %v", err)
	}
	if got, pending, err := svc.PendingPasswordUserID(ctx, pendingAuthKeyID); err != nil || pending || got != 0 {
		t.Fatalf("PendingPasswordUserID(system auth) = %d pending=%v err=%v, want not pending", got, pending, err)
	}
	if _, found, err := authz.ByAuthKey(ctx, pendingAuthKeyID); err != nil || found {
		t.Fatalf("pending system authorization after lookup found=%v err=%v, want deleted", found, err)
	}

	if _, err := svc.BindVerifiedLogin(ctx, domain.Authorization{AuthKeyID: [8]byte{0x73}}, domain.OfficialSystemUserID); !errors.Is(err, ErrSystemUserLoginForbidden) {
		t.Fatalf("BindVerifiedLogin official system user err = %v, want ErrSystemUserLoginForbidden", err)
	}
	if _, err := svc.AcceptLoginToken(ctx, domain.Authorization{AuthKeyID: [8]byte{0x74}}, domain.OfficialSystemUserID); !errors.Is(err, ErrSystemUserLoginForbidden) {
		t.Fatalf("AcceptLoginToken official system user err = %v, want ErrSystemUserLoginForbidden", err)
	}
}

func TestMultipleAuthKeysKeepSeparateUsers(t *testing.T) {
	ctx := context.Background()
	authz := memory.NewAuthorizationStore()
	svc := NewService(memory.NewUserStore(), authz, memory.NewCodeStore(), nil, nil, "12345")
	var key1, key2 [8]byte
	key1[0] = 1
	key2[0] = 2

	hash1, err := svc.SendCode(ctx, "+15550005001")
	if err != nil {
		t.Fatalf("SendCode user1: %v", err)
	}
	verifyCodeForSignUp(t, svc, "+15550005001", hash1, "12345")
	user1, _, err := svc.SignUp(ctx, domain.Authorization{AuthKeyID: key1}, "+15550005001", hash1, "One", "")
	if err != nil {
		t.Fatalf("SignUp user1: %v", err)
	}
	hash2, err := svc.SendCode(ctx, "+15550005002")
	if err != nil {
		t.Fatalf("SendCode user2: %v", err)
	}
	verifyCodeForSignUp(t, svc, "+15550005002", hash2, "12345")
	user2, _, err := svc.SignUp(ctx, domain.Authorization{AuthKeyID: key2}, "+15550005002", hash2, "Two", "")
	if err != nil {
		t.Fatalf("SignUp user2: %v", err)
	}

	got1, found, err := svc.UserID(ctx, key1)
	if err != nil || !found || got1 != user1.ID {
		t.Fatalf("key1 user = %d found=%v err=%v, want %d", got1, found, err, user1.ID)
	}
	got2, found, err := svc.UserID(ctx, key2)
	if err != nil || !found || got2 != user2.ID {
		t.Fatalf("key2 user = %d found=%v err=%v, want %d", got2, found, err, user2.ID)
	}
	if got1 == got2 {
		t.Fatalf("auth keys mapped to same user id %d", got1)
	}
}

func TestLogOutThenSignInSameAuthKeySwitchesUser(t *testing.T) {
	ctx := context.Background()
	authz := memory.NewAuthorizationStore()
	svc := NewService(memory.NewUserStore(), authz, memory.NewCodeStore(), nil, nil, "12345")
	var key [8]byte
	key[0] = 9

	hash1, err := svc.SendCode(ctx, "+15550006001")
	if err != nil {
		t.Fatalf("SendCode user1: %v", err)
	}
	verifyCodeForSignUp(t, svc, "+15550006001", hash1, "12345")
	user1, _, err := svc.SignUp(ctx, domain.Authorization{AuthKeyID: key}, "+15550006001", hash1, "One", "")
	if err != nil {
		t.Fatalf("SignUp user1: %v", err)
	}
	if got, found, err := svc.UserID(ctx, key); err != nil || !found || got != user1.ID {
		t.Fatalf("initial auth user = %d found=%v err=%v, want %d", got, found, err, user1.ID)
	}
	if err := svc.LogOut(ctx, key); err != nil {
		t.Fatalf("LogOut: %v", err)
	}
	if got, found, err := svc.UserID(ctx, key); err != nil || found || got != 0 {
		t.Fatalf("after logout user = %d found=%v err=%v, want none", got, found, err)
	}

	hash2, err := svc.SendCode(ctx, "+15550006002")
	if err != nil {
		t.Fatalf("SendCode user2: %v", err)
	}
	verifyCodeForSignUp(t, svc, "+15550006002", hash2, "12345")
	user2, _, err := svc.SignUp(ctx, domain.Authorization{AuthKeyID: key}, "+15550006002", hash2, "Two", "")
	if err != nil {
		t.Fatalf("SignUp user2: %v", err)
	}
	if got, found, err := svc.UserID(ctx, key); err != nil || !found || got != user2.ID {
		t.Fatalf("after switch user = %d found=%v err=%v, want %d", got, found, err, user2.ID)
	}
	if user1.ID == user2.ID {
		t.Fatalf("user ids did not change after switch: %d", user1.ID)
	}
}

func TestResetAuthorizationKeepsProtocolAuthKeyForRPCLogout(t *testing.T) {
	ctx := context.Background()
	authz := memory.NewAuthorizationStore()
	keys := memory.NewAuthKeyStore()
	svc := NewService(memory.NewUserStore(), authz, memory.NewCodeStore(), keys, nil, "12345")
	key := [8]byte{0x31}
	if err := keys.Save(ctx, store.AuthKeyData{ID: key}); err != nil {
		t.Fatalf("save auth key: %v", err)
	}
	hash, err := svc.SendCode(ctx, "+15550007001")
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	verifyCodeForSignUp(t, svc, "+15550007001", hash, "12345")
	u, _, err := svc.SignUp(ctx, domain.Authorization{AuthKeyID: key}, "+15550007001", hash, "One", "")
	if err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	items, err := authz.ListByUser(ctx, u.ID)
	if err != nil || len(items) != 1 {
		t.Fatalf("ListByUser = %d err=%v, want one authorization", len(items), err)
	}

	deleted, found, err := svc.ResetAuthorization(ctx, u.ID, items[0].Hash)
	if err != nil || !found || deleted.AuthKeyID != key {
		t.Fatalf("ResetAuthorization deleted=%x found=%v err=%v, want key %x", deleted.AuthKeyID, found, err, key)
	}
	if _, found, err := keys.Get(ctx, key); err != nil || !found {
		t.Fatalf("auth key after reset found=%v err=%v, want present for RPC 401", found, err)
	}
	if _, found, err := svc.UserID(ctx, key); err != nil || found {
		t.Fatalf("user after reset found=%v err=%v, want missing", found, err)
	}
}

func TestResetAuthorizationsKeepsRevokedProtocolAuthKeys(t *testing.T) {
	ctx := context.Background()
	authz := memory.NewAuthorizationStore()
	keys := memory.NewAuthKeyStore()
	users := memory.NewUserStore()
	svc := NewService(users, authz, memory.NewCodeStore(), keys, nil, "12345")
	keep := [8]byte{0x41}
	revoked := [8]byte{0x42}
	for _, key := range [][8]byte{keep, revoked} {
		if err := keys.Save(ctx, store.AuthKeyData{ID: key}); err != nil {
			t.Fatalf("save auth key %x: %v", key, err)
		}
	}
	hash, err := svc.SendCode(ctx, "+15550007002")
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	verifyCodeForSignUp(t, svc, "+15550007002", hash, "12345")
	u, _, err := svc.SignUp(ctx, domain.Authorization{AuthKeyID: keep}, "+15550007002", hash, "Two", "")
	if err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	if err := authz.Bind(ctx, domain.Authorization{AuthKeyID: revoked, UserID: u.ID}); err != nil {
		t.Fatalf("bind revoked authorization: %v", err)
	}

	deleted, err := svc.ResetAuthorizations(ctx, u.ID, keep)
	if err != nil || len(deleted) != 1 || deleted[0].AuthKeyID != revoked {
		t.Fatalf("ResetAuthorizations deleted=%v err=%v, want revoked key", deleted, err)
	}
	if _, found, err := keys.Get(ctx, revoked); err != nil || !found {
		t.Fatalf("revoked auth key found=%v err=%v, want present for RPC 401", found, err)
	}
	if _, found, err := keys.Get(ctx, keep); err != nil || !found {
		t.Fatalf("kept auth key found=%v err=%v, want present", found, err)
	}
	if _, found, err := svc.UserID(ctx, revoked); err != nil || found {
		t.Fatalf("revoked user found=%v err=%v, want missing", found, err)
	}
	if got, found, err := svc.UserID(ctx, keep); err != nil || !found || got != u.ID {
		t.Fatalf("kept user=%d found=%v err=%v, want %d", got, found, err, u.ID)
	}
}

func TestSignUpWritesOfficialLoginMessage(t *testing.T) {
	ctx := context.Background()
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	delivery := &captureLoginCodeDelivery{}
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345",
		WithLoginMessages(messages, dialogs),
		WithLoginCodeDelivery(delivery),
	)

	hash, err := svc.SendCode(ctx, "+15550004311")
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	if len(delivery.requests) != 0 {
		t.Fatalf("unregistered SendCode delivered before user exists: %+v", delivery.requests)
	}
	verifyCodeForSignUp(t, svc, "+15550004311", hash, "12345")
	u, msg, err := svc.SignUp(ctx, domain.Authorization{}, "+15550004311", hash, "Test", "User")
	if err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	if len(delivery.requests) != 0 {
		t.Fatalf("SignUp unexpectedly used existing-account delivery: %+v", delivery.requests)
	}

	list, err := dialogs.ListByUser(ctx, u.ID, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(list.Dialogs) != 1 || list.Dialogs[0].Peer.ID != domain.OfficialSystemUserID {
		t.Fatalf("dialogs = %+v, want official system dialog", list.Dialogs)
	}
	if len(list.Users) != 1 || list.Users[0].ID != domain.OfficialSystemUserID || !list.Users[0].Verified || !list.Users[0].Support {
		t.Fatalf("users = %+v, want verified support system user", list.Users)
	}
	if msg.ID == 0 || !strings.Contains(msg.Body, "Login code: 12345") {
		t.Fatalf("login message = %+v, want returned official login code message", msg)
	}
	// SignUp now also fires the unconditional welcome message (777000), so the
	// official history holds both the bootstrap login-code message and the
	// welcome notification.
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID}
	history, err := messages.ListByUser(ctx, u.ID, domain.MessageFilter{HasPeer: true, Peer: peer, Limit: 10})
	if err != nil {
		t.Fatalf("ListByUser messages: %v", err)
	}
	if len(history.Messages) != 2 {
		t.Fatalf("messages = %+v, want login code message + welcome message", history.Messages)
	}
	var loginMsg, welcomeMsg domain.Message
	for _, m := range history.Messages {
		if strings.Contains(m.Body, "Login code: 12345") {
			loginMsg = m
		} else {
			welcomeMsg = m
		}
	}
	if loginMsg.ID == 0 || welcomeMsg.ID == 0 {
		t.Fatalf("messages = %+v, want one login code message and one welcome message", history.Messages)
	}
	if list.Dialogs[0].TopMessage != welcomeMsg.ID || list.Dialogs[0].UnreadCount != 2 {
		t.Fatalf("dialog top/unread = %+v, message ids = %d/%d", list.Dialogs[0], loginMsg.ID, welcomeMsg.ID)
	}
}

func TestSendCodeLoginMessagePreservesOfficialDialogReadWatermark(t *testing.T) {
	ctx := context.Background()
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	events := memory.NewUpdateEventStore()
	delivery := memory.NewLoginCodeDeliveryStore(messages, events)
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345",
		WithLoginMessages(messages, dialogs),
		WithLoginCodeDelivery(delivery),
	)
	phone := "+15550004312"

	hash, err := svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode signup: %v", err)
	}
	verifyCodeForSignUp(t, svc, phone, hash, "12345")
	u, _, err := svc.SignUp(ctx, domain.Authorization{}, phone, hash, "Test", "User")
	if err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID}
	// SignUp fired a welcome message (ID 2) right after the login code (ID 1),
	// so a full-dialog read now lands on the welcome message's ID.
	if read, err := dialogs.MarkRead(ctx, u.ID, peer, domain.MaxMessageBoxID); err != nil {
		t.Fatalf("MarkRead first login message: %v", err)
	} else if read.MaxID != 2 || read.StillUnreadCount != 0 {
		t.Fatalf("read first login message = %+v, want max_id 2 unread 0", read)
	}
	assertOfficialDialog := func(wantTop, wantRead, wantUnread int) {
		t.Helper()
		list, err := dialogs.ListByUser(ctx, u.ID, domain.DialogFilter{Limit: 10})
		if err != nil {
			t.Fatalf("ListByUser: %v", err)
		}
		if len(list.Dialogs) != 1 {
			t.Fatalf("dialogs = %+v, want official dialog", list.Dialogs)
		}
		got := list.Dialogs[0]
		if got.TopMessage != wantTop || got.ReadInboxMaxID != wantRead || got.UnreadCount != wantUnread {
			t.Fatalf("dialog = %+v, want top=%d read=%d unread=%d", got, wantTop, wantRead, wantUnread)
		}
	}
	latestLoginMessage := func(wantCount int) domain.Message {
		t.Helper()
		history, err := messages.ListByUser(ctx, u.ID, domain.MessageFilter{
			HasPeer: true,
			Peer:    peer,
			Limit:   10,
		})
		if err != nil || len(history.Messages) != wantCount {
			t.Fatalf("official history count=%d err=%v, want %d", len(history.Messages), err, wantCount)
		}
		latest := history.Messages[0]
		for _, msg := range history.Messages[1:] {
			if msg.ID > latest.ID {
				latest = msg
			}
		}
		return latest
	}

	hash, err = svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode signin second: %v", err)
	}
	// Official history after the second SendCode: login code (1), welcome (2),
	// new login code (3).
	second := latestLoginMessage(3)
	// Core sequencing: when SendCode returns, message/dialog/unread are already committed and SignIn has not happened yet.
	assertOfficialDialog(second.ID, 2, 1)
	_, signInMessage, needSignUp, err := svc.SignIn(ctx, domain.Authorization{}, phone, hash, "12345")
	if err != nil || needSignUp {
		t.Fatalf("SignIn second needSignUp=%v err=%v", needSignUp, err)
	}
	if signInMessage.ID != 0 {
		t.Fatalf("SignIn second returned a late login message %+v", signInMessage)
	}
	// SignIn adds only the welcome notification (never a late login code).
	assertOfficialDialog(latestLoginMessage(4).ID, 2, 2)

	hash, err = svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode signin third: %v", err)
	}
	third := latestLoginMessage(5)
	assertOfficialDialog(third.ID, 2, 3)
	_, signInMessage, needSignUp, err = svc.SignIn(ctx, domain.Authorization{}, phone, hash, "12345")
	if err != nil || needSignUp {
		t.Fatalf("SignIn third needSignUp=%v err=%v", needSignUp, err)
	}
	if signInMessage.ID != 0 {
		t.Fatalf("SignIn third returned a late login message %+v", signInMessage)
	}
	assertOfficialDialog(latestLoginMessage(6).ID, 2, 4)
}

func TestSignInExistingTwoFactorAccountNeedsPassword(t *testing.T) {
	ctx := context.Background()
	passwords := memory.NewPasswordStore()
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	delivery := memory.NewLoginCodeDeliveryStore(messages, memory.NewUpdateEventStore())
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345",
		WithPasswords(passwords),
		WithLoginCodeDelivery(delivery),
	)
	var key [8]byte
	key[0] = 7

	hash, err := svc.SendCode(ctx, "+15550004312")
	if err != nil {
		t.Fatalf("SendCode signup: %v", err)
	}
	verifyCodeForSignUp(t, svc, "+15550004312", hash, "12345")
	u, _, err := svc.SignUp(ctx, domain.Authorization{AuthKeyID: key}, "+15550004312", hash, "Two", "Factor")
	if err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	if err := svc.LogOut(ctx, key); err != nil {
		t.Fatalf("LogOut: %v", err)
	}
	if err := passwords.Save(ctx, u.ID, domain.PasswordSettings{HasPassword: true}); err != nil {
		t.Fatalf("save password settings: %v", err)
	}

	hash, err = svc.SendCode(ctx, "+15550004312")
	if err != nil {
		t.Fatalf("SendCode signin: %v", err)
	}
	got, signInMessage, needSignUp, err := svc.SignIn(ctx, domain.Authorization{AuthKeyID: key}, "+15550004312", hash, "12345")
	if !errors.Is(err, domain.ErrSessionPasswordNeeded) {
		t.Fatalf("SignIn err = %v, want ErrSessionPasswordNeeded", err)
	}
	if needSignUp || got.ID != u.ID {
		t.Fatalf("SignIn user=%+v needSignUp=%v, want existing 2FA user", got, needSignUp)
	}
	if signInMessage.ID != 0 {
		t.Fatalf("2FA SignIn returned a late login message %+v", signInMessage)
	}
	// Two-step verification incomplete: business authorization (UserID) must be treated as not logged in, to avoid bypassing 2FA.
	bound, found, err := svc.UserID(ctx, key)
	if err != nil || found || bound != 0 {
		t.Fatalf("UserID after password-needed = %d found=%v err=%v, want not-found", bound, found, err)
	}
	// But the user awaiting verification can still be located, for auth.checkPassword to continue.
	pendingUID, pending, err := svc.PendingPasswordUserID(ctx, key)
	if err != nil || !pending || pendingUID != u.ID {
		t.Fatalf("PendingPasswordUserID = %d pending=%v err=%v, want %d", pendingUID, pending, err, u.ID)
	}
	// After two-step verification passes, it becomes fully authorized.
	if err := svc.CompletePasswordSignIn(ctx, key, 0); !errors.Is(err, store.ErrAuthorizationStateChanged) {
		t.Fatalf("CompletePasswordSignIn without expected user err=%v, want authorization state changed", err)
	}
	if err := svc.CompletePasswordSignIn(ctx, key, u.ID); err != nil {
		t.Fatalf("CompletePasswordSignIn: %v", err)
	}
	bound, found, err = svc.UserID(ctx, key)
	if err != nil || !found || bound != u.ID {
		t.Fatalf("UserID after 2FA passed = %d found=%v err=%v, want %d", bound, found, err, u.ID)
	}
	list, err := dialogs.ListByUser(ctx, u.ID, domain.DialogFilter{Limit: 10})
	if err != nil || len(list.Messages) != 1 {
		t.Fatalf("2FA login-code messages after password = %+v err=%v, want exactly the SendCode message", list.Messages, err)
	}
}

func testAuthKey(seed byte) mtcrypto.AuthKey {
	var raw mtcrypto.Key
	for i := range raw {
		raw[i] = seed + byte(i)
	}
	return raw.WithID()
}

func saveAuthKey(t *testing.T, keys store.AuthKeyStore, key mtcrypto.AuthKey) {
	saveAuthKeyWithExpiry(t, keys, key, 0)
}

func saveAuthKeyWithExpiry(t *testing.T, keys store.AuthKeyStore, key mtcrypto.AuthKey, expiresAt int) {
	t.Helper()
	var value [256]byte
	copy(value[:], key.Value[:])
	if err := keys.Save(context.Background(), store.AuthKeyData{ID: key.ID, Value: value, ExpiresAt: expiresAt}); err != nil {
		t.Fatalf("save auth key: %v", err)
	}
}
