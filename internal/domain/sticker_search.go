package domain

import (
	"sort"
	"strings"
)

// SearchStickerSets returns a stable public catalog projection. It never
// mutates the shared catalog or includes archived, deleted, or system sets.
func SearchStickerSets(sets []StickerSet, kind StickerSetKind, query string) []StickerSet {
	tokens := strings.Fields(stickerSearchText(query))
	result := make([]StickerSet, 0)
	for _, set := range sets {
		if set.ID == 0 || set.Deleted || set.Archived || set.Kind != kind {
			continue
		}
		if stickerSearchMatches(stickerSearchText(set.Title+" "+set.ShortName), tokens) {
			result = append(result, set)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].SortOrder != result[j].SortOrder {
			return result[i].SortOrder < result[j].SortOrder
		}
		return result[i].ID < result[j].ID
	})
	return result
}

// SearchStickerDocuments searches metadata before materializing only the
// requested page of document payloads. Keywords remain specific to each item.
func SearchStickerDocuments(sets []StickerSet, kind StickerSetKind, query, emojis string, offset, limit int) []int64 {
	result := make([]int64, 0, limit)
	if limit == 0 {
		return result
	}
	tokens := strings.Fields(stickerSearchText(query))
	emojiTokens := strings.Fields(stickerSearchText(emojis))
	seen := make(map[int64]bool)
	matched := 0
	for _, set := range SearchStickerSets(sets, kind, "") {
		words := make(map[int64]string)
		emojiByID := make(map[int64]string)
		for _, pack := range set.Packs {
			for _, id := range pack.DocumentIDs {
				emojiByID[id] += " " + stickerSearchText(pack.Emoticon)
			}
		}
		for _, entry := range set.Keywords {
			words[entry.DocumentID] += " " + stickerSearchText(strings.Join(entry.Keywords, " "))
		}
		title := stickerSearchText(set.Title + " " + set.ShortName)
		for _, id := range set.DocumentIDs {
			if id == 0 || seen[id] {
				continue
			}
			text := title + " " + words[id] + " " + emojiByID[id]
			if !stickerSearchMatches(text, tokens) {
				continue
			}
			emojiMatches := len(emojiTokens) == 0
			for _, emoji := range emojiTokens {
				for _, candidate := range strings.Fields(emojiByID[id]) {
					if candidate == emoji {
						emojiMatches = true
					}
				}
			}
			if !emojiMatches {
				continue
			}
			seen[id] = true
			if matched >= offset {
				result = append(result, id)
			}
			matched++
			if len(result) == limit {
				return result
			}
		}
	}
	return result
}

func stickerSearchText(value string) string {
	return strings.ToLower(strings.NewReplacer("\ufe0f", "", "\ufe0e", "").Replace(value))
}

func stickerSearchMatches(text string, tokens []string) bool {
	for _, token := range tokens {
		if !strings.Contains(text, token) {
			return false
		}
	}
	return true
}
