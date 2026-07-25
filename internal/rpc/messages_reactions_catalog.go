package rpc

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"sort"
	"strings"

	"github.com/iamxvbaba/td/tg"

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
	var savedPeer domain.Peer
	if peer, ok := req.GetPeer(); ok && peer != nil {
		savedPeer, err = r.checkedDomainPeerFromInputPeer(ctx, userID, peer)
		if err != nil {
			return nil, err
		}
	}
	if r.deps.Messages == nil {
		return savedReactionTagsEmpty(req.Hash), nil
	}
	tags, err := r.deps.Messages.SavedReactionTags(ctx, userID, savedPeer, domain.MaxSavedReactionTags)
	if err != nil {
		return nil, messageReactionErr(err)
	}
	return savedReactionTagsFromDomain(tags, req.Hash, savedPeer.ID == 0), nil
}

func (r *Router) onMessagesGetDefaultTagReactions(ctx context.Context, hash int64) (tg.MessagesReactionsClass, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	return messagesReactionsFromDomain(
		r.reactionsWithCatalogFallback(ctx, nil, domain.MaxChannelMessageReactionsPerUser),
		hash,
	), nil
}

func messagesReactionsEmpty(_ int64) tg.MessagesReactionsClass {
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

func savedReactionTagsFromDomain(tags []domain.SavedReactionTag, requestHash int64, includeTitles bool) tg.MessagesSavedReactionTagsClass {
	tags = append([]domain.SavedReactionTag(nil), tags...)
	sort.SliceStable(tags, func(i, j int) bool {
		if tags[i].Count != tags[j].Count {
			return tags[i].Count > tags[j].Count
		}
		return savedReactionTagLongID(tags[i].Reaction) > savedReactionTagLongID(tags[j].Reaction)
	})
	hash := savedReactionTagListHash(tags, includeTitles)
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
		if includeTitles && tag.Title != "" {
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
	var hash uint64
	for _, reaction := range reactions {
		hash = telegramListHashNext(hash, savedReactionTagLongID(reaction))
	}
	return int64(hash)
}

func savedReactionTagListHash(tags []domain.SavedReactionTag, includeTitles bool) int64 {
	if len(tags) == 0 {
		return 0
	}
	var hash uint64
	for _, tag := range tags {
		hash = telegramListHashNext(hash, savedReactionTagLongID(tag.Reaction))
		if includeTitles && tag.Title != "" {
			hash = telegramListHashNext(hash, md5LongID(tag.Title))
		}
		hash = telegramListHashNext(hash, uint64(tag.Count))
	}
	return int64(hash)
}

func savedReactionTagLongID(reaction domain.MessageReaction) uint64 {
	switch reaction.Type {
	case domain.MessageReactionEmoji:
		return md5LongID(strings.ReplaceAll(reaction.Emoticon, "\ufe0f", ""))
	case domain.MessageReactionCustomEmoji:
		return uint64(reaction.DocumentID)
	default:
		return 0
	}
}

func md5LongID(value string) uint64 {
	sum := md5.Sum([]byte(value))
	return binary.BigEndian.Uint64(sum[:8])
}

func telegramListHashNext(hash, id uint64) uint64 {
	hash ^= hash >> 21
	hash ^= hash << 35
	hash ^= hash >> 4
	return hash + id
}
