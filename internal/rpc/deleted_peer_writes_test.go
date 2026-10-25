package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appcontacts "telesrv/internal/app/contacts"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type tombstonePeersUsers struct {
	UsersService
	deleted map[int64]bool
}

func (s *tombstonePeersUsers) ByID(_ context.Context, _, userID int64) (domain.User, bool, error) {
	if s.deleted[userID] {
		return domain.User{ID: userID, AccessHash: 77, Deleted: true, DeletedAt: 1_800_000_000}, true, nil
	}
	return domain.User{ID: userID, AccessHash: 77, FirstName: "Live"}, true, nil
}

// Every write path that accepts an InputPeer resolves it without reading the
// users row, so each one has to reject a tombstone explicitly. These are the
// paths that had none: forwarding, channel invites/member adds/admin grants,
// premium gift recipients and contacts.
func TestPeerWritesRejectDeletedAccount(t *testing.T) {
	const deletedID = int64(404)
	r := New(Config{}, Deps{
		Users:    &tombstonePeersUsers{deleted: map[int64]bool{deletedID: true}},
		Contacts: appcontacts.NewService(memory.NewContactStore()),
	}, zaptest.NewLogger(t), clock.System)
	deletedInputUser := tg.InputUserClass(&tg.InputUser{UserID: deletedID, AccessHash: 77})
	ctx := WithUserID(context.Background(), 7)

	if r.peerIsDeletedAccount(ctx, domain.Peer{Type: domain.PeerTypeUser, ID: deletedID}) != true {
		t.Fatal("peerIsDeletedAccount must recognise the tombstone")
	}
	if r.peerIsDeletedAccount(ctx, domain.Peer{Type: domain.PeerTypeUser, ID: 8}) {
		t.Fatal("peerIsDeletedAccount must not fire for a live peer")
	}
	if r.peerIsDeletedAccount(ctx, domain.Peer{Type: domain.PeerTypeChannel, ID: deletedID}) {
		t.Fatal("channels are never account tombstones")
	}
	if r.giftRecipientDeleted(ctx, 7, domain.Peer{Type: domain.PeerTypeUser, ID: deletedID}) != true {
		t.Fatal("giftRecipientDeleted must recognise the tombstone")
	}
	if r.giftRecipientDeleted(ctx, 7, domain.Peer{Type: domain.PeerTypeUser, ID: 8}) {
		t.Fatal("giftRecipientDeleted must not fire for a live peer")
	}

	// Channel invites, member adds, admin grants, bans and premium gift
	// recipients all funnel through this helper.
	if _, err := r.userIDsFromInputUsers(ctx, 7, []tg.InputUserClass{deletedInputUser}); err == nil {
		t.Fatal("a tombstone was accepted as an invite/member/admin target")
	}
	if _, err := r.onContactsAddContact(ctx, &tg.ContactsAddContactRequest{
		ID: &tg.InputUser{UserID: deletedID, AccessHash: 77},
	}); err == nil {
		t.Fatal("a tombstone was accepted as a contact")
	}

	// Same guard on the channel admin grant, which resolves a single user
	// instead of going through the shared invite helper.
	withContacts := New(Config{}, Deps{
		Users:    &tombstonePeersUsers{deleted: map[int64]bool{deletedID: true}},
		Channels: appchannels.NewService(memory.NewChannelStore()),
	}, zaptest.NewLogger(t), clock.System)
	if _, err := withContacts.onChannelsEditAdmin(ctx, &tg.ChannelsEditAdminRequest{
		Channel: &tg.InputChannel{ChannelID: 100, AccessHash: 5},
		UserID:  &tg.InputUser{UserID: deletedID, AccessHash: 77},
	}); err == nil {
		t.Fatal("a tombstone was granted channel admin rights")
	}
}
