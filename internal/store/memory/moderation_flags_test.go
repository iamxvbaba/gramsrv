package memory

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

func TestModerationStoresRejectScamAndFakeTogether(t *testing.T) {
	ctx := context.Background()
	users := NewUserStore()
	user, err := users.Create(ctx, domain.User{Phone: "+15550009999", FirstName: "Flag"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := users.SetScamFake(ctx, user.ID, true, true); !errors.Is(err, domain.ErrPeerModerationFlagsInvalid) {
		t.Fatalf("user SetScamFake error=%v", err)
	}
	gotUser, found, err := users.ByID(ctx, user.ID)
	if err != nil || !found || gotUser.Scam || gotUser.Fake {
		t.Fatalf("user after rejected flags=%+v found=%v err=%v", gotUser, found, err)
	}

	channels := NewChannelStore()
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: user.ID, Title: "Flags", Megagroup: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := channels.SetChannelScamFake(ctx, created.Channel.ID, true, true); !errors.Is(err, domain.ErrPeerModerationFlagsInvalid) {
		t.Fatalf("channel SetChannelScamFake error=%v", err)
	}
	gotChannel, err := channels.GetChannelByID(ctx, created.Channel.ID)
	if err != nil || gotChannel.Scam || gotChannel.Fake {
		t.Fatalf("channel after rejected flags=%+v err=%v", gotChannel, err)
	}
}
