package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// updateMessageWidgetData carries its own account pts on Layer 233. td drops it
// for 225-232, and those sessions recover the gap through getDifference: the
// online delivery must not add an empty updateDeleteMessages or any other
// filler that consumes the same pts.
func TestLayer233WidgetDataOutboxCarriesNoPtsFiller(t *testing.T) {
	event := domain.UpdateEvent{
		UserID: 10, Type: domain.UpdateEventMessageWidgetData, Pts: 41, PtsCount: 1, Date: 1700000000,
		Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 20},
		Message: domain.Message{
			OwnerUserID: 10, ID: 7, Pts: 41, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 20},
			Media: &domain.MessageMedia{Kind: domain.MessageMediaKindWidget, Widget: &domain.MessageWidget{
				PublicData: "p", PublicDataHash: widgetDataHash("p"), PublicDataSet: true,
			}},
		},
	}
	if event.LacksWirePts() {
		t.Fatal("message_widget_data must not use aux pts bookkeeping: its constructor carries pts")
	}
	updates := tgUpdateForOutboxEventForViewer(event, 10)
	if updates == nil || len(updates.Updates) != 1 {
		t.Fatalf("outbox updates = %#v, want only updateMessageWidgetData", updates)
	}
	update, ok := updates.Updates[0].(*tg.UpdateMessageWidgetData)
	if !ok {
		t.Fatalf("outbox update = %T, want *tg.UpdateMessageWidgetData", updates.Updates[0])
	}
	if update.Pts != 41 || update.PtsCount != 1 || update.MsgID != 7 {
		t.Fatalf("outbox update = %+v, want pts 41/1 for msg 7", update)
	}
}
