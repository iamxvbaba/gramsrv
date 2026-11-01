package rpc

import (
	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func tgUpdateForOutboxEvent(event domain.UpdateEvent) *tg.Updates {
	return tgUpdateForOutboxEventForViewer(event, event.UserID)
}

func tgUpdateForOutboxEventForViewer(event domain.UpdateEvent, viewerUserID int64) *tg.Updates {
	if viewerUserID == 0 {
		viewerUserID = event.UserID
	}
	switch event.Type {
	case domain.UpdateEventNewMessage:
		if event.Message.Deleted {
			return tgDeletedPrivateMessageOutboxUpdate(event)
		}
		return tgPrivateMessageUpdates(event, event.Message, 0, false, tgUsersForViewer(viewerUserID, event.Users), tgChannels(viewerUserID, event.Channels))
	case domain.UpdateEventReadHistoryInbox, domain.UpdateEventReadHistoryOutbox:
		var update tg.UpdateClass
		if event.Type == domain.UpdateEventReadHistoryOutbox {
			update = tgReadHistoryOutboxUpdate(event)
		} else {
			update = tgReadHistoryInboxUpdate(event)
		}
		if update == nil {
			return nil
		}
		return &tg.Updates{
			Updates: appendAuxPtsBookkeeping([]tg.UpdateClass{update}, event),
			Date:    event.Date,
			Seq:     0,
		}
	case domain.UpdateEventChannelState:
		update := tgOtherUpdateFromEvent(event)
		if update == nil {
			return nil
		}
		return &tg.Updates{
			Updates: appendAuxPtsBookkeeping([]tg.UpdateClass{update}, event),
			Chats:   tgChannels(viewerUserID, event.Channels),
			Date:    event.Date,
			Seq:     0,
		}
	case domain.UpdateEventNoop:
		return nil
	default:
		update := tgOtherUpdateFromEvent(event)
		if update == nil {
			return nil
		}
		return &tg.Updates{
			Updates: appendAuxPtsBookkeeping([]tg.UpdateClass{update}, event),
			Date:    event.Date,
			Seq:     0,
		}
	}
}

func tgDeletedPrivateMessageOutboxUpdate(event domain.UpdateEvent) *tg.Updates {
	if event.Pts <= 0 || event.PtsCount <= 0 {
		return nil
	}
	ids := []int{}
	if event.Message.ID > 0 && event.Message.ID <= domain.MaxMessageBoxID {
		ids = append(ids, event.Message.ID)
	}
	date := event.Date
	if date == 0 {
		date = event.Message.Date
	}
	return &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateDeleteMessages{
			Messages: ids,
			Pts:      event.Pts,
			PtsCount: event.PtsCount,
		}},
		Date: date,
		Seq:  0,
	}
}

// appendAuxPtsBookkeeping attaches an empty updateDeleteMessages to events that "consume account
// pts but whose TL update carries no pts": the client advances its local watermark by pts/pts_count
// without producing any visible change. Without it, the client watermark stops before the event and
// the next pts-bearing update is treated as a gap.
func appendAuxPtsBookkeeping(updates []tg.UpdateClass, event domain.UpdateEvent) []tg.UpdateClass {
	if event.Pts <= 0 || event.PtsCount <= 0 || !event.LacksWirePts() {
		return updates
	}
	return append(updates, &tg.UpdateDeleteMessages{
		Messages: []int{},
		Pts:      event.Pts,
		PtsCount: event.PtsCount,
	})
}
