package rpc

import (
	"context"
	"errors"
	"github.com/iamxvbaba/td/tg"
	"strings"
	"telesrv/internal/domain"
	"unicode/utf8"
)

func (r *Router) onMessagesUpdateSavedReactionTag(ctx context.Context, req *tg.MessagesUpdateSavedReactionTagRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	reaction, err := domainMessageReactionFromTL(req.Reaction)
	if err != nil {
		return false, err
	}
	if reaction.Type != domain.MessageReactionEmoji {
		return false, reactionInvalidErr()
	}
	title, ok := req.GetTitle()
	if !ok {
		title = ""
	}
	if utf8.RuneCountInString(title) > maxSavedReactionTagTitle {
		return false, limitInvalidErr()
	}
	if r.deps.Channels != nil {
		if err := r.deps.Channels.UpdateSavedReactionTag(ctx, userID, domain.SavedReactionTag{
			UserID:   userID,
			Reaction: reaction,
			Title:    title,
		}); err != nil {
			return false, channelInvalidErr(err)
		}
	}
	r.pushUserUpdates(ctx, userID, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateSavedReactionTags{}},
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	})
	return true, nil
}

func (r *Router) reactionPeer(ctx context.Context, peer tg.InputPeerClass, savedPeer func() (tg.InputPeerClass, bool)) (int64, domain.Peer, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return 0, domain.Peer{}, internalErr()
	}
	out, err := r.checkedDomainPeerFromInputPeer(ctx, userID, peer)
	if err != nil {
		return 0, domain.Peer{}, err
	}
	if savedPeer != nil {
		if input, ok := savedPeer(); ok && input != nil {
			if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, input); err != nil {
				return 0, domain.Peer{}, err
			}
		}
	}
	return userID, out, nil
}

func domainMessageReactionsFromTL(req *tg.MessagesSendReactionRequest) ([]domain.MessageReaction, error) {
	if req == nil {
		return nil, nil
	}
	reactions, ok := req.GetReaction()
	if !ok || len(reactions) == 0 {
		return nil, nil
	}
	out := make([]domain.MessageReaction, 0, len(reactions))
	seen := make(map[string]struct{}, len(reactions))
	for _, reaction := range reactions {
		parsed, err := domainMessageReactionFromTL(reaction)
		if err != nil {
			return nil, err
		}
		key := parsed.Key()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, parsed)
	}
	return out, nil
}

func optionalDomainMessageReaction(reaction tg.ReactionClass) (*domain.MessageReaction, error) {
	if reaction == nil {
		return nil, nil
	}
	if reactionClassNil(reaction) {
		return nil, reactionInvalidErr()
	}
	out, err := domainMessageReactionFromTL(reaction)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func domainMessageReactionFromTL(reaction tg.ReactionClass) (domain.MessageReaction, error) {
	if reactionClassNil(reaction) {
		return domain.MessageReaction{}, reactionInvalidErr()
	}
	switch typed := reaction.(type) {
	case *tg.ReactionEmoji:
		emoticon := strings.TrimSpace(typed.Emoticon)
		if emoticon == "" || utf8.RuneCountInString(emoticon) > domain.MaxChannelReactionEmoticonLength {
			return domain.MessageReaction{}, reactionInvalidErr()
		}
		return domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: emoticon}, nil
	case *tg.ReactionCustomEmoji:
		if typed.DocumentID <= 0 {
			return domain.MessageReaction{}, reactionInvalidErr()
		}
		return domain.MessageReaction{Type: domain.MessageReactionCustomEmoji, DocumentID: typed.DocumentID}, nil
	case nil, *tg.ReactionEmpty, *tg.ReactionPaid:
		return domain.MessageReaction{}, reactionInvalidErr()
	default:
		return domain.MessageReaction{}, inputConstructorInvalidErr()
	}
}

func reactionClassNil(reaction tg.ReactionClass) bool {
	switch typed := reaction.(type) {
	case nil:
		return true
	case *tg.ReactionEmpty:
		return typed == nil
	case *tg.ReactionEmoji:
		return typed == nil
	case *tg.ReactionCustomEmoji:
		return typed == nil
	case *tg.ReactionPaid:
		return typed == nil
	default:
		return false
	}
}

func domainStoryReactionValueFromTL(reaction tg.ReactionClass) (domain.MessageReaction, error) {
	if reactionClassNil(reaction) {
		return domain.MessageReaction{}, reactionInvalidErr()
	}
	switch typed := reaction.(type) {
	case *tg.ReactionEmoji:
		return domainMessageReactionFromTL(typed)
	case *tg.ReactionCustomEmoji:
		if typed.DocumentID <= 0 {
			return domain.MessageReaction{}, reactionInvalidErr()
		}
		return domain.MessageReaction{Type: domain.MessageReactionCustomEmoji, DocumentID: typed.DocumentID}, nil
	case nil, *tg.ReactionEmpty, *tg.ReactionPaid:
		return domain.MessageReaction{}, reactionInvalidErr()
	default:
		return domain.MessageReaction{}, inputConstructorInvalidErr()
	}
}

func messageReactionErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrMessageIDInvalid):
		return messageIDInvalidErr()
	default:
		return internalErr()
	}
}

func channelReactionErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrReactionInvalid):
		return reactionInvalidErr()
	case errors.Is(err, domain.ErrReactionsTooMany):
		// 官方：消息去重 emoji 种类已达 reactions_uniq_max（或 chat 自定义 reactions_limit）时
		// 新种类 reaction 报 400 REACTIONS_TOO_MANY，追加已有种类不受限。
		return tgerr400("REACTIONS_TOO_MANY")
	default:
		return channelInvalidErr(err)
	}
}
