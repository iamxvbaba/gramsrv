package rpc

import (
	"context"
	"errors"
	"testing"

	"github.com/iamxvbaba/td/tg"

	appaccount "telesrv/internal/app/account"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

func newForumNotifyTestAccount() *appaccount.Service {
	settings := memory.NewPasswordStore()
	return appaccount.NewService(settings, appaccount.WithNotifySettings(settings))
}

func TestForumNotifyProjectionPreservesTopicInheritanceAndIsolation(t *testing.T) {
	r, _ := notifyRouter(t)
	const user = int64(1001)
	ctx := WithUserID(context.Background(), user)
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: 77}
	scope := domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: peer, TopicID: 42}
	mute := 2000000000
	preview := false
	svc := r.deps.Account.(*appaccount.Service)
	if err := svc.SaveNotifySettings(ctx, user, scope, domain.PeerNotifySettings{MuteUntil: &mute, ShowPreviews: &preview}); err != nil {
		t.Fatal(err)
	}
	response := func() *tg.MessagesForumTopics {
		return &tg.MessagesForumTopics{Topics: []tg.ForumTopicClass{
			&tg.ForumTopic{ID: 1}, &tg.ForumTopic{ID: 42}, &tg.ForumTopicDeleted{ID: 99},
		}}
	}
	projected, err := r.applyForumTopicNotifySettings(ctx, user, peer.ID, response())
	if err != nil {
		t.Fatal(err)
	}
	inherited := projected.Topics[0].(*tg.ForumTopic).NotifySettings
	if _, exists := inherited.GetMuteUntil(); exists {
		t.Fatal("missing topic preference must inherit its parent")
	}
	explicit := projected.Topics[1].(*tg.ForumTopic).NotifySettings
	if got, exists := explicit.GetMuteUntil(); !exists || got != mute {
		t.Fatalf("mute = %d %v", got, exists)
	}
	if got, exists := explicit.GetShowPreviews(); !exists || got {
		t.Fatalf("preview = %v %v", got, exists)
	}
	for _, other := range []struct{ owner, channel int64 }{{user + 1, peer.ID}, {user, peer.ID + 1}} {
		isolated, err := r.applyForumTopicNotifySettings(ctx, other.owner, other.channel, response())
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := isolated.Topics[1].(*tg.ForumTopic).NotifySettings.GetMuteUntil(); exists {
			t.Fatal("notification preferences crossed owner or channel")
		}
	}
	api, err := r.onAccountGetNotifySettings(ctx, &tg.InputNotifyForumTopic{Peer: &tg.InputPeerChannel{ChannelID: peer.ID}, TopMsgID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := api.GetMuteUntil(); exists {
		t.Fatal("direct topic lookup lost inheritance")
	}
	if err := svc.ResetNotifySettingsWithDelivery(ctx, user, func(snapshot store.NotifySettingsResetSnapshot) ([]store.DeliveryEffect, error) {
		return []store.DeliveryEffect{store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
			TargetUserID: snapshot.OwnerUserID, Payload: []byte{1}, RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
		})}, nil
	}); err != nil {
		t.Fatal(err)
	}
	reset, err := r.applyForumTopicNotifySettings(ctx, user, peer.ID, response())
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := reset.Topics[1].(*tg.ForumTopic).NotifySettings.GetMuteUntil(); exists {
		t.Fatal("reset topic must inherit")
	}
}

type failedForumNotifyAccount struct{ AccountService }

func (failedForumNotifyAccount) ForumTopicNotifySettings(context.Context, int64, domain.Peer, []int) (map[int]domain.PeerNotifySettings, error) {
	return nil, errors.New("storage unavailable")
}
func TestForumNotifyProjectionFailsClosedOnMissingOrFailedStore(t *testing.T) {
	r, _ := notifyRouter(t)
	for _, account := range []AccountService{nil, failedForumNotifyAccount{}} {
		r.deps.Account = account
		result, err := r.applyForumTopicNotifySettings(context.Background(), 1, 2, &tg.MessagesForumTopics{Topics: []tg.ForumTopicClass{&tg.ForumTopic{ID: 42}}})
		if err == nil || result != nil {
			t.Fatal("missing preferences dependency returned a default success")
		}
	}
}
