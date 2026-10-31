package files

import (
	"context"
	"fmt"
	"strings"

	"telesrv/internal/domain"
)

// Fixed ID/AccessHash for the synthesized set: the real set IDs exported by the seed are on the
// order of 1.2e15, so a clearly isolated constant range avoids key collisions; idempotency is
// guaranteed by the system_key lookup, and the constants only need to be stable.
const (
	defaultEmojiStatusSetID         int64 = 7_777_000_000_000_001
	defaultEmojiStatusSetAccessHash int64 = 7_777_000_000_000_002
)

// defaultEmojiStatusEmoticons is the curated set of emoji for default statuses (aligned with the
// common entries of the official default status picker), ordered by display order. Emoticons
// that do not match are silently skipped (depending on the seed content).
var defaultEmojiStatusEmoticons = []string{
	"\U0001f4bc", // 💼 work
	"\U0001f393", // 🎓 study
	"\U0001f3e0", // 🏠 at home
	"\U0001f334", // 🌴 vacation
	"\U0001f3d6", // 🏖 beach
	"✈",          // ✈️ travel
	"\U0001f912", // 🤒 sick
	"\U0001f634", // 😴 sleeping
	"☕",          // ☕ coffee
	"\U0001f4bb", // 💻 coding / office
	"\U0001f4da", // 📚 reading
	"\U0001f3ae", // 🎮 gaming
	"\U0001f3a7", // 🎧 listening to music
	"⚽",          // ⚽ sports
	"\U0001f3c6", // 🏆 winning
	"❤",          // ❤️ love
	"\U0001f60e", // 😎 cool
	"\U0001f319", // 🌙 do not disturb
	"⭐",          // ⭐ star
	"\U0001f525", // 🔥 fire
	"\U0001f44d", // 👍 thumbs up
	"\U0001f389", // 🎉 celebrate
	"\U0001f914", // 🤔 thinking
	"\U0001f607", // 😇 angel
	"\U0001f973", // 🥳 party
	"\U0001f602", // 😂 laughing
	"\U0001f970", // 🥰 affection
	"\U0001f62d", // 😭 crying
	"\U0001f92f", // 🤯 mind blown
	"\U0001f440", // 👀 watching
	"\U0001f4af", // 💯 perfect score
	"\U0001f64f", // 🙏 thanks
	"\U0001f91d", // 🤝 collaboration
	"✍",          // ✍️ writing
	"\U0001f697", // 🚗 commuting
	"\U0001f355", // 🍕 eating
	"\U0001f382", // 🎂 birthday
	"\U0001f338", // 🌸 spring
	"⛄",          // ⛄ winter
	"\U0001f984", // 🦄 unicorn
}

// EnsureDefaultEmojiStatusSet idempotently synthesizes the default emoji status system set:
// curates documents from the seeded animated_emoji system set by emoticon (reusing document
// rows and blobs, not copying bytes). Returns (number of documents in the set, whether it was
// created this time). If animated_emoji is not seeded it is silently skipped.
func (s *Service) EnsureDefaultEmojiStatusSet(ctx context.Context) (int, bool, error) {
	if existing, found, err := s.media.GetStickerSetBySystemKey(ctx, domain.StickerSetSystemKeyEmojiDefaultStatuses); err != nil {
		return 0, false, fmt.Errorf("lookup default emoji status set: %w", err)
	} else if found {
		return len(existing.DocumentIDs), false, nil
	}
	source, found, err := s.media.GetStickerSetBySystemKey(ctx, "animated_emoji")
	if err != nil {
		return 0, false, fmt.Errorf("lookup animated_emoji set: %w", err)
	}
	if !found || len(source.Packs) == 0 {
		return 0, false, nil
	}
	byEmoticon := make(map[string][]int64, len(source.Packs))
	for _, pack := range source.Packs {
		key := normalizeStatusEmoticon(pack.Emoticon)
		if key == "" || len(pack.DocumentIDs) == 0 {
			continue
		}
		byEmoticon[key] = append(byEmoticon[key], pack.DocumentIDs...)
	}
	set := domain.StickerSet{
		ID:         defaultEmojiStatusSetID,
		AccessHash: defaultEmojiStatusSetAccessHash,
		ShortName:  "TelesrvDefaultStatuses",
		Title:      "Default Emoji Statuses",
		Kind:       domain.StickerSetKindSystem,
		SystemKey:  domain.StickerSetSystemKeyEmojiDefaultStatuses,
		Official:   true,
		Animated:   source.Animated,
		Emojis:     true,
	}
	seen := make(map[int64]struct{})
	for _, emoticon := range defaultEmojiStatusEmoticons {
		ids := byEmoticon[normalizeStatusEmoticon(emoticon)]
		if len(ids) == 0 {
			continue
		}
		pack := domain.StickerPack{Emoticon: emoticon}
		for _, id := range ids {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			set.DocumentIDs = append(set.DocumentIDs, id)
			pack.DocumentIDs = append(pack.DocumentIDs, id)
		}
		if len(pack.DocumentIDs) > 0 {
			set.Packs = append(set.Packs, pack)
		}
	}
	if len(set.DocumentIDs) == 0 {
		return 0, false, nil
	}
	set.Count = len(set.DocumentIDs)
	set.Hash = stickerSetDocsHash(set.DocumentIDs)
	docs, err := s.media.GetDocuments(ctx, set.DocumentIDs)
	if err != nil {
		return 0, false, fmt.Errorf("load default emoji status documents: %w", err)
	}
	if err := s.media.PutStickerSet(ctx, set); err != nil {
		return 0, false, fmt.Errorf("persist default emoji status set: %w", err)
	}
	s.stickerSetCache.put(set, orderDocuments(docs, set.DocumentIDs))
	return set.Count, true, nil
}

// normalizeStatusEmoticon unifies variation-selector differences ("❤" vs "❤️"), matching the
// spelling forms on both sides of the seed packs and the curated list.
func normalizeStatusEmoticon(e string) string {
	return strings.ReplaceAll(strings.TrimSpace(e), "️", "")
}

// stickerSetDocsHash computes a stable set hash from the document ID list (messages.getStickerSet
// and account.getDefaultEmojiStatuses share one cache decision).
func stickerSetDocsHash(ids []int64) int {
	var h uint64
	for _, id := range ids {
		h ^= uint64(id)
		h = h*0x4f25 + uint64(id)
	}
	return int(h & 0x7fffffff)
}
