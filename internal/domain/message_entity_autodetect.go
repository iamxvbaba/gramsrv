package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// MessageEntitySpan describes an already occupied UTF-16 range while deriving
// automatic entities. It deliberately carries no TL or entity-type semantics.
type MessageEntitySpan struct {
	Offset int
	Length int
}

// DetectAutomaticMessageEntities derives the server-recognized lexical
// entities that do not require user intent: mentions, hashtags, cashtags and
// bot commands. Offsets and lengths use Telegram's UTF-16 code-unit indexing.
//
// occupied ranges win over derived entities. URL-like tokens are also treated
// as occupied for lexical triggers, so strings such as https://sgq.me/@name do
// not become misleading mention entities even when URL projection is handled
// by a different boundary.
func DetectAutomaticMessageEntities(message string, occupied []MessageEntitySpan) []MessageEntity {
	if message == "" || !strings.ContainsAny(message, "@#$/") {
		return nil
	}
	type interval struct{ start, end int }
	blocked := make([]interval, 0, len(occupied)+8)
	for _, span := range occupied {
		if span.Offset >= 0 && span.Length > 0 {
			blocked = append(blocked, interval{start: span.Offset, end: span.Offset + span.Length})
		}
	}
	overlaps := func(start, end int) bool {
		for _, span := range blocked {
			if start < span.end && span.start < end {
				return true
			}
		}
		return false
	}
	var out []MessageEntity
	accept := func(entity MessageEntity) {
		if entity.Length <= 0 || len(out) >= MaxMessageEntityCount {
			return
		}
		end := entity.Offset + entity.Length
		if overlaps(entity.Offset, end) {
			return
		}
		out = append(out, entity)
		blocked = append(blocked, interval{start: entity.Offset, end: end})
	}

	for _, entity := range detectMentionMessageEntities(message) {
		accept(entity)
	}
	for _, entity := range detectHashtagMessageEntities(message) {
		accept(entity)
	}
	for _, entity := range detectCashtagMessageEntities(message) {
		accept(entity)
	}
	for _, entity := range detectBotCommandMessageEntities(message) {
		accept(entity)
	}
	return out
}

func detectMentionMessageEntities(message string) []MessageEntity {
	var out []MessageEntity
	for i := 0; i < len(message); i++ {
		if message[i] != '@' || automaticEntityInsideURLLikeToken(message, i) {
			continue
		}
		if r, ok := previousRune(message, i); ok && (automaticEntityWordRune(r) || r == '@') {
			continue
		}
		end := i + 1
		for end < len(message) && automaticEntityUsernameByte(message[end]) {
			end++
		}
		if length := end - i - 1; length < 1 || length > 32 {
			continue
		}
		out = append(out, MessageEntity{
			Type:   MessageEntityMention,
			Offset: automaticEntityUTF16Length(message[:i]),
			Length: automaticEntityUTF16Length(message[i:end]),
		})
		i = end - 1
	}
	return out
}

func detectBotCommandMessageEntities(message string) []MessageEntity {
	var out []MessageEntity
	for i := 0; i < len(message); i++ {
		if message[i] != '/' || automaticEntityInsideURLLikeToken(message, i) {
			continue
		}
		if r, ok := previousRune(message, i); ok && (automaticEntityWordRune(r) || r == '/' || r == '@' || r == '<') {
			continue
		}
		end := i + 1
		for end < len(message) && automaticEntityUsernameByte(message[end]) {
			end++
		}
		if length := end - i - 1; length < 1 || length > 64 {
			continue
		}
		if end < len(message) && message[end] == '@' {
			botEnd := end + 1
			for botEnd < len(message) && automaticEntityUsernameByte(message[botEnd]) {
				botEnd++
			}
			if length := botEnd - end - 1; length >= 1 && length <= 32 {
				end = botEnd
			}
		}
		out = append(out, MessageEntity{
			Type:   MessageEntityBotCommand,
			Offset: automaticEntityUTF16Length(message[:i]),
			Length: automaticEntityUTF16Length(message[i:end]),
		})
		i = end - 1
	}
	return out
}

func detectHashtagMessageEntities(message string) []MessageEntity {
	var out []MessageEntity
	for i := 0; i < len(message); i++ {
		if message[i] != '#' || automaticEntityInsideURLLikeToken(message, i) {
			continue
		}
		if r, ok := previousRune(message, i); ok && (automaticEntityWordRune(r) || r == '#' || r == '@') {
			continue
		}
		end := i + 1
		var first rune
		count := 0
		for end < len(message) {
			r, size := utf8.DecodeRuneInString(message[end:])
			if size <= 0 || !automaticEntityHashtagRune(r) {
				break
			}
			if count == 0 {
				first = r
			}
			count++
			end += size
		}
		if count >= 1 && count <= 256 && !unicode.IsDigit(first) {
			out = append(out, MessageEntity{
				Type:   MessageEntityHashtag,
				Offset: automaticEntityUTF16Length(message[:i]),
				Length: automaticEntityUTF16Length(message[i:end]),
			})
			i = end - 1
		}
	}
	return out
}

func detectCashtagMessageEntities(message string) []MessageEntity {
	var out []MessageEntity
	for i := 0; i < len(message); i++ {
		if message[i] != '$' || automaticEntityInsideURLLikeToken(message, i) {
			continue
		}
		if r, ok := previousRune(message, i); ok && (automaticEntityWordRune(r) || r == '$') {
			continue
		}
		end := i + 1
		for end < len(message) && message[end] >= 'A' && message[end] <= 'Z' {
			end++
		}
		if length := end - i - 1; length < 1 || length > 8 {
			continue
		}
		if r, size := utf8.DecodeRuneInString(message[end:]); size > 0 && automaticEntityWordRune(r) {
			continue
		}
		out = append(out, MessageEntity{
			Type:   MessageEntityCashtag,
			Offset: automaticEntityUTF16Length(message[:i]),
			Length: automaticEntityUTF16Length(message[i:end]),
		})
		i = end - 1
	}
	return out
}

func automaticEntityInsideURLLikeToken(message string, byteIndex int) bool {
	start := byteIndex
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(message[:start])
		if size <= 0 || automaticEntityURLBoundary(r) {
			break
		}
		start -= size
	}
	prefix := strings.TrimLeft(message[start:byteIndex], "([{（【")
	if strings.Contains(prefix, "://") {
		return true
	}
	separator := strings.IndexAny(prefix, "/?#")
	if separator <= 0 {
		return false
	}
	host := prefix[:separator]
	return strings.Contains(host, ".") && !strings.Contains(host, "@")
}

func automaticEntityURLBoundary(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune("<>\"'）】", r)
}

func previousRune(message string, byteIndex int) (rune, bool) {
	if byteIndex <= 0 || byteIndex > len(message) {
		return 0, false
	}
	r, size := utf8.DecodeLastRuneInString(message[:byteIndex])
	return r, size > 0
}

func automaticEntityWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func automaticEntityHashtagRune(r rune) bool {
	return automaticEntityWordRune(r)
}

func automaticEntityUsernameByte(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

func automaticEntityUTF16Length(text string) int {
	length := 0
	for _, r := range text {
		length++
		if r > 0xffff {
			length++
		}
	}
	return length
}
