package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// TestAccountRegisterDeviceDisabledIsNoop locks in the pre-feature behaviour: with
// no PushTokenStore wired, register/unregister return true without a store call,
// even for an empty token.
func TestAccountRegisterDeviceDisabledIsNoop(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), 1001)

	ok, err := r.onAccountRegisterDevice(ctx, &tg.AccountRegisterDeviceRequest{TokenType: domain.PushTokenTypeFCM})
	if err != nil || !ok {
		t.Fatalf("disabled register = %v, %v; want true, nil", ok, err)
	}
	if ok, err := r.onAccountUnregisterDevice(ctx, &tg.AccountUnregisterDeviceRequest{}); err != nil || !ok {
		t.Fatalf("disabled unregister = %v, %v; want true, nil", ok, err)
	}
}

// TestAccountRegisterDevicePersistsAndReassigns covers the enabled path: the token
// is trimmed and stored, re-registering it for another account reassigns it,
// unregister is owner-scoped, and an empty token is rejected with TOKEN_EMPTY.
func TestAccountRegisterDevicePersistsAndReassigns(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	store := memory.NewPushTokenStore()
	r.deps.PushTokens = store
	ctx := WithUserID(context.Background(), 1001)

	ok, err := r.onAccountRegisterDevice(ctx, &tg.AccountRegisterDeviceRequest{
		TokenType: domain.PushTokenTypeFCM, Token: " android-fcm-token ",
		Secret: []byte{7, 8}, NoMuted: true,
	})
	if err != nil || !ok {
		t.Fatalf("register = %v, %v; want true, nil", ok, err)
	}
	tokens, err := store.ListPushTokensForUser(ctx, 1001)
	if err != nil || len(tokens) != 1 {
		t.Fatalf("list user 1001 = %d, %v", len(tokens), err)
	}
	if got := tokens[0]; got.Token != "android-fcm-token" || !got.NoMuted || len(got.Secret) != 2 {
		t.Fatalf("stored token = %+v", got)
	}

	other := WithUserID(context.Background(), 2002)
	if ok, err := r.onAccountRegisterDevice(other, &tg.AccountRegisterDeviceRequest{
		TokenType: domain.PushTokenTypeFCM, Token: "android-fcm-token",
	}); err != nil || !ok {
		t.Fatalf("reassign = %v, %v", ok, err)
	}
	if tokens, _ := store.ListPushTokensForUser(ctx, 1001); len(tokens) != 0 {
		t.Fatalf("old owner still has %d tokens, want 0", len(tokens))
	}
	if tokens, _ := store.ListPushTokensForUser(other, 2002); len(tokens) != 1 {
		t.Fatalf("new owner has %d tokens, want 1", len(tokens))
	}

	if ok, err := r.onAccountUnregisterDevice(other, &tg.AccountUnregisterDeviceRequest{
		TokenType: domain.PushTokenTypeFCM, Token: "android-fcm-token",
	}); err != nil || !ok {
		t.Fatalf("unregister = %v, %v; want true, nil", ok, err)
	}
	if tokens, _ := store.ListPushTokensForUser(other, 2002); len(tokens) != 0 {
		t.Fatalf("after unregister = %d tokens, want 0", len(tokens))
	}

	if ok, err := r.onAccountRegisterDevice(ctx, &tg.AccountRegisterDeviceRequest{
		TokenType: domain.PushTokenTypeFCM, Token: "   ",
	}); ok || !tgerr.Is(err, "TOKEN_EMPTY") {
		t.Fatalf("empty token = %v, %v; want TOKEN_EMPTY", ok, err)
	}
}
