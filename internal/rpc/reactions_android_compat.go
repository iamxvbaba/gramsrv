package rpc

import (
	"context"
	"strings"
	"sync"

	"github.com/gotd/td/tg"

	"telesrv/internal/domain"
)

// availableReactionDocumentMapCache caches the global reaction catalog mapping.
// Returned maps are shared and must be treated as read-only.
type availableReactionDocumentMapCache struct {
	mu                sync.RWMutex
	loaded            bool
	emojiToDocumentID map[string]int64
	documentIDToEmoji map[int64]string
}

func channelMemberCanChangeInfo(member domain.ChannelMember) bool {
	return member.Role == domain.ChannelRoleCreator ||
		(member.Role == domain.ChannelRoleAdmin && member.AdminRights.ChangeInfo)
}

func (r *Router) applyAndroidChannelReactionEditorCompat(ctx context.Context, full *tg.ChannelFull, canChangeInfo bool) {
	if full == nil || !canChangeInfo || ClientTypeFrom(ctx) != ClientTypeAndroid {
		return
	}
	raw, ok := full.GetAvailableReactions()
	if !ok {
		return
	}
	some, ok := raw.(*tg.ChatReactionsSome)
	if !ok || len(some.Reactions) == 0 {
		return
	}
	emojiToDocumentID, _ := r.availableReactionDocumentMaps(ctx)
	if len(emojiToDocumentID) == 0 {
		return
	}
	out := &tg.ChatReactionsSome{Reactions: make([]tg.ReactionClass, 0, len(some.Reactions))}
	changed := false
	for _, reaction := range some.Reactions {
		if emoji, ok := reaction.(*tg.ReactionEmoji); ok && emoji != nil {
			if documentID := emojiToDocumentID[strings.TrimSpace(emoji.Emoticon)]; documentID > 0 {
				out.Reactions = append(out.Reactions, &tg.ReactionCustomEmoji{DocumentID: documentID})
				changed = true
				continue
			}
		}
		out.Reactions = append(out.Reactions, reaction)
	}
	if changed {
		full.SetAvailableReactions(out)
	}
}

func (r *Router) normalizeDefaultReactionDocuments(ctx context.Context, reactions []domain.MessageReaction) []domain.MessageReaction {
	needsCatalog := false
	for _, reaction := range reactions {
		if reaction.Type == domain.MessageReactionCustomEmoji && reaction.DocumentID > 0 {
			needsCatalog = true
			break
		}
	}
	if !needsCatalog {
		return reactions
	}
	_, documentIDToEmoji := r.availableReactionDocumentMaps(ctx)
	if len(documentIDToEmoji) == 0 {
		return reactions
	}
	out := make([]domain.MessageReaction, 0, len(reactions))
	seen := make(map[string]struct{}, len(reactions))
	for _, reaction := range reactions {
		normalized := normalizeDefaultReactionDocument(reaction, documentIDToEmoji)
		key := normalized.Key()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func normalizeDefaultReactionDocument(reaction domain.MessageReaction, documentIDToEmoji map[int64]string) domain.MessageReaction {
	if reaction.Type != domain.MessageReactionCustomEmoji || reaction.DocumentID <= 0 {
		return reaction
	}
	if emoticon := documentIDToEmoji[reaction.DocumentID]; emoticon != "" {
		return domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: emoticon}
	}
	return reaction
}

func (r *Router) availableReactionDocumentMaps(ctx context.Context) (map[string]int64, map[int64]string) {
	if r == nil || r.deps.Files == nil {
		return nil, nil
	}
	return r.availableReactionDocuments.get(ctx, r.deps.Files)
}

func (c *availableReactionDocumentMapCache) get(ctx context.Context, files FilesService) (map[string]int64, map[int64]string) {
	if files == nil {
		return nil, nil
	}
	c.mu.RLock()
	if c.loaded {
		emojiToDocumentID, documentIDToEmoji := c.emojiToDocumentID, c.documentIDToEmoji
		c.mu.RUnlock()
		return emojiToDocumentID, documentIDToEmoji
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded {
		return c.emojiToDocumentID, c.documentIDToEmoji
	}
	catalog, err := files.ListAvailableReactions(ctx)
	if err != nil {
		return nil, nil
	}
	c.emojiToDocumentID, c.documentIDToEmoji = buildAvailableReactionDocumentMaps(catalog)
	c.loaded = true
	return c.emojiToDocumentID, c.documentIDToEmoji
}

func (c *availableReactionDocumentMapCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loaded = false
	c.emojiToDocumentID = nil
	c.documentIDToEmoji = nil
}

func buildAvailableReactionDocumentMaps(catalog []domain.AvailableReaction) (map[string]int64, map[int64]string) {
	emojiToDocumentID := make(map[string]int64, len(catalog))
	documentIDToEmoji := make(map[int64]string, len(catalog))
	for _, item := range catalog {
		emoticon := strings.TrimSpace(item.Reaction)
		if item.Inactive || emoticon == "" || item.ActivateAnimationID <= 0 {
			continue
		}
		if _, exists := emojiToDocumentID[emoticon]; !exists {
			emojiToDocumentID[emoticon] = item.ActivateAnimationID
		}
		if _, exists := documentIDToEmoji[item.ActivateAnimationID]; !exists {
			documentIDToEmoji[item.ActivateAnimationID] = emoticon
		}
	}
	return emojiToDocumentID, documentIDToEmoji
}
