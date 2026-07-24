package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func TestModerationProfileUpdateCarriesStandardFlagsAndPts(t *testing.T) {
	const (
		viewerID = int64(1001)
		targetID = int64(2002)
	)
	event := domain.UpdateEvent{
		UserID: viewerID,
		Type:   domain.UpdateEventUserProfile,
		Pts:    7, PtsCount: 1, Date: 1700000000,
		Peer: domain.Peer{Type: domain.PeerTypeUser, ID: targetID},
		Users: []domain.User{{
			ID: targetID, AccessHash: 22, FirstName: "Flagged", Scam: true,
		}},
	}

	updates := tgUpdateForOutboxEventForViewer(event, viewerID)
	if updates == nil || len(updates.Updates) != 2 {
		t.Fatalf("updates = %+v", updates)
	}
	refresh, ok := updates.Updates[0].(*tg.UpdateUser)
	if !ok || refresh.UserID != targetID {
		t.Fatalf("refresh = %T %+v", updates.Updates[0], updates.Updates[0])
	}
	bookkeeping, ok := updates.Updates[1].(*tg.UpdateDeleteMessages)
	if !ok || bookkeeping.Pts != 7 || bookkeeping.PtsCount != 1 || len(bookkeeping.Messages) != 0 {
		t.Fatalf("bookkeeping = %T %+v", updates.Updates[1], updates.Updates[1])
	}
	if len(updates.Users) != 1 {
		t.Fatalf("users = %+v", updates.Users)
	}
	user, ok := updates.Users[0].(*tg.User)
	if !ok || user.ID != targetID || !user.Scam || user.Fake {
		t.Fatalf("user = %T %+v", updates.Users[0], updates.Users[0])
	}

	difference := tgUpdatesDifference(viewerID, domain.UpdateDifference{
		State:  domain.UpdateState{Pts: 7, Date: event.Date},
		Events: []domain.UpdateEvent{event},
	})
	full, ok := difference.(*tg.UpdatesDifference)
	if !ok || len(full.OtherUpdates) != 1 || len(full.Users) != 1 {
		t.Fatalf("difference = %T %+v", difference, difference)
	}
	if refresh, ok := full.OtherUpdates[0].(*tg.UpdateUser); !ok || refresh.UserID != targetID {
		t.Fatalf("difference refresh = %T %+v", full.OtherUpdates[0], full.OtherUpdates[0])
	}
	if user, ok := full.Users[0].(*tg.User); !ok || !user.Scam || user.Fake {
		t.Fatalf("difference user = %T %+v", full.Users[0], full.Users[0])
	}
}

func TestChannelModerationUpdateCarriesStandardFlagsAndPts(t *testing.T) {
	const (
		viewerID  = int64(3003)
		channelID = int64(4004)
	)
	event := domain.UpdateEvent{
		UserID: viewerID,
		Type:   domain.UpdateEventChannelState,
		Pts:    9, PtsCount: 1, Date: 1700000001,
		Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: channelID},
		Channels: []domain.Channel{{
			ID: channelID, AccessHash: 44, CreatorUserID: viewerID,
			Title: "Flagged channel", Megagroup: true, Scam: true,
		}},
	}

	updates := tgUpdateForOutboxEventForViewer(event, viewerID)
	if updates == nil || len(updates.Updates) != 2 {
		t.Fatalf("updates = %+v", updates)
	}
	refresh, ok := updates.Updates[0].(*tg.UpdateChannel)
	if !ok || refresh.ChannelID != channelID {
		t.Fatalf("refresh = %T %+v", updates.Updates[0], updates.Updates[0])
	}
	bookkeeping, ok := updates.Updates[1].(*tg.UpdateDeleteMessages)
	if !ok || bookkeeping.Pts != 9 || bookkeeping.PtsCount != 1 || len(bookkeeping.Messages) != 0 {
		t.Fatalf("bookkeeping = %T %+v", updates.Updates[1], updates.Updates[1])
	}
	if len(updates.Chats) != 1 {
		t.Fatalf("chats = %+v", updates.Chats)
	}
	channel, ok := updates.Chats[0].(*tg.Channel)
	if !ok || channel.ID != channelID || !channel.Scam || channel.Fake {
		t.Fatalf("channel = %T %+v", updates.Chats[0], updates.Chats[0])
	}
}
