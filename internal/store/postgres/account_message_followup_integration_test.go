package postgres

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
	"testing"
)

func TestParticipantHistoryMatchesVisibleSender(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	owner := createTestUser(t, ctx, users, "+1616"+suffix+"01", "Owner", "")
	member := createTestUser(t, ctx, users, "+1616"+suffix+"02", "Member", "")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=ANY($1::bigint[])", []int64{owner.ID, member.ID})
	})
	for name, channels := range map[string]store.ChannelStore{"postgres": newTestChannelStore(pool), "memory": memory.NewChannelStore()} {
		t.Run(name, func(t *testing.T) {
			created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner.ID, Title: "Visible sender", Megagroup: true, MemberUserIDs: []int64{member.ID}, Date: 1800000000})
			if err != nil {
				t.Fatal(err)
			}
			id := created.Channel.ID
			if name == "postgres" {
				t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id=$1", id) })
			}
			identity := domain.Peer{Type: domain.PeerTypeChannel, ID: id}
			var sent []domain.ChannelMessage
			for i, actor := range []int64{owner.ID, member.ID, owner.ID, member.ID} {
				var as *domain.Peer
				if i >= 2 {
					as = &identity
				}
				result, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: actor, ChannelID: id, SendAs: as, RandomID: int64(i + 1), Message: "body", Date: 1800000001 + i})
				if err != nil {
					t.Fatal(err)
				}
				sent = append(sent, result.Message)
			}
			req := domain.DeleteChannelParticipantHistoryRequest{UserID: member.ID, ChannelID: id, Participant: identity, Date: 1800000010}
			if _, err := channels.DeleteChannelParticipantHistory(ctx, req); !errors.Is(err, domain.ErrChannelAdminRequired) {
				t.Fatalf("non-admin: %v", err)
			}
			req.UserID = owner.ID
			req.Participant = domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
			personal, err := channels.DeleteChannelParticipantHistory(ctx, req)
			if err != nil || !reflect.DeepEqual(personal.DeletedIDs, []int{sent[0].ID}) {
				t.Fatalf("personal delete: %+v %v", personal, err)
			}
			req.Participant = identity
			visible, err := channels.DeleteChannelParticipantHistory(ctx, req)
			if err != nil || len(visible.DeletedIDs) != 2 || visible.Event.PtsCount != 2 {
				t.Fatalf("visible delete: %+v %v", visible, err)
			}
			wanted := map[int]bool{sent[2].ID: true, sent[3].ID: true}
			for _, deleted := range visible.DeletedIDs {
				if !wanted[deleted] {
					t.Fatalf("deleted other sender: %d", deleted)
				}
				delete(wanted, deleted)
			}
			if len(wanted) != 0 {
				t.Fatal("missed second actor with same visible sender")
			}
			repeated, err := channels.DeleteChannelParticipantHistory(ctx, req)
			if err != nil || len(repeated.DeletedIDs) != 0 || repeated.Event.PtsCount != 0 || repeated.Channel.Pts != visible.Channel.Pts {
				t.Fatalf("empty retry: %+v %v", repeated, err)
			}
			diff, err := channels.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{UserID: member.ID, ChannelID: id, Pts: personal.Channel.Pts, Limit: 100})
			if err != nil || len(diff.Events) != 1 || diff.Events[0].Type != domain.ChannelUpdateDeleteMessages || diff.Pts != visible.Channel.Pts {
				t.Fatalf("offline difference: %+v %v", diff, err)
			}
		})
	}
}

func TestMediaReplacementPreservesCaptionAndBothOwnerEvents(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	a := createTestUser(t, ctx, users, "+1613"+suffix+"01", "Media owner", "")
	b := createTestUser(t, ctx, users, "+1613"+suffix+"02", "Media peer", "")
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=ANY($1::bigint[])", []int64{a.ID, b.ID}) })
	messages := NewMessageStore(pool, WithMessageAllocators(&perUserCounterAllocator{}))
	entities := []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: 6}}
	original := &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 101, AccessHash: 10}}
	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: a.ID, RecipientUserID: b.ID, RandomID: 9991, Message: "Styled caption", Entities: entities, Media: original, Date: 1800001000})
	if err != nil {
		t.Fatal(err)
	}
	replacement := &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 102, AccessHash: 20}}
	req := domain.EditMessageRequest{OwnerUserID: a.ID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: b.ID}, ID: sent.SenderMessage.ID, ReplaceMedia: true, PreserveText: true, Media: replacement, EditDate: 1800001001}
	edited, err := messages.EditMessage(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(edited.Edited) != 2 {
		t.Fatalf("owner projections = %d", len(edited.Edited))
	}
	for _, entry := range edited.Edited {
		if entry.Message.Body != "Styled caption" || !reflect.DeepEqual(entry.Message.Entities, entities) || entry.Message.Media.Photo.ID != 102 || entry.Event.Type != domain.UpdateEventEditMessage || entry.Event.PtsCount != 1 {
			t.Fatalf("media edit projection: %+v", entry)
		}
	}
	if edited.Self().Message.ID != sent.SenderMessage.ID {
		t.Fatal("replacement changed message identity")
	}
	_, err = messages.EditMessage(ctx, req)
	if !errors.Is(err, domain.ErrMessageNotModified) {
		t.Fatalf("replay = %v", err)
	}
	for _, owner := range []int64{a.ID, b.ID} {
		events, err := NewUpdateEventStore(pool).ListAfter(ctx, owner, 0, 20)
		if err != nil || len(events) != 2 {
			t.Fatalf("durable events %v %v", events, err)
		}
	}
}

func TestReactionPatchesRetainConcurrentSoundAndDefault(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	u := createTestUser(t, ctx, users, "+1614"+randomSuffix(t)+"01", "Settings", "")
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=$1", u.ID) })
	settings := NewPasswordStore(pool)
	notify := domain.DefaultAccountReactionSettings().Notify
	notify.Sound = domain.NotificationSound{Kind: domain.NotificationSoundRingtone, ID: 9223372036854775806}
	reaction := domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "fire"}
	// Both read/modify/write operations begin together; the account lock must retain both patches.
	var wg sync.WaitGroup
	start := make(chan struct{})
	failures := make(chan error, 2)
	for _, patch := range []domain.AccountReactionSettingsPatch{{Notify: &notify}, {DefaultReaction: &reaction}} {
		wg.Add(1)
		go func(p domain.AccountReactionSettingsPatch) {
			defer wg.Done()
			<-start
			var effects store.DeliveryEffectsBuilder[domain.AccountReactionSettings]
			if !p.NotifyOnly() {
				// The default reaction has no update constructor in v2 either.
				effects = func(domain.AccountReactionSettings) ([]store.DeliveryEffect, error) { return nil, nil }
			}
			_, err := settings.UpdateReactionSettings(ctx, u.ID, p, effects)
			failures <- err
		}(patch)
	}
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	current, found, err := settings.GetReactionSettings(ctx, u.ID)
	if err != nil || !found || current.Notify.Sound != notify.Sound || current.DefaultReaction != reaction {
		t.Fatalf("concurrent settings = %+v %v", current, err)
	}
	scope := domain.NotifyScope{Kind: domain.NotifyScopeChats}
	value := domain.PeerNotifySettings{Sound: &notify.Sound, StoriesSound: &domain.NotificationSound{Kind: domain.NotificationSoundNone}}
	if err := settings.SaveNotifySettings(ctx, u.ID, scope, value); err != nil {
		t.Fatal(err)
	}
	stored, found, err := settings.GetNotifySettings(ctx, u.ID, scope)
	if err != nil || !found || !reflect.DeepEqual(stored, value) {
		t.Fatalf("sound roundtrip = %+v %v", stored, err)
	}
	bad := domain.PeerNotifySettings{Sound: &domain.NotificationSound{Kind: 99}}
	if err := settings.SaveNotifySettings(ctx, u.ID, scope, bad); err == nil {
		t.Fatal("accepted invalid sound")
	}
}
