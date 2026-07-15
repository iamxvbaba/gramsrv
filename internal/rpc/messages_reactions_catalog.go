package rpc

import (
	"context"
	"github.com/iamxvbaba/td/tg"
	"hash/fnv"
	"strconv"
	"strings"
	"telesrv/internal/compat/tdesktop"
	"telesrv/internal/domain"
)

func (r *Router) onMessagesGetTopReactions(ctx context.Context, req *tg.MessagesGetTopReactionsRequest) (tg.MessagesReactionsClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if req.Limit < 0 || req.Limit > maxSearchResultsLimit {
		return nil, limitInvalidErr()
	}
	limit := req.Limit
	if limit == 0 {
		limit = defaultTopReactionsLimit
	}
	reactions := []domain.MessageReaction{}
	if r.deps.Channels != nil {
		var err error
		reactions, err = r.deps.Channels.TopReactions(ctx, userID, limit)
		if err != nil {
			return nil, channelInvalidErr(err)
		}
	}
	return messagesReactionsFromDomain(r.reactionsWithCatalogFallback(ctx, reactions, limit), req.Hash), nil
}

func (r *Router) onMessagesGetRecentReactions(ctx context.Context, req *tg.MessagesGetRecentReactionsRequest) (tg.MessagesReactionsClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if req.Limit < 0 || req.Limit > maxSearchResultsLimit {
		return nil, limitInvalidErr()
	}
	if r.deps.Channels == nil {
		return messagesReactionsEmpty(req.Hash), nil
	}
	reactions, err := r.deps.Channels.RecentReactions(ctx, userID, req.Limit)
	if err != nil {
		return nil, channelInvalidErr(err)
	}
	return messagesReactionsFromDomain(reactions, req.Hash), nil
}

func (r *Router) onMessagesClearRecentReactions(ctx context.Context) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if r.deps.Channels != nil {
		if err := r.deps.Channels.ClearRecentReactions(ctx, userID); err != nil {
			return false, channelInvalidErr(err)
		}
	}
	return true, nil
}

func (r *Router) onMessagesGetSavedReactionTags(ctx context.Context, req *tg.MessagesGetSavedReactionTagsRequest) (tg.MessagesSavedReactionTagsClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if peer, ok := req.GetPeer(); ok && peer != nil {
		if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, peer); err != nil {
			return nil, err
		}
		return savedReactionTagsEmpty(req.Hash), nil
	}
	if r.deps.Channels == nil {
		return savedReactionTagsEmpty(req.Hash), nil
	}
	tags, err := r.deps.Channels.SavedReactionTags(ctx, userID, domain.MaxSavedReactionTags)
	if err != nil {
		return nil, channelInvalidErr(err)
	}
	return savedReactionTagsFromDomain(tags, req.Hash), nil
}

func (r *Router) onMessagesGetDefaultTagReactions(ctx context.Context, hash int64) (tg.MessagesReactionsClass, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	return messagesReactionsEmpty(hash), nil
}

func messagesReactionsEmpty(hash int64) tg.MessagesReactionsClass {
	if hash != 0 {
		return &tg.MessagesReactionsNotModified{}
	}
	return &tg.MessagesReactions{
		Hash:      0,
		Reactions: []tg.ReactionClass{},
	}
}

func messagesReactionsFromDomain(reactions []domain.MessageReaction, requestHash int64) tg.MessagesReactionsClass {
	hash := messageReactionListHash(reactions)
	if hash != 0 && requestHash == hash {
		return &tg.MessagesReactionsNotModified{}
	}
	out := make([]tg.ReactionClass, 0, len(reactions))
	for _, reaction := range reactions {
		tgReaction := tgMessageReaction(reaction)
		if tgReaction != nil {
			out = append(out, tgReaction)
		}
	}
	return &tg.MessagesReactions{
		Hash:      hash,
		Reactions: out,
	}
}

func savedReactionTagsEmpty(_ int64) tg.MessagesSavedReactionTagsClass {
	return &tg.MessagesSavedReactionTags{
		Tags: []tg.SavedReactionTag{},
		Hash: 0,
	}
}

func savedReactionTagsFromDomain(tags []domain.SavedReactionTag, requestHash int64) tg.MessagesSavedReactionTagsClass {
	hash := savedReactionTagListHash(tags)
	if hash != 0 && requestHash == hash {
		return &tg.MessagesSavedReactionTagsNotModified{}
	}
	out := make([]tg.SavedReactionTag, 0, len(tags))
	for _, tag := range tags {
		reaction := tgMessageReaction(tag.Reaction)
		if reaction == nil {
			continue
		}
		item := tg.SavedReactionTag{
			Reaction: reaction,
			Count:    tag.Count,
		}
		if tag.Title != "" {
			item.SetTitle(tag.Title)
		}
		out = append(out, item)
	}
	if len(out) == 0 {
		return savedReactionTagsEmpty(requestHash)
	}
	return &tg.MessagesSavedReactionTags{
		Tags: out,
		Hash: hash,
	}
}

func (r *Router) reactionsWithCatalogFallback(ctx context.Context, reactions []domain.MessageReaction, limit int) []domain.MessageReaction {
	return mergeReactionCatalogFallback(reactions, r.availableReactionCatalog(ctx, limit), limit)
}

func (r *Router) availableReactionCatalog(ctx context.Context, limit int) []domain.MessageReaction {
	if limit <= 0 {
		return nil
	}
	if r.deps.Files != nil {
		catalog, err := r.deps.Files.ListAvailableReactions(ctx)
		if err == nil {
			out := make([]domain.MessageReaction, 0, min(limit, len(catalog)))
			for _, item := range catalog {
				emoticon := strings.TrimSpace(item.Reaction)
				if item.Inactive || emoticon == "" {
					continue
				}
				out = append(out, domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: emoticon})
				if len(out) >= limit {
					return out
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	return staticReactionCatalog()
}

func staticReactionCatalog() []domain.MessageReaction {
	emoticons := tdesktop.DefaultReactionEmoticons()
	out := make([]domain.MessageReaction, 0, len(emoticons))
	for _, emoticon := range emoticons {
		out = append(out, domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: emoticon})
	}
	return out
}

func mergeReactionCatalogFallback(reactions, fallback []domain.MessageReaction, limit int) []domain.MessageReaction {
	if limit <= 0 {
		return []domain.MessageReaction{}
	}
	if limit > domain.MaxTopMessageReactions {
		limit = domain.MaxTopMessageReactions
	}
	out := make([]domain.MessageReaction, 0, limit)
	seen := make(map[string]struct{}, limit)
	for _, reaction := range reactions {
		if !reaction.Valid() {
			continue
		}
		key := reaction.Key()
		if _, ok := seen[key]; ok {
			continue
		}
		out = append(out, reaction)
		seen[key] = struct{}{}
		if len(out) >= limit {
			return out
		}
	}
	for _, reaction := range fallback {
		if !reaction.Valid() {
			continue
		}
		key := reaction.Key()
		if _, ok := seen[key]; ok {
			continue
		}
		out = append(out, reaction)
		seen[key] = struct{}{}
		if len(out) >= limit {
			return out
		}
	}
	return out
}

func messageReactionListHash(reactions []domain.MessageReaction) int64 {
	if len(reactions) == 0 {
		return 0
	}
	h := fnv.New64a()
	for _, reaction := range reactions {
		_, _ = h.Write([]byte(reaction.Type))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(reaction.Value()))
		_, _ = h.Write([]byte{0xff})
	}
	sum := int64(h.Sum64() & 0x7fffffffffffffff)
	if sum == 0 {
		return 1
	}
	return sum
}

func savedReactionTagListHash(tags []domain.SavedReactionTag) int64 {
	if len(tags) == 0 {
		return 0
	}
	h := fnv.New64a()
	for _, tag := range tags {
		_, _ = h.Write([]byte(tag.Reaction.Type))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(tag.Reaction.Value()))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(tag.Title))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(strconv.Itoa(tag.Count)))
		_, _ = h.Write([]byte{0xff})
	}
	sum := int64(h.Sum64() & 0x7fffffffffffffff)
	if sum == 0 {
		return 1
	}
	return sum
}
