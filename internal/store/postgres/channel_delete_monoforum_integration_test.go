package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// TestDeleteChannelCascadesLinkedMonoforumPostgres regression Risk A: when deleting a parent broadcast
// channel with Direct Messages enabled, the linked monoforum is soft-deleted in the same transaction and returned in the result. Not cascading would leave an orphan mono pointing at the deleted parent channel.
// Gated on TELESRV_TEST_POSTGRES_DSN.
func TestDeleteChannelCascadesLinkedMonoforumPostgres(t *testing.T) {
	pool := testPool(t) // an unset DSN makes it t.Skip
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 91, Phone: "+1778" + suffix + "31", FirstName: "DelMonoOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	var channelIDs []int64
	t.Cleanup(func() {
		if len(channelIDs) > 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = ANY($1::bigint[])", channelIDs)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", owner.ID)
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID, Title: "Del Mono BC " + suffix, Broadcast: true, Date: 1700000900,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	parentID := created.Channel.ID
	channelIDs = append(channelIDs, parentID)

	enabled, err := channels.SetPaidMessagesPrice(ctx, owner.ID, parentID, 0, true)
	if err != nil {
		t.Fatalf("enable DM: %v", err)
	}
	monoID := enabled.Channel.LinkedMonoforumID
	if monoID == 0 {
		t.Fatalf("no monoforum created")
	}
	channelIDs = append(channelIDs, monoID)

	res, err := channels.DeleteChannel(ctx, domain.DeleteChannelRequest{UserID: owner.ID, ChannelID: parentID, Date: 1700001000})
	if err != nil {
		t.Fatalf("delete parent: %v", err)
	}
	if !res.Channel.Deleted {
		t.Fatalf("parent not marked deleted: %+v", res.Channel)
	}
	if res.LinkedMonoforum == nil || res.LinkedMonoforum.ID != monoID || !res.LinkedMonoforum.Deleted {
		t.Fatalf("result LinkedMonoforum = %+v, want deleted mono %d", res.LinkedMonoforum, monoID)
	}

	// Both rows must be deleted=true in the DB.
	var parentDeleted, monoDeleted bool
	if err := pool.QueryRow(ctx, "SELECT deleted FROM channels WHERE id = $1", parentID).Scan(&parentDeleted); err != nil {
		t.Fatalf("read parent deleted: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT deleted FROM channels WHERE id = $1", monoID).Scan(&monoDeleted); err != nil {
		t.Fatalf("read mono deleted: %v", err)
	}
	if !parentDeleted || !monoDeleted {
		t.Fatalf("deleted flags parent=%v mono=%v, want both true (orphan monoforum left behind)", parentDeleted, monoDeleted)
	}

	// After the delete, getDialogs no longer returns the parent channel or the mono.
	dialogs, err := channels.ListChannelDialogs(ctx, owner.ID, domain.DialogFilter{Limit: 100})
	if err != nil {
		t.Fatalf("list dialogs after delete: %v", err)
	}
	for _, d := range dialogs.Dialogs {
		if d.Peer.ID == monoID || d.Peer.ID == parentID {
			t.Fatalf("dialogs after delete still include parent/mono: %+v", d)
		}
	}
}
