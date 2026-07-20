package memory

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

func mustCommunityTestUser(t *testing.T, store *UserStore, firstName, phone string) domain.User {
	t.Helper()
	user, err := store.Create(context.Background(), domain.User{AccessHash: int64(len(phone) + len(firstName)), Phone: phone, FirstName: firstName})
	if err != nil {
		t.Fatalf("create user %q: %v", firstName, err)
	}
	return user
}

func mustCommunityTestChannel(t *testing.T, store *ChannelStore, creator domain.User, title string, members ...domain.User) domain.Channel {
	t.Helper()
	memberIDs := make([]int64, 0, len(members))
	for _, member := range members {
		memberIDs = append(memberIDs, member.ID)
	}
	created, err := store.CreateChannel(context.Background(), domain.CreateChannelRequest{
		CreatorUserID: creator.ID,
		Title:         title,
		Megagroup:     true,
		MemberUserIDs: memberIDs,
		Date:          1_800_000_000,
	})
	if err != nil {
		t.Fatalf("create channel %q: %v", title, err)
	}
	return created.Channel
}

func TestCommunityLifecycleRequestsSearchAndModeration(t *testing.T) {
	ctx := context.Background()
	users := NewUserStore()
	owner := mustCommunityTestUser(t, users, "Community Owner", "15551000001")
	member := mustCommunityTestUser(t, users, "Alice Searchable", "15551000002")
	channels := NewChannelStore()
	initial := mustCommunityTestChannel(t, channels, owner, "Initial", member)
	store := NewCommunityStore(users, channels, nil, nil)

	created, err := store.CreateCommunity(ctx, domain.CreateCommunityRequest{
		CreatorUserID: owner.ID,
		Title:         "Engineering",
		InitialPeer:   domain.Peer{Type: domain.PeerTypeChannel, ID: initial.ID},
		Visibility:    domain.CommunityPeerHidden,
		Date:          1_800_000_001,
	})
	if err != nil {
		t.Fatalf("create community: %v", err)
	}
	if len(created.Links) != 1 || created.Links[0].Visibility != domain.CommunityPeerHidden {
		t.Fatalf("initial links = %+v, want one hidden link", created.Links)
	}
	if len(created.ServiceMessages) != 1 || created.ServiceMessages[0].Message.Action == nil ||
		created.ServiceMessages[0].Message.Action.Type != domain.ChannelActionChangeCommunity ||
		created.ServiceMessages[0].Message.Action.CommunityID != created.Community.ID || created.ServiceMessages[0].Event.Pts == 0 {
		t.Fatalf("create service messages = %+v, want durable change-community action", created.ServiceMessages)
	}
	initialView, err := channels.GetChannel(ctx, owner.ID, initial.ID)
	if err != nil || initialView.Channel.LinkedCommunityID != created.Community.ID {
		t.Fatalf("initial linked community = %d, err=%v, want %d", initialView.Channel.LinkedCommunityID, err, created.Community.ID)
	}

	_, err = store.CreateCommunity(ctx, domain.CreateCommunityRequest{
		CreatorUserID: owner.ID,
		Title:         "Duplicate",
		InitialPeer:   domain.Peer{Type: domain.PeerTypeChannel, ID: initial.ID},
		Visibility:    domain.CommunityPeerVisible,
		Date:          1_800_000_002,
	})
	if !errors.Is(err, domain.ErrCommunityPeerLinked) {
		t.Fatalf("reuse linked peer error = %v, want ErrCommunityPeerLinked", err)
	}

	owned := mustCommunityTestChannel(t, channels, member, "Member Owned")
	requested, err := store.ToggleCommunityPeerLink(ctx, domain.CommunityTogglePeerLinkRequest{
		ActorUserID: member.ID,
		CommunityID: created.Community.ID,
		Peer:        domain.Peer{Type: domain.PeerTypeChannel, ID: owned.ID},
		Visibility:  domain.CommunityPeerVisible,
		Date:        1_800_000_003,
	})
	if err != nil || !requested.RequestCreated {
		t.Fatalf("link request = %+v, err=%v, want pending request", requested, err)
	}
	page, err := store.ListCommunityPeerLinkRequests(ctx, owner.ID, created.Community.ID, "", 20)
	if err != nil || page.TotalCount != 1 || len(page.Requests) != 1 || page.Requests[0].RequestedBy != member.ID {
		t.Fatalf("request page = %+v, err=%v", page, err)
	}
	approved, err := store.DecideCommunityPeerLinkRequest(ctx, owner.ID, created.Community.ID, requested.Peer, false, 1_800_000_004)
	if err != nil || approved.Link == nil || approved.RequestedBy != member.ID || approved.ServiceMessage == nil {
		t.Fatalf("approved request = %+v, err=%v", approved, err)
	}
	ownerView, err := store.GetCommunity(ctx, owner.ID, created.Community.ID)
	if err != nil {
		t.Fatalf("owner community view after approval: %v", err)
	}
	for _, link := range ownerView.Links {
		if link.Peer == approved.Peer && link.CanViewHistory {
			t.Fatalf("community admin without private-channel membership advertised can_view_history")
		}
	}
	privateVisible := mustCommunityTestChannel(t, channels, owner, "Private Visible")
	linked, err := store.ToggleCommunityPeerLink(ctx, domain.CommunityTogglePeerLinkRequest{
		ActorUserID: owner.ID,
		CommunityID: created.Community.ID,
		Peer:        domain.Peer{Type: domain.PeerTypeChannel, ID: privateVisible.ID},
		Visibility:  domain.CommunityPeerVisible,
		Date:        1_800_000_004,
	})
	if err != nil || linked.Link == nil {
		t.Fatalf("link private visible channel = %+v, err=%v", linked, err)
	}
	memberView, err := store.GetCommunity(ctx, member.ID, created.Community.ID)
	if err != nil {
		t.Fatalf("member community view: %v", err)
	}
	foundPrivate := false
	for _, link := range memberView.Links {
		if link.Peer.ID == privateVisible.ID {
			foundPrivate = true
			if link.CanViewHistory {
				t.Fatalf("private visible channel advertised can_view_history to non-member")
			}
		}
	}
	if !foundPrivate {
		t.Fatalf("visible private channel missing from member community view")
	}

	_, err = store.ToggleCommunityPeerLink(ctx, domain.CommunityTogglePeerLinkRequest{
		ActorUserID: owner.ID,
		CommunityID: created.Community.ID,
		Peer:        approved.Peer,
		Visibility:  domain.CommunityPeerHidden,
		Date:        1_800_000_005,
	})
	if !errors.Is(err, domain.ErrCommunityPeerLinked) {
		t.Fatalf("change visibility in place error = %v, want unlink/relink requirement", err)
	}

	participants, err := store.ListCommunityParticipants(ctx, owner.ID, created.Community.ID, domain.ChannelParticipantsFilter{
		Kind:  domain.ChannelParticipantsSearch,
		Query: "searchABLE",
	}, 0, 20)
	if err != nil || participants.Count != 1 || len(participants.Participants) != 1 || participants.Participants[0].UserID != member.ID {
		t.Fatalf("participant name search = %+v, err=%v, want member", participants, err)
	}
	admins, err := store.ListCommunityParticipants(ctx, member.ID, created.Community.ID, domain.ChannelParticipantsFilter{
		Kind: domain.ChannelParticipantsAdmins,
	}, 0, 100)
	if err != nil || admins.Count != 1 || len(admins.Participants) != 1 || admins.Participants[0].UserID != owner.ID {
		t.Fatalf("member-visible Community admins = %+v, err=%v, want creator", admins, err)
	}
	if _, err := store.ListCommunityParticipants(ctx, member.ID, created.Community.ID, domain.ChannelParticipantsFilter{
		Kind: domain.ChannelParticipantsKicked,
	}, 0, 100); !errors.Is(err, domain.ErrCommunityAdminRequired) {
		t.Fatalf("member Community kicked list error = %v, want admin required", err)
	}
	outsider := mustCommunityTestUser(t, users, "Community Outsider", "15551000003")
	if _, err := store.ListCommunityParticipants(ctx, outsider.ID, created.Community.ID, domain.ChannelParticipantsFilter{
		Kind: domain.ChannelParticipantsAdmins,
	}, 0, 100); !errors.Is(err, domain.ErrCommunityPrivate) {
		t.Fatalf("outsider Community admins error = %v, want private", err)
	}

	ban, err := store.ToggleCommunityParticipantBanned(ctx, owner.ID, created.Community.ID, member.ID, false, 1_800_000_006)
	if err != nil {
		t.Fatalf("ban community participant: %v", err)
	}
	if !ban.Changed || len(ban.ChannelBans) != 1 || len(ban.RemovedLinks) != 1 || ban.RemovedLinks[0].Peer.ID != owned.ID {
		t.Fatalf("ban result = %+v, want one channel ban and owned-link removal", ban)
	}
	if action := ban.RemovedLinks[0].ServiceMessage.Message.Action; action == nil || action.Type != domain.ChannelActionChangeCommunity || action.CommunityID != 0 {
		t.Fatalf("unlink service action = %+v, want change-community(0)", action)
	}
	if got := ban.RemovedLinks[0].ServiceMessage.Channel.LinkedCommunityID; got != 0 {
		t.Fatalf("unlink service channel linked community = %d, want 0", got)
	}
	kicked, err := channels.GetParticipant(ctx, owner.ID, initial.ID, member.ID)
	if err != nil || kicked.Status != domain.ChannelMemberKicked || !kicked.BannedRights.ViewMessages {
		t.Fatalf("linked channel participant = %+v, err=%v, want kicked", kicked, err)
	}
	ownedView, err := channels.GetChannel(ctx, member.ID, owned.ID)
	if err != nil || ownedView.Channel.LinkedCommunityID != 0 {
		t.Fatalf("owned channel linked community = %d, err=%v, want 0", ownedView.Channel.LinkedCommunityID, err)
	}
	if _, err := store.GetCommunity(ctx, member.ID, created.Community.ID); !errors.Is(err, domain.ErrCommunityPrivate) {
		t.Fatalf("banned member get community error = %v, want private", err)
	}
	repeated, err := store.ToggleCommunityParticipantBanned(ctx, owner.ID, created.Community.ID, member.ID, false, 1_800_000_007)
	if err != nil || repeated.Changed || len(repeated.ChannelBans) != 0 || len(repeated.RemovedLinks) != 0 {
		t.Fatalf("repeated ban = %+v err=%v, want idempotent no-op", repeated, err)
	}
}

func TestCommunityCollapsedPinAndMixedOrder(t *testing.T) {
	ctx := context.Background()
	users := NewUserStore()
	owner := mustCommunityTestUser(t, users, "Owner", "15551000011")
	channels := NewChannelStore()
	store := NewCommunityStore(users, channels, nil, nil)

	makeCommunity := func(title string) domain.CommunityView {
		channel := mustCommunityTestChannel(t, channels, owner, title+" Channel")
		view, err := store.CreateCommunity(ctx, domain.CreateCommunityRequest{
			CreatorUserID: owner.ID,
			Title:         title,
			InitialPeer:   domain.Peer{Type: domain.PeerTypeChannel, ID: channel.ID},
			Visibility:    domain.CommunityPeerVisible,
			Date:          1_800_000_100,
		})
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		if _, changed, err := store.SetCommunityCollapsed(ctx, owner.ID, view.Community.ID, true); err != nil || !changed {
			t.Fatalf("collapse %s: changed=%v err=%v", title, changed, err)
		}
		if changed, err := store.SetCommunityPinned(ctx, owner.ID, view.Community.ID, true); err != nil || !changed {
			t.Fatalf("pin %s: changed=%v err=%v", title, changed, err)
		}
		return view
	}
	one := makeCommunity("One")
	two := makeCommunity("Two")
	changed, err := store.ReorderCommunityPinned(ctx, owner.ID, []domain.Peer{
		{Type: domain.PeerTypeChannel, ID: 77},
		{Type: domain.PeerTypeCommunity, ID: one.Community.ID},
		{Type: domain.PeerTypeUser, ID: 88},
		{Type: domain.PeerTypeCommunity, ID: two.Community.ID},
	}, true)
	if err != nil || !changed {
		t.Fatalf("mixed pinned reorder: changed=%v err=%v", changed, err)
	}
	oneView, _ := store.GetCommunity(ctx, owner.ID, one.Community.ID)
	twoView, _ := store.GetCommunity(ctx, owner.ID, two.Community.ID)
	if !oneView.State.Pinned || !twoView.State.Pinned || oneView.State.PinnedOrder <= twoView.State.PinnedOrder {
		t.Fatalf("pinned orders one=%+v two=%+v, want global mixed order preserved", oneView.State, twoView.State)
	}
	uncollapsed, changed, err := store.SetCommunityCollapsed(ctx, owner.ID, one.Community.ID, false)
	if err != nil || !changed || uncollapsed.State.Pinned {
		t.Fatalf("uncollapse state = %+v changed=%v err=%v, want pin cleared", uncollapsed.State, changed, err)
	}
}

func TestCommunityCanUseOwnedBotAsInitialPeer(t *testing.T) {
	ctx := context.Background()
	users := NewUserStore()
	owner := mustCommunityTestUser(t, users, "Bot Owner", "15551000021")
	bots := NewBotStore(users)
	bot, _, err := bots.CreateBotAccount(ctx, domain.User{
		AccessHash: 91, FirstName: "Community Bot", Username: "community_bot",
	}, domain.BotProfile{OwnerUserID: owner.ID})
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	store := NewCommunityStore(users, NewChannelStore(), bots, nil)
	created, err := store.CreateCommunity(ctx, domain.CreateCommunityRequest{
		CreatorUserID: owner.ID,
		Title:         "Bot Community",
		InitialPeer:   domain.Peer{Type: domain.PeerTypeUser, ID: bot.ID},
		Visibility:    domain.CommunityPeerVisible,
		Date:          1_800_000_200,
	})
	if err != nil {
		t.Fatalf("create bot community: %v", err)
	}
	if len(created.Links) != 1 || created.Links[0].Peer.ID != bot.ID || !created.Links[0].CanViewHistory {
		t.Fatalf("bot community links = %+v", created.Links)
	}
	if len(created.ServiceMessages) != 0 {
		t.Fatalf("bot link service messages = %+v, want none", created.ServiceMessages)
	}
	updatedBot, ok, err := users.ByID(ctx, bot.ID)
	if err != nil || !ok || updatedBot.LinkedCommunityID != created.Community.ID {
		t.Fatalf("bot linked community = %d ok=%v err=%v", updatedBot.LinkedCommunityID, ok, err)
	}
}
