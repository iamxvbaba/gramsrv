package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

type forumTopicNotifyService interface {
	ForumTopicNotifySettings(context.Context, int64, domain.Peer, []int) (map[int]domain.PeerNotifySettings, error)
}

// Topic list and by-ID reads must expose the same explicit preferences as
// account.getNotifySettings. Missing rows preserve inheritance; read failures
// cannot be presented as default preferences.
func (r *Router) applyForumTopicNotifySettings(ctx context.Context, userID, channelID int64, response *tg.MessagesForumTopics) (*tg.MessagesForumTopics, error) {
	ids := make([]int, 0, len(response.Topics))
	for _, topic := range response.Topics {
		if topic, ok := topic.(*tg.ForumTopic); ok {
			ids = append(ids, topic.ID)
		}
	}
	if len(ids) == 0 {
		return response, nil
	}
	svc, ok := r.deps.Account.(forumTopicNotifyService)
	if !ok {
		return nil, internalErr()
	}
	settings, err := svc.ForumTopicNotifySettings(ctx, userID, domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}, ids)
	if err != nil {
		return nil, internalErr()
	}
	for _, topic := range response.Topics {
		if topic, ok := topic.(*tg.ForumTopic); ok {
			explicit := settings[topic.ID]
			topic.NotifySettings = *tgScopeNotifySettings(domain.NotifyScope{TopicID: topic.ID}, &explicit)
		}
	}
	return response, nil
}
