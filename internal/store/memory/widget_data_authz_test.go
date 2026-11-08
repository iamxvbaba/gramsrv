package memory

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

// A widget data patch is only allowed for the bot that sent the message (or the
// via bot); a recipient must not rewrite it.
func TestMessageStoreWidgetDataPatchRequiresAuthor(t *testing.T) {
	ctx := context.Background()
	messages := NewMessageStore(NewDialogStore())
	botID := int64(1000000401)
	recipientID := int64(1000000402)

	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID: botID, RecipientUserID: recipientID, RandomID: 401, Message: "widget", Date: 1700000000,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindWidget, Widget: &domain.MessageWidget{WidgetID: 11, PublicData: "a"}},
	})
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}
	public := "forged"
	if _, err := messages.EditMessage(ctx, domain.EditMessageRequest{
		OwnerUserID: recipientID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: botID},
		ID: sent.RecipientMessage.ID, WidgetData: &domain.MessageWidgetDataPatch{PublicData: &public},
	}); !errors.Is(err, domain.ErrMessageAuthorRequired) {
		t.Fatalf("recipient widget patch err = %v, want ErrMessageAuthorRequired", err)
	}
}

// A channel subscriber must not rewrite a bot's channel widget data.
func TestChannelStoreWidgetDataPatchRequiresAuthor(t *testing.T) {
	ctx := context.Background()
	st := NewChannelStore()
	const creator = int64(1000000411)
	const member = int64(1000000412)

	created, err := st.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: creator, Title: "WD authz", Megagroup: true, Date: 1700000000})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID := created.Channel.ID
	st.members[channelID][member] = domain.ChannelMember{UserID: member, ChannelID: channelID}
	sent, err := st.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: creator, ChannelID: channelID, Message: "widget", Date: 1700000000,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindWidget, Widget: &domain.MessageWidget{WidgetID: 12, PublicData: "a"}},
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	public := "forged"
	if _, err := st.EditChannelMessage(ctx, domain.EditChannelMessageRequest{
		UserID: member, ChannelID: channelID, ID: sent.Message.ID,
		WidgetData: &domain.MessageWidgetDataPatch{PublicData: &public},
	}); !errors.Is(err, domain.ErrMessageAuthorRequired) {
		t.Fatalf("member channel widget patch err = %v, want ErrMessageAuthorRequired", err)
	}
}
