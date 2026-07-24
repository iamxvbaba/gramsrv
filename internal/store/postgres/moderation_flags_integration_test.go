package postgres

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

func TestModerationFlagsRejectImpossibleStateAtPostgresBoundary(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	user := createTestUser(t, ctx, users, "+1781"+suffix+"71", "ModerationFlags", "")

	if _, err := users.SetScamFake(ctx, user.ID, true, true); !errors.Is(err, domain.ErrPeerModerationFlagsInvalid) {
		t.Fatalf("user store error=%v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET scam=true,fake=true WHERE id=$1`, user.ID); err == nil {
		t.Fatal("users CHECK constraint accepted scam=true,fake=true")
	}

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: user.ID,
		Title:         "Moderation " + suffix,
		Megagroup:     true,
		Date:          1700002000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if _, err := channels.SetChannelScamFake(ctx, created.Channel.ID, true, true); !errors.Is(err, domain.ErrPeerModerationFlagsInvalid) {
		t.Fatalf("channel store error=%v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE channels SET scam=true,fake=true WHERE id=$1`, created.Channel.ID); err == nil {
		t.Fatal("channels CHECK constraint accepted scam=true,fake=true")
	}

	gotUser, found, err := users.ByID(ctx, user.ID)
	if err != nil || !found || gotUser.Scam || gotUser.Fake {
		t.Fatalf("user after rejected writes=%+v found=%v err=%v", gotUser, found, err)
	}
	gotChannel, err := channels.GetChannelByID(ctx, created.Channel.ID)
	if err != nil || gotChannel.Scam || gotChannel.Fake {
		t.Fatalf("channel after rejected writes=%+v err=%v", gotChannel, err)
	}
}

func TestUserModerationFlagsDoNotAdvanceAccountPts(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	contacts := NewContactStore(pool)
	events := NewUpdateEventStore(pool)

	target := createTestUser(t, ctx, users, "+1782"+suffix+"71", "FlagTarget", "")
	savedTargetViewer := createTestUser(t, ctx, users, "+1782"+suffix+"72", "SavedTarget", "")
	savedByTargetViewer := createTestUser(t, ctx, users, "+1782"+suffix+"73", "SavedByTarget", "")
	unrelated := createTestUser(t, ctx, users, "+1782"+suffix+"74", "Unrelated", "")

	if _, err := contacts.Upsert(ctx, savedTargetViewer.ID, domain.ContactInput{
		ContactUserID: target.ID, FirstName: target.FirstName,
	}); err != nil {
		t.Fatalf("save target contact: %v", err)
	}
	if _, err := contacts.Upsert(ctx, target.ID, domain.ContactInput{
		ContactUserID: savedByTargetViewer.ID, FirstName: savedByTargetViewer.FirstName,
	}); err != nil {
		t.Fatalf("save reverse contact: %v", err)
	}
	viewers := []domain.User{target, savedTargetViewer, savedByTargetViewer, unrelated}
	baseline := make(map[int64]int, len(viewers))
	for _, viewer := range viewers {
		pts, err := events.MaxContiguousPts(ctx, viewer.ID)
		if err != nil {
			t.Fatalf("viewer %d baseline pts: %v", viewer.ID, err)
		}
		baseline[viewer.ID] = pts
	}

	updated, err := users.SetScamFake(ctx, target.ID, true, false)
	if err != nil {
		t.Fatalf("set scam: %v", err)
	}
	if !updated.Scam || updated.Fake {
		t.Fatalf("updated flags = scam:%v fake:%v", updated.Scam, updated.Fake)
	}

	for _, viewer := range viewers {
		pts, err := events.MaxContiguousPts(ctx, viewer.ID)
		if err != nil || pts != baseline[viewer.ID] {
			t.Fatalf("viewer %d pts=%d want=%d err=%v", viewer.ID, pts, baseline[viewer.ID], err)
		}
		got, err := events.ListAfter(ctx, viewer.ID, baseline[viewer.ID], 10)
		if err != nil || len(got) != 0 {
			t.Fatalf("viewer %d moderation events=%+v err=%v", viewer.ID, got, err)
		}
	}
	var outboxCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM dispatch_outbox
WHERE target_user_id = ANY($1::bigint[])
  AND event_type = 'user_profile'`,
		[]int64{target.ID, savedTargetViewer.ID, savedByTargetViewer.ID, unrelated.ID},
	).Scan(&outboxCount); err != nil || outboxCount != 0 {
		t.Fatalf("profile outbox count=%d err=%v", outboxCount, err)
	}

	audience, err := users.ModerationFlagAudience(ctx, target.ID, 4096)
	if err != nil {
		t.Fatalf("moderation audience: %v", err)
	}
	audienceSet := make(map[int64]struct{}, len(audience))
	for _, userID := range audience {
		audienceSet[userID] = struct{}{}
	}
	for _, viewer := range []domain.User{target, savedTargetViewer, savedByTargetViewer} {
		if _, ok := audienceSet[viewer.ID]; !ok {
			t.Fatalf("viewer %d missing from audience %v", viewer.ID, audience)
		}
	}
	if _, ok := audienceSet[unrelated.ID]; ok {
		t.Fatalf("unrelated viewer included in audience %v", audience)
	}

	if _, err := users.SetScamFake(ctx, target.ID, true, false); err != nil {
		t.Fatalf("repeat same flags: %v", err)
	}
	if _, err := users.SetScamFake(ctx, target.ID, false, true); err != nil {
		t.Fatalf("switch to fake: %v", err)
	}
	for _, viewer := range viewers {
		pts, err := events.MaxContiguousPts(ctx, viewer.ID)
		if err != nil || pts != baseline[viewer.ID] {
			t.Fatalf("viewer %d final pts=%d want=%d err=%v", viewer.ID, pts, baseline[viewer.ID], err)
		}
	}
}

func TestChannelModerationFlagsDoNotAdvanceMemberAccountPts(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	channels := NewChannelStore(pool)
	events := NewUpdateEventStore(pool)

	owner := createTestUser(t, ctx, users, "+1783"+suffix+"71", "FlagOwner", "")
	member := createTestUser(t, ctx, users, "+1783"+suffix+"72", "FlagMember", "")
	unrelated := createTestUser(t, ctx, users, "+1783"+suffix+"73", "FlagUnrelated", "")
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		MemberUserIDs: []int64{member.ID},
		Title:         "Flagged Channel " + suffix,
		Megagroup:     true,
		Date:          1700003000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	baseline := make(map[int64]int)
	for _, viewer := range []domain.User{owner, member, unrelated} {
		pts, err := events.MaxContiguousPts(ctx, viewer.ID)
		if err != nil {
			t.Fatalf("viewer %d baseline pts: %v", viewer.ID, err)
		}
		baseline[viewer.ID] = pts
	}

	updated, err := channels.SetChannelScamFake(ctx, created.Channel.ID, true, false)
	if err != nil {
		t.Fatalf("set channel scam: %v", err)
	}
	if !updated.Scam || updated.Fake {
		t.Fatalf("updated flags = scam:%v fake:%v", updated.Scam, updated.Fake)
	}
	for _, viewer := range []domain.User{owner, member, unrelated} {
		pts, err := events.MaxContiguousPts(ctx, viewer.ID)
		if err != nil || pts != baseline[viewer.ID] {
			t.Fatalf("viewer %d pts=%d want=%d err=%v", viewer.ID, pts, baseline[viewer.ID], err)
		}
		got, err := events.ListAfter(ctx, viewer.ID, baseline[viewer.ID], 10)
		if err != nil || len(got) != 0 {
			t.Fatalf("viewer %d moderation events=%+v err=%v", viewer.ID, got, err)
		}
	}

	var outboxCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM dispatch_outbox
WHERE target_user_id = ANY($1::bigint[])
  AND event_type = 'channel_state'
  AND pts > 0`,
		[]int64{owner.ID, member.ID, unrelated.ID},
	).Scan(&outboxCount); err != nil || outboxCount != 0 {
		t.Fatalf("channel state outbox count=%d err=%v", outboxCount, err)
	}

	if _, err := channels.SetChannelScamFake(ctx, created.Channel.ID, true, false); err != nil {
		t.Fatalf("repeat same channel flags: %v", err)
	}
	if _, err := channels.SetChannelScamFake(ctx, created.Channel.ID, false, true); err != nil {
		t.Fatalf("switch channel to fake: %v", err)
	}
	for _, viewer := range []domain.User{owner, member, unrelated} {
		pts, err := events.MaxContiguousPts(ctx, viewer.ID)
		if err != nil || pts != baseline[viewer.ID] {
			t.Fatalf("viewer %d final pts=%d want=%d err=%v", viewer.ID, pts, baseline[viewer.ID], err)
		}
	}
}
