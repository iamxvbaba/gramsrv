package account

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	storepkg "telesrv/internal/store"
	"telesrv/internal/store/memory"
)

// Official clients create a business chat link with no text and fill it in
// afterwards with account.editBusinessChatLink.
func TestBusinessChatLinkCanStartEmpty(t *testing.T) {
	ctx := context.Background()
	const userID int64 = 1002
	store := memory.NewPasswordStore()
	svc := NewService(store, WithBusinessAutomation(store))

	link, err := svc.CreateBusinessChatLink(ctx, userID, domain.BusinessChatLinkInput{})
	if err != nil {
		t.Fatalf("create empty link: %v", err)
	}
	if link.Slug == "" || link.Message != "" {
		t.Fatalf("empty link = %+v", link)
	}
	edited, err := svc.EditBusinessChatLink(ctx, userID, link.Slug, domain.BusinessChatLinkInput{Message: "Hello!", Title: "Sales"})
	if err != nil || edited.Message != "Hello!" {
		t.Fatalf("edit = %+v err %v", edited, err)
	}
}

// "All 1-to-1 chats except…" with no exceptions is saved as exclude_selected
// with nothing selected, which means everyone.
func TestBusinessGreetingAndAwayAcceptEveryoneExcept(t *testing.T) {
	ctx := context.Background()
	const userID int64 = 1003
	store := memory.NewPasswordStore()
	svc := NewService(store, WithBusinessAutomation(store))
	// v2 saves quick replies through the delivery-effect mutation path.
	if _, err := svc.MutateQuickReplies(ctx, storepkg.QuickReplyAccountMutation{
		Kind: storepkg.QuickReplyAccountSaveText, UserID: userID, Date: 1, Shortcut: "hello",
		Message: domain.QuickReplyMessage{RandomID: 1, Date: 1, Message: "Hi"},
	}, accountQuickReplyTestEffects); err != nil {
		t.Fatal(err)
	}
	everyone := domain.BusinessRecipients{ExcludeSelected: true}
	if _, err := svc.UpdateBusinessGreetingMessage(ctx, userID, &domain.BusinessGreetingMessage{ShortcutID: 1, Recipients: everyone, NoActivityDays: 7}); err != nil {
		t.Fatalf("greeting for everyone: %v", err)
	}
	if _, err := svc.UpdateBusinessAwayMessage(ctx, userID, &domain.BusinessAwayMessage{ShortcutID: 1, Recipients: everyone,
		Schedule: domain.BusinessAwaySchedule{Kind: domain.BusinessAwayScheduleAlways}}); err != nil {
		t.Fatalf("away for everyone: %v", err)
	}
	// Nothing selected without exclude_selected still selects nobody.
	_, err := svc.UpdateBusinessGreetingMessage(ctx, userID, &domain.BusinessGreetingMessage{ShortcutID: 1, NoActivityDays: 7})
	if !errors.Is(err, domain.ErrBusinessRecipientsEmpty) {
		t.Fatalf("greeting for nobody err = %v, want ErrBusinessRecipientsEmpty", err)
	}
}
