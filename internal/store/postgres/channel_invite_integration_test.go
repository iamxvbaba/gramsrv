package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"telesrv/internal/domain"
	"testing"
	"time"
)

func TestChannelStoreCreateChannelCreatesPermanentInviteAndHasLink(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 30,
		Phone:      "+1777" + suffix + "11",
		FirstName:  "LinkOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", owner.ID)
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Linked On Create " + suffix,
		Megagroup:     true,
		Date:          1700000300,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID
	if !created.Channel.HasLink {
		t.Fatalf("created channel HasLink = false, want true")
	}
	view, err := channels.GetChannel(ctx, owner.ID, channelID)
	if err != nil {
		t.Fatalf("get channel: %v", err)
	}
	if !view.Channel.HasLink {
		t.Fatalf("stored channel HasLink = false, want true")
	}
	if view.ExportedInvite == nil || !view.ExportedInvite.Permanent || view.ExportedInvite.Revoked || view.ExportedInvite.AdminUserID != owner.ID {
		t.Fatalf("owner view exported invite = %+v, want creator permanent main link", view.ExportedInvite)
	}
	invites, err := channels.ListExportedInvites(ctx, domain.ChannelInviteListRequest{
		UserID:      owner.ID,
		ChannelID:   channelID,
		AdminUserID: owner.ID,
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("list exported invites: %v", err)
	}
	if invites.Count != 1 || len(invites.Invites) != 1 || !invites.Invites[0].Permanent || invites.Invites[0].Revoked {
		t.Fatalf("invites = %+v, want one active permanent main link", invites)
	}
}

func TestChannelStoreJoinRejectsKickedMember(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 33,
		Phone:      "+1777" + suffix + "21",
		FirstName:  "BanOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{
		AccessHash: 34,
		Phone:      "+1777" + suffix + "22",
		FirstName:  "BanMember",
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	helper, err := users.Create(ctx, domain.User{
		AccessHash: 35,
		Phone:      "+1777" + suffix + "23",
		FirstName:  "BanHelper",
	})
	if err != nil {
		t.Fatalf("create helper: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, member.ID, helper.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Ban Join " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID, helper.ID},
		Date:          1700000305,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID
	ptsFloor := created.Channel.Pts
	banned, err := channels.EditChannelBanned(ctx, domain.EditChannelBannedRequest{
		UserID:      owner.ID,
		ChannelID:   channelID,
		Participant: domain.Peer{Type: domain.PeerTypeUser, ID: member.ID},
		BannedRights: domain.ChannelBannedRights{
			ViewMessages: true,
			UntilDate:    1700001300,
		},
		Date: 1700000306,
	})
	if err != nil {
		t.Fatalf("kick member: %v", err)
	}
	// A participant update does not consume channel pts, but kicking in a megagroup produces an
	// "X removed Y" service message that consumes one pts; offline members converge their member state via it.
	if banned.Event.Pts != 0 || banned.Event.PtsCount != 0 {
		t.Fatalf("kick participant event = (%d,%d), must stay transient", banned.Event.Pts, banned.Event.PtsCount)
	}
	if banned.ServiceEvent.Pts != ptsFloor+1 || banned.Channel.Pts != ptsFloor+1 {
		t.Fatalf("kick service pts = %d channel %d, want %d", banned.ServiceEvent.Pts, banned.Channel.Pts, ptsFloor+1)
	}
	if banned.Message.Action == nil || banned.Message.Action.Type != domain.ChannelActionChatDelete {
		t.Fatalf("kick service message = %+v, want ChatDelete action", banned.Message)
	}
	banDiff, err := channels.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		Pts:       ptsFloor,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("difference after kick: %v", err)
	}
	if len(banDiff.Events) != 1 || banDiff.Pts != ptsFloor+1 {
		t.Fatalf("difference after kick = %+v, want only the kick service message at pts %d", banDiff, ptsFloor+1)
	}
	if _, err := channels.JoinChannel(ctx, channelID, member.ID, 1700000307); !errors.Is(err, domain.ErrChannelUserBanned) {
		t.Fatalf("kicked JoinChannel err = %v, want ErrChannelUserBanned", err)
	}
	if _, err := channels.InviteToChannel(ctx, channelID, helper.ID, []int64{member.ID}, 1700000308); !errors.Is(err, domain.ErrUserKicked) {
		t.Fatalf("helper InviteToChannel kicked err = %v, want ErrUserKicked", err)
	}
	restored, err := channels.InviteToChannel(ctx, channelID, owner.ID, []int64{member.ID}, 1700000309)
	if err != nil {
		t.Fatalf("owner InviteToChannel kicked: %v", err)
	}
	if len(restored.Members) != 1 || restored.Members[0].Status != domain.ChannelMemberActive || restored.Members[0].BannedRights != (domain.ChannelBannedRights{}) {
		t.Fatalf("restored members = %+v, want active unbanned member", restored.Members)
	}
	if restored.Channel.ParticipantsCount != 3 || restored.Channel.KickedCount != 0 {
		t.Fatalf("restored counts = participants:%d kicked:%d, want 3/0", restored.Channel.ParticipantsCount, restored.Channel.KickedCount)
	}
	if _, err := channels.InviteToChannel(ctx, channelID, owner.ID, []int64{member.ID}, 1700000310); !errors.Is(err, domain.ErrUserAlreadyParticipant) {
		t.Fatalf("duplicate InviteToChannel err = %v, want ErrUserAlreadyParticipant", err)
	}
}

func TestChannelStoreImportInviteRequestNeededAndUsageLimitErrors(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 61,
		Phone:      "+1888" + suffix + "21",
		FirstName:  "InviteErrorOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	first, err := users.Create(ctx, domain.User{
		AccessHash: 62,
		Phone:      "+1888" + suffix + "22",
		FirstName:  "InviteErrorFirst",
	})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := users.Create(ctx, domain.User{
		AccessHash: 63,
		Phone:      "+1888" + suffix + "23",
		FirstName:  "InviteErrorSecond",
	})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, first.ID, second.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Invite Errors " + suffix,
		Megagroup:     true,
		Date:          1700000340,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID
	requested, err := channels.ExportInvite(ctx, domain.ExportChannelInviteRequest{
		UserID:        owner.ID,
		ChannelID:     channelID,
		Title:         "approval",
		RequestNeeded: true,
		Date:          1700000341,
	})
	if err != nil {
		t.Fatalf("export request-needed invite: %v", err)
	}
	if _, err := channels.ImportInvite(ctx, domain.ImportChannelInviteRequest{
		UserID: first.ID,
		Hash:   requested.Invite.Hash,
		Date:   1700000342,
	}); !errors.Is(err, domain.ErrInviteRequestSent) {
		t.Fatalf("import request-needed err = %v, want ErrInviteRequestSent", err)
	}
	limited, err := channels.ExportInvite(ctx, domain.ExportChannelInviteRequest{
		UserID:     owner.ID,
		ChannelID:  channelID,
		Title:      "one",
		UsageLimit: 1,
		Date:       1700000343,
	})
	if err != nil {
		t.Fatalf("export limited invite: %v", err)
	}
	if _, err := channels.ImportInvite(ctx, domain.ImportChannelInviteRequest{
		UserID: first.ID,
		Hash:   limited.Invite.Hash,
		Date:   1700000344,
	}); err != nil {
		t.Fatalf("first import limited invite: %v", err)
	}
	if _, err := channels.ImportInvite(ctx, domain.ImportChannelInviteRequest{
		UserID: second.ID,
		Hash:   limited.Invite.Hash,
		Date:   1700000345,
	}); !errors.Is(err, domain.ErrUsersTooMuch) {
		t.Fatalf("second import limited invite err = %v, want ErrUsersTooMuch", err)
	}
}

func TestChannelStorePendingJoinRequestsSummaryAndInviteAdmins(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	userIDs := make([]int64, 0, 11)
	createUser := func(label string, accessHash int64, phoneSuffix int) domain.User {
		t.Helper()
		user, err := users.Create(ctx, domain.User{
			AccessHash: accessHash,
			Phone:      fmt.Sprintf("+1889%s%02d", suffix, phoneSuffix),
			FirstName:  label,
		})
		if err != nil {
			t.Fatalf("create %s: %v", label, err)
		}
		userIDs = append(userIDs, user.ID)
		return user
	}
	owner := createUser("PendingOwner", 71, 1)
	inviteAdmin := createUser("PendingInviteAdmin", 72, 2)
	plainMember := createUser("PendingPlainMember", 73, 3)
	changeAdmin := createUser("PendingChangeAdmin", 74, 4)
	requesters := make([]domain.User, 0, domain.MaxChannelPendingJoinRecentRequesters+2)
	for i := 0; i < domain.MaxChannelPendingJoinRecentRequesters+2; i++ {
		requesters = append(requesters, createUser("PendingRequester", int64(80+i), 10+i))
	}

	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", userIDs)
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Pending Summary " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{inviteAdmin.ID, plainMember.ID, changeAdmin.ID},
		Date:          1700000360,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID
	if _, err := channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		MemberID:  inviteAdmin.ID,
		AdminRights: domain.ChannelAdminRights{
			InviteUsers: true,
		},
		Date: 1700000361,
	}); err != nil {
		t.Fatalf("promote invite admin: %v", err)
	}
	if _, err := channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		MemberID:  changeAdmin.ID,
		AdminRights: domain.ChannelAdminRights{
			ChangeInfo: true,
		},
		Date: 1700000362,
	}); err != nil {
		t.Fatalf("promote change-info admin: %v", err)
	}
	invite, err := channels.ExportInvite(ctx, domain.ExportChannelInviteRequest{
		UserID:        owner.ID,
		ChannelID:     channelID,
		Title:         "approval",
		RequestNeeded: true,
		Date:          1700000363,
	})
	if err != nil {
		t.Fatalf("export invite: %v", err)
	}
	for i, requester := range requesters {
		_, err := channels.ImportInvite(ctx, domain.ImportChannelInviteRequest{
			UserID: requester.ID,
			Hash:   invite.Invite.Hash,
			Date:   1700000370 + i,
		})
		if !errors.Is(err, domain.ErrInviteRequestSent) {
			t.Fatalf("import pending %d err = %v, want ErrInviteRequestSent", i, err)
		}
	}
	pending, err := channels.PendingJoinRequests(ctx, channelID, 99)
	if err != nil {
		t.Fatalf("pending join requests: %v", err)
	}
	if pending.Count != len(requesters) || len(pending.RecentRequesters) != domain.MaxChannelPendingJoinRecentRequesters {
		t.Fatalf("pending summary = %+v, want bounded recent with full count", pending)
	}
	if pending.RecentRequesters[0] != requesters[len(requesters)-1].ID ||
		pending.RecentRequesters[len(pending.RecentRequesters)-1] != requesters[2].ID {
		t.Fatalf("recent requesters = %+v, want newest first", pending.RecentRequesters)
	}
	admins, err := channels.ListChannelInviteAdminMemberIDs(ctx, channelID, 0)
	if err != nil {
		t.Fatalf("invite admins: %v", err)
	}
	want := []int64{owner.ID, inviteAdmin.ID, changeAdmin.ID}
	if !reflect.DeepEqual(admins, want) {
		t.Fatalf("invite admins = %+v, want %+v", admins, want)
	}
}

func TestChannelStoreImportInviteUsageLimitSeesConcurrentIncrement(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 64,
		Phone:      "+1888" + suffix + "31",
		FirstName:  "InviteLimitOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	joiner, err := users.Create(ctx, domain.User{
		AccessHash: 65,
		Phone:      "+1888" + suffix + "32",
		FirstName:  "InviteLimitJoiner",
	})
	if err != nil {
		t.Fatalf("create joiner: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, joiner.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Invite Limit Race " + suffix,
		Megagroup:     true,
		Date:          1700000350,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID
	invite, err := channels.ExportInvite(ctx, domain.ExportChannelInviteRequest{
		UserID:     owner.ID,
		ChannelID:  channelID,
		Title:      "single",
		UsageLimit: 1,
		Date:       1700000351,
	})
	if err != nil {
		t.Fatalf("export limited invite: %v", err)
	}

	lockTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock tx: %v", err)
	}
	defer func() { _ = lockTx.Rollback(ctx) }()
	if _, err := lockTx.Exec(ctx, `
UPDATE channel_invites
SET usage_count = usage_limit
WHERE channel_id = $1 AND invite_id = $2`, channelID, invite.Invite.InviteID); err != nil {
		t.Fatalf("lock and update invite usage: %v", err)
	}

	importCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := channels.ImportInvite(importCtx, domain.ImportChannelInviteRequest{
			UserID: joiner.ID,
			Hash:   invite.Invite.Hash,
			Date:   1700000352,
		})
		errCh <- err
	}()
	time.Sleep(100 * time.Millisecond)
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatalf("commit lock tx: %v", err)
	}
	err = <-errCh
	if !errors.Is(err, domain.ErrUsersTooMuch) {
		t.Fatalf("concurrent import err = %v, want ErrUsersTooMuch after seeing committed usage_count", err)
	}
	if _, err := channels.GetChannel(ctx, joiner.ID, channelID); !errors.Is(err, domain.ErrChannelPrivate) {
		t.Fatalf("joiner channel after rejected import err = %v, want ErrChannelPrivate", err)
	}
}

// TestChannelStoreEnsurePermanentInvite verifies the primary link's idempotency guarantee: create when missing, reuse when present,
// recreate after revocation, and reject unauthorized users (after creating a channel DrKLO takes
// invites[0] directly from getExportedChatInvites; a missing primary link crashes the client).
func TestChannelStoreEnsurePermanentInvite(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 71,
		Phone:      "+1888" + suffix + "41",
		FirstName:  "EnsureOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	outsider, err := users.Create(ctx, domain.User{
		AccessHash: 72,
		Phone:      "+1888" + suffix + "42",
		FirstName:  "EnsureOutsider",
	})
	if err != nil {
		t.Fatalf("create outsider: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channel_invites WHERE channel_id = $1", channelID)
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, outsider.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Ensure Permanent " + suffix,
		Broadcast:     true,
		Date:          1700000400,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID

	first, err := channels.EnsurePermanentInvite(ctx, channelID, owner.ID, 1700000401)
	if err != nil {
		t.Fatalf("ensure permanent invite: %v", err)
	}
	if !first.Permanent || first.Revoked || first.AdminUserID != owner.ID || first.Hash == "" {
		t.Fatalf("ensured invite = %+v, want creator's non-revoked permanent link", first)
	}

	second, err := channels.EnsurePermanentInvite(ctx, channelID, owner.ID, 1700000500)
	if err != nil {
		t.Fatalf("ensure permanent invite again: %v", err)
	}
	if second.Hash != first.Hash {
		t.Fatalf("second ensure hash = %q, want stable %q (must not duplicate)", second.Hash, first.Hash)
	}

	if _, err := channels.EditExportedInvite(ctx, domain.EditChannelInviteRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		Hash:      first.Hash,
		Revoked:   true,
		Date:      1700000600,
	}); err != nil {
		t.Fatalf("revoke permanent invite: %v", err)
	}
	third, err := channels.EnsurePermanentInvite(ctx, channelID, owner.ID, 1700000601)
	if err != nil {
		t.Fatalf("ensure after revoke: %v", err)
	}
	if !third.Permanent || third.Revoked || third.Hash == first.Hash {
		t.Fatalf("post-revoke ensure = %+v, want fresh non-revoked permanent link", third)
	}

	if _, err := channels.EnsurePermanentInvite(ctx, channelID, outsider.ID, 0); err == nil {
		t.Fatal("ensure by non-member must fail")
	}

	list, err := channels.ListExportedInvites(ctx, domain.ChannelInviteListRequest{
		UserID:      owner.ID,
		ChannelID:   channelID,
		AdminUserID: owner.ID,
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("list invites: %v", err)
	}
	found := false
	for _, invite := range list.Invites {
		if invite.Hash == third.Hash && invite.Permanent && !invite.Revoked {
			found = true
		}
	}
	if !found {
		t.Fatalf("invite list = %+v, missing ensured permanent link %q", list.Invites, third.Hash)
	}
}
