package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestChannelUpdateRetentionFloorDifferenceAndDirtyCheckpointPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 71,
		Phone:      "+1766" + suffix + "01",
		FirstName:  "RetentionOwner",
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
		Title:         "Retention PG " + suffix,
		Megagroup:     true,
		Date:          1_700_020_000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID
	sent := make([]domain.SendChannelMessageResult, 0, 3)
	for i := 1; i <= 3; i++ {
		result, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
			UserID: owner.ID, ChannelID: channelID, RandomID: int64(7000 + i), Message: "retention", Date: 1_700_020_000 + i,
		})
		if err != nil {
			t.Fatalf("send message %d: %v", i, err)
		}
		sent = append(sent, result)
	}

	pruned, err := channels.PruneChannelUpdateEvents(ctx, channelID, sent[1].Event.Pts, 100)
	if err != nil {
		t.Fatalf("prune channel updates: %v", err)
	}
	if pruned.Deleted != 3 || pruned.Checkpoint.RetainedThroughPts != sent[1].Event.Pts {
		t.Fatalf("prune result = %+v, want deleted=3 floor=%d", pruned, sent[1].Event.Pts)
	}
	var floor, latestDate, latestPts, remaining int
	if err := pool.QueryRow(ctx, `
SELECT cp.retained_through_pts, cp.latest_event_date, cp.latest_pts,
       (SELECT COUNT(*) FROM channel_update_events e WHERE e.channel_id = cp.channel_id)::int
FROM channel_update_checkpoints cp
WHERE cp.channel_id = $1`, channelID).Scan(&floor, &latestDate, &latestPts, &remaining); err != nil {
		t.Fatalf("read retention checkpoint: %v", err)
	}
	if floor != sent[1].Event.Pts || latestDate != sent[2].Event.Date || latestPts != sent[2].Event.Pts || remaining != 1 {
		t.Fatalf("checkpoint/db = floor:%d latest:%d/%d remaining:%d", floor, latestDate, latestPts, remaining)
	}

	below, err := channels.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{
		UserID: owner.ID, ChannelID: channelID, Pts: floor - 1, Limit: 100,
	})
	if err != nil {
		t.Fatalf("difference below retained floor: %v", err)
	}
	if !below.TooLong || below.Pts != sent[2].Event.Pts {
		t.Fatalf("difference below floor = %+v, want too-long snapshot at pts %d", below, sent[2].Event.Pts)
	}
	atFloor, err := channels.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{
		UserID: owner.ID, ChannelID: channelID, Pts: floor, Limit: 100,
	})
	if err != nil {
		t.Fatalf("difference at retained floor: %v", err)
	}
	if atFloor.TooLong || len(atFloor.Events) != 1 || atFloor.Events[0].Pts != sent[2].Event.Pts {
		t.Fatalf("difference at floor = %+v, want normal incremental event pts %d", atFloor, sent[2].Event.Pts)
	}

	allPruned, err := channels.PruneChannelUpdateEvents(ctx, channelID, sent[2].Event.Pts, 100)
	if err != nil {
		t.Fatalf("prune remaining channel update: %v", err)
	}
	if allPruned.Deleted != 1 {
		t.Fatalf("remaining prune = %+v, want deleted=1", allPruned)
	}
	dirty, err := channels.ListDirtyActiveChannelsForUser(ctx, owner.ID, sent[2].Event.Date-1, 0, 10)
	if err != nil {
		t.Fatalf("list dirty channels after prune: %v", err)
	}
	if len(dirty) != 1 || dirty[0].ChannelID != channelID || dirty[0].Pts != sent[2].Event.Pts {
		t.Fatalf("dirty channels after prune = %+v, want channel %d pts %d", dirty, channelID, sent[2].Event.Pts)
	}
}

func TestDeleteExpiredChannelUpdateEventsContinuesPastCandidatePagePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: time.Now().UnixNano(),
		Phone:      "+1767" + suffix + "01",
		FirstName:  "RetentionPageOwner",
	})
	if err != nil {
		t.Fatalf("create retention page owner: %v", err)
	}
	channelIDs := make([]int64, 0, 320)
	t.Cleanup(func() {
		if len(channelIDs) > 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = ANY($1::bigint[])", channelIDs)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", owner.ID)
	})

	channels := NewChannelStore(pool)
	for i := 0; i < 320; i++ {
		created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
			CreatorUserID: owner.ID,
			Title:         fmt.Sprintf("Retention page %s/%03d", suffix, i),
			Megagroup:     true,
			// Keep these rows ahead of ordinary developer/test data in the global seek.
			Date: 1,
		})
		if err != nil {
			t.Fatalf("create retention candidate %d: %v", i, err)
		}
		channelIDs = append(channelIDs, created.Channel.ID)
	}

	deleted, err := channels.DeleteExpiredChannelUpdateEvents(ctx, time.Second, len(channelIDs))
	if err != nil {
		t.Fatalf("delete expired channel updates across pages: %v", err)
	}
	if deleted != len(channelIDs) {
		t.Fatalf("deleted expired channel updates = %d, want %d (must continue after page 256)", deleted, len(channelIDs))
	}
	var remaining, advanced int
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM channel_update_events WHERE channel_id = ANY($1::bigint[]))::int,
  (SELECT count(*) FROM channel_update_checkpoints
   WHERE channel_id = ANY($1::bigint[]) AND retained_through_pts = 1)::int`, channelIDs).Scan(&remaining, &advanced); err != nil {
		t.Fatalf("read paged channel retention result: %v", err)
	}
	if remaining != 0 || advanced != len(channelIDs) {
		t.Fatalf("paged retention remaining/advanced = %d/%d, want 0/%d", remaining, advanced, len(channelIDs))
	}
}

func TestDeleteExpiredChannelUpdateEventsIsolatesGapAndContinuesPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	owner, err := NewUserStore(pool).Create(ctx, domain.User{
		AccessHash: time.Now().UnixNano(),
		Phone:      "+1768" + suffix + "01",
		FirstName:  "RetentionGapOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	channels := NewChannelStore(pool)
	bad, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID, Title: "Retention bad " + suffix, Megagroup: true, Date: 1,
	})
	if err != nil {
		t.Fatalf("create bad channel: %v", err)
	}
	healthy, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID, Title: "Retention healthy " + suffix, Megagroup: true, Date: 2,
	})
	if err != nil {
		t.Fatalf("create healthy channel: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = ANY($1::bigint[])", []int64{bad.Channel.ID, healthy.Channel.ID})
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", owner.ID)
	})

	// Deliberately model a persisted invariant violation: floor=0 but the first event ends at pts=2
	// with pts_count=1. The bad channel is the oldest global candidate and must be reported without
	// preventing the healthy channel behind it from advancing.
	if _, err := pool.Exec(ctx, `
WITH moved_event AS (
  UPDATE channel_update_events SET pts = 2, date = 1 WHERE channel_id = $1 RETURNING channel_id
), moved_channel AS (
  UPDATE channels SET pts = 2 WHERE id = $1 RETURNING id
)
UPDATE channel_update_checkpoints
SET latest_pts = 2, latest_event_date = 1
WHERE channel_id = $1
`, bad.Channel.ID); err != nil {
		t.Fatalf("inject channel retention gap: %v", err)
	}

	deleted, err := channels.DeleteExpiredChannelUpdateEvents(ctx, time.Second, 10)
	if err == nil || !strings.Contains(err.Error(), "has gap") {
		t.Fatalf("gap retention err = %v, want reported invariant violation", err)
	}
	if deleted < 1 {
		t.Fatalf("deleted across bad+healthy channels = %d, want at least the healthy channel's event", deleted)
	}
	var badFloor, badRows, healthyFloor, healthyRows int
	if scanErr := pool.QueryRow(ctx, `
SELECT
  (SELECT retained_through_pts FROM channel_update_checkpoints WHERE channel_id = $1)::int,
  (SELECT count(*) FROM channel_update_events WHERE channel_id = $1)::int,
  (SELECT retained_through_pts FROM channel_update_checkpoints WHERE channel_id = $2)::int,
  (SELECT count(*) FROM channel_update_events WHERE channel_id = $2)::int
`, bad.Channel.ID, healthy.Channel.ID).Scan(&badFloor, &badRows, &healthyFloor, &healthyRows); scanErr != nil {
		t.Fatalf("read isolated retention state: %v", scanErr)
	}
	if badFloor != 0 || badRows != 1 || healthyFloor != 1 || healthyRows != 0 {
		t.Fatalf("isolated state bad=%d/%d healthy=%d/%d, want 0/1 and 1/0", badFloor, badRows, healthyFloor, healthyRows)
	}
}
