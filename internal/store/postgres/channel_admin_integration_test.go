package postgres

import (
	"context"
	"errors"
	"telesrv/internal/domain"
	"testing"
)

func TestChannelStoreEditAboutPersistsAndChecksPermission(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 131,
		Phone:      "+1888" + suffix + "01",
		FirstName:  "AboutOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{
		AccessHash: 132,
		Phone:      "+1888" + suffix + "02",
		FirstName:  "AboutMember",
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
		Title:         "About " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700000600,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID

	if _, err := channels.EditChannelAbout(ctx, domain.EditChannelAboutRequest{
		UserID:    member.ID,
		ChannelID: channelID,
		About:     "member cannot edit",
		Date:      1700000601,
	}); !errors.Is(err, domain.ErrChannelAdminRequired) {
		t.Fatalf("EditChannelAbout by member err = %v, want ErrChannelAdminRequired", err)
	}

	updated, err := channels.EditChannelAbout(ctx, domain.EditChannelAboutRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		About:     "owner about",
		Date:      1700000602,
	})
	if err != nil {
		t.Fatalf("EditChannelAbout by owner: %v", err)
	}
	if updated.About != "owner about" {
		t.Fatalf("updated about = %q, want owner about", updated.About)
	}
	view, err := channels.GetChannel(ctx, member.ID, channelID)
	if err != nil {
		t.Fatalf("GetChannel by member: %v", err)
	}
	if view.Channel.About != "owner about" {
		t.Fatalf("member view about = %q, want owner about", view.Channel.About)
	}
}

func TestChannelStoreCreatorCanEditOwnAdminRights(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 141,
		Phone:      "+1888" + suffix + "11",
		FirstName:  "CreatorSelfOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	admin, err := users.Create(ctx, domain.User{
		AccessHash: 142,
		Phone:      "+1888" + suffix + "12",
		FirstName:  "CreatorSelfAdmin",
	})
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, admin.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Creator Self " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{admin.ID},
		Date:          1700000610,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID

	edited, err := channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		MemberID:  owner.ID,
		AdminRights: domain.ChannelAdminRights{
			Anonymous: true,
		},
		Date: 1700000611,
	})
	if err != nil {
		t.Fatalf("creator edits own admin rights: %v", err)
	}
	if edited.Participant.Role != domain.ChannelRoleCreator || !edited.Participant.AdminRights.Anonymous || !edited.Participant.AdminRights.ChangeInfo || !edited.Participant.AdminRights.AddAdmins {
		t.Fatalf("edited creator = %+v, want creator with anonymous plus full creator projection", edited.Participant)
	}
	persisted, err := channels.GetParticipant(ctx, owner.ID, channelID, owner.ID)
	if err != nil {
		t.Fatalf("get creator participant: %v", err)
	}
	if persisted.Role != domain.ChannelRoleCreator || !persisted.AdminRights.Anonymous || !persisted.AdminRights.ChangeInfo || !persisted.AdminRights.AddAdmins {
		t.Fatalf("persisted creator = %+v, want creator with anonymous plus full creator projection", persisted)
	}

	if _, err := channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		MemberID:  admin.ID,
		AdminRights: domain.ChannelAdminRights{
			ChangeInfo: true,
			AddAdmins:  true,
		},
		Date: 1700000612,
	}); err != nil {
		t.Fatalf("promote admin: %v", err)
	}
	if _, err := channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
		UserID:    admin.ID,
		ChannelID: channelID,
		MemberID:  owner.ID,
		AdminRights: domain.ChannelAdminRights{
			ChangeInfo: true,
		},
		Date: 1700000613,
	}); !errors.Is(err, domain.ErrChannelUserCreator) {
		t.Fatalf("admin edits creator err = %v, want ErrChannelUserCreator", err)
	}
}

func TestChannelStoreAdminLogFiltersAndSearch(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 51,
		Phone:      "+1999" + suffix + "01",
		FirstName:  "AdminLogOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := users.Create(ctx, domain.User{
		AccessHash: 52,
		Phone:      "+1999" + suffix + "02",
		FirstName:  "AdminLogFriend",
	})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	invited, err := users.Create(ctx, domain.User{
		AccessHash: 53,
		Phone:      "+1999" + suffix + "03",
		FirstName:  "AdminLogInvited",
	})
	if err != nil {
		t.Fatalf("create invited: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, friend.ID, invited.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Admin Log " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{friend.ID},
		Date:          1700000500,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID

	if _, err := channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		MemberID:  friend.ID,
		AdminRights: domain.ChannelAdminRights{
			ChangeInfo:  true,
			InviteUsers: true,
			PinMessages: true,
		},
		Rank: "ops",
		Date: 1700000501,
	}); err != nil {
		t.Fatalf("edit admin: %v", err)
	}
	sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  501,
		Message:   "needle admin log body",
		Date:      1700000502,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	if _, err := channels.UpdatePinnedMessage(ctx, domain.UpdateChannelPinnedMessageRequest{
		UserID:    friend.ID,
		ChannelID: channelID,
		MessageID: sent.Message.ID,
		Pinned:    true,
		Date:      1700000503,
	}); err != nil {
		t.Fatalf("pin message: %v", err)
	}
	if _, err := channels.InviteToChannel(ctx, channelID, friend.ID, []int64{invited.ID}, 1700000504); err != nil {
		t.Fatalf("invite to channel: %v", err)
	}

	searched, err := channels.ListAdminLog(ctx, domain.ChannelAdminLogRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		Query:     "needle",
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("search admin log: %v", err)
	}
	if len(searched.Events) == 0 {
		t.Fatalf("search admin log returned no events, want message body match")
	}

	pinned, err := channels.ListAdminLog(ctx, domain.ChannelAdminLogRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		Filter:    domain.ChannelAdminLogFilter{Pinned: true},
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("pinned admin log: %v", err)
	}
	if len(pinned.Events) != 1 || pinned.Events[0].Type != domain.ChannelAdminLogUpdatePinned || pinned.Events[0].Message == nil {
		t.Fatalf("pinned events = %+v, want one update_pinned with message", pinned.Events)
	}

	byFriend, err := channels.ListAdminLog(ctx, domain.ChannelAdminLogRequest{
		UserID:       owner.ID,
		ChannelID:    channelID,
		AdminUserIDs: []int64{friend.ID},
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("friend admin log: %v", err)
	}
	if len(byFriend.Events) == 0 {
		t.Fatalf("friend admin log returned no events, want pin/invite")
	}
	for _, event := range byFriend.Events {
		if event.UserID != friend.ID {
			t.Fatalf("friend admin log event actor = %d, want %d in %+v", event.UserID, friend.ID, byFriend.Events)
		}
	}

	if _, err := channels.ListAdminLog(ctx, domain.ChannelAdminLogRequest{
		UserID:    invited.ID,
		ChannelID: channelID,
		Limit:     10,
	}); err != domain.ErrChannelAdminRequired {
		t.Fatalf("member admin log err = %v, want ErrChannelAdminRequired", err)
	}
}

// TestChannelStoreListSendAsChannelsPostgres exercises the postgres ListSendAsChannels query against a
// real database: creator-owned and PostMessages-admin broadcast channels are included; an admin without
// PostMessages, an owned megagroup, and a member-only broadcast are excluded.
func TestChannelStoreListSendAsChannelsPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 211, Phone: "+1444" + suffix + "01", FirstName: "SendAsOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	host, err := users.Create(ctx, domain.User{AccessHash: 212, Phone: "+1444" + suffix + "02", FirstName: "SendAsHost"})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}

	channels := NewChannelStore(pool)
	channelIDs := make([]int64, 0, 5)
	t.Cleanup(func() {
		for _, id := range channelIDs {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", id)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, host.ID})
	})
	mk := func(req domain.CreateChannelRequest) domain.CreateChannelResult {
		created, err := channels.CreateChannel(ctx, req)
		if err != nil {
			t.Fatalf("create channel %q: %v", req.Title, err)
		}
		channelIDs = append(channelIDs, created.Channel.ID)
		return created
	}

	owned := mk(domain.CreateChannelRequest{CreatorUserID: owner.ID, Title: "SA Owned " + suffix, Broadcast: true, Date: 1700000700})
	postAdmin := mk(domain.CreateChannelRequest{CreatorUserID: host.ID, Title: "SA PostAdmin " + suffix, Broadcast: true, MemberUserIDs: []int64{owner.ID}, Date: 1700000701})
	editAdmin := mk(domain.CreateChannelRequest{CreatorUserID: host.ID, Title: "SA EditAdmin " + suffix, Broadcast: true, MemberUserIDs: []int64{owner.ID}, Date: 1700000702})
	megagroup := mk(domain.CreateChannelRequest{CreatorUserID: owner.ID, Title: "SA Megagroup " + suffix, Megagroup: true, Date: 1700000703})
	memberOnly := mk(domain.CreateChannelRequest{CreatorUserID: host.ID, Title: "SA Member " + suffix, Broadcast: true, MemberUserIDs: []int64{owner.ID}, Date: 1700000704})

	if _, err := channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
		UserID:      host.ID,
		ChannelID:   postAdmin.Channel.ID,
		MemberID:    owner.ID,
		AdminRights: domain.ChannelAdminRights{PostMessages: true},
		Date:        1700000711,
	}); err != nil {
		t.Fatalf("edit post-messages admin: %v", err)
	}
	if _, err := channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
		UserID:      host.ID,
		ChannelID:   editAdmin.Channel.ID,
		MemberID:    owner.ID,
		AdminRights: domain.ChannelAdminRights{EditMessages: true, DeleteMessages: true},
		Date:        1700000712,
	}); err != nil {
		t.Fatalf("edit edit-only admin: %v", err)
	}

	list, err := channels.ListSendAsChannels(ctx, owner.ID)
	if err != nil {
		t.Fatalf("ListSendAsChannels: %v", err)
	}
	got := make(map[int64]bool, len(list))
	for _, ch := range list {
		got[ch.ID] = true
	}
	if !got[owned.Channel.ID] || !got[postAdmin.Channel.ID] {
		t.Fatalf("send-as channels %v missing creator-owned %d or post-admin %d", got, owned.Channel.ID, postAdmin.Channel.ID)
	}
	if got[editAdmin.Channel.ID] || got[megagroup.Channel.ID] || got[memberOnly.Channel.ID] {
		t.Fatalf("send-as channels %v should exclude edit-admin %d, megagroup %d, member-only %d", got, editAdmin.Channel.ID, megagroup.Channel.ID, memberOnly.Channel.ID)
	}
}

func TestChannelStoreCommonChannelsOnlySharedMegagroups(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 51, Phone: "+1888" + suffix + "01", FirstName: "CommonOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := users.Create(ctx, domain.User{AccessHash: 52, Phone: "+1888" + suffix + "02", FirstName: "CommonFriend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	other, err := users.Create(ctx, domain.User{AccessHash: 53, Phone: "+1888" + suffix + "03", FirstName: "CommonOther"})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	var channelIDs []int64
	t.Cleanup(func() {
		if len(channelIDs) != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = ANY($1::bigint[])", channelIDs)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, friend.ID, other.ID})
	})

	channels := NewChannelStore(pool)
	create := func(title string, broadcast bool, memberIDs []int64, date int) domain.CreateChannelResult {
		t.Helper()
		created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
			CreatorUserID: owner.ID,
			Title:         title,
			Broadcast:     broadcast,
			Megagroup:     !broadcast,
			MemberUserIDs: memberIDs,
			Date:          date,
		})
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		channelIDs = append(channelIDs, created.Channel.ID)
		return created
	}
	first := create("common one "+suffix, false, []int64{friend.ID}, 1700000800)
	second := create("common two "+suffix, false, []int64{friend.ID}, 1700000801)
	create("broadcast excluded "+suffix, true, []int64{friend.ID}, 1700000802)
	left := create("left excluded "+suffix, false, []int64{friend.ID}, 1700000803)
	if _, err := channels.LeaveChannel(ctx, left.Channel.ID, friend.ID, 1700000804); err != nil {
		t.Fatalf("leave channel: %v", err)
	}
	create("not shared "+suffix, false, []int64{other.ID}, 1700000805)

	page, err := channels.ListCommonChannels(ctx, domain.CommonChannelsRequest{
		UserID:       owner.ID,
		TargetUserID: friend.ID,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("list common channels: %v", err)
	}
	if page.Count != 2 || len(page.Channels) != 2 || page.Channels[0].ID != first.Channel.ID || page.Channels[1].ID != second.Channel.ID {
		t.Fatalf("common channels = %+v, want two shared megagroups in id order", page)
	}

	next, err := channels.ListCommonChannels(ctx, domain.CommonChannelsRequest{
		UserID:       owner.ID,
		TargetUserID: friend.ID,
		MaxID:        first.Channel.ID,
		Limit:        1,
	})
	if err != nil {
		t.Fatalf("list common channels after max id: %v", err)
	}
	if next.Count != 2 || len(next.Channels) != 1 || next.Channels[0].ID != second.Channel.ID {
		t.Fatalf("paged common channels = %+v, want second channel with full count", next)
	}

	countOnly, err := channels.ListCommonChannels(ctx, domain.CommonChannelsRequest{
		UserID:       owner.ID,
		TargetUserID: friend.ID,
		CountOnly:    true,
	})
	if err != nil {
		t.Fatalf("count common channels: %v", err)
	}
	if countOnly.Count != 2 || len(countOnly.Channels) != 0 {
		t.Fatalf("count-only common channels = %+v, want count without channels", countOnly)
	}
}

func TestChannelStoreListActiveChannelIDsForUserUsesMembershipIndexState(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 81, Phone: "+1888" + suffix + "41", FirstName: "ActiveIndexOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := users.Create(ctx, domain.User{AccessHash: 82, Phone: "+1888" + suffix + "42", FirstName: "ActiveIndexFriend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	var channelIDs []int64
	t.Cleanup(func() {
		if len(channelIDs) != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = ANY($1::bigint[])", channelIDs)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, friend.ID})
	})

	channels := NewChannelStore(pool)
	create := func(title string, date int) domain.CreateChannelResult {
		t.Helper()
		created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
			CreatorUserID: owner.ID,
			Title:         title,
			Megagroup:     true,
			MemberUserIDs: []int64{friend.ID},
			Date:          date,
		})
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		channelIDs = append(channelIDs, created.Channel.ID)
		return created
	}
	activeA := create("active index one "+suffix, 1700000830)
	left := create("left index excluded "+suffix, 1700000831)
	activeB := create("active index two "+suffix, 1700000832)
	deleted := create("deleted index excluded "+suffix, 1700000833)
	if _, err := channels.LeaveChannel(ctx, left.Channel.ID, friend.ID, 1700000834); err != nil {
		t.Fatalf("leave channel: %v", err)
	}
	if _, err := channels.DeleteChannel(ctx, domain.DeleteChannelRequest{
		UserID:    owner.ID,
		ChannelID: deleted.Channel.ID,
		Date:      1700000835,
	}); err != nil {
		t.Fatalf("delete channel: %v", err)
	}

	want := []int64{activeA.Channel.ID, activeB.Channel.ID}
	if want[0] > want[1] {
		want[0], want[1] = want[1], want[0]
	}
	got, err := channels.ListActiveChannelIDsForUser(ctx, friend.ID, 0, 10)
	if err != nil {
		t.Fatalf("list active channel ids: %v", err)
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("active channel ids = %v, want %v", got, want)
	}

	firstPage, err := channels.ListActiveChannelIDsForUser(ctx, friend.ID, 0, 1)
	if err != nil {
		t.Fatalf("list first active channel id: %v", err)
	}
	if len(firstPage) != 1 || firstPage[0] != want[0] {
		t.Fatalf("first active page = %v, want [%d]", firstPage, want[0])
	}
	nextPage, err := channels.ListActiveChannelIDsForUser(ctx, friend.ID, want[0], 10)
	if err != nil {
		t.Fatalf("list next active channel ids: %v", err)
	}
	if len(nextPage) != 1 || nextPage[0] != want[1] {
		t.Fatalf("next active page = %v, want [%d]", nextPage, want[1])
	}
	empty, err := channels.ListActiveChannelIDsForUser(ctx, friend.ID, want[1], 10)
	if err != nil {
		t.Fatalf("list active channel ids after last: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("active ids after last = %v, want empty", empty)
	}
	if _, err := channels.ListActiveChannelIDsForUser(ctx, friend.ID, -1, 10); !errors.Is(err, domain.ErrChannelInvalid) {
		t.Fatalf("negative cursor err = %v, want ErrChannelInvalid", err)
	}
}

func TestChannelStoreUserChannelMemberIndexListPaths(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 91, Phone: "+1888" + suffix + "51", FirstName: "IndexListOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := users.Create(ctx, domain.User{AccessHash: 92, Phone: "+1888" + suffix + "52", FirstName: "IndexListFriend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	var channelIDs []int64
	t.Cleanup(func() {
		if len(channelIDs) != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = ANY($1::bigint[])", channelIDs)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, friend.ID})
	})

	channels := NewChannelStore(pool)
	publicCandidate, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "index public " + suffix,
		Megagroup:     true,
		Date:          1700000840,
	})
	if err != nil {
		t.Fatalf("create public candidate: %v", err)
	}
	channelIDs = append(channelIDs, publicCandidate.Channel.ID)
	if _, err := channels.UpdateUsername(ctx, domain.UpdateChannelUsernameRequest{
		UserID:    owner.ID,
		ChannelID: publicCandidate.Channel.ID,
		Username:  "idxpublic" + suffix,
	}); err != nil {
		t.Fatalf("set public username: %v", err)
	}
	admined, err := channels.ListAdminedPublicChannels(ctx, owner.ID)
	if err != nil {
		t.Fatalf("list admined public channels: %v", err)
	}
	if len(admined) != 1 || admined[0].ID != publicCandidate.Channel.ID {
		t.Fatalf("admined public = %+v, want public candidate", admined)
	}
	if _, err := channels.UpdateUsername(ctx, domain.UpdateChannelUsernameRequest{
		UserID:    owner.ID,
		ChannelID: publicCandidate.Channel.ID,
		Username:  "",
	}); err != nil {
		t.Fatalf("clear public username: %v", err)
	}
	admined, err = channels.ListAdminedPublicChannels(ctx, owner.ID)
	if err != nil {
		t.Fatalf("list admined public channels after clear: %v", err)
	}
	if len(admined) != 0 {
		t.Fatalf("admined public after clear = %+v, want empty", admined)
	}

	discussionCandidate, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "index discussion " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{friend.ID},
		Date:          1700000841,
	})
	if err != nil {
		t.Fatalf("create discussion candidate: %v", err)
	}
	channelIDs = append(channelIDs, discussionCandidate.Channel.ID)
	friendGroups, err := channels.ListDiscussionGroups(ctx, friend.ID, 10)
	if err != nil {
		t.Fatalf("list friend discussion groups before admin: %v", err)
	}
	if len(friendGroups) != 0 {
		t.Fatalf("friend discussion groups before admin = %+v, want empty", friendGroups)
	}
	if _, err := channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
		UserID:    owner.ID,
		ChannelID: discussionCandidate.Channel.ID,
		MemberID:  friend.ID,
		AdminRights: domain.ChannelAdminRights{
			PinMessages: true,
		},
		Date: 1700000842,
	}); err != nil {
		t.Fatalf("grant pin admin: %v", err)
	}
	friendGroups, err = channels.ListDiscussionGroups(ctx, friend.ID, 10)
	if err != nil {
		t.Fatalf("list friend discussion groups after admin: %v", err)
	}
	if len(friendGroups) != 1 || friendGroups[0].ID != discussionCandidate.Channel.ID {
		t.Fatalf("friend discussion groups after admin = %+v, want candidate", friendGroups)
	}
	if _, err := channels.SetForum(ctx, owner.ID, discussionCandidate.Channel.ID, true, false); err != nil {
		t.Fatalf("enable forum: %v", err)
	}
	friendGroups, err = channels.ListDiscussionGroups(ctx, friend.ID, 10)
	if err != nil {
		t.Fatalf("list friend discussion groups after forum: %v", err)
	}
	if len(friendGroups) != 0 {
		t.Fatalf("friend discussion groups after forum = %+v, want empty", friendGroups)
	}
}

func TestChannelStoreListStoryPostableChannelsPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 9901, Phone: "+1888" + suffix + "41", FirstName: "StoryPostOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	admin, err := users.Create(ctx, domain.User{AccessHash: 9902, Phone: "+1888" + suffix + "42", FirstName: "StoryPostAdmin"})
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	var channelIDs []int64
	t.Cleanup(func() {
		if len(channelIDs) > 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = ANY($1::bigint[])", channelIDs)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, admin.ID})
	})

	channels := NewChannelStore(pool)
	owned, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: admin.ID,
		Title:         "pg owned story " + suffix,
		Broadcast:     true,
		Date:          1700003400,
	})
	if err != nil {
		t.Fatalf("create owned channel: %v", err)
	}
	channelIDs = append(channelIDs, owned.Channel.ID)
	postable, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "pg postable story " + suffix,
		Broadcast:     true,
		MemberUserIDs: []int64{admin.ID},
		Date:          1700003401,
	})
	if err != nil {
		t.Fatalf("create postable channel: %v", err)
	}
	channelIDs = append(channelIDs, postable.Channel.ID)
	if _, err := channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
		ChannelID: postable.Channel.ID,
		UserID:    owner.ID,
		MemberID:  admin.ID,
		AdminRights: domain.ChannelAdminRights{
			PostStories: true,
		},
		Date: 1700003402,
	}); err != nil {
		t.Fatalf("grant post stories: %v", err)
	}
	editOnly, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "pg edit-only story " + suffix,
		Broadcast:     true,
		MemberUserIDs: []int64{admin.ID},
		Date:          1700003403,
	})
	if err != nil {
		t.Fatalf("create edit-only channel: %v", err)
	}
	channelIDs = append(channelIDs, editOnly.Channel.ID)
	if _, err := channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
		ChannelID: editOnly.Channel.ID,
		UserID:    owner.ID,
		MemberID:  admin.ID,
		AdminRights: domain.ChannelAdminRights{
			EditStories: true,
		},
		Date: 1700003404,
	}); err != nil {
		t.Fatalf("grant edit stories only: %v", err)
	}

	list, err := channels.ListStoryPostableChannels(ctx, admin.ID)
	if err != nil {
		t.Fatalf("ListStoryPostableChannels: %v", err)
	}
	got := make([]int64, 0, len(list))
	for _, channel := range list {
		got = append(got, channel.ID)
	}
	want := []int64{postable.Channel.ID, owned.Channel.ID}
	if len(got) != len(want) {
		t.Fatalf("story postable channel ids = %v, want %v; excluded edit-only=%d", got, want, editOnly.Channel.ID)
	}
	if got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("story postable channel ids = %v, want %v; excluded edit-only=%d", got, want, editOnly.Channel.ID)
	}
}

func TestChannelStoreLeftChannelsReturnsPagedLeftMemberships(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 61, Phone: "+1889" + suffix + "01", FirstName: "LeftOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := users.Create(ctx, domain.User{AccessHash: 62, Phone: "+1889" + suffix + "02", FirstName: "LeftFriend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	var channelIDs []int64
	t.Cleanup(func() {
		if len(channelIDs) != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = ANY($1::bigint[])", channelIDs)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, friend.ID})
	})

	channels := NewChannelStore(pool)
	create := func(title string, broadcast bool, date int) domain.CreateChannelResult {
		t.Helper()
		created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
			CreatorUserID: owner.ID,
			Title:         title,
			Broadcast:     broadcast,
			Megagroup:     !broadcast,
			MemberUserIDs: []int64{friend.ID},
			Date:          date,
		})
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		channelIDs = append(channelIDs, created.Channel.ID)
		return created
	}
	older := create("older left "+suffix, false, 1700000810)
	newer := create("newer left "+suffix, true, 1700000811)
	create("active excluded "+suffix, false, 1700000812)
	if _, err := channels.LeaveChannel(ctx, older.Channel.ID, friend.ID, 1700000813); err != nil {
		t.Fatalf("leave older channel: %v", err)
	}
	if _, err := channels.LeaveChannel(ctx, newer.Channel.ID, friend.ID, 1700000814); err != nil {
		t.Fatalf("leave newer channel: %v", err)
	}

	page, err := channels.ListLeftChannels(ctx, friend.ID, 0, 1)
	if err != nil {
		t.Fatalf("list left channels: %v", err)
	}
	if page.Count != 2 || len(page.Channels) != 1 || page.Channels[0].Channel.ID != newer.Channel.ID || page.Channels[0].Self.Status != domain.ChannelMemberLeft {
		t.Fatalf("first left page = %+v, want newest left channel and full count", page)
	}
	next, err := channels.ListLeftChannels(ctx, friend.ID, 1, 1)
	if err != nil {
		t.Fatalf("list next left channels: %v", err)
	}
	if next.Count != 2 || len(next.Channels) != 1 || next.Channels[0].Channel.ID != older.Channel.ID {
		t.Fatalf("second left page = %+v, want older left channel", next)
	}
	empty, err := channels.ListLeftChannels(ctx, friend.ID, 2, 1)
	if err != nil {
		t.Fatalf("list empty left page: %v", err)
	}
	if empty.Count != 2 || len(empty.Channels) != 0 {
		t.Fatalf("empty left page = %+v, want full count and no chats", empty)
	}
	if _, err := channels.ListLeftChannels(ctx, friend.ID, domain.MaxLeftChannelsOffset+1, 1); !errors.Is(err, domain.ErrChannelInvalid) {
		t.Fatalf("huge offset err = %v, want ErrChannelInvalid", err)
	}
}

func TestChannelStoreDiscussionGroupLinksAreBidirectional(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 71, Phone: "+1890" + suffix + "01", FirstName: "DiscussionOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	var channelIDs []int64
	t.Cleanup(func() {
		if len(channelIDs) != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = ANY($1::bigint[])", channelIDs)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", owner.ID)
	})

	channels := NewChannelStore(pool)
	create := func(title string, broadcast bool, date int) domain.CreateChannelResult {
		t.Helper()
		created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
			CreatorUserID: owner.ID,
			Title:         title,
			Broadcast:     broadcast,
			Megagroup:     !broadcast,
			Date:          date,
		})
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		channelIDs = append(channelIDs, created.Channel.ID)
		return created
	}
	broadcast := create("discussion broadcast "+suffix, true, 1700000820)
	firstGroup := create("discussion first "+suffix, false, 1700000821)
	secondGroup := create("discussion second "+suffix, false, 1700000822)

	candidates, err := channels.ListDiscussionGroups(ctx, owner.ID, 10)
	if err != nil {
		t.Fatalf("list discussion groups: %v", err)
	}
	if len(candidates) < 2 || candidates[0].ID != secondGroup.Channel.ID || candidates[1].ID != firstGroup.Channel.ID {
		t.Fatalf("discussion groups = %+v, want newest creator megagroups", candidates)
	}

	linked, err := channels.SetDiscussionGroup(ctx, owner.ID, broadcast.Channel.ID, firstGroup.Channel.ID)
	if err != nil {
		t.Fatalf("link first discussion group: %v", err)
	}
	if len(linked.Channels) != 2 {
		t.Fatalf("linked changed channels = %+v, want broadcast and group", linked.Channels)
	}
	gotBroadcast, err := channels.GetChannelByID(ctx, broadcast.Channel.ID)
	if err != nil {
		t.Fatalf("get linked broadcast: %v", err)
	}
	gotFirst, err := channels.GetChannelByID(ctx, firstGroup.Channel.ID)
	if err != nil {
		t.Fatalf("get linked first group: %v", err)
	}
	if gotBroadcast.LinkedChatID != firstGroup.Channel.ID || gotFirst.LinkedChatID != broadcast.Channel.ID {
		t.Fatalf("first link = broadcast %d group %d, want bidirectional", gotBroadcast.LinkedChatID, gotFirst.LinkedChatID)
	}

	replaced, err := channels.SetDiscussionGroup(ctx, owner.ID, broadcast.Channel.ID, secondGroup.Channel.ID)
	if err != nil {
		t.Fatalf("replace discussion group: %v", err)
	}
	if len(replaced.Channels) != 3 {
		t.Fatalf("replace changed channels = %+v, want broadcast old group new group", replaced.Channels)
	}
	gotBroadcast, _ = channels.GetChannelByID(ctx, broadcast.Channel.ID)
	gotFirst, _ = channels.GetChannelByID(ctx, firstGroup.Channel.ID)
	gotSecond, err := channels.GetChannelByID(ctx, secondGroup.Channel.ID)
	if err != nil {
		t.Fatalf("get linked second group: %v", err)
	}
	if gotBroadcast.LinkedChatID != secondGroup.Channel.ID || gotSecond.LinkedChatID != broadcast.Channel.ID || gotFirst.LinkedChatID != 0 {
		t.Fatalf("replace link = broadcast %d first %d second %d, want old cleared and new bidirectional",
			gotBroadcast.LinkedChatID, gotFirst.LinkedChatID, gotSecond.LinkedChatID)
	}

	if _, err := channels.SetDiscussionGroup(ctx, owner.ID, 0, secondGroup.Channel.ID); err != nil {
		t.Fatalf("unlink from group side: %v", err)
	}
	gotBroadcast, _ = channels.GetChannelByID(ctx, broadcast.Channel.ID)
	gotSecond, _ = channels.GetChannelByID(ctx, secondGroup.Channel.ID)
	if gotBroadcast.LinkedChatID != 0 || gotSecond.LinkedChatID != 0 {
		t.Fatalf("unlink = broadcast %d group %d, want both cleared", gotBroadcast.LinkedChatID, gotSecond.LinkedChatID)
	}
	if _, err := channels.SetDiscussionGroup(ctx, owner.ID, 0, secondGroup.Channel.ID); !errors.Is(err, domain.ErrLinkNotModified) {
		t.Fatalf("repeat unlink err = %v, want ErrLinkNotModified", err)
	}
	if _, err := channels.SetPreHistoryHidden(ctx, owner.ID, firstGroup.Channel.ID, true); err != nil {
		t.Fatalf("hide first group prehistory: %v", err)
	}
	if _, err := channels.SetDiscussionGroup(ctx, owner.ID, broadcast.Channel.ID, firstGroup.Channel.ID); !errors.Is(err, domain.ErrMegagroupPrehistoryHidden) {
		t.Fatalf("hidden prehistory err = %v, want ErrMegagroupPrehistoryHidden", err)
	}
}
