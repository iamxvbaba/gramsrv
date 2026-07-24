package postgres

import (
	"context"
	"errors"
	"testing"

	appdialogs "telesrv/internal/app/dialogs"
	"telesrv/internal/domain"
)

func TestPublicChannelAndMegagroupPreviewPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 941, Phone: "+1941" + suffix + "01", FirstName: "PreviewOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	viewer, err := users.Create(ctx, domain.User{AccessHash: 942, Phone: "+1942" + suffix + "02", FirstName: "PreviewViewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	channels := NewChannelStore(pool)
	var channelIDs []int64
	t.Cleanup(func() {
		if len(channelIDs) != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = ANY($1::bigint[])", channelIDs)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, viewer.ID})
	})

	for i, tc := range []struct {
		name      string
		broadcast bool
	}{
		{name: "broadcast", broadcast: true},
		{name: "megagroup"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
				CreatorUserID: owner.ID,
				Title:         "Public Preview " + tc.name + " " + suffix,
				Broadcast:     tc.broadcast,
				Megagroup:     !tc.broadcast,
				Date:          1700009400 + i,
			})
			if err != nil {
				t.Fatalf("create channel: %v", err)
			}
			channelIDs = append(channelIDs, created.Channel.ID)
			public, err := channels.UpdateUsername(ctx, domain.UpdateChannelUsernameRequest{
				UserID: owner.ID, ChannelID: created.Channel.ID, Username: "pub" + tc.name + suffix,
			})
			if err != nil {
				t.Fatalf("make public: %v", err)
			}
			sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
				UserID: owner.ID, ChannelID: public.ID, RandomID: int64(94100 + i), Message: "public history", Date: 1700009410 + i,
			})
			if err != nil {
				t.Fatalf("send public message: %v", err)
			}
			history, err := channels.ListChannelHistory(ctx, viewer.ID, domain.ChannelHistoryFilter{ChannelID: public.ID, Limit: 20})
			if err != nil {
				t.Fatalf("public preview history: %v", err)
			}
			found := false
			for _, message := range history.Messages {
				if message.ID == sent.Message.ID {
					found = true
				}
			}
			if !found || history.Self.Status != domain.ChannelMemberLeft {
				t.Fatalf("preview history = %+v self=%+v", history.Messages, history.Self)
			}
			audience, err := channels.FilterChannelMessageAudienceIDs(ctx, public.ID, []int64{viewer.ID, owner.ID, viewer.ID})
			if err != nil {
				t.Fatalf("filter public message audience: %v", err)
			}
			if len(audience) != 2 || audience[0] != owner.ID || audience[1] != viewer.ID {
				t.Fatalf("public message audience = %v, want owner/member and viewer/subscriber", audience)
			}
			diff, err := channels.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{
				UserID: viewer.ID, ChannelID: public.ID, Pts: created.Event.Pts, Limit: 20,
			})
			if err != nil {
				t.Fatalf("public preview difference: %v", err)
			}
			if !diff.Final || diff.Pts != sent.Event.Pts || len(diff.NewMessages) != 1 || diff.NewMessages[0].ID != sent.Message.ID {
				t.Fatalf("public preview difference = %+v, want sent message through pts %d", diff, sent.Event.Pts)
			}
			if _, err := channels.GetParticipants(ctx, viewer.ID, public.ID, domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsRecent}, 0, 20); err != nil {
				t.Fatalf("public preview participants: %v", err)
			}
			if _, err := channels.GetParticipant(ctx, viewer.ID, public.ID, viewer.ID); !errors.Is(err, domain.ErrUserNotParticipant) {
				t.Fatalf("public preview self participant err = %v, want ErrUserNotParticipant", err)
			}
			dialogs := appdialogs.NewService(nil, channels)
			peerDialogs, err := dialogs.GetPeerDialogs(ctx, viewer.ID, []domain.Peer{{Type: domain.PeerTypeChannel, ID: public.ID}})
			if err != nil {
				t.Fatalf("public preview peer dialogs: %v", err)
			}
			if len(peerDialogs.Dialogs) != 1 || len(peerDialogs.ChannelMessages) != 0 || len(peerDialogs.Channels) != 1 {
				t.Fatalf("public preview peer dialogs = %+v, want one zero-top bootstrap", peerDialogs)
			}
			previewDialog := peerDialogs.Dialogs[0]
			if previewDialog.TopMessage != 0 || !previewDialog.ChannelLeft ||
				previewDialog.ReadInboxMaxID != 0 || previewDialog.ReadOutboxMaxID != 0 ||
				previewDialog.Pts != sent.Event.Pts {
				t.Fatalf("public preview bootstrap dialog = %+v", previewDialog)
			}
			var memberExists bool
			if err := pool.QueryRow(ctx, `SELECT EXISTS (
SELECT 1 FROM channel_members WHERE channel_id = $1 AND user_id = $2
)`, public.ID, viewer.ID).Scan(&memberExists); err != nil {
				t.Fatalf("check preview member row: %v", err)
			}
			if memberExists {
				t.Fatal("public preview persisted a channel member row")
			}
			if _, err := channels.JoinChannel(ctx, public.ID, viewer.ID, 1700009420+i); err != nil {
				t.Fatalf("join public peer: %v", err)
			}
			if _, err := channels.LeaveChannel(ctx, public.ID, viewer.ID, 1700009430+i); err != nil {
				t.Fatalf("leave public peer: %v", err)
			}
			filtered, err := channels.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{
				UserID: viewer.ID, ChannelID: public.ID, Pts: sent.Event.Pts, Limit: 20,
			})
			if err != nil {
				t.Fatalf("public difference across participant events: %v", err)
			}
			if tc.broadcast {
				if !filtered.Final || filtered.Pts != sent.Event.Pts || len(filtered.Events) != 0 ||
					len(filtered.NewMessages) != 0 || len(filtered.OtherUpdates) != 0 {
					t.Fatalf("broadcast difference after transient participant changes = %+v, want unchanged PTS", filtered)
				}
			} else {
				if !filtered.Final || filtered.Pts <= sent.Event.Pts || len(filtered.NewMessages) != 2 ||
					len(filtered.OtherUpdates) != 0 {
					t.Fatalf("megagroup join/leave difference = %+v, want two real service messages", filtered)
				}
				for _, message := range filtered.NewMessages {
					if message.Action == nil {
						t.Fatalf("megagroup join/leave difference message = %+v, want service action", message)
					}
				}
			}
			if _, err := channels.GetParticipant(ctx, viewer.ID, public.ID, viewer.ID); !errors.Is(err, domain.ErrUserNotParticipant) {
				t.Fatalf("left self participant err = %v, want ErrUserNotParticipant", err)
			}
			if _, err := channels.ListChannelHistory(ctx, viewer.ID, domain.ChannelHistoryFilter{ChannelID: public.ID, Limit: 20}); err != nil {
				t.Fatalf("public history after leave: %v", err)
			}
		})
	}

	private, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID, Title: "Private Preview " + suffix, Megagroup: true, Date: 1700009450,
	})
	if err != nil {
		t.Fatalf("create private group: %v", err)
	}
	channelIDs = append(channelIDs, private.Channel.ID)
	if audience, err := channels.FilterChannelMessageAudienceIDs(ctx, private.Channel.ID, []int64{viewer.ID}); err != nil || len(audience) != 0 {
		t.Fatalf("private message audience = %v err %v, want empty", audience, err)
	}
	if _, err := channels.ListChannelHistory(ctx, viewer.ID, domain.ChannelHistoryFilter{ChannelID: private.Channel.ID, Limit: 20}); !errors.Is(err, domain.ErrChannelPrivate) {
		t.Fatalf("private preview history err = %v, want ErrChannelPrivate", err)
	}
}

func TestLinkedDiscussionGuestPeerDialogProjectionPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 951, Phone: "+1951" + suffix + "01", FirstName: "DiscussionOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	subscriber, err := users.Create(ctx, domain.User{AccessHash: 952, Phone: "+1952" + suffix + "02", FirstName: "DiscussionSubscriber"})
	if err != nil {
		t.Fatalf("create subscriber: %v", err)
	}
	outsider, err := users.Create(ctx, domain.User{AccessHash: 953, Phone: "+1953" + suffix + "03", FirstName: "DiscussionOutsider"})
	if err != nil {
		t.Fatalf("create outsider: %v", err)
	}
	channels := NewChannelStore(pool,
		WithChannelRowCache(NewChannelRowCache(32)),
		WithChannelMemberCache(NewChannelMemberCache(64)))
	var channelIDs []int64
	t.Cleanup(func() {
		if len(channelIDs) != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = ANY($1::bigint[])", channelIDs)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, subscriber.ID, outsider.ID})
	})

	broadcast, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID, Title: "Peer Dialog Source " + suffix, Broadcast: true, Date: 1700009500,
	})
	if err != nil {
		t.Fatalf("create broadcast: %v", err)
	}
	channelIDs = append(channelIDs, broadcast.Channel.ID)
	group, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID, Title: "Peer Dialog Group " + suffix, Megagroup: true, Date: 1700009501,
	})
	if err != nil {
		t.Fatalf("create discussion group: %v", err)
	}
	channelIDs = append(channelIDs, group.Channel.ID)
	if _, err := channels.SetDiscussionGroup(ctx, owner.ID, broadcast.Channel.ID, group.Channel.ID); err != nil {
		t.Fatalf("set discussion group: %v", err)
	}
	if _, err := channels.InviteToChannel(ctx, broadcast.Channel.ID, owner.ID, []int64{subscriber.ID}, 1700009502); err != nil {
		t.Fatalf("invite broadcast subscriber: %v", err)
	}
	post, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: owner.ID, ChannelID: broadcast.Channel.ID, RandomID: 95101, Message: "peer dialog root", Date: 1700009503,
	})
	if err != nil || post.Discussion == nil {
		t.Fatalf("send linked post = %+v err %v", post, err)
	}

	views, err := channels.GetChannels(ctx, subscriber.ID, []int64{group.Channel.ID})
	if err != nil || len(views) != 1 {
		t.Fatalf("batch linked guest views = %+v err %v, want one", views, err)
	}
	view := views[0]
	if !view.Self.Guest || view.Self.Status != domain.ChannelMemberLeft || view.Dialog.TopMessageID == 0 || view.Channel.Pts == 0 {
		t.Fatalf("batch linked guest view = %+v dialog=%+v channel=%+v", view.Self, view.Dialog, view.Channel)
	}
	dialogs := appdialogs.NewService(nil, channels)
	peerDialogs, err := dialogs.GetPeerDialogs(ctx, subscriber.ID, []domain.Peer{{Type: domain.PeerTypeChannel, ID: group.Channel.ID}})
	if err != nil {
		t.Fatalf("get linked guest peer dialogs: %v", err)
	}
	if len(peerDialogs.Dialogs) != 1 || len(peerDialogs.Channels) != 1 || len(peerDialogs.ChannelMessages) == 0 {
		t.Fatalf("linked guest peer dialogs = %+v, want transient dialog/channel/top message", peerDialogs)
	}
	if peerDialogs.Dialogs[0].TopMessage == 0 || peerDialogs.Dialogs[0].Pts != view.Channel.Pts {
		t.Fatalf("linked guest dialog = %+v, want top message and pts %d", peerDialogs.Dialogs[0], view.Channel.Pts)
	}
	directReplies, err := channels.ListChannelReplies(ctx, subscriber.ID, domain.ChannelRepliesFilter{
		ChannelID: group.Channel.ID, RootMessageID: post.Discussion.Message.ID, Limit: 20,
	})
	if err != nil || !directReplies.Self.Guest || directReplies.Self.Status != domain.ChannelMemberLeft || directReplies.Channel.ID != group.Channel.ID {
		t.Fatalf("direct linked guest replies = %+v err %v, want guest self for group", directReplies, err)
	}
	viaBroadcastReplies, err := channels.ListChannelReplies(ctx, subscriber.ID, domain.ChannelRepliesFilter{
		ChannelID: broadcast.Channel.ID, RootMessageID: post.Message.ID, Limit: 20,
	})
	if err != nil || !viaBroadcastReplies.Self.Guest || viaBroadcastReplies.Self.Status != domain.ChannelMemberLeft || viaBroadcastReplies.Channel.ID != group.Channel.ID {
		t.Fatalf("broadcast linked guest replies = %+v err %v, want guest self for target group", viaBroadcastReplies, err)
	}

	outsiderViews, err := channels.GetChannels(ctx, outsider.ID, []int64{group.Channel.ID})
	if err != nil || len(outsiderViews) != 0 {
		t.Fatalf("private discussion outsider views = %+v err %v, want empty", outsiderViews, err)
	}
	outsiderDialogs, err := dialogs.GetPeerDialogs(ctx, outsider.ID, []domain.Peer{{Type: domain.PeerTypeChannel, ID: group.Channel.ID}})
	if err != nil || len(outsiderDialogs.Dialogs) != 0 {
		t.Fatalf("private discussion outsider dialogs = %+v err %v, want empty", outsiderDialogs, err)
	}
	if _, err := channels.InviteToChannel(ctx, broadcast.Channel.ID, owner.ID, []int64{outsider.ID}, 1700009504); err != nil {
		t.Fatalf("invite second broadcast subscriber: %v", err)
	}
	if _, err := channels.EditChannelBanned(ctx, domain.EditChannelBannedRequest{
		UserID:      owner.ID,
		ChannelID:   group.Channel.ID,
		Participant: domain.Peer{Type: domain.PeerTypeUser, ID: outsider.ID},
		BannedRights: domain.ChannelBannedRights{
			ViewMessages: true,
			UntilDate:    2147483647,
		},
		Date: 1700009505,
	}); err != nil {
		t.Fatalf("ban linked subscriber from target group: %v", err)
	}
	bannedViews, err := channels.GetChannels(ctx, outsider.ID, []int64{group.Channel.ID})
	if err != nil || len(bannedViews) != 1 || !bannedViews[0].Forbidden || bannedViews[0].Self.Guest {
		t.Fatalf("target-banned linked subscriber views = %+v err %v, want forbidden non-guest", bannedViews, err)
	}
	bannedDialogs, err := dialogs.GetPeerDialogs(ctx, outsider.ID, []domain.Peer{{Type: domain.PeerTypeChannel, ID: group.Channel.ID}})
	if !errors.Is(err, domain.ErrChannelUserBanned) || len(bannedDialogs.Dialogs) != 0 {
		t.Fatalf("target-banned linked subscriber dialogs = %+v err %v, want ErrChannelUserBanned without dialog", bannedDialogs, err)
	}
	if _, err := channels.ListChannelReplies(ctx, outsider.ID, domain.ChannelRepliesFilter{
		ChannelID: group.Channel.ID, RootMessageID: post.Discussion.Message.ID, Limit: 20,
	}); !errors.Is(err, domain.ErrChannelUserBanned) {
		t.Fatalf("target-banned direct replies err = %v, want ErrChannelUserBanned", err)
	}
	if _, err := channels.ListChannelReplies(ctx, outsider.ID, domain.ChannelRepliesFilter{
		ChannelID: broadcast.Channel.ID, RootMessageID: post.Message.ID, Limit: 20,
	}); !errors.Is(err, domain.ErrChannelUserBanned) {
		t.Fatalf("target-banned broadcast replies err = %v, want ErrChannelUserBanned", err)
	}

	var memberExists, dialogExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (
SELECT 1 FROM channel_members WHERE channel_id = $1 AND user_id = $2
)`, group.Channel.ID, subscriber.ID).Scan(&memberExists); err != nil {
		t.Fatalf("check transient guest member: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT EXISTS (
SELECT 1 FROM channel_dialogs WHERE channel_id = $1 AND user_id = $2
)`, group.Channel.ID, subscriber.ID).Scan(&dialogExists); err != nil {
		t.Fatalf("check transient guest dialog: %v", err)
	}
	if memberExists || dialogExists {
		t.Fatalf("transient guest persisted state: member=%v dialog=%v", memberExists, dialogExists)
	}
}
