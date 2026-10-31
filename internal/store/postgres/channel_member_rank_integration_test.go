package postgres

import (
	"context"
	"errors"
	"telesrv/internal/domain"
	"testing"
)

// TestChannelStoreEditMemberRank verifies rank-only editing persistence in PG, the permission matrix,
// and that participant_edit_rank passes the channel_admin_log_events_type_check constraint (0086).
func TestChannelStoreEditMemberRank(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 141, Phone: "+1889" + suffix + "01", FirstName: "RankOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{AccessHash: 142, Phone: "+1889" + suffix + "02", FirstName: "RankMember"})
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
		Title:         "Rank Integration",
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700009000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID

	// The creator sets a tag on an ordinary member: the role is unchanged, the rank is persisted.
	res, err := channels.EditChannelMemberRank(ctx, domain.EditChannelMemberRankRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		MemberID:  member.ID,
		Rank:      "navigator",
		Date:      1700009001,
	})
	if err != nil {
		t.Fatalf("creator edits member rank: %v", err)
	}
	if res.Participant.Role != domain.ChannelRoleMember || res.Participant.Rank != "navigator" {
		t.Fatalf("participant after rank edit = %+v, want member role with navigator rank", res.Participant)
	}
	stored, err := channels.GetParticipant(ctx, owner.ID, channelID, member.ID)
	if err != nil {
		t.Fatalf("get participant: %v", err)
	}
	if stored.Role != domain.ChannelRoleMember || stored.Rank != "navigator" {
		t.Fatalf("stored participant = %+v, want member role with navigator rank", stored)
	}

	// An ordinary member edits themself: switch on → success; default_banned_rights.edit_rank=true → rejected.
	if _, err := channels.EditChannelMemberRank(ctx, domain.EditChannelMemberRankRequest{
		UserID:    member.ID,
		ChannelID: channelID,
		MemberID:  member.ID,
		Rank:      "pilot",
		Date:      1700009002,
	}); err != nil {
		t.Fatalf("member edits own rank: %v", err)
	}
	if _, err := channels.EditChannelDefaultBannedRights(ctx, domain.EditChannelDefaultBannedRightsRequest{
		UserID:       owner.ID,
		ChannelID:    channelID,
		BannedRights: domain.ChannelBannedRights{EditRank: true},
		Date:         1700009003,
	}); err != nil {
		t.Fatalf("disable member tags: %v", err)
	}
	view, err := channels.GetChannel(ctx, member.ID, channelID)
	if err != nil {
		t.Fatalf("get channel: %v", err)
	}
	if !view.Channel.DefaultBannedRights.EditRank {
		t.Fatalf("default banned rights = %+v, want edit_rank=true round-trip", view.Channel.DefaultBannedRights)
	}
	if _, err := channels.EditChannelMemberRank(ctx, domain.EditChannelMemberRankRequest{
		UserID:    member.ID,
		ChannelID: channelID,
		MemberID:  member.ID,
		Rank:      "pilot2",
		Date:      1700009004,
	}); !errors.Is(err, domain.ErrChannelRightForbidden) {
		t.Fatalf("member self edit with switch off err = %v, want ErrChannelRightForbidden", err)
	}
	// An ordinary member editing someone else → requires admin rights.
	if _, err := channels.EditChannelMemberRank(ctx, domain.EditChannelMemberRankRequest{
		UserID:    member.ID,
		ChannelID: channelID,
		MemberID:  owner.ID,
		Rank:      "x",
		Date:      1700009005,
	}); !errors.Is(err, domain.ErrChannelUserCreator) && !errors.Is(err, domain.ErrChannelAdminRequired) {
		t.Fatalf("member edits creator err = %v, want creator/admin-required rejection", err)
	}

	// The manage_ranks admin right bit round-trips through PG.
	if _, err := channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
		UserID:      owner.ID,
		ChannelID:   channelID,
		MemberID:    member.ID,
		AdminRights: domain.ChannelAdminRights{ChangeInfo: true, ManageRanks: true},
		Date:        1700009006,
	}); err != nil {
		t.Fatalf("promote with manage_ranks: %v", err)
	}
	promoted, err := channels.GetParticipant(ctx, owner.ID, channelID, member.ID)
	if err != nil {
		t.Fatalf("get promoted participant: %v", err)
	}
	if !promoted.AdminRights.ManageRanks {
		t.Fatalf("promoted admin rights = %+v, want manage_ranks=true round-trip", promoted.AdminRights)
	}

	// admin log: the participant_edit_rank row is persisted through the 0086 CHECK and is searchable by filter.
	log, err := channels.ListAdminLog(ctx, domain.ChannelAdminLogRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		Limit:     20,
		Filter:    domain.ChannelAdminLogFilter{EditRank: true},
	})
	if err != nil {
		t.Fatalf("list admin log: %v", err)
	}
	if len(log.Events) != 2 {
		t.Fatalf("edit rank admin log events = %d (%+v), want 2", len(log.Events), log.Events)
	}
	for _, event := range log.Events {
		if event.Type != domain.ChannelAdminLogParticipantEditRank {
			t.Fatalf("admin log event type = %s, want participant_edit_rank", event.Type)
		}
		if event.Participant == nil || event.Participant.UserID == 0 {
			t.Fatalf("admin log event participant missing: %+v", event)
		}
	}
	if log.Events[0].NewString != "pilot" || log.Events[1].NewString != "navigator" {
		t.Fatalf("admin log ranks = %q,%q, want pilot,navigator (desc order)", log.Events[0].NewString, log.Events[1].NewString)
	}

	// Re-entry is a brand-new participant: an admin (manage_ranks + rank) becomes an ordinary member after leaving and rejoining,
	// with no admin rights and no rank; some restriction lists are independent of membership and remain in effect after re-entry.
	if _, err := channels.EditChannelBanned(ctx, domain.EditChannelBannedRequest{
		UserID:       owner.ID,
		ChannelID:    channelID,
		Participant:  domain.Peer{Type: domain.PeerTypeUser, ID: member.ID},
		BannedRights: domain.ChannelBannedRights{SendMessages: true},
		Date:         1700009007,
	}); err != nil {
		t.Fatalf("restrict member: %v", err)
	}
	if _, err := channels.LeaveChannel(ctx, channelID, member.ID, 1700009008); err != nil {
		t.Fatalf("member leaves: %v", err)
	}
	if _, err := channels.JoinChannel(ctx, channelID, member.ID, 1700009009); err != nil {
		t.Fatalf("member rejoins: %v", err)
	}
	rejoined, err := channels.GetParticipant(ctx, owner.ID, channelID, member.ID)
	if err != nil {
		t.Fatalf("get rejoined member: %v", err)
	}
	if rejoined.Role != domain.ChannelRoleMember || rejoined.Rank != "" || rejoined.AdminRights != (domain.ChannelAdminRights{}) {
		t.Fatalf("rejoined member = %+v, want plain member without rank/admin rights", rejoined)
	}
	if !rejoined.BannedRights.SendMessages {
		t.Fatalf("rejoined member banned rights = %+v, want partial restriction kept", rejoined.BannedRights)
	}
	if rejoined.JoinedAt != 1700009009 || rejoined.InviterUserID != 0 {
		t.Fatalf("rejoined member joined_at/inviter = %d/%d, want fresh join date and no inviter", rejoined.JoinedAt, rejoined.InviterUserID)
	}

	// The creator leaving hands ownership to another active member; the old creator rejoining is an ordinary member,
	// keeping no creator tag.
	if _, err := channels.EditChannelMemberRank(ctx, domain.EditChannelMemberRankRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		MemberID:  owner.ID,
		Rank:      "captain",
		Date:      1700009010,
	}); err != nil {
		t.Fatalf("creator sets own rank: %v", err)
	}
	if _, err := channels.LeaveChannel(ctx, channelID, owner.ID, 1700009011); err != nil {
		t.Fatalf("creator leaves: %v", err)
	}
	newCreator, err := channels.GetParticipant(ctx, member.ID, channelID, member.ID)
	if err != nil {
		t.Fatalf("get future creator: %v", err)
	}
	if newCreator.Role != domain.ChannelRoleCreator {
		t.Fatalf("future creator = %+v, want creator role", newCreator)
	}
	if _, err := channels.JoinChannel(ctx, channelID, owner.ID, 1700009012); err != nil {
		t.Fatalf("creator rejoins: %v", err)
	}
	creatorBack, err := channels.GetParticipant(ctx, member.ID, channelID, owner.ID)
	if err != nil {
		t.Fatalf("get rejoined old creator: %v", err)
	}
	if creatorBack.Role != domain.ChannelRoleMember || creatorBack.Rank != "" {
		t.Fatalf("rejoined old creator = %+v, want plain member without rank", creatorBack)
	}

	// Member Tags are megagroup-only: on a broadcast, creator/self always get MEGAGROUP_ID_INVALID.
	var broadcastID int64
	t.Cleanup(func() {
		if broadcastID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", broadcastID)
		}
	})
	broadcast, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Rank Broadcast",
		Broadcast:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700009100,
	})
	if err != nil {
		t.Fatalf("create broadcast: %v", err)
	}
	broadcastID = broadcast.Channel.ID
	if _, err := channels.EditChannelMemberRank(ctx, domain.EditChannelMemberRankRequest{
		UserID:    owner.ID,
		ChannelID: broadcastID,
		MemberID:  member.ID,
		Rank:      "vip",
		Date:      1700009101,
	}); !errors.Is(err, domain.ErrMegagroupIDInvalid) {
		t.Fatalf("creator tags broadcast subscriber err = %v, want ErrMegagroupIDInvalid", err)
	}
	if _, err := channels.EditChannelMemberRank(ctx, domain.EditChannelMemberRankRequest{
		UserID:    member.ID,
		ChannelID: broadcastID,
		MemberID:  member.ID,
		Rank:      "fan",
		Date:      1700009102,
	}); !errors.Is(err, domain.ErrMegagroupIDInvalid) {
		t.Fatalf("broadcast subscriber tags self err = %v, want ErrMegagroupIDInvalid", err)
	}
}
