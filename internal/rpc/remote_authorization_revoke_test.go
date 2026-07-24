package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	appauth "telesrv/internal/app/auth"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

func TestAccountResetAuthorizationKeepsProtocolKeyAndReturnsRPC401(t *testing.T) {
	ctx := context.Background()
	currentAuthKeyID := [8]byte{0x71}
	targetAuthKeyID := [8]byte{0x72}
	const (
		userID     = int64(1000000001)
		targetHash = int64(2026072401)
	)

	authKeys := memory.NewAuthKeyStore()
	authorizations := memory.NewAuthorizationStore()
	for _, authKeyID := range [][8]byte{currentAuthKeyID, targetAuthKeyID} {
		if err := authKeys.Save(ctx, store.AuthKeyData{ID: authKeyID}); err != nil {
			t.Fatalf("save auth key %x: %v", authKeyID, err)
		}
	}
	authService := appauth.NewService(nil, authorizations, nil, authKeys, nil, "12345")
	if err := authorizations.Bind(ctx, domain.Authorization{
		AuthKeyID: currentAuthKeyID,
		UserID:    userID,
		Hash:      2026072400,
	}); err != nil {
		t.Fatalf("bind current authorization: %v", err)
	}
	if err := authorizations.Bind(ctx, domain.Authorization{
		AuthKeyID: targetAuthKeyID,
		UserID:    userID,
		Hash:      targetHash,
	}); err != nil {
		t.Fatalf("bind target authorization: %v", err)
	}

	r := New(Config{}, Deps{
		Auth:  authService,
		Files: &fakeFiles{},
	}, zaptest.NewLogger(t), clock.System)

	var warmTarget bin.Buffer
	if err := (&tg.UploadSaveFilePartRequest{
		FileID:   1,
		FilePart: 0,
		Bytes:    []byte{1},
	}).Encode(&warmTarget); err != nil {
		t.Fatalf("encode target warm-up RPC: %v", err)
	}
	if _, err := r.Dispatch(ctx, targetAuthKeyID, 101, &warmTarget); err != nil {
		t.Fatalf("target warm-up RPC: %v", err)
	}

	var reset bin.Buffer
	if err := (&tg.AccountResetAuthorizationRequest{Hash: targetHash}).Encode(&reset); err != nil {
		t.Fatalf("encode account.resetAuthorization: %v", err)
	}
	if result, err := r.Dispatch(ctx, currentAuthKeyID, 102, &reset); err != nil {
		t.Fatalf("account.resetAuthorization: %v", err)
	} else if value, ok := dispatchCanonicalValue(result).(bool); !ok || !value {
		t.Fatalf("account.resetAuthorization result = %#v, want true", dispatchCanonicalValue(result))
	}

	if _, found, err := authKeys.Get(ctx, targetAuthKeyID); err != nil || !found {
		t.Fatalf("target protocol auth key found=%v err=%v, want retained", found, err)
	}
	if _, found, err := authorizations.ByAuthKey(ctx, targetAuthKeyID); err != nil || found {
		t.Fatalf("target business authorization found=%v err=%v, want removed", found, err)
	}
	if current, found, err := authorizations.ByAuthKey(ctx, currentAuthKeyID); err != nil || !found || current.UserID != userID {
		t.Fatalf("current authorization=%+v found=%v err=%v, want retained user %d", current, found, err, userID)
	}

	var afterRevoke bin.Buffer
	if err := (&tg.UploadSaveFilePartRequest{
		FileID:   1,
		FilePart: 1,
		Bytes:    []byte{2},
	}).Encode(&afterRevoke); err != nil {
		t.Fatalf("encode target post-revoke RPC: %v", err)
	}
	if _, err := r.Dispatch(ctx, targetAuthKeyID, 103, &afterRevoke); !tgerr.Is(err, "AUTH_KEY_UNREGISTERED") {
		t.Fatalf("target post-revoke RPC err=%v, want AUTH_KEY_UNREGISTERED", err)
	}

	var logout bin.Buffer
	if err := (&tg.AuthLogOutRequest{}).Encode(&logout); err != nil {
		t.Fatalf("encode target auth.logOut cleanup: %v", err)
	}
	if result, err := r.Dispatch(ctx, targetAuthKeyID, 104, &logout); err != nil {
		t.Fatalf("target auth.logOut cleanup: %v", err)
	} else if _, ok := dispatchCanonicalValue(result).(*tg.AuthLoggedOut); !ok {
		t.Fatalf("target auth.logOut cleanup result=%#v, want *tg.AuthLoggedOut", dispatchCanonicalValue(result))
	}
	if _, found, err := authKeys.Get(ctx, targetAuthKeyID); err != nil || !found {
		t.Fatalf("target protocol auth key after logout cleanup found=%v err=%v, want retained", found, err)
	}
}
