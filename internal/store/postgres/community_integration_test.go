package postgres

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

func TestCommunityStoreLifecycleIsAtomicInPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 801, Phone: "+1888" + suffix + "01", FirstName: "CommunityOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{AccessHash: 802, Phone: "+1888" + suffix + "02", FirstName: "SearchableMember"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	channels := NewChannelStore(pool)
	initial, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Community Initial " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1_800_200_000,
	})
	if err != nil {
		t.Fatalf("create initial channel: %v", err)
	}
	owned, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: member.ID,
		Title:         "Community Owned " + suffix,
		Megagroup:     true,
		Date:          1_800_200_001,
	})
	if err != nil {
		t.Fatalf("create owned channel: %v", err)
	}
	owned.Channel, err = channels.UpdateUsername(ctx, domain.UpdateChannelUsernameRequest{
		UserID: member.ID, ChannelID: owned.Channel.ID, Username: "communitypreview" + suffix,
	})
	if err != nil {
		t.Fatalf("make owned channel public: %v", err)
	}
	if _, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: member.ID, ChannelID: owned.Channel.ID, RandomID: 9_900_000_001,
		Message: "community public preview search", Date: 1_800_200_001,
	}); err != nil {
		t.Fatalf("send public preview message: %v", err)
	}
	var communityID int64
	t.Cleanup(func() {
		if communityID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM communities WHERE id=$1", communityID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id=ANY($1::bigint[])", []int64{initial.Channel.ID, owned.Channel.ID})
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=ANY($1::bigint[])", []int64{owner.ID, member.ID})
	})

	store := NewCommunityStore(pool, nil, nil)
	created, err := store.CreateCommunity(ctx, domain.CreateCommunityRequest{
		CreatorUserID: owner.ID,
		Title:         "Postgres Community " + suffix,
		InitialPeer:   domain.Peer{Type: domain.PeerTypeChannel, ID: initial.Channel.ID},
		Visibility:    domain.CommunityPeerHidden,
		Date:          1_800_200_002,
	})
	if err != nil {
		t.Fatalf("create community: %v", err)
	}
	communityID = created.Community.ID
	if len(created.ServiceMessages) != 1 || created.ServiceMessages[0].Event.Pts == 0 {
		t.Fatalf("create service messages = %+v", created.ServiceMessages)
	}
	var linkedID int64
	if err := pool.QueryRow(ctx, "SELECT linked_community_id FROM channels WHERE id=$1", initial.Channel.ID).Scan(&linkedID); err != nil || linkedID != communityID {
		t.Fatalf("initial linked_community_id = %d err=%v, want %d", linkedID, err, communityID)
	}

	requested, err := store.ToggleCommunityPeerLink(ctx, domain.CommunityTogglePeerLinkRequest{
		ActorUserID: member.ID,
		CommunityID: communityID,
		Peer:        domain.Peer{Type: domain.PeerTypeChannel, ID: owned.Channel.ID},
		Visibility:  domain.CommunityPeerVisible,
		Date:        1_800_200_003,
	})
	if err != nil || !requested.RequestCreated {
		t.Fatalf("create peer link request = %+v err=%v", requested, err)
	}
	approved, err := store.DecideCommunityPeerLinkRequest(ctx, owner.ID, communityID, requested.Peer, false, 1_800_200_004)
	if err != nil || approved.Link == nil || approved.RequestedBy != member.ID || approved.ServiceMessage == nil {
		t.Fatalf("approve link request = %+v err=%v", approved, err)
	}
	search, err := channels.SearchJoinedMessages(ctx, owner.ID, domain.ChannelGlobalSearchRequest{
		Query: "public preview", ChannelIDs: []int64{owned.Channel.ID}, RestrictChannelIDs: true,
		AllowPublicPreview: true, Limit: 20,
	})
	if err != nil || len(search.Messages) != 1 || search.Messages[0].ChannelID != owned.Channel.ID {
		t.Fatalf("community public-preview search = %+v err=%v", search.Messages, err)
	}

	participants, err := store.ListCommunityParticipants(ctx, owner.ID, communityID, domain.ChannelParticipantsFilter{
		Kind: domain.ChannelParticipantsSearch, Query: "SEARCHABLE",
	}, 0, 20)
	if err != nil || participants.Count != 1 || len(participants.Participants) != 1 || participants.Participants[0].UserID != member.ID {
		t.Fatalf("participant search = %+v err=%v", participants, err)
	}
	admins, err := store.ListCommunityParticipants(ctx, member.ID, communityID, domain.ChannelParticipantsFilter{
		Kind: domain.ChannelParticipantsAdmins,
	}, 0, 100)
	if err != nil || admins.Count != 1 || len(admins.Participants) != 1 || admins.Participants[0].UserID != owner.ID {
		t.Fatalf("member-visible Community admins = %+v err=%v", admins, err)
	}
	if _, err := store.ListCommunityParticipants(ctx, member.ID, communityID, domain.ChannelParticipantsFilter{
		Kind: domain.ChannelParticipantsBanned,
	}, 0, 100); !errors.Is(err, domain.ErrCommunityAdminRequired) {
		t.Fatalf("member Community banned list error = %v, want admin required", err)
	}

	ban, err := store.ToggleCommunityParticipantBanned(ctx, owner.ID, communityID, member.ID, false, 1_800_200_005)
	if err != nil {
		t.Fatalf("ban participant: %v", err)
	}
	if !ban.Changed || len(ban.ChannelBans) != 1 || len(ban.RemovedLinks) != 1 {
		t.Fatalf("ban result = %+v", ban)
	}
	if err := pool.QueryRow(ctx, "SELECT linked_community_id FROM channels WHERE id=$1", owned.Channel.ID).Scan(&linkedID); err != nil || linkedID != 0 {
		t.Fatalf("owned linked_community_id after ban = %d err=%v, want 0", linkedID, err)
	}
	if _, err := store.GetCommunity(ctx, member.ID, communityID); !errors.Is(err, domain.ErrCommunityPrivate) {
		t.Fatalf("banned member get community error = %v, want private", err)
	}
}
