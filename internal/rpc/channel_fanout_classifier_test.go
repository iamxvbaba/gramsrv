package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// channelUpdateWirePts returns the channel pts of a channel update (if that TL update carries
// channel pts). Only updates carrying channel pts advance the client's channel PtsWaiter, which is
// the criterion for the first class "channel-payload-pts durable" of the design §5 classifier; the
// rest (participant=qts, channel state=no pts) do not carry it.
func channelUpdateWirePts(u tg.UpdateClass) (int, bool) {
	switch v := u.(type) {
	case *tg.UpdateNewChannelMessage:
		return v.Pts, true
	case *tg.UpdateEditChannelMessage:
		return v.Pts, true
	case *tg.UpdateDeleteChannelMessages:
		return v.Pts, true
	case *tg.UpdatePinnedChannelMessages:
		return v.Pts, true
	default:
		return 0, false
	}
}

// TestChannelUpdateEventTypePtsClassification locks in the base axis of the design §5 classifier:
// which domain.ChannelUpdateEventType produce, via tgChannelUpdate, a real "channel-pts-bearing,
// advances the client's channel PtsWaiter" payload (-> eligible for the Phase 0 async durable
// fan-out worker), and which do not carry channel pts (participant=qts / noop -> must be excluded
// from ChannelFanoutJob and take the dedicated synchronous path).
//
// When adding a ChannelUpdateEventType it must be explicitly classified in this table: forgetting
// triggers a completeness assertion failure, forcing a human decision and preventing no-pts /
// transient events from being wrongly stuffed into the (channel_id, pts) durable worker (design
// §2.1/§8-D6).
func TestChannelUpdateEventTypePtsClassification(t *testing.T) {
	const viewer = int64(9001)
	msg := domain.ChannelMessage{ChannelID: 1, ID: 10, SenderUserID: 2, Date: 1, Body: "hi"}

	cases := []struct {
		name           string
		eventType      domain.ChannelUpdateEventType
		event          domain.ChannelUpdateEvent
		wantCarriesPts bool // whether it produces a channel-pts-bearing wire update (= eligible for the durable worker)
	}{
		{
			name:           "new_message",
			eventType:      domain.ChannelUpdateNewMessage,
			event:          domain.ChannelUpdateEvent{Type: domain.ChannelUpdateNewMessage, ChannelID: 1, Pts: 5, PtsCount: 1, Message: msg},
			wantCarriesPts: true,
		},
		{
			name:           "edit_message",
			eventType:      domain.ChannelUpdateEditMessage,
			event:          domain.ChannelUpdateEvent{Type: domain.ChannelUpdateEditMessage, ChannelID: 1, Pts: 6, PtsCount: 1, Message: msg},
			wantCarriesPts: true,
		},
		{
			name:           "delete_messages",
			eventType:      domain.ChannelUpdateDeleteMessages,
			event:          domain.ChannelUpdateEvent{Type: domain.ChannelUpdateDeleteMessages, ChannelID: 1, Pts: 7, PtsCount: 1, MessageIDs: []int{10}},
			wantCarriesPts: true,
		},
		{
			name:           "pinned_messages",
			eventType:      domain.ChannelUpdatePinnedMessages,
			event:          domain.ChannelUpdateEvent{Type: domain.ChannelUpdatePinnedMessages, ChannelID: 1, Pts: 8, PtsCount: 1, MessageIDs: []int{10}, Pinned: true},
			wantCarriesPts: true,
		},
		{
			// participant is qts/no-channel-pts: must be excluded from the durable worker and take
			// the participant-specific path (account-level channel state / getFullChannel /
			// getParticipants fallback). Design §8-D6.
			name:           "participant",
			eventType:      domain.ChannelUpdateParticipant,
			event:          domain.ChannelUpdateEvent{Type: domain.ChannelUpdateParticipant, ChannelID: 1, Participant: domain.ChannelMember{UserID: 2}},
			wantCarriesPts: false,
		},
		{
			name:           "noop",
			eventType:      domain.ChannelUpdateNoop,
			event:          domain.ChannelUpdateEvent{Type: domain.ChannelUpdateNoop, ChannelID: 1},
			wantCarriesPts: false,
		},
	}

	// Completeness: this table must cover every known ChannelUpdateEventType. An unclassified new type -> failure.
	allKnownTypes := map[domain.ChannelUpdateEventType]struct{}{
		domain.ChannelUpdateNewMessage:     {},
		domain.ChannelUpdateEditMessage:    {},
		domain.ChannelUpdateDeleteMessages: {},
		domain.ChannelUpdateParticipant:    {},
		domain.ChannelUpdatePinnedMessages: {},
		domain.ChannelUpdateNoop:           {},
	}
	covered := make(map[domain.ChannelUpdateEventType]struct{}, len(cases))
	for _, tc := range cases {
		covered[tc.eventType] = struct{}{}
	}
	for et := range allKnownTypes {
		if _, ok := covered[et]; !ok {
			t.Fatalf("ChannelUpdateEventType %q is not classified in the classifier's pinned table -- a new type must have its pts classification explicitly decided", et)
		}
	}
	if len(covered) != len(allKnownTypes) {
		t.Fatalf("classifier table covers %d types, %d known; a new type requires syncing allKnownTypes + this table", len(covered), len(allKnownTypes))
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			update := tgChannelUpdate(viewer, tc.event)
			if tc.eventType == domain.ChannelUpdateNoop {
				if update != nil {
					t.Fatalf("noop event produced update %#v, want nil", update)
				}
				return
			}
			if update == nil {
				t.Fatalf("event %q produced nil update", tc.eventType)
			}
			pts, carries := channelUpdateWirePts(update)
			if carries != tc.wantCarriesPts {
				t.Fatalf("event %q carriesChannelPts=%v (%T), want %v", tc.eventType, carries, update, tc.wantCarriesPts)
			}
			if tc.wantCarriesPts && pts != tc.event.Pts {
				t.Fatalf("event %q wire pts=%d, want %d", tc.eventType, pts, tc.event.Pts)
			}
		})
	}
}

// TestChannelFanoutEntranceClassificationLocked pins the "channel-scoped proactive delivery
// entrances" classification of design §5 and the current dispatch method as a test-time boundary
// (guarding against regression + documenting why some channel-pts-bearing entrances stay
// synchronous).
//
// This table is a manually maintained list (it does not reflect the call graph automatically);
// it is consistent with docs/channel-fanout-async-design.md §5. When adding/changing a
// channel-scoped proactive delivery entrance you must sync this table and re-check the
// classification during review:
//
//	already async (durable channel-payload-pts -> enters enqueueChannelFanout* async worker):
//	  send / sendMedia / forward / forum-topic-msg / discussion linkage / edit / geolive-edit / todo /
//	  bot-inline-edit / delete / deleteParticipantHistory / pin(updatePinnedMessage) / unpinAll /
//	  TTL-expiry-delete.
//	deliberately kept synchronous and locked (mixed container / MustDeliver loses the difference
//	recovery surface / cold start, see the reason column below):
//	  createChannel / importChatInvite / inviteToChannel / createChat(invite) / joinChannel /
//	  leaveChannel / editBanned(kick) / hideChatJoinRequest / hideAllChatJoinRequests /
//	  editTitle / group-call started/ended/invite service message.
//	no channel pts, excluded from the durable worker (participant / state / TTL-set / forum-pin /
//	  read-outbox / group-call version / typing); viewer-only has no durable event (poll / reaction)
//	  -- migrated only in Phase 4.
//
// Here we only assert a set of "invariant-level" classification facts, as a compile-time/test-time guardrail.
func TestChannelFanoutEntranceClassificationLocked(t *testing.T) {
	type entrance struct {
		name           string
		carriesChanPts bool
		asyncFanout    bool   // whether it currently goes through the async durable worker
		reason         string // if it carries pts yet stays synchronous, records why (mixed-container / MustDeliver / cold start)
	}
	// Only list "channel-pts-bearing" entrances (no-pts participant/state/typing are covered by the event-type table above and the design exclusion rules).
	entrances := []entrance{
		{name: "send/sendMedia/forward/forum-msg/discussion/edit/geolive/todo/bot-inline/delete/pin/unpinAll/deleteParticipantHistory/expiry-delete", carriesChanPts: true, asyncFanout: true},
		{name: "editTitle", carriesChanPts: true, asyncFanout: true, reason: "split containers: a no-pts UpdateChannel is sent synchronously (no difference recovery surface), while the pts-bearing title service message (both broadcast+megagroup produce pts) goes through async fan-out (has difference recovery surface + nudge fallback)"},
		{name: "createChannel", carriesChanPts: true, asyncFanout: false, reason: "cold start: recipients=creator+few initial invitees, no large-group amplification/no latency benefit, the RPC result is tightly coupled with the first push"},
		{name: "importChatInvite/inviteToChannel/createChat-invite/joinChannel/hideChatJoinRequest/hideAllChatJoinRequests", carriesChanPts: true, asyncFanout: false, reason: "goes through the channelOperationUpdates mixed container (pts service message + no-pts UpdateChannel; broadcast only UpdateChannel), and a new member is MustDeliver and loses the difference recovery surface -- must first split the container + no-fold before it can be async"},
		{name: "leaveChannel/editBanned(kick)", carriesChanPts: true, asyncFanout: false, reason: "the leaver/kicked user is in recipients and has already lost the channel difference recovery surface (MustDeliver), and the droppable queue has no no-fold channel; the participant part has no pts"},
		{name: "groupCallServiceMessage", carriesChanPts: true, asyncFanout: false, reason: "goes through the channelOperationUpdates mixed container; the version signal must keep a special path, so only the service message can be split out and made async"},
	}
	for _, e := range entrances {
		if !e.carriesChanPts {
			t.Fatalf("entrance %q is wrongly listed in this table (the table only lists channel-pts-bearing entrances)", e.name)
		}
		if !e.asyncFanout && e.reason == "" {
			t.Fatalf("entrance %q carries channel pts yet stays synchronous, so the reason for staying synchronous must be recorded (mixed-container/MustDeliver/cold start)", e.name)
		}
	}
}
