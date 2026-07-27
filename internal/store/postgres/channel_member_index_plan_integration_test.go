package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

func TestChannelMemberIndexListPathPlansUseUserIndex(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner := createTestUser(t, ctx, users, "+1887"+suffix+"61", "PlanIndexOwner", "")
	member := createTestUser(t, ctx, users, "+1887"+suffix+"62", "PlanIndexMember", "")
	var channelIDs []int64
	t.Cleanup(func() {
		if len(channelIDs) != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = ANY($1::bigint[])", channelIDs)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, member.ID})
	})

	channels := NewChannelStore(pool)
	publicAdmined, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "plan admined " + suffix,
		Megagroup:     true,
		Date:          1700000850,
	})
	if err != nil {
		t.Fatalf("create public admined: %v", err)
	}
	channelIDs = append(channelIDs, publicAdmined.Channel.ID)
	publicUsername := "planadmined" + suffix
	if _, err := channels.UpdateUsername(ctx, domain.UpdateChannelUsernameRequest{
		UserID:    owner.ID,
		ChannelID: publicAdmined.Channel.ID,
		Username:  publicUsername,
	}); err != nil {
		t.Fatalf("set public username: %v", err)
	}
	left, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "plan left " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700000851,
	})
	if err != nil {
		t.Fatalf("create left: %v", err)
	}
	channelIDs = append(channelIDs, left.Channel.ID)
	if _, err := channels.LeaveChannel(ctx, left.Channel.ID, member.ID, 1700000852); err != nil {
		t.Fatalf("leave channel: %v", err)
	}
	discussion, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "plan discussion " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700000853,
	})
	if err != nil {
		t.Fatalf("create discussion: %v", err)
	}
	channelIDs = append(channelIDs, discussion.Channel.ID)
	if _, err := channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
		UserID:    owner.ID,
		ChannelID: discussion.Channel.ID,
		MemberID:  member.ID,
		AdminRights: domain.ChannelAdminRights{
			PinMessages: true,
		},
		Date: 1700000854,
	}); err != nil {
		t.Fatalf("grant discussion admin: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin explain tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatalf("disable seqscan: %v", err)
	}

	leftPlan := explainText(t, ctx, tx, `
SELECT channel_id
FROM user_channel_member_index
WHERE user_id = $1
  AND status = 'left'
  AND (broadcast OR megagroup)
  AND NOT deleted
ORDER BY left_at DESC, channel_id DESC
OFFSET $2
LIMIT $3
`, member.ID, 0, domain.MaxLeftChannelsLimit)
	requirePlanContains(t, leftPlan, "user_channel_member_index_left_idx")
	requirePlanNotContains(t, leftPlan, "channel_members_p")

	leftDetailPlan := explainText(t, ctx, tx, `
SELECT channel_id
FROM channel_members
WHERE user_id = $1
  AND channel_id = ANY($2::bigint[])
  AND status = 'left'
`, member.ID, []int64{left.Channel.ID})
	requireUniquePartitionCountAtMost(t, leftDetailPlan, `channel_members_p\d+`, 1)

	channelDetailPlan := explainText(t, ctx, tx, `
SELECT id
FROM channels
WHERE id = ANY($1::bigint[])
  AND NOT deleted
`, []int64{left.Channel.ID})
	requireUniquePartitionCountAtMost(t, channelDetailPlan, `channels_p\d+`, 1)

	usernameLookupPlan := explainText(t, ctx, tx, `
SELECT peer_id
FROM peer_usernames
WHERE username_lower = $1 AND peer_type = 'channel'
`, publicUsername)
	requirePlanContains(t, usernameLookupPlan, "peer_usernames_pkey")
	requirePlanNotMatches(t, usernameLookupPlan, `channels_p\d+`)

	usernameChannelDetailPlan := explainText(t, ctx, tx, `
SELECT id
FROM channels c
WHERE c.id = $1
  AND NOT c.deleted
`, publicAdmined.Channel.ID)
	requireUniquePartitionCountAtMost(t, usernameChannelDetailPlan, `channels_p\d+`, 1)
	requirePlanNotContains(t, usernameChannelDetailPlan, "Append")

	adminedPlan := explainText(t, ctx, tx, `
SELECT channel_id
FROM user_channel_member_index
WHERE user_id = $1
  AND status = 'active'
  AND role IN ('creator','admin')
  AND public_username
  AND NOT deleted
ORDER BY channel_id DESC
LIMIT $2
`, owner.ID, domain.MaxAdminedPublicChannels)
	requirePlanContains(t, adminedPlan, "user_channel_member_index_admined_public_idx")
	requirePlanNotContains(t, adminedPlan, "channel_members_p")

	discussionPlan := explainText(t, ctx, tx, `
SELECT channel_id
FROM user_channel_member_index
WHERE user_id = $1
  AND status = 'active'
  AND megagroup
  AND NOT broadcast
  AND NOT forum
  AND NOT deleted
  AND (role = 'creator' OR can_pin_messages)
ORDER BY channel_id DESC
LIMIT $2
`, member.ID, domain.MaxDiscussionGroupsLimit)
	requirePlanContains(t, discussionPlan, "user_channel_member_index_discussion_idx")
	requirePlanNotContains(t, discussionPlan, "channel_members_p")

	dialogCandidatePlan := explainText(t, ctx, tx, `
SELECT channel_id
FROM user_channel_member_index
WHERE user_id = $1
  AND status = 'active'
  AND NOT deleted
ORDER BY channel_id ASC
LIMIT $2
`, owner.ID, channelDialogCandidateLimit)
	requirePlanContains(t, dialogCandidatePlan, "user_channel_member_index")
	requirePlanNotContains(t, dialogCandidatePlan, "channel_members_p")

	dialogDetailPlan := explainText(t, ctx, tx, `
SELECT c.id,
       CASE WHEN c.top_message_id > m.available_min_id THEN c.top_message_id ELSE 0 END AS visible_top_id,
       CASE WHEN c.top_message_id > m.available_min_id THEN COALESCE(top_msg.message_date, d.top_message_date, c.date) ELSE 0 END AS visible_top_date
FROM channel_members m
JOIN channels c ON c.id = m.channel_id AND c.id = ANY($2::bigint[]) AND NOT c.deleted
LEFT JOIN channel_messages top_msg ON top_msg.channel_id = m.channel_id AND top_msg.channel_id = ANY($2::bigint[]) AND top_msg.id = c.top_message_id AND NOT top_msg.deleted
LEFT JOIN channel_dialogs d ON d.user_id = m.user_id AND d.channel_id = m.channel_id
WHERE m.user_id = $1
  AND m.channel_id = ANY($2::bigint[])
  AND m.status = 'active'
ORDER BY visible_top_date DESC,
         visible_top_id DESC,
         c.id DESC
LIMIT $3
`, owner.ID, []int64{publicAdmined.Channel.ID}, channelDialogQueryLimit)
	requireUniquePartitionCountAtMost(t, dialogDetailPlan, `channel_members_p\d+`, 1)
	requireUniquePartitionCountAtMost(t, dialogDetailPlan, `channels_p\d+`, 1)
	requireUniquePartitionCountAtMost(t, dialogDetailPlan, `channel_messages_p\d+`, 1)

	inactiveCandidatePlan := explainText(t, ctx, tx, `
SELECT channel_id
FROM user_channel_member_index
WHERE user_id = $1
  AND status = 'active'
  AND (broadcast OR megagroup)
  AND NOT deleted
ORDER BY channel_id ASC
LIMIT $2
`, owner.ID, channelDialogCandidateLimit)
	requirePlanContains(t, inactiveCandidatePlan, "user_channel_member_index")
	requirePlanNotContains(t, inactiveCandidatePlan, "channel_members_p")

	inactiveDetailPlan := explainText(t, ctx, tx, `
SELECT c.id,
       CASE WHEN c.top_message_id > m.available_min_id THEN c.top_message_id ELSE 0 END AS visible_top_id,
       CASE WHEN c.top_message_id > m.available_min_id THEN COALESCE(top_msg.message_date, d.top_message_date, c.date) ELSE GREATEST(c.date, m.joined_at) END AS visible_top_date
FROM channel_members m
JOIN channels c ON c.id = m.channel_id AND c.id = ANY($2::bigint[]) AND NOT c.deleted
LEFT JOIN channel_messages top_msg ON top_msg.channel_id = m.channel_id AND top_msg.channel_id = ANY($2::bigint[]) AND top_msg.id = c.top_message_id AND NOT top_msg.deleted
LEFT JOIN channel_dialogs d ON d.user_id = m.user_id AND d.channel_id = m.channel_id
WHERE m.user_id = $1
  AND m.channel_id = ANY($2::bigint[])
  AND m.status = 'active'
  AND (c.broadcast OR c.megagroup)
ORDER BY visible_top_date ASC,
         visible_top_id ASC,
         c.id ASC
LIMIT $3
`, owner.ID, []int64{publicAdmined.Channel.ID}, domain.MaxInactiveChannelsLimit)
	requireUniquePartitionCountAtMost(t, inactiveDetailPlan, `channel_members_p\d+`, 1)
	requireUniquePartitionCountAtMost(t, inactiveDetailPlan, `channels_p\d+`, 1)
	requireUniquePartitionCountAtMost(t, inactiveDetailPlan, `channel_messages_p\d+`, 1)

	mentionAffectedPlan := explainText(t, ctx, tx, `
SELECT DISTINCT user_id
FROM channel_unread_mention_index
WHERE channel_id = $1
  AND message_id = ANY($2::int[])
ORDER BY user_id ASC
`, publicAdmined.Channel.ID, []int32{1, 2})
	requirePlanContains(t, mentionAffectedPlan, "Index")
	requireUniquePartitionCountAtMost(t, mentionAffectedPlan, `channel_unread_mention_index_p\d+`, 1)
	requirePlanNotMatches(t, mentionAffectedPlan, `channel_unread_mentions_p\d+`)

	mentionDeleteBatchPlan := explainText(t, ctx, tx, `
WITH affected AS (
    SELECT DISTINCT unnest($3::bigint[]) AS user_id
),
deleted_mentions AS (
    DELETE FROM channel_unread_mentions um
    USING affected a
    WHERE um.user_id = ANY($3::bigint[])
      AND um.user_id = a.user_id
      AND um.channel_id = $1
      AND um.message_id = ANY($2::int[])
    RETURNING um.user_id, um.channel_id, um.message_id
),
deleted_index AS (
    DELETE FROM channel_unread_mention_index i
    USING affected a
    WHERE i.channel_id = $1
      AND i.user_id = a.user_id
      AND i.message_id = ANY($2::int[])
),
counts_before AS (
    SELECT user_id, COUNT(*)::int AS count
    FROM channel_unread_mentions
    WHERE channel_id = $1
      AND user_id = ANY($3::bigint[])
    GROUP BY user_id
),
deleted_counts AS (
    SELECT user_id, COUNT(*)::int AS count
    FROM deleted_mentions
    GROUP BY user_id
)
UPDATE channel_dialogs d
SET unread_mentions_count = GREATEST(COALESCE(c.count, 0) - COALESCE(dc.count, 0), 0),
    updated_at = now()
FROM affected a
LEFT JOIN counts_before c ON c.user_id = a.user_id
LEFT JOIN deleted_counts dc ON dc.user_id = a.user_id
WHERE d.user_id = ANY($3::bigint[])
  AND d.channel_id = $1
  AND d.user_id = a.user_id
`, publicAdmined.Channel.ID, []int32{1, 2}, []int64{member.ID})
	requireUniquePartitionCountAtMost(t, mentionDeleteBatchPlan, `channel_unread_mentions_p\d+`, 1)
	requireUniquePartitionCountAtMost(t, mentionDeleteBatchPlan, `channel_dialogs_p\d+`, 1)
	requireUniquePartitionCountAtMost(t, mentionDeleteBatchPlan, `channel_unread_mention_index_p\d+`, 1)
}
