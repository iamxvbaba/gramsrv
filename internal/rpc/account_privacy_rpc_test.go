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

func TestAccountPrivacyAllKeysRoundTripAndRecordDifferenceEvents(t *testing.T) {
	ctx := context.Background()
	const userID int64 = 8101
	authKeyID := [8]byte{8, 1}
	sessionID := int64(81)
	privacy := appprivacy.NewService(memory.NewPrivacyStore(), memory.NewContactStore())
	events := memory.NewUpdateEventStore()
	updates := appupdates.NewService(memory.NewUpdateStateStore(), events)
	router := New(Config{}, Deps{
		Privacy:  privacy,
		Updates:  updates,
		Sessions: &captureSessions{},
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
		})
	}

	recorded, err := events.ListAfter(ctx, userID, 0, 100)
	if err != nil {
		t.Fatalf("list privacy events: %v", err)
	}
	if len(recorded) != len(keys) {
		t.Fatalf("privacy events=%d, want %d", len(recorded), len(keys))
	}
	for i, event := range recorded {
		if event.Type != domain.UpdateEventPrivacy ||
			event.Privacy.OwnerUserID != userID ||
			event.Privacy.Key != keys[i].domain ||
			event.PtsCount != 1 {
			t.Fatalf("event[%d]=%+v, want durable privacy snapshot for %q", i, event, keys[i].domain)
		}
	}

	difference, err := updates.GetDifference(ctx, [8]byte{8, 2}, userID, domain.UpdateState{})
	if err != nil {
		t.Fatalf("getDifference: %v", err)
	}
	wireDifference, ok := tgUpdatesDifference(userID, difference).(*tg.UpdatesDifference)
	if !ok || len(wireDifference.OtherUpdates) != len(keys) {
		t.Fatalf("wire difference=%T updates=%d, want %d privacy updates", wireDifference, len(wireDifference.OtherUpdates), len(keys))
	}
	for i, update := range wireDifference.OtherUpdates {
		privacyUpdate, ok := update.(*tg.UpdatePrivacy)
		if !ok {
			t.Fatalf("difference update[%d]=%T, want updatePrivacy", i, update)
		}
		if !keys[i].wire(privacyUpdate.Key) {
			t.Fatalf("difference update[%d] key=%T, want %q", i, privacyUpdate.Key, keys[i].domain)
		}
	}
}
