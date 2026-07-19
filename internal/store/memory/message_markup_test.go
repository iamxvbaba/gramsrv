package memory

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

func TestPrivateReplyKeyboardSurvivesBothBoxesAndHistory(t *testing.T) {
	store := NewMessageStore(NewDialogStore())
	markup := &domain.MessageReplyMarkup{
		Type:        domain.MessageReplyMarkupKeyboard,
		Keyboard:    [][]domain.MarkupButton{{{Type: domain.MarkupButtonText, Text: "Help"}}},
		Resize:      true,
		SingleUse:   true,
		Persistent:  true,
		Placeholder: "Choose",
	}
	res, err := store.SendPrivateText(context.Background(), domain.SendPrivateTextRequest{
		SenderUserID: 10, RecipientUserID: 20, RandomID: 30,
		Message: "pick", Date: 40, ReplyMarkup: markup,
	})
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}
	assertReplyKeyboard := func(name string, got *domain.MessageReplyMarkup) {
		t.Helper()
		if got == nil || got.Kind() != domain.MessageReplyMarkupKeyboard || len(got.Keyboard) != 1 ||
			len(got.Keyboard[0]) != 1 || got.Keyboard[0][0].Text != "Help" || !got.Resize ||
			!got.SingleUse || !got.Persistent || got.Placeholder != "Choose" {
			t.Fatalf("%s = %#v", name, got)
		}
	}
	assertReplyKeyboard("sender", res.SenderMessage.ReplyMarkup)
	assertReplyKeyboard("recipient", res.RecipientMessage.ReplyMarkup)
	markup.Keyboard[0][0].Text = "mutated"
	list, err := store.GetByIDs(context.Background(), 20, []int{res.RecipientMessage.ID})
	if err != nil || len(list.Messages) != 1 {
		t.Fatalf("GetByIDs = %+v, %v", list, err)
	}
	assertReplyKeyboard("recipient history", list.Messages[0].ReplyMarkup)
}
