package rpc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	appaccount "telesrv/internal/app/account"
	appprivacy "telesrv/internal/app/privacy"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type noPaidTokenFixture struct {
	r        *Router
	account  *appaccount.Service
	messages *captureMessages
	tokens   *memory.NoPaidMessagesTokenStore
	alice    domain.User // redeemer
	bob      domain.User // owner who charges Stars
	carol    domain.User // no username
}

func newNoPaidTokenFixture(t *testing.T) noPaidTokenFixture {
	t.Helper()
	ctx := context.Background()
	usersStore := memory.NewUserStore()
	create := func(u domain.User) domain.User {
		created, err := usersStore.Create(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	alice := create(domain.User{AccessHash: 11, Phone: "15550009101", FirstName: "Alice"})
	bob := create(domain.User{AccessHash: 22, Phone: "15550009102", FirstName: "Bob", Username: "bob_paid"})
	carol := create(domain.User{AccessHash: 33, Phone: "15550009103", FirstName: "Carol"})
	users := appusers.NewService(usersStore)
	privacy := appprivacy.NewService(memory.NewPrivacyStore(), memory.NewContactStore()).ConfigureReadModels(users, nil)
	settings := memory.NewPasswordStore()
	account := appaccount.NewService(settings, appaccount.WithAccountSettings(settings))
	messages := &captureMessages{}
	tokens := memory.NewNoPaidMessagesTokenStore()
	r := New(Config{PublicBaseURL: "https://sgq.me"}, Deps{
		Account: account, Privacy: privacy, Users: users, Messages: messages,
		NoPaidMessagesTokens: tokens,
	}, zaptest.NewLogger(t), clock.System)
	return noPaidTokenFixture{r: r, account: account, messages: messages, tokens: tokens, alice: alice, bob: bob, carol: carol}
}

func TestNoPaidMessagesTokenExportReusesAndRevokes(t *testing.T) {
	f := newNoPaidTokenFixture(t)
	ctx := WithUserID(context.Background(), f.bob.ID)

	permanent, err := f.r.onContactsExportNoPaidMessagesToken(ctx, &tg.ContactsExportNoPaidMessagesTokenRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(permanent.Token) < 16 || permanent.URL != "https://sgq.me/bob_paid?free="+permanent.Token {
		t.Fatalf("permanent token = %+v", permanent)
	}
	if _, ok := permanent.GetExpires(); ok {
		t.Fatal("permanent token carries an expiry")
	}
	again, err := f.r.onContactsExportNoPaidMessagesToken(ctx, &tg.ContactsExportNoPaidMessagesTokenRequest{})
	if err != nil || again.Token != permanent.Token {
		t.Fatalf("re-export = %+v, %v; want the same active token", again, err)
	}

	temporary, err := f.r.onContactsExportNoPaidMessagesToken(ctx, &tg.ContactsExportNoPaidMessagesTokenRequest{Temporary: true})
	if err != nil {
		t.Fatal(err)
	}
	expires, ok := temporary.GetExpires()
	if !ok || temporary.Token == permanent.Token || int64(expires) < time.Now().Add(23*time.Hour).Unix() {
		t.Fatalf("temporary token = %+v", temporary)
	}

	revoked, err := f.r.onContactsExportNoPaidMessagesToken(ctx, &tg.ContactsExportNoPaidMessagesTokenRequest{Revoke: true})
	if err != nil || revoked.Token == permanent.Token {
		t.Fatalf("revoke = %+v, %v; want a new token", revoked, err)
	}
	if _, ok, _ := f.tokens.ResolveNoPaidMessagesToken(context.Background(), f.bob.ID, permanent.Token, time.Now()); ok {
		t.Fatal("revoked token still resolves")
	}
	if _, ok, _ := f.tokens.ResolveNoPaidMessagesToken(context.Background(), f.bob.ID, temporary.Token, time.Now()); !ok {
		t.Fatal("revoking the permanent token revoked the temporary one")
	}

	if _, err := f.r.onContactsExportNoPaidMessagesToken(WithUserID(context.Background(), f.carol.ID), &tg.ContactsExportNoPaidMessagesTokenRequest{}); !tgerr.Is(err, "USERNAME_REQUIRED") {
		t.Fatalf("no-username export err = %v, want USERNAME_REQUIRED", err)
	}
}

func TestNoPaidMessagesTokenImportExemptsRedeemer(t *testing.T) {
	f := newNoPaidTokenFixture(t)
	bg := context.Background()
	if _, err := f.account.SetGlobalPrivacy(bg, f.bob.ID, domain.GlobalPrivacy{NoncontactPeersPaidStars: 9}); err != nil {
		t.Fatal(err)
	}
	exported, err := f.r.onContactsExportNoPaidMessagesToken(WithUserID(bg, f.bob.ID), &tg.ContactsExportNoPaidMessagesTokenRequest{})
	if err != nil {
		t.Fatal(err)
	}
	aliceCtx := WithUserID(bg, f.alice.ID)
	bobInput := &tg.InputUser{UserID: f.bob.ID, AccessHash: f.bob.AccessHash}

	before, err := f.r.privateContactRestrictionFor(bg, f.alice.ID, f.bob.ID)
	if err != nil || before.paidStars != 9 {
		t.Fatalf("restriction before = %+v, %v; want 9 stars", before, err)
	}
	for name, token := range map[string]string{"unknown": "nope-not-a-real-token", "empty": ""} {
		if _, err := f.r.onContactsImportNoPaidMessagesToken(aliceCtx, &tg.ContactsImportNoPaidMessagesTokenRequest{UserID: bobInput, Token: token}); !tgerr.Is(err, "NO_PAID_MESSAGES_TOKEN_INVALID") {
			t.Fatalf("%s token err = %v", name, err)
		}
	}
	// A token is bound to its owner: presenting Bob's token for Carol fails.
	carolInput := &tg.InputUser{UserID: f.carol.ID, AccessHash: f.carol.AccessHash}
	if _, err := f.r.onContactsImportNoPaidMessagesToken(aliceCtx, &tg.ContactsImportNoPaidMessagesTokenRequest{UserID: carolInput, Token: exported.Token}); !tgerr.Is(err, "NO_PAID_MESSAGES_TOKEN_INVALID") {
		t.Fatalf("foreign owner err = %v", err)
	}
	if _, err := f.r.onContactsImportNoPaidMessagesToken(WithUserID(bg, f.bob.ID), &tg.ContactsImportNoPaidMessagesTokenRequest{UserID: &tg.InputUserSelf{}, Token: exported.Token}); !tgerr.Is(err, "USER_ID_INVALID") {
		t.Fatalf("self import err = %v", err)
	}

	got, err := f.r.onContactsImportNoPaidMessagesToken(aliceCtx, &tg.ContactsImportNoPaidMessagesTokenRequest{UserID: bobInput, Token: exported.Token})
	if err != nil {
		t.Fatal(err)
	}
	user, ok := got.(*tg.User)
	if !ok || user.ID != f.bob.ID {
		t.Fatalf("import result = %#v", got)
	}
	if stars, ok := user.GetSendPaidMessagesStars(); ok || stars != 0 {
		t.Fatalf("redeemer still charged %d stars", stars)
	}
	after, err := f.r.privateContactRestrictionFor(bg, f.alice.ID, f.bob.ID)
	if err != nil || after.paidStars != 0 {
		t.Fatalf("restriction after = %+v, %v; want free", after, err)
	}
	req := f.messages.sendReq
	if f.messages.sendUserID != f.alice.ID || req.SenderUserID != f.alice.ID || req.RecipientUserID != f.bob.ID ||
		req.Media == nil || req.Media.ServiceAction == nil ||
		req.Media.ServiceAction.Kind != domain.MessageServiceActionNoPaidMessagesTokenUsed {
		t.Fatalf("service message = user %d %+v", f.messages.sendUserID, req)
	}
	if !strings.HasPrefix(exported.URL, "https://sgq.me/") {
		t.Fatalf("url = %q", exported.URL)
	}

	// Redeeming again is idempotent and posts no second notice.
	f.messages.sendReq = domain.SendPrivateTextRequest{}
	if _, err := f.r.onContactsImportNoPaidMessagesToken(aliceCtx, &tg.ContactsImportNoPaidMessagesTokenRequest{UserID: bobInput, Token: exported.Token}); err != nil {
		t.Fatal(err)
	}
	if f.messages.sendReq.Media != nil {
		t.Fatal("second redemption posted another notice")
	}
}

func TestNoPaidMessagesTokenUsedActionConverts(t *testing.T) {
	action := tgMessageServiceAction(domain.Message{Media: &domain.MessageMedia{
		Kind:          domain.MessageMediaKindService,
		ServiceAction: &domain.MessageServiceAction{Kind: domain.MessageServiceActionNoPaidMessagesTokenUsed},
	}})
	if _, ok := action.(*tg.MessageActionNoPaidMessagesTokenUsed); !ok {
		t.Fatalf("action = %T", action)
	}
}
