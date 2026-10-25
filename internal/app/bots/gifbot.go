package bots

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"telesrv/internal/domain"
)

func (s *Service) HandlesInlineBot(botUserID int64) bool {
	return s != nil && botUserID == domain.GifBotUserID
}

func (s *Service) OnInlineQuery(ctx context.Context, botUserID, _ int64, query, offset string) (domain.BotInlineResults, bool, error) {
	if !s.HandlesInlineBot(botUserID) {
		return domain.BotInlineResults{}, false, nil
	}
	if offset != "" {
		return domain.BotInlineResults{Gallery: true, CacheTime: 60}, true, nil
	}
	if s.gifCatalog == nil {
		return domain.BotInlineResults{}, true, domain.ErrGifCatalogUnavailable
	}
	entries, err := s.gifCatalog.ListGifCatalog(ctx, true)
	if err != nil {
		return domain.BotInlineResults{}, true, err
	}
	entries = rankGifCatalogEntries(entries, query)
	ids := make([]int64, len(entries))
	for i := range entries {
		ids[i] = entries[i].DocumentID
	}
	docs, err := s.gifCatalog.GetDocuments(ctx, ids)
	if err != nil {
		return domain.BotInlineResults{}, true, err
	}
	byID := make(map[int64]domain.Document, len(docs))
	for _, doc := range docs {
		byID[doc.ID] = doc
	}
	results := make([]domain.BotInlineResult, 0, len(entries))
	for _, entry := range entries {
		doc, ok := byID[entry.DocumentID]
		if !ok {
			return domain.BotInlineResults{}, true, fmt.Errorf("gif catalog entry %d references missing document %d", entry.ID, entry.DocumentID)
		}
		if !doc.IsGif() {
			return domain.BotInlineResults{}, true, fmt.Errorf("gif catalog document %d is not GIFv", doc.ID)
		}
		docCopy := doc
		results = append(results, domain.BotInlineResult{
			ID: strconv.FormatInt(entry.ID, 10), Type: "gif", Title: entry.Title,
			Media: &domain.MessageMedia{Kind: domain.MessageMediaKindDocument, Document: &docCopy},
		})
	}
	return domain.BotInlineResults{Gallery: true, CacheTime: 60, Results: results}, true, nil
}

func rankGifCatalogEntries(entries []domain.GifCatalogEntry, query string) []domain.GifCatalogEntry {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return entries
	}
	matched := make([]domain.GifCatalogEntry, 0, len(entries))
	categoryMatched := make([]domain.GifCatalogEntry, 0, len(entries))
	rest := make([]domain.GifCatalogEntry, 0, len(entries))
	for _, entry := range entries {
		category := entry.Category
		if category == "" {
			category = domain.ClassifyGifCategory(entry.Title, entry.FileName)
		}
		if strings.Contains(strings.ToLower(entry.Title), query) {
			matched = append(matched, entry)
		} else if domain.GifCategoryMatchesQuery(category, query) {
			categoryMatched = append(categoryMatched, entry)
		} else {
			rest = append(rest, entry)
		}
	}
	return append(append(matched, categoryMatched...), rest...)
}
