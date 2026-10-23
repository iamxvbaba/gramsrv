package admin

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

type packMessages struct {
	fakeMessagesService
	sent []domain.SendPrivateTextRequest
}

func (p *packMessages) SendPrivateText(_ context.Context, userID int64, req domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error) {
	if userID != domain.OfficialSystemUserID || req.SenderUserID != domain.OfficialSystemUserID {
		panic("gift pack must come from the system notifications account")
	}
	p.sent = append(p.sent, req)
	return domain.SendPrivateTextResult{}, nil
}

func TestGrantsArriveAsGiftPacks(t *testing.T) {
	ctx := context.Background()
	msgs := &packMessages{}
	svc := NewService(Dependencies{
		Commands: newMemoryCommandRepo(),
		Users: &fakeUsersService{users: map[int64]domain.User{
			1001: {ID: 1001, Phone: "1001", FirstName: "Alice"},
		}},
		Stars: &fakeStarsService{balances: map[int64]domain.StarsBalance{
			1001: {UserID: 1001, Balance: 0, Granted: true},
		}},
		Messages: msgs,
		Now:      fixedNow,
	})
	stars := GrantStarsRequest{CommandMeta: CommandMeta{CommandID: "bonus-stars-1001", Actor: "bot", Reason: "bonus"}, UserID: 1001, Amount: 1000}
	if _, err := svc.GrantStars(ctx, stars); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GrantStars(ctx, stars); err != nil { // replay
		t.Fatal(err)
	}
	if len(msgs.sent) != 1 {
		t.Fatalf("stars packs sent = %d, want 1 (replay must not stack)", len(msgs.sent))
	}
	got := msgs.sent[0]
	if got.RecipientUserID != 1001 || got.Media == nil || got.Media.ServiceAction.Kind != domain.MessageServiceActionGiftStars ||
		got.Media.ServiceAction.GiftStars.Stars != 1000 || got.Media.ServiceAction.GiftStars.BalanceAfter != 1000 {
		t.Fatalf("stars pack = %+v", got.Media.ServiceAction.GiftStars)
	}

	if _, err := svc.GrantPremium(ctx, GrantPremiumRequest{CommandMeta: CommandMeta{CommandID: "bonus-premium-1001", Actor: "bot", Reason: "bonus"}, UserID: 1001, Months: 3}); err != nil {
		t.Fatal(err)
	}
	if len(msgs.sent) != 2 {
		t.Fatalf("premium pack not sent: %d", len(msgs.sent))
	}
	premium := msgs.sent[1].Media.ServiceAction
	if premium.Kind != domain.MessageServiceActionGiftPremium || premium.GiftPremium.Days != 90 || premium.GiftPremium.Amount <= 0 || premium.GiftPremium.Currency != "XTR" {
		t.Fatalf("premium pack = %+v", premium.GiftPremium)
	}
	// Revoking (months 0) sends nothing.
	if _, err := svc.GrantPremium(ctx, GrantPremiumRequest{CommandMeta: CommandMeta{CommandID: "revoke-1001", Actor: "ops", Reason: "test"}, UserID: 1001, Months: 0}); err != nil {
		t.Fatal(err)
	}
	if len(msgs.sent) != 2 {
		t.Fatal("revoke sent a gift pack")
	}
}
