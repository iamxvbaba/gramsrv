package rpc

import (
	"telesrv/internal/domain"
)

// Automatic entity completion for Bot API outbound text.
//
// The official server runs lexical entity detection (@mention / #hashtag / $cashtag / bot
// command / URL) on every outbound message, regardless of whether the user or a bot sent it.
// The Bot API's parse_mode/entities only describe the bot's explicit rich-text intent and does
// not produce these entities; meanwhile clients (TDesktop's ItemTextBotDefaultOptions, TWeb's
// wrapRichText) only render the messageEntityBotCommand the server sends, so a missing one means
// no blue highlight -- hence /buy /start sent by something like grammystore via
// POST /bot<token>/sendMessage must be completed by the server, matching the effect of the
// built-in bot going through serviceBotReplyEntities.
//
// The only difference from the MTProto-side augmentAutoEntities is the entity container: the Bot
// API boundary already holds domain entities, so no detour through tg conversion is needed. The
// detection rules themselves reuse the same domain/rpc detectors, so the entities produced by
// both paths are identical.

// augmentBotAPIAutoEntities supplements the server-detected automatic entities on top of the
// entities the Bot API already parsed. The bot's explicit entities are kept first and ordered
// ahead; supplements are dropped when they intersect any existing range, to avoid pushing a
// mention/hashtag/command inside code/pre/text_url. URL spans also count as exclusion zones, so
// the @name in https://t.me/@name does not become a mention entity.
func (r *Router) augmentBotAPIAutoEntities(message string, entities []domain.MessageEntity) []domain.MessageEntity {
	if message == "" || len(entities) >= domain.MaxMessageEntityCount {
		return entities
	}
	type interval struct{ start, end int }
	occupied := make([]interval, 0, len(entities)+8)
	for _, entity := range entities {
		if entity.Length > 0 && entity.Offset >= 0 {
			occupied = append(occupied, interval{start: entity.Offset, end: entity.Offset + entity.Length})
		}
	}
	overlaps := func(start, end int) bool {
		for _, iv := range occupied {
			if start < iv.end && iv.start < end {
				return true
			}
		}
		return false
	}

	var extra []domain.MessageEntity
	accept := func(entity domain.MessageEntity) {
		if entity.Length <= 0 || len(entities)+len(extra) >= domain.MaxMessageEntityCount {
			return
		}
		if overlaps(entity.Offset, entity.Offset+entity.Length) {
			return
		}
		extra = append(extra, entity)
		occupied = append(occupied, interval{start: entity.Offset, end: entity.Offset + entity.Length})
	}

	// Add URLs first: this is the official order and also lets subsequent lexical detection avoid the inside of links.
	for _, u := range detectURLEntities(message, r.appLinks) {
		accept(domain.MessageEntity{Type: domain.MessageEntityURL, Offset: u.GetOffset(), Length: u.GetLength()})
	}

	spans := make([]domain.MessageEntitySpan, 0, len(occupied))
	for _, iv := range occupied {
		spans = append(spans, domain.MessageEntitySpan{Offset: iv.start, Length: iv.end - iv.start})
	}
	for _, entity := range domain.DetectAutomaticMessageEntities(message, spans) {
		accept(entity)
	}

	if len(extra) == 0 {
		return entities
	}
	out := make([]domain.MessageEntity, 0, len(entities)+len(extra))
	out = append(out, entities...)
	return append(out, extra...)
}
