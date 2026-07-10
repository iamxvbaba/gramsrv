package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/clock"
	"github.com/gotd/td/tg"
	"go.uber.org/zap/zaptest"

	appaccount "telesrv/internal/app/account"
	appupdates "telesrv/internal/app/updates"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestAccountChangePhoneRPCReturnsSelfPushesOthersAndReplaysDifference(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	auths := memory.NewAuthorizationStore()
	codes := memory.NewCodeStore()
	events := memory.NewUpdateEventStore()
	user, err := users.Create(ctx, domain.User{AccessHash: 401, Phone: "15550013001", FirstName: "Alice"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	authKeyID := [8]byte{4, 3, 2, 1}
	if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: authKeyID, UserID: user.ID, CreatedAt: time.Now().Add(-48 * time.Hour)}); err != nil {
		t.Fatalf("bind auth: %v", err)
	}
	accountSvc := appaccount.NewService(
		memory.NewPasswordStore(),
		appaccount.WithUsers(users),
		appaccount.WithPhoneChange(memory.NewPhoneChangeStore(users, events), auths, codes, nil, "12345", time.Minute, 5),
	)
	sessions := &captureSessions{}
	r := New(Config{}, Deps{Account: accountSvc, Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	reqCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, user.ID), authKeyID), 77)

	sentClass, err := r.onAccountSendChangePhoneCode(reqCtx, &tg.AccountSendChangePhoneCodeRequest{PhoneNumber: "+1 555 001 3002"})
	if err != nil {
		t.Fatalf("send change phone code: %v", err)
	}
	sent, ok := sentClass.(*tg.AuthSentCode)
	if !ok {
		t.Fatalf("sent code = %T", sentClass)
	}
	if _, ok := sent.Type.(*tg.AuthSentCodeTypeSMS); !ok || sent.PhoneCodeHash == "" {
		t.Fatalf("sent code type/hash = %T/%q", sent.Type, sent.PhoneCodeHash)
	}

	userClass, err := r.onAccountChangePhone(reqCtx, &tg.AccountChangePhoneRequest{
		PhoneNumber:   "15550013002",
		PhoneCodeHash: sent.PhoneCodeHash,
		PhoneCode:     "12345",
	})
	if err != nil {
		t.Fatalf("change phone: %v", err)
	}
	self, ok := userClass.(*tg.User)
	if !ok || self.ID != user.ID || self.Phone != "15550013002" {
		t.Fatalf("returned self = %T %+v", userClass, userClass)
	}

	otherPush, ok := sessions.lastUserPush().(*tg.Updates)
	if !ok || len(otherPush.Updates) != 2 {
		t.Fatalf("other-session push = %T %+v", sessions.lastUserPush(), sessions.lastUserPush())
	}
	phoneUpdate, ok := otherPush.Updates[0].(*tg.UpdateUserPhone)
	if !ok || phoneUpdate.UserID != user.ID || phoneUpdate.Phone != "15550013002" {
		t.Fatalf("phone update = %T %+v", otherPush.Updates[0], otherPush.Updates[0])
	}
	if _, ok := otherPush.Updates[1].(*tg.UpdateDeleteMessages); !ok {
		t.Fatalf("pts bookkeeping = %T", otherPush.Updates[1])
	}
	currentPush, ok := sessions.snapshot().message.(*tg.Updates)
	if !ok || len(currentPush.Updates) != 1 {
		t.Fatalf("current-session bookkeeping = %T %+v", sessions.snapshot().message, sessions.snapshot().message)
	}
	if _, ok := currentPush.Updates[0].(*tg.UpdateDeleteMessages); !ok {
		t.Fatalf("current bookkeeping update = %T", currentPush.Updates[0])
	}

	updateSvc := appupdates.NewService(memory.NewUpdateStateStore(), events)
	diff, err := updateSvc.GetDifference(ctx, [8]byte{9}, user.ID, domain.UpdateState{Pts: 0})
	if err != nil {
		t.Fatalf("get difference: %v", err)
	}
	tgDiff, ok := tgUpdatesDifference(user.ID, diff).(*tg.UpdatesDifference)
	if !ok || len(tgDiff.OtherUpdates) != 1 {
		t.Fatalf("difference = %T %+v", tgUpdatesDifference(user.ID, diff), tgUpdatesDifference(user.ID, diff))
	}
	replayed, ok := tgDiff.OtherUpdates[0].(*tg.UpdateUserPhone)
	if !ok || replayed.UserID != user.ID || replayed.Phone != "15550013002" {
		t.Fatalf("replayed update = %T %+v", tgDiff.OtherUpdates[0], tgDiff.OtherUpdates[0])
	}
}

func TestAccountChangePhoneRPCMapsCodeAndOccupiedErrors(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	auths := memory.NewAuthorizationStore()
	codes := memory.NewCodeStore()
	events := memory.NewUpdateEventStore()
	user, _ := users.Create(ctx, domain.User{AccessHash: 411, Phone: "15550013101", FirstName: "Alice"})
	occupied, _ := users.Create(ctx, domain.User{AccessHash: 412, Phone: "15550013102", FirstName: "Bob"})
	authKeyID := [8]byte{5, 4, 3, 2}
	if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: authKeyID, UserID: user.ID}); err != nil {
		t.Fatalf("bind auth: %v", err)
	}
	accountSvc := appaccount.NewService(memory.NewPasswordStore(),
		appaccount.WithUsers(users),
		appaccount.WithPhoneChange(memory.NewPhoneChangeStore(users, events), auths, codes, nil, "12345", time.Minute, 5))
	r := New(Config{}, Deps{Account: accountSvc}, zaptest.NewLogger(t), clock.System)
	reqCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, user.ID), authKeyID), 88)

	if _, err := r.onAccountSendChangePhoneCode(reqCtx, &tg.AccountSendChangePhoneCodeRequest{PhoneNumber: occupied.Phone}); err == nil {
		t.Fatal("occupied phone unexpectedly accepted")
	} else {
		assertPhoneRPCErr(t, err, "PHONE_NUMBER_OCCUPIED")
	}
	if _, err := r.onAccountChangePhone(reqCtx, &tg.AccountChangePhoneRequest{PhoneNumber: "15550013103"}); err == nil {
		t.Fatal("empty code unexpectedly accepted")
	} else {
		assertPhoneRPCErr(t, err, "PHONE_CODE_EMPTY")
	}
}
