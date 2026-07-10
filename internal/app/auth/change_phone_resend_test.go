package auth

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

func TestResendCodePreservesChangePhoneScopeAndSMSDelivery(t *testing.T) {
	ctx := context.Background()
	codes := memory.NewCodeStore()
	authKeyID := [8]byte{8, 7, 6}
	rec := store.PhoneCode{
		Phone:       "15550014001",
		Code:        "old",
		Channel:     codeChannelPhone,
		Purpose:     store.PhoneCodePurposeChangePhone,
		UserID:      42,
		AuthKeyID:   authKeyID,
		SessionID:   77,
		Attempts:    2,
		MaxAttempts: 5,
	}
	if err := codes.Set(ctx, "old-hash", rec, time.Minute); err != nil {
		t.Fatalf("set old code: %v", err)
	}
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), codes, nil, nil, "12345", WithCodeTTL(time.Minute))
	if _, err := svc.ResendCodeForAuthKey(ctx, [8]byte{1}, rec.Phone, "old-hash"); err != ErrCodeInvalid {
		t.Fatalf("cross-auth resend err = %v", err)
	}
	if _, found, _ := codes.Get(ctx, "old-hash"); !found {
		t.Fatal("cross-auth resend invalidated victim hash")
	}
	hash, err := svc.ResendCodeForAuthKey(ctx, authKeyID, rec.Phone, "old-hash")
	if err != nil {
		t.Fatalf("resend change code: %v", err)
	}
	if hash == "" || hash == "old-hash" {
		t.Fatalf("new hash = %q", hash)
	}
	if _, found, _ := codes.Get(ctx, "old-hash"); found {
		t.Fatal("old hash remains valid")
	}
	got, found, err := codes.Get(ctx, hash)
	if err != nil || !found {
		t.Fatalf("new code found=%v err=%v", found, err)
	}
	if got.Purpose != rec.Purpose || got.UserID != rec.UserID || got.AuthKeyID != rec.AuthKeyID || got.SessionID != rec.SessionID || got.Code != "12345" || got.Attempts != 0 {
		t.Fatalf("resent scoped code = %+v", got)
	}
	delivery, found, err := svc.CodeDelivery(ctx, hash)
	if err != nil || !found || delivery.Kind != domain.AuthCodeDeliverySMS || delivery.Length != 5 {
		t.Fatalf("delivery = %+v found=%v err=%v", delivery, found, err)
	}
	if err := svc.CancelCodeForAuthKey(ctx, [8]byte{2}, rec.Phone, hash); err != ErrCodeInvalid {
		t.Fatalf("cross-auth cancel err = %v", err)
	}
	if _, found, _ := codes.Get(ctx, hash); !found {
		t.Fatal("cross-auth cancel invalidated victim hash")
	}
	if err := svc.CancelCodeForAuthKey(ctx, authKeyID, rec.Phone, hash); err != nil {
		t.Fatalf("scoped cancel: %v", err)
	}
	if _, found, _ := codes.Get(ctx, hash); found {
		t.Fatal("scoped cancel left hash valid")
	}
}
