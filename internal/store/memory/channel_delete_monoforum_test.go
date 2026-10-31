package memory

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// TestDeleteChannelCascadesLinkedMonoforum verifies that deleting a parent broadcast
// channel with Direct Messages enabled also soft-deletes the associated monoforum
// virtual channel and returns it with the result. Without the cascade it would leave an
// orphan with monoforum=true but linked_monoforum_id pointing at a deleted parent
// (web client rendering crashes + DB garbage accumulating on delete).
func TestDeleteChannelCascadesLinkedMonoforum(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	broadcast, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1, Title: "DM Broadcast", Broadcast: true, Date: 1_700_000_900,
	})
	if err != nil {
		t.Fatalf("create broadcast: %v", err)
	}
	parentID := broadcast.Channel.ID
	enabled, err := store.SetPaidMessagesPrice(ctx, 1, parentID, 0, true)
	if err != nil {
		t.Fatalf("enable DM: %v", err)
	}
	monoID := enabled.Channel.LinkedMonoforumID
	if monoID == 0 {
		t.Fatalf("no monoforum created")
	}

	res, err := store.DeleteChannel(ctx, domain.DeleteChannelRequest{UserID: 1, ChannelID: parentID, Date: 1_700_001_000})
	if err != nil {
		t.Fatalf("delete parent: %v", err)
	}
	if !res.Channel.Deleted {
		t.Fatalf("parent not marked deleted: %+v", res.Channel)
	}
	if res.LinkedMonoforum == nil || res.LinkedMonoforum.ID != monoID || !res.LinkedMonoforum.Deleted {
		t.Fatalf("result LinkedMonoforum = %+v, want deleted mono %d", res.LinkedMonoforum, monoID)
	}
	if mono := store.channels[monoID]; !mono.Deleted {
		t.Fatalf("monoforum %d not soft-deleted in store: %+v", monoID, mono)
	}
	// After the delete, getDialogs must no longer return the parent channel or the mono.
	dialogs, err := store.ListChannelDialogs(ctx, 1, domain.DialogFilter{Limit: 100})
	if err != nil {
		t.Fatalf("list dialogs after delete: %v", err)
	}
	for _, d := range dialogs.Dialogs {
		if d.Peer.ID == monoID || d.Peer.ID == parentID {
			t.Fatalf("dialogs after delete still include parent/mono: %+v", d)
		}
	}
}

// TestDeleteChannelNoMonoforumNoCascade locks that deleting an ordinary channel without
// DMs enabled is unaffected: LinkedMonoforum is nil and the cascade is not triggered by mistake.
func TestDeleteChannelNoMonoforumNoCascade(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	plain, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1, Title: "Plain Group", Megagroup: true, Date: 1_700_000_900,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	res, err := store.DeleteChannel(ctx, domain.DeleteChannelRequest{UserID: 1, ChannelID: plain.Channel.ID, Date: 1_700_001_000})
	if err != nil {
		t.Fatalf("delete channel: %v", err)
	}
	if res.LinkedMonoforum != nil {
		t.Fatalf("plain channel delete returned LinkedMonoforum %+v, want nil", res.LinkedMonoforum)
	}
}
