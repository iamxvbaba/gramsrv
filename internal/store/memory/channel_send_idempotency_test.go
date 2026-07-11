package memory

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

func TestChannelRandomIDReplayUsesCurrentSnapshotAndDurableDeleteMemory(t *testing.T) {
	ctx := context.Background()
	channels := NewChannelStore()
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "replay convergence",
		Megagroup:     true,
		Date:          1_700_001_000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	req := domain.SendChannelMessageRequest{
		UserID: 1, ChannelID: created.Channel.ID, RandomID: 77001,
		Message: "original", Date: 1_700_001_001,
	}
	first, err := channels.SendChannelMessage(ctx, req)
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	conflict := req
	conflict.Message = "same random id, different intent"
	if _, err := channels.SendChannelMessage(ctx, conflict); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
		t.Fatalf("conflicting channel replay err=%v, want ErrMessageRandomIDDuplicate", err)
	}
	edited, err := channels.EditChannelMessage(ctx, domain.EditChannelMessageRequest{
		UserID: 1, ChannelID: created.Channel.ID, ID: first.Message.ID,
		Message: "edited", EditDate: 1_700_001_002,
	})
	if err != nil {
		t.Fatalf("edit channel message: %v", err)
	}
	ptsBeforeReplay := channels.ptsSeq[created.Channel.ID]
	eventsBeforeReplay := len(channels.events[created.Channel.ID])
	replay, err := channels.SendChannelMessage(ctx, req)
	if err != nil {
		t.Fatalf("replay after edit: %v", err)
	}
	if !replay.Duplicate || replay.Message.Body != "edited" || replay.Message.Pts != edited.Message.Pts || replay.Event.Pts != first.Event.Pts || replay.ReplayDeleteEvent != nil {
		t.Fatalf("replay after edit = %+v, want current snapshot with first-send pts", replay)
	}
	if channels.ptsSeq[created.Channel.ID] != ptsBeforeReplay || len(channels.events[created.Channel.ID]) != eventsBeforeReplay {
		t.Fatalf("edit replay mutated channel pts/events = %d/%d, want %d/%d", channels.ptsSeq[created.Channel.ID], len(channels.events[created.Channel.ID]), ptsBeforeReplay, eventsBeforeReplay)
	}
	deleted, err := channels.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{
		UserID: 1, ChannelID: created.Channel.ID, IDs: []int{first.Message.ID}, Date: 1_700_001_003,
	})
	if err != nil {
		t.Fatalf("delete channel message: %v", err)
	}
	ptsBeforeReplay = channels.ptsSeq[created.Channel.ID]
	eventsBeforeReplay = len(channels.events[created.Channel.ID])
	replay, err = channels.SendChannelMessage(ctx, req)
	if err != nil {
		t.Fatalf("replay after delete: %v", err)
	}
	if !replay.Duplicate || replay.Message.Body != "original" || replay.Message.Pts != first.Message.Pts || replay.Event.Pts != first.Event.Pts {
		t.Fatalf("replay after delete = %+v, want immutable first snapshot", replay)
	}
	if replay.ReplayDeleteEvent == nil || replay.ReplayDeleteEvent.Pts != deleted.Event.Pts || len(replay.ReplayDeleteEvent.MessageIDs) != 1 || replay.ReplayDeleteEvent.MessageIDs[0] != first.Message.ID {
		t.Fatalf("replay delete = %+v, want durable event %+v", replay.ReplayDeleteEvent, deleted.Event)
	}
	if channels.ptsSeq[created.Channel.ID] != ptsBeforeReplay || len(channels.events[created.Channel.ID]) != eventsBeforeReplay {
		t.Fatalf("delete replay mutated channel pts/events = %d/%d, want %d/%d", channels.ptsSeq[created.Channel.ID], len(channels.events[created.Channel.ID]), ptsBeforeReplay, eventsBeforeReplay)
	}
}
