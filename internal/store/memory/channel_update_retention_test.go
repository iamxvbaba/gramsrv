package memory

import (
	"context"
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestChannelUpdateRetentionFloorDifferenceAndDirtyCheckpointMemory(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	const ownerID int64 = 701
	created, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: ownerID,
		Title:         "retention memory",
		Megagroup:     true,
		Date:          1_700_010_000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID := created.Channel.ID
	sent := make([]domain.SendChannelMessageResult, 0, 3)
	for i := 1; i <= 3; i++ {
		result, err := store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
			UserID:    ownerID,
			ChannelID: channelID,
			RandomID:  int64(9000 + i),
			Message:   "retention",
			Date:      1_700_010_000 + i,
		})
		if err != nil {
			t.Fatalf("send message %d: %v", i, err)
		}
		sent = append(sent, result)
	}

	// Delete create + first two messages. The third message remains as the normal incremental page.
	pruned, err := store.PruneChannelUpdateEvents(ctx, channelID, sent[1].Event.Pts, 100)
	if err != nil {
		t.Fatalf("prune channel updates: %v", err)
	}
	if pruned.Deleted != 3 || pruned.Checkpoint.RetainedThroughPts != sent[1].Event.Pts {
		t.Fatalf("prune result = %+v, want deleted=3 floor=%d", pruned, sent[1].Event.Pts)
	}
	if pruned.Checkpoint.LatestPts != sent[2].Event.Pts || pruned.Checkpoint.LatestEventDate != sent[2].Event.Date {
		t.Fatalf("checkpoint latest = %+v, want pts/date %d/%d", pruned.Checkpoint, sent[2].Event.Pts, sent[2].Event.Date)
	}

	below, err := store.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{
		UserID: ownerID, ChannelID: channelID, Pts: pruned.Checkpoint.RetainedThroughPts - 1, Limit: 100,
	})
	if err != nil {
		t.Fatalf("difference below retained floor: %v", err)
	}
	if !below.TooLong || below.Pts != sent[2].Event.Pts || below.Dialog.ChannelID != channelID {
		t.Fatalf("difference below floor = %+v, want complete too-long snapshot at pts %d", below, sent[2].Event.Pts)
	}
	atFloor, err := store.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{
		UserID: ownerID, ChannelID: channelID, Pts: pruned.Checkpoint.RetainedThroughPts, Limit: 100,
	})
	if err != nil {
		t.Fatalf("difference at retained floor: %v", err)
	}
	if atFloor.TooLong || len(atFloor.Events) != 1 || atFloor.Events[0].Pts != sent[2].Event.Pts {
		t.Fatalf("difference at floor = %+v, want one normal incremental event at pts %d", atFloor, sent[2].Event.Pts)
	}

	// Remove the remaining row. Dirty-channel recovery must still use checkpoint.latest_event_date.
	allPruned, err := store.PruneChannelUpdateEvents(ctx, channelID, sent[2].Event.Pts, 100)
	if err != nil {
		t.Fatalf("prune remaining channel updates: %v", err)
	}
	if allPruned.Deleted != 1 || len(store.events[channelID]) != 0 {
		t.Fatalf("remaining prune = %+v events=%v, want empty event log", allPruned, store.events[channelID])
	}
	dirty, err := store.ListDirtyActiveChannelsForUser(ctx, ownerID, sent[2].Event.Date-1, 0, 10)
	if err != nil {
		t.Fatalf("list dirty channels after prune: %v", err)
	}
	if len(dirty) != 1 || dirty[0].ChannelID != channelID || dirty[0].Pts != sent[2].Event.Pts {
		t.Fatalf("dirty channels after prune = %+v, want channel %d pts %d", dirty, channelID, sent[2].Event.Pts)
	}
}

func TestPruneChannelUpdateEventsRejectsInvalidPtsCountMemory(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	created, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 901,
		Title:         "invalid retention event",
		Megagroup:     true,
		Date:          1_700_020_000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID := created.Channel.ID
	channel := store.channels[channelID]
	channel.Pts++
	store.channels[channelID] = channel
	store.ptsSeq[channelID] = channel.Pts
	store.appendChannelEventLocked(domain.ChannelUpdateEvent{
		ChannelID: channelID,
		Type:      domain.ChannelUpdateNoop,
		Pts:       channel.Pts,
		PtsCount:  0,
		Date:      1_700_020_001,
	})

	_, err = store.PruneChannelUpdateEvents(ctx, channelID, channel.Pts, 100)
	if err == nil || !strings.Contains(err.Error(), "invalid pts_count=0") {
		t.Fatalf("prune invalid event err = %v, want fail-fast pts_count error", err)
	}
}

func TestDeleteExpiredChannelUpdateEventsIsBoundedMemory(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	created, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 801,
		Title:         "expired retention memory",
		Megagroup:     true,
		Date:          1_600_000_000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	for i := 1; i <= 3; i++ {
		if _, err := store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
			UserID: 801, ChannelID: created.Channel.ID, RandomID: int64(8000 + i), Message: "old", Date: 1_600_000_000 + i,
		}); err != nil {
			t.Fatalf("send old message %d: %v", i, err)
		}
	}
	deleted, err := store.DeleteExpiredChannelUpdateEvents(ctx, time.Hour, 2)
	if err != nil {
		t.Fatalf("delete expired channel updates: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d, want bounded batch 2", deleted)
	}
	checkpoint := store.retention[created.Channel.ID]
	if checkpoint.RetainedThroughPts != 2 || len(store.events[created.Channel.ID]) != 2 {
		t.Fatalf("after bounded prune checkpoint=%+v events=%d, want floor=2 and 2 rows", checkpoint, len(store.events[created.Channel.ID]))
	}
}
