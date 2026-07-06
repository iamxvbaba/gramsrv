package rpc

import (
	"context"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/clock"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

func TestChatlistsExportInviteRPCShape(t *testing.T) {
	fake := &fakeChatlistsService{
		exportFolder: domain.DialogFolder{
			ID:           2,
			Title:        "Team",
			IsChatlist:   true,
			HasMyInvites: true,
			IncludePeers: []domain.DialogFolderPeer{{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 42}, AccessHash: 4200}},
		},
		exportInvite: domain.ChatlistInvite{
			OwnerUserID: 1001,
			FilterID:    2,
			Slug:        "slug-rpc",
			Title:       "Main link",
			Peers:       []domain.DialogFolderPeer{{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 42}, AccessHash: 4200}},
			Date:        100,
		},
	}
	r := New(Config{PublicBaseURL: "http://127.0.0.1:2401"}, Deps{Chatlists: fake}, zap.NewNop(), clock.System)
	got, err := r.onChatlistsExportChatlistInvite(WithUserID(context.Background(), 1001), &tg.ChatlistsExportChatlistInviteRequest{
		Chatlist: tg.InputChatlistDialogFilter{FilterID: 2},
		Title:    "Main link",
		Peers:    []tg.InputPeerClass{&tg.InputPeerChannel{ChannelID: 42, AccessHash: 4200}},
	})
	if err != nil {
		t.Fatalf("export rpc: %v", err)
	}
	filter, ok := got.Filter.(*tg.DialogFilterChatlist)
	if !ok || !filter.HasMyInvites || filter.ID != 2 {
		t.Fatalf("filter = %#v, want dialogFilterChatlist with has_my_invites", got.Filter)
	}
	if got.Invite.URL != "http://127.0.0.1:2401/addlist/slug-rpc" || len(got.Invite.Peers) != 1 {
		t.Fatalf("invite = %+v, want url and one peer", got.Invite)
	}
	if fake.exportPeers[0].Peer.Type != domain.PeerTypeChannel || fake.exportPeers[0].Peer.ID != 42 || fake.exportPeers[0].AccessHash != 4200 {
		t.Fatalf("service peers = %+v, want parsed input peer", fake.exportPeers)
	}
}

func TestChatlistsExportInvitePublicBaseURLIsRouterScoped(t *testing.T) {
	fake := &fakeChatlistsService{
		exportFolder: domain.DialogFolder{ID: 2, Title: "Team", IsChatlist: true, HasMyInvites: true},
		exportInvite: domain.ChatlistInvite{
			OwnerUserID: 1001,
			FilterID:    2,
			Slug:        "slug-rpc",
			Title:       "Main link",
		},
	}
	local := New(Config{PublicBaseURL: "http://127.0.0.1:2401"}, Deps{Chatlists: fake}, zap.NewNop(), clock.System)
	_ = New(Config{PublicBaseURL: "https://telesrv.net"}, Deps{Chatlists: fake}, zap.NewNop(), clock.System)
	got, err := local.onChatlistsExportChatlistInvite(WithUserID(context.Background(), 1001), &tg.ChatlistsExportChatlistInviteRequest{
		Chatlist: tg.InputChatlistDialogFilter{FilterID: 2},
		Title:    "Main link",
	})
	if err != nil {
		t.Fatalf("export rpc: %v", err)
	}
	if got.Invite.URL != "http://127.0.0.1:2401/addlist/slug-rpc" {
		t.Fatalf("invite url = %q, want local router base URL", got.Invite.URL)
	}
}

func TestChatlistsJoinInviteReturnsDialogFilterUpdate(t *testing.T) {
	fake := &fakeChatlistsService{
		joinResult: domain.ChatlistJoinResult{
			Folder: domain.DialogFolder{
				ID:         3,
				Title:      "Team",
				IsChatlist: true,
				IncludePeers: []domain.DialogFolderPeer{
					{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 42}, AccessHash: 4200},
				},
			},
			ChannelResults: []domain.CreateChannelResult{
				{
					Channel: domain.Channel{
						ID:         42,
						AccessHash: 4200,
						Title:      "Team channel",
						Megagroup:  true,
						Pts:        9,
					},
					Members: []domain.ChannelMember{
						{ChannelID: 42, UserID: 2002, Status: domain.ChannelMemberActive},
					},
				},
			},
			Date: 100,
		},
	}
	r := New(Config{}, Deps{Chatlists: fake}, zap.NewNop(), clock.System)
	got, err := r.onChatlistsJoinChatlistInvite(WithUserID(context.Background(), 2002), &tg.ChatlistsJoinChatlistInviteRequest{
		Slug:  "slug-rpc",
		Peers: []tg.InputPeerClass{&tg.InputPeerChannel{ChannelID: 42, AccessHash: 4200}},
	})
	if err != nil {
		t.Fatalf("join rpc: %v", err)
	}
	updates, ok := got.(*tg.Updates)
	if !ok || len(updates.Updates) == 0 {
		t.Fatalf("updates = %#v, want updates with updateDialogFilter", got)
	}
	update, ok := updates.Updates[0].(*tg.UpdateDialogFilter)
	if !ok || update.ID != 3 {
		t.Fatalf("first update = %#v, want updateDialogFilter id=3", updates.Updates[0])
	}
	filter, ok := update.Filter.(*tg.DialogFilterChatlist)
	if !ok || filter.ID != 3 || len(filter.IncludePeers) != 1 {
		t.Fatalf("update filter = %#v, want joined chatlist filter", update.Filter)
	}
	if fake.joinPeers[0].Peer.Type != domain.PeerTypeChannel || fake.joinPeers[0].Peer.ID != 42 || fake.joinPeers[0].AccessHash != 4200 {
		t.Fatalf("service join peers = %+v, want parsed input peer", fake.joinPeers)
	}
	var gotChannelNudge bool
	for _, u := range updates.Updates {
		nudge, ok := u.(*tg.UpdateChannelTooLong)
		if !ok || nudge.ChannelID != 42 {
			continue
		}
		pts, ok := nudge.GetPts()
		if !ok || pts != 9 {
			t.Fatalf("channel nudge pts = %d ok %v, want 9 true", pts, ok)
		}
		gotChannelNudge = true
	}
	if !gotChannelNudge {
		t.Fatalf("updates = %#v, want UpdateChannelTooLong for joined channel", updates.Updates)
	}
}

func TestChatlistsJoinedChannelNudgesCarryPts(t *testing.T) {
	sessions := &captureSessions{}
	r := New(Config{}, Deps{Sessions: sessions}, zap.NewNop(), clock.System)
	r.pushChatlistJoinedChannelNudges(context.Background(), 2002, []domain.Channel{
		{ID: 42, Pts: 9},
		{ID: 43},
	})
	updates, ok := sessions.lastUserPush().(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("pushed updates = %#v, want one UpdateChannelTooLong", sessions.lastUserPush())
	}
	nudge, ok := updates.Updates[0].(*tg.UpdateChannelTooLong)
	if !ok || nudge.ChannelID != 42 {
		t.Fatalf("pushed update = %#v, want channel 42 nudge", updates.Updates[0])
	}
	pts, ok := nudge.GetPts()
	if !ok || pts != 9 {
		t.Fatalf("pushed nudge pts = %d ok %v, want 9 true", pts, ok)
	}

	sessions.clearMessages()
	r.pushChatlistJoinedChannelNudges(WithSessionID(context.Background(), 77), 2002, []domain.Channel{{ID: 44, Pts: 11}})
	snap := sessions.snapshot()
	currentUpdates, ok := snap.message.(*tg.Updates)
	if !ok || len(currentUpdates.Updates) != 1 {
		t.Fatalf("current session push = %#v, want one UpdateChannelTooLong", snap.message)
	}
	if ids := sessions.pushedUserIDs(); len(ids) != 0 {
		t.Fatalf("current session nudge pushed to user ids = %v, want none", ids)
	}
}

func TestChatlistsCheckInviteRPCAlreadyAndFreshShapes(t *testing.T) {
	peer := domain.DialogFolderPeer{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 42}, AccessHash: 4200}
	fake := &fakeChatlistsService{
		checkPreview: domain.ChatlistInvitePreview{
			Invite: domain.ChatlistInvite{Slug: "slug-rpc", Peers: []domain.DialogFolderPeer{peer}},
			OwnerFolder: domain.DialogFolder{
				ID:           2,
				Title:        "Team",
				HasEmoticon:  true,
				Emoticon:     ":)",
				IncludePeers: []domain.DialogFolderPeer{peer},
			},
			Missing: []domain.DialogFolderPeer{peer},
		},
	}
	r := New(Config{}, Deps{Chatlists: fake}, zap.NewNop(), clock.System)
	fresh, err := r.onChatlistsCheckChatlistInvite(WithUserID(context.Background(), 2002), "slug-rpc")
	if err != nil {
		t.Fatalf("check fresh rpc: %v", err)
	}
	invite, ok := fresh.(*tg.ChatlistsChatlistInvite)
	if !ok || invite.Title.Text != "Team" || len(invite.Peers) != 1 {
		t.Fatalf("fresh = %#v, want chatlistInvite with title and peer", fresh)
	}
	if emoticon, ok := invite.GetEmoticon(); !ok || emoticon != ":)" {
		t.Fatalf("fresh emoticon = %q ok %v, want folder emoji", emoticon, ok)
	}

	local := domain.DialogFolder{ID: 5, Title: "Team", IsChatlist: true, IncludePeers: []domain.DialogFolderPeer{peer}}
	fake.checkPreview.LocalFolder = &local
	fake.checkPreview.Membership = &domain.ChatlistMembership{UserID: 2002, LocalFilterID: 5, Slug: "slug-rpc"}
	fake.checkPreview.Missing = nil
	fake.checkPreview.Already = []domain.DialogFolderPeer{peer}
	already, err := r.onChatlistsCheckChatlistInvite(WithUserID(context.Background(), 2002), "slug-rpc")
	if err != nil {
		t.Fatalf("check already rpc: %v", err)
	}
	gotAlready, ok := already.(*tg.ChatlistsChatlistInviteAlready)
	if !ok || gotAlready.FilterID != 5 || len(gotAlready.AlreadyPeers) != 1 {
		t.Fatalf("already = %#v, want already filter id and peer", already)
	}
}

func TestChatlistsEditInviteRevokedFlagRoundTrips(t *testing.T) {
	fake := &fakeChatlistsService{
		exportInvite: domain.ChatlistInvite{
			OwnerUserID: 1001,
			FilterID:    2,
			Slug:        "slug-rpc",
			Title:       "Main link",
			Revoked:     true,
		},
	}
	r := New(Config{PublicBaseURL: "https://telesrv.net"}, Deps{Chatlists: fake}, zap.NewNop(), clock.System)
	var flags bin.Fields
	flags.Set(0)
	got, err := r.onChatlistsEditExportedInvite(WithUserID(context.Background(), 1001), &tg.ChatlistsEditExportedInviteRequest{
		Flags:    flags,
		Chatlist: tg.InputChatlistDialogFilter{FilterID: 2},
		Slug:     "slug-rpc",
	})
	if err != nil {
		t.Fatalf("edit rpc: %v", err)
	}
	if !fake.editRevoke {
		t.Fatalf("service revoke = false, want true")
	}
	if !got.Flags.Has(0) {
		t.Fatalf("edited invite flags = %v, want revoked bit", got.Flags)
	}
}

type fakeChatlistsService struct {
	exportFolder  domain.DialogFolder
	exportInvite  domain.ChatlistInvite
	exportPeers   []domain.DialogFolderPeer
	checkPreview  domain.ChatlistInvitePreview
	joinResult    domain.ChatlistJoinResult
	joinPeers     []domain.DialogFolderPeer
	updates       domain.ChatlistUpdates
	joinUpdFolder domain.DialogFolder
	editRevoke    bool
}

func (f *fakeChatlistsService) ExportInvite(_ context.Context, _ int64, _ int, _ string, peers []domain.DialogFolderPeer, _ int) (domain.DialogFolder, domain.ChatlistInvite, error) {
	f.exportPeers = append([]domain.DialogFolderPeer(nil), peers...)
	return f.exportFolder, f.exportInvite, nil
}

func (f *fakeChatlistsService) ListInvites(context.Context, int64, int) ([]domain.ChatlistInvite, error) {
	return []domain.ChatlistInvite{f.exportInvite}, nil
}

func (f *fakeChatlistsService) EditInvite(_ context.Context, _ int64, _ int, _ string, _ *string, _ *[]domain.DialogFolderPeer, revoke bool) (domain.ChatlistInvite, error) {
	f.editRevoke = revoke
	return f.exportInvite, nil
}

func (f *fakeChatlistsService) DeleteInvite(context.Context, int64, int, string) (domain.DialogFolder, bool, error) {
	return domain.DialogFolder{}, false, nil
}

func (f *fakeChatlistsService) CheckInvite(context.Context, int64, string) (domain.ChatlistInvitePreview, error) {
	return f.checkPreview, nil
}

func (f *fakeChatlistsService) JoinInvite(_ context.Context, _ int64, _ string, peers []domain.DialogFolderPeer, _ int) (domain.ChatlistJoinResult, error) {
	f.joinPeers = append([]domain.DialogFolderPeer(nil), peers...)
	return f.joinResult, nil
}

func (f *fakeChatlistsService) GetUpdates(context.Context, int64, int) (domain.ChatlistUpdates, error) {
	return f.updates, nil
}

func (f *fakeChatlistsService) JoinUpdates(context.Context, int64, int, []domain.DialogFolderPeer, int) (domain.ChatlistJoinResult, error) {
	return domain.ChatlistJoinResult{Folder: f.joinUpdFolder}, nil
}

func (f *fakeChatlistsService) HideUpdates(context.Context, int64, int) error {
	return nil
}

func (f *fakeChatlistsService) Leave(context.Context, int64, int, []domain.DialogFolderPeer, int) (domain.ChatlistLeaveResult, error) {
	return domain.ChatlistLeaveResult{FilterID: 3}, nil
}

func (f *fakeChatlistsService) LeaveSuggestions(context.Context, int64, int) ([]domain.DialogFolderPeer, error) {
	return f.exportInvite.Peers, nil
}
