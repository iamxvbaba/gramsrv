package postgres

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

func TestModerationFlagsRejectImpossibleStateAtPostgresBoundary(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	user := createTestUser(t, ctx, users, "+1781"+suffix+"71", "ModerationFlags", "")

	if _, err := users.SetScamFake(ctx, user.ID, true, true); !errors.Is(err, domain.ErrPeerModerationFlagsInvalid) {
		t.Fatalf("user store error=%v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET scam=true,fake=true WHERE id=$1`, user.ID); err == nil {
		t.Fatal("users CHECK constraint accepted scam=true,fake=true")
	}

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: user.ID,
		Title:         "Moderation " + suffix,
		Megagroup:     true,
		Date:          1700002000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if _, err := channels.SetChannelScamFake(ctx, created.Channel.ID, true, true); !errors.Is(err, domain.ErrPeerModerationFlagsInvalid) {
		t.Fatalf("channel store error=%v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE channels SET scam=true,fake=true WHERE id=$1`, created.Channel.ID); err == nil {
		t.Fatal("channels CHECK constraint accepted scam=true,fake=true")
	}

	gotUser, found, err := users.ByID(ctx, user.ID)
	if err != nil || !found || gotUser.Scam || gotUser.Fake {
		t.Fatalf("user after rejected writes=%+v found=%v err=%v", gotUser, found, err)
	}
	gotChannel, err := channels.GetChannelByID(ctx, created.Channel.ID)
	if err != nil || gotChannel.Scam || gotChannel.Fake {
		t.Fatalf("channel after rejected writes=%+v err=%v", gotChannel, err)
	}
}
