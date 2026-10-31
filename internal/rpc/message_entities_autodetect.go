package rpc

import (
	"strings"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
	"telesrv/internal/links"
)

// Server-side automatic entity detection: @mention / #hashtag / $cashtag / bot command / bank card number.
//
// The official Telegram server detects these "automatically recognizable" entities in the raw
// message text and writes them into message.entities; when a client sends sendMessage it only
// sends "user intent" entities (bold/italic/code/textUrl/customEmoji/
// inputMessageEntityMentionName, etc.) plus the url detected locally by the client. For a
// normally sent message DrKLO uses useManualParse=false, does not locally Linkify @username, and
// relies entirely on message.entities to render the @mention highlight -- so if the server does
// not add messageEntityMention, @username is not rendered as clickable blue. For url detection see
// detectURLEntities(webpage_url_extract.go).
//
// All offsets/lengths are counted in UTF-16 code units (the Telegram entity convention), reusing
// utf16CodeUnitLen; the @/#/$/ trigger character itself counts toward the entity length. Detection
// is purely by text regex and does not consult the database to check whether a username/command
// actually exists (the official server does the same: messageEntityMention carries no user_id, and
// the client resolves the username when clicked).

// augmentAutoEntities adds the server-detected automatic entities on top of the entities the
// client already sent. An addition is dropped when its interval overlaps any existing entity
// (a client rich-text intent entity or an auto entity added earlier), to avoid stamping
// mention/hashtag inside code/pre/textUrl/an existing mentionName or overlapping each other
// (matching the official server's behavior of not duplicating entities). URLs are filled in span
// by span; carrying some HTTP URL from the client must not cause us to skip an app-link in the
// same message that the client does not recognize. Client entities stay in front (preferentially
// kept when trimming past the limit), and the result is trimmed to the entity limit.
func augmentAutoEntities(message string, entities []tg.MessageEntityClass, appLinks links.AppLinkBuilder) []tg.MessageEntityClass {
	// Fast path: the vast majority of messages contain no automatically recognizable trigger
	// character. A single ContainsAny scan short-circuits and returns, skipping the full-text
	// scans and interval allocation of each detector below (plain-text sending has zero extra
	// cost). A bare-domain URL only needs a single '.' to trigger (e.g. github.com); once inside
	// detection it still performs TLD/boundary validation; a bank card number only triggers at 13+
	// digits (TDLib does not recognize card numbers in received messages; the official server sends
	// messageEntityBankCard); email/phone are unimplemented and therefore not in the trigger set.
	if message == "" || (!strings.ContainsAny(message, "@#$/.") && !domain.MayContainBankCardNumber(message)) {
		return entities
	}
	type interval struct{ start, end int }
	occupied := make([]interval, 0, len(entities)+8)
	for _, e := range entities {
		if ln := e.GetLength(); ln > 0 {
			off := e.GetOffset()
			occupied = append(occupied, interval{off, off + ln})
		}
	}
	overlaps := func(s, e int) bool {
		for _, iv := range occupied {
			if s < iv.end && iv.start < e {
				return true
			}
		}
		return false
	}

	// extra is allocated lazily: the slice is only created and entities copied when an entity is
	// actually added. Cases like "email a@b.com" that have a trigger character but yield no entity
	// (@ preceded by a word character -> not a mention) return the original entities with zero copies.
	var extra []tg.MessageEntityClass
	accept := func(c tg.MessageEntityClass) {
		if len(entities)+len(extra) >= maxMessageEntityCount {
			return
		}
		ln := c.GetLength()
		if ln <= 0 {
			return
		}
		off := c.GetOffset()
		if overlaps(off, off+ln) {
			return
		}
		extra = append(extra, c)
		occupied = append(occupied, interval{off, off + ln})
	}

	// URL spans are always computed and added to the exclusion region (occupied), so @mention/
	// #hashtag etc. do not land inside a URL path (such as the @scam in https://t.me/@scam, which
	// is both inconsistent with the official semantics and a phishing risk). Filling in span by
	// span covers mixed messages where "the client carries an HTTP entity but does not recognize
	// telesrv://", while still avoiding duplicate entities.
	for _, u := range detectURLEntities(message, appLinks) {
		ln := u.GetLength()
		if ln <= 0 {
			continue
		}
		off := u.GetOffset()
		if overlaps(off, off+ln) {
			continue
		}
		occupied = append(occupied, interval{off, off + ln})
		if len(entities)+len(extra) < maxMessageEntityCount {
			extra = append(extra, u)
		}
	}

	// The remaining automatic entities are produced by the protocol-neutral detector; the RPC
	// boundary only projects the domain entity into TL. This way server-generated system messages
	// can reuse the same UTF-16/URL exclusion rules, while tg types still do not cross into RPC.
	spans := make([]domain.MessageEntitySpan, 0, len(occupied))
	for _, interval := range occupied {
		spans = append(spans, domain.MessageEntitySpan{Offset: interval.start, Length: interval.end - interval.start})
	}
	for _, c := range tgMessageEntities(domain.DetectAutomaticMessageEntities(message, spans)) {
		accept(c)
	}

	if len(extra) == 0 {
		return entities
	}
	out := make([]tg.MessageEntityClass, 0, len(entities)+len(extra))
	out = append(out, entities...)
	return append(out, extra...)
}

func (r *Router) augmentAutoEntities(message string, entities []tg.MessageEntityClass) []tg.MessageEntityClass {
	return augmentAutoEntities(message, entities, r.appLinks)
}
