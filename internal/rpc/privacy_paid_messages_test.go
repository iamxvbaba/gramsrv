package rpc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appaccount "telesrv/internal/app/account"
	appprivacy "telesrv/internal/app/privacy"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func newPrivateRequirementRouter(t *testing.T, senderPremium bool) (*Router, *appaccount.Service, *appprivacy.Service, *memory.ContactStore, domain.User, domain.User) {
	t.Helper()
	ctx := context.Background()
	usersStore := memory.NewUserStore()
	aliceInput := domain.User{AccessHash: 11, Phone: "15550008101", FirstName: "Alice"}
	if senderPremium {
		aliceInput.PremiumUntil = int(time.Now().Add(time.Hour).Unix())
	}
	alice, err := usersStore.Create(ctx, aliceInput)
	if err != nil {
		t.Fatalf("create Alice: %v", err)
	}
	bob, err := usersStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550008102", FirstName: "Bob"})
	if err != nil {
		t.Fatalf("create Bob: %v", err)
	}
	users := appusers.NewService(usersStore)
	contacts := memory.NewContactStore()
	privacy := appprivacy.NewService(memory.NewPrivacyStore(), contacts).ConfigureReadModels(users, nil)
	settingsStore := memory.NewPasswordStore()
	account := appaccount.NewService(settingsStore, appaccount.WithAccountSettings(settingsStore))
	router := New(Config{}, Deps{
		Account:  account,
		Privacy:  privacy,
		Users:    users,
		Messages: &captureMessages{},
	}, zaptest.NewLogger(t), clock.System)
	return router, account, privacy, contacts, alice, bob
}

func TestPrivateContactRequirementUsesNoPaidMessagesReadModel(t *testing.T) {
	ctx := context.Background()
	r, account, privacy, contacts, alice, bob := newPrivateRequirementRouter(t, false)
	if _, err := account.SetGlobalPrivacy(ctx, bob.ID, domain.GlobalPrivacy{NoncontactPeersPaidStars: 7}); err != nil {
		t.Fatalf("set Bob paid requirement: %v", err)
	}
	full, err := r.onUsersGetFullUser(WithUserID(ctx, alice.ID), &tg.InputUser{
		UserID: bob.ID, AccessHash: bob.AccessHash,
	})
	if err != nil {
		t.Fatalf("get Bob full user: %v", err)
	}
	if stars, ok := full.FullUser.GetSendPaidMessagesStars(); !ok || stars != 7 {
		t.Fatalf("full user paid stars = %d, %v; want 7, true", stars, ok)
	}
	projected, ok := full.Users[0].(*tg.User)
	if !ok {
		t.Fatalf("projected user = %T, want *tg.User", full.Users[0])
	}
	if stars, ok := projected.GetSendPaidMessagesStars(); !ok || stars != 7 {
		t.Fatalf("user paid stars = %d, %v; want 7, true", stars, ok)
	}
	requirements, err := r.onUsersGetRequirementsToContact(WithUserID(ctx, alice.ID), []tg.InputUserClass{
		&tg.InputUser{UserID: bob.ID, AccessHash: bob.AccessHash},
	})
	if err != nil {
		t.Fatalf("get requirements: %v", err)
	}
	if paid, ok := requirements[0].(*tg.RequirementToContactPaidMessages); !ok || paid.StarsAmount != 7 {
		t.Fatalf("requirement = %#v, want paid 7", requirements[0])
	}

	send := func(randomID int64, allow int64) error {
		req := &tg.MessagesSendMessageRequest{
			Peer:     &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
			Message:  "hello",
			RandomID: randomID,
		}
		if allow != 0 {
			req.SetAllowPaidStars(allow)
		}
		_, err := r.onMessagesSendMessage(WithUserID(ctx, alice.ID), req)
		return err
	}

	if err := send(81001, 0); err == nil || !strings.Contains(err.Error(), "ALLOW_PAYMENT_REQUIRED") || !strings.Contains(err.Error(), "(7)") {
		t.Fatalf("non-exempt send err = %v, want ALLOW_PAYMENT_REQUIRED 7", err)
	}
	if err := send(81002, 7); err == nil || !strings.Contains(err.Error(), "PAYMENT_UNSUPPORTED") {
		t.Fatalf("authorized paid send err = %v, want explicit unsupported ledger", err)
	}

	if _, err := privacy.SetRules(ctx, bob.ID, domain.PrivacyKeyNoPaidMessages, []domain.PrivacyRule{{
		Kind:    domain.PrivacyRuleAllowUsers,
		UserIDs: []int64{alice.ID},
	}, {
		Kind: domain.PrivacyRuleDisallowAll,
	}}); err != nil {
		t.Fatalf("allow Alice in NoPaidMessages: %v", err)
	}
	if err := send(81003, 0); err != nil {
		t.Fatalf("explicit no-paid exception send: %v", err)
	}
	full, err = r.onUsersGetFullUser(WithUserID(ctx, alice.ID), &tg.InputUser{
		UserID: bob.ID, AccessHash: bob.AccessHash,
	})
	if err != nil {
		t.Fatalf("get exempt Bob full user: %v", err)
	}
	if _, ok := full.FullUser.GetSendPaidMessagesStars(); ok || full.FullUser.ContactRequirePremium {
		t.Fatalf("exempt full user still carries contact restriction: %+v", full.FullUser)
	}

	if _, err := privacy.SetRules(ctx, bob.ID, domain.PrivacyKeyNoPaidMessages, []domain.PrivacyRule{{Kind: domain.PrivacyRuleDisallowAll}}); err != nil {
		t.Fatalf("clear explicit exception: %v", err)
	}
	if _, err := contacts.Upsert(ctx, bob.ID, domain.ContactInput{
		ContactUserID: alice.ID,
		FirstName:     "Alice",
	}); err != nil {
		t.Fatalf("Bob add Alice contact: %v", err)
	}
	if err := send(81004, 0); err != nil {
		t.Fatalf("recipient contact must be free: %v", err)
	}
}

func TestPrivateContactRequirementPremiumGateUsesViewerFactsReadModel(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name          string
		senderPremium bool
		wantErr       bool
	}{
		{name: "non-premium blocked", wantErr: true},
		{name: "premium allowed", senderPremium: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, account, _, _, alice, bob := newPrivateRequirementRouter(t, tc.senderPremium)
			if _, err := account.SetGlobalPrivacy(ctx, bob.ID, domain.GlobalPrivacy{NewNoncontactPeersRequirePremium: true}); err != nil {
				t.Fatalf("set premium requirement: %v", err)
			}
			_, err := r.onMessagesSendMessage(WithUserID(ctx, alice.ID), &tg.MessagesSendMessageRequest{
				Peer:     &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
				Message:  "hello",
				RandomID: 82001,
			})
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "PREMIUM_ACCOUNT_REQUIRED") {
					t.Fatalf("send err = %v, want PREMIUM_ACCOUNT_REQUIRED", err)
				}
			} else if err != nil {
				t.Fatalf("premium sender should pass: %v", err)
			}
		})
	}
}
