package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// TestChannelStorePrivateChatForbiddenRoundTripPostgres guards the shared
// channelColumns/scanChannel order for the SafeLink group private-chat switch.
func TestChannelStorePrivateChatForbiddenRoundTripPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 281,
		Phone:      "+1666" + suffix + "01",
		FirstName:  "PrivateChatOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{
		AccessHash: 282,
		Phone:      "+1666" + suffix + "02",
		FirstName:  "PrivateChatMember",
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, member.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Private Chat " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700001700,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID

	updated, err := channels.SetPrivateChatForbidden(ctx, owner.ID, channelID, true)
	if err != nil {
		t.Fatalf("enable private chat forbidden: %v", err)
	}
	if !updated.PrivateChatForbidden {
		t.Fatalf("updated PrivateChatForbidden = false, want true")
	}

	var raw bool
	if err := pool.QueryRow(ctx, `SELECT private_chat_forbidden FROM channels WHERE id = $1`, channelID).Scan(&raw); err != nil {
		t.Fatalf("read raw private_chat_forbidden: %v", err)
	}
	if !raw {
		t.Fatalf("raw private_chat_forbidden = false, want true")
	}

	view, err := channels.GetChannel(ctx, member.ID, channelID)
	if err != nil {
		t.Fatalf("get channel: %v", err)
	}
	if !view.Channel.PrivateChatForbidden {
		t.Fatalf("scanned PrivateChatForbidden = false, want true")
	}
	if view.Channel.JoinToSend {
		t.Fatalf("JoinToSend = true, want false")
	}
}
