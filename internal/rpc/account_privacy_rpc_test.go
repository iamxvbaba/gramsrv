package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appprivacy "telesrv/internal/app/privacy"
	appupdates "telesrv/internal/app/updates"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestAccountPrivacyAllKeysRoundTripWithoutAdvancingPts(t *testing.T) {
	ctx := context.Background()
	const userID int64 = 8101
	authKeyID := [8]byte{8, 1}
	sessionID := int64(81)
	privacy := appprivacy.NewService(memory.NewPrivacyStore(), memory.NewContactStore())
	events := memory.NewUpdateEventStore()
	updates := appupdates.NewService(memory.NewUpdateStateStore(), events)
	sessions := &captureSessions{}
	router := New(Config{}, Deps{
		Privacy:  privacy,
		Updates:  updates,
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	requestCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, userID), authKeyID), sessionID)

	keys := []struct {
		name   string
		input  tg.InputPrivacyKeyClass
		domain domain.PrivacyKey
		wire   func(tg.PrivacyKeyClass) bool
	}{
		{"status_timestamp", &tg.InputPrivacyKeyStatusTimestamp{}, domain.PrivacyKeyStatusTimestamp, func(v tg.PrivacyKeyClass) bool { _, ok := v.(*tg.PrivacyKeyStatusTimestamp); return ok }},
		{"chat_invite", &tg.InputPrivacyKeyChatInvite{}, domain.PrivacyKeyChatInvite, func(v tg.PrivacyKeyClass) bool { _, ok := v.(*tg.PrivacyKeyChatInvite); return ok }},
		{"phone_call", &tg.InputPrivacyKeyPhoneCall{}, domain.PrivacyKeyPhoneCall, func(v tg.PrivacyKeyClass) bool { _, ok := v.(*tg.PrivacyKeyPhoneCall); return ok }},
		{"phone_p2p", &tg.InputPrivacyKeyPhoneP2P{}, domain.PrivacyKeyPhoneP2P, func(v tg.PrivacyKeyClass) bool { _, ok := v.(*tg.PrivacyKeyPhoneP2P); return ok }},
		{"forwards", &tg.InputPrivacyKeyForwards{}, domain.PrivacyKeyForwards, func(v tg.PrivacyKeyClass) bool { _, ok := v.(*tg.PrivacyKeyForwards); return ok }},
		{"profile_photo", &tg.InputPrivacyKeyProfilePhoto{}, domain.PrivacyKeyProfilePhoto, func(v tg.PrivacyKeyClass) bool { _, ok := v.(*tg.PrivacyKeyProfilePhoto); return ok }},
		{"phone_number", &tg.InputPrivacyKeyPhoneNumber{}, domain.PrivacyKeyPhoneNumber, func(v tg.PrivacyKeyClass) bool { _, ok := v.(*tg.PrivacyKeyPhoneNumber); return ok }},
		{"added_by_phone", &tg.InputPrivacyKeyAddedByPhone{}, domain.PrivacyKeyAddedByPhone, func(v tg.PrivacyKeyClass) bool { _, ok := v.(*tg.PrivacyKeyAddedByPhone); return ok }},
		{"voice_messages", &tg.InputPrivacyKeyVoiceMessages{}, domain.PrivacyKeyVoiceMessages, func(v tg.PrivacyKeyClass) bool { _, ok := v.(*tg.PrivacyKeyVoiceMessages); return ok }},
		{"about", &tg.InputPrivacyKeyAbout{}, domain.PrivacyKeyAbout, func(v tg.PrivacyKeyClass) bool { _, ok := v.(*tg.PrivacyKeyAbout); return ok }},
		{"birthday", &tg.InputPrivacyKeyBirthday{}, domain.PrivacyKeyBirthday, func(v tg.PrivacyKeyClass) bool { _, ok := v.(*tg.PrivacyKeyBirthday); return ok }},
		{"star_gifts_auto_save", &tg.InputPrivacyKeyStarGiftsAutoSave{}, domain.PrivacyKeyStarGiftsAutoSave, func(v tg.PrivacyKeyClass) bool { _, ok := v.(*tg.PrivacyKeyStarGiftsAutoSave); return ok }},
		{"no_paid_messages", &tg.InputPrivacyKeyNoPaidMessages{}, domain.PrivacyKeyNoPaidMessages, func(v tg.PrivacyKeyClass) bool { _, ok := v.(*tg.PrivacyKeyNoPaidMessages); return ok }},
		{"saved_music", &tg.InputPrivacyKeySavedMusic{}, domain.PrivacyKeySavedMusic, func(v tg.PrivacyKeyClass) bool { _, ok := v.(*tg.PrivacyKeySavedMusic); return ok }},
	}

	for _, test := range keys {
		t.Run(test.name, func(t *testing.T) {
			gotKey, ok := domainPrivacyKeyFromInput(test.input)
			if !ok || gotKey != test.domain {
				t.Fatalf("input key maps to %q/%v, want %q/true", gotKey, ok, test.domain)
			}
			if !test.wire(tgPrivacyKey(test.domain)) {
				t.Fatalf("domain key %q projected as %T", test.domain, tgPrivacyKey(test.domain))
			}
			set, err := router.onAccountSetPrivacy(requestCtx, &tg.AccountSetPrivacyRequest{
				Key:   test.input,
				Rules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueDisallowAll{}},
			})
			if err != nil {
				t.Fatalf("setPrivacy: %v", err)
			}
			if len(set.Rules) != 1 {
				t.Fatalf("setPrivacy rules=%d, want 1", len(set.Rules))
			}
			if _, ok := set.Rules[0].(*tg.PrivacyValueDisallowAll); !ok {
				t.Fatalf("setPrivacy rule=%T, want disallowAll", set.Rules[0])
			}
			get, err := router.onAccountGetPrivacy(requestCtx, test.input)
			if err != nil {
				t.Fatalf("getPrivacy: %v", err)
			}
			if len(get.Rules) != 1 {
				t.Fatalf("getPrivacy rules=%d, want 1", len(get.Rules))
			}
			if _, ok := get.Rules[0].(*tg.PrivacyValueDisallowAll); !ok {
				t.Fatalf("getPrivacy rule=%T, want disallowAll", get.Rules[0])
			}
			pushed, ok := sessions.lastUserPush().(*tg.Updates)
			if !ok || len(pushed.Updates) != 1 {
				t.Fatalf("online push=%T/%+v, want one updatePrivacy", sessions.lastUserPush(), pushed)
			}
			privacyUpdate, ok := pushed.Updates[0].(*tg.UpdatePrivacy)
			if !ok {
				t.Fatalf("online push update=%T, want updatePrivacy(%q)", pushed.Updates[0], test.domain)
			}
			if !test.wire(privacyUpdate.Key) {
				t.Fatalf("online push key=%T, want %q", privacyUpdate.Key, test.domain)
			}
		})
	}
	if pushedUserIDs := sessions.pushedUserIDs(); len(pushedUserIDs) != len(keys) {
		t.Fatalf("online privacy pushes=%v, want exactly one per key", pushedUserIDs)
	} else {
		for i, pushedUserID := range pushedUserIDs {
			if pushedUserID != userID {
				t.Fatalf("online privacy push[%d] target=%d, want owner %d", i, pushedUserID, userID)
			}
		}
	}
	if snapshot := sessions.snapshot(); snapshot.sessionID != sessionID || snapshot.userID != userID {
		t.Fatalf("online push exclusion/target=%+v, want current session %d excluded for user %d", snapshot, sessionID, userID)
	}

	recorded, err := events.ListAfter(ctx, userID, 0, 100)
	if err != nil {
		t.Fatalf("list account update events: %v", err)
	}
	if len(recorded) != 0 {
		t.Fatalf("account update events=%+v, want none for privacy changes", recorded)
	}
	state, err := updates.CurrentState(ctx, userID)
	if err != nil {
		t.Fatalf("current update state: %v", err)
	}
	if state.Pts != 0 {
		t.Fatalf("privacy changes advanced pts to %d, want 0", state.Pts)
	}

	difference, err := updates.GetDifference(ctx, [8]byte{8, 2}, userID, domain.UpdateState{})
	if err != nil {
		t.Fatalf("getDifference: %v", err)
	}
	if difference.State.Pts != 0 || len(difference.Events) != 0 {
		t.Fatalf("difference after privacy changes=%+v, want empty pts=0", difference)
	}

	// A real message-box update immediately after privacy changes must still
	// receive pts=1. This catches both hidden privacy allocations and gaps left
	// behind by synthetic bookkeeping events.
	message := domain.Message{
		ID:          1,
		OwnerUserID: userID,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 8102},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: 8102},
		Date:        1700000000,
		Body:        "after privacy",
	}
	event, state, err := updates.RecordNewMessage(ctx, authKeyID, userID, message)
	if err != nil {
		t.Fatalf("record adjacent message update: %v", err)
	}
	if event.Pts != 1 || event.PtsCount != 1 || state.Pts != 1 {
		t.Fatalf("adjacent message event/state=%+v/%+v, want first pts=1", event, state)
	}
	difference, err = updates.GetDifference(ctx, [8]byte{8, 2}, userID, domain.UpdateState{})
	if err != nil {
		t.Fatalf("getDifference after message: %v", err)
	}
	if difference.State.Pts != 1 || len(difference.Events) != 1 || difference.Events[0].Type != domain.UpdateEventNewMessage {
		t.Fatalf("difference after adjacent message=%+v, want one contiguous new_message at pts=1", difference)
	}
}
