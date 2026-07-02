package bots

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"telesrv/internal/domain"
)

type serviceBotEntitySpan struct {
	start int
	end   int
}

func serviceBotReplyEntities(text string, explicit []domain.MessageEntity) []domain.MessageEntity {
	if text == "" && len(explicit) == 0 {
		return nil
	}
	out := append([]domain.MessageEntity(nil), explicit...)
	occupied := make([]serviceBotEntitySpan, 0, len(out)+8)
	for _, entity := range out {
		if entity.Length <= 0 {
			continue
		}
		occupied = append(occupied, serviceBotEntitySpan{start: entity.Offset, end: entity.Offset + entity.Length})
	}
	appendEntity := func(entity domain.MessageEntity) {
		if entity.Length <= 0 || len(out) >= domain.MaxMessageEntityCount {
			return
		}
		span := serviceBotEntitySpan{start: entity.Offset, end: entity.Offset + entity.Length}
		if serviceBotSpanOverlaps(span, occupied) {
			return
		}
		out = append(out, entity)
		occupied = append(occupied, span)
	}
	for _, span := range serviceBotURLByteSpans(text) {
		offset, length := utf16Range(text, span.start, span.end)
		appendEntity(domain.MessageEntity{Type: domain.MessageEntityURL, Offset: offset, Length: length})
	}
	for _, span := range serviceBotCommandByteSpans(text) {
		offset, length := utf16Range(text, span.start, span.end)
		appendEntity(domain.MessageEntity{Type: domain.MessageEntityBotCommand, Offset: offset, Length: length})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Offset != out[j].Offset {
			return out[i].Offset < out[j].Offset
		}
		return out[i].Length < out[j].Length
	})
	return out
}

func serviceBotURLByteSpans(text string) []serviceBotEntitySpan {
	var spans []serviceBotEntitySpan
	for i := 0; i < len(text); {
		if !strings.HasPrefix(text[i:], "https://") && !strings.HasPrefix(text[i:], "http://") {
			_, size := utf8.DecodeRuneInString(text[i:])
			i += size
			continue
		}
		start := i
		i += len("http://")
		if strings.HasPrefix(text[start:], "https://") {
			i = start + len("https://")
		}
		for i < len(text) {
			r, size := utf8.DecodeRuneInString(text[i:])
			if unicode.IsSpace(r) || r == '<' || r == '>' {
				break
			}
			i += size
		}
		end := trimServiceBotURLTrailingPunctuation(text, start, i)
		if end > start {
			spans = append(spans, serviceBotEntitySpan{start: start, end: end})
		}
		if i == start {
			i++
		}
	}
	return spans
}

func trimServiceBotURLTrailingPunctuation(text string, start, end int) int {
	for end > start {
		r, size := utf8.DecodeLastRuneInString(text[start:end])
		if !strings.ContainsRune(".,;:!?)]}", r) {
			break
		}
		end -= size
	}
	return end
}

func serviceBotCommandByteSpans(text string) []serviceBotEntitySpan {
	var spans []serviceBotEntitySpan
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if r != '/' || !serviceBotCommandStart(text, i) {
			i += size
			continue
		}
		start := i
		i += size
		commandStartEnd := i
		for i < len(text) {
			r, size = utf8.DecodeRuneInString(text[i:])
			if !serviceBotCommandChar(r) {
				break
			}
			i += size
		}
		if i == commandStartEnd {
			continue
		}
		if i < len(text) {
			r, size = utf8.DecodeRuneInString(text[i:])
			if r == '@' {
				mentionEnd := i + size
				for mentionEnd < len(text) {
					r, size = utf8.DecodeRuneInString(text[mentionEnd:])
					if !serviceBotCommandChar(r) {
						break
					}
					mentionEnd += size
				}
				if mentionEnd > i+size {
					i = mentionEnd
				}
			}
		}
		spans = append(spans, serviceBotEntitySpan{start: start, end: i})
	}
	return spans
}

func serviceBotCommandStart(text string, byteIndex int) bool {
	if byteIndex == 0 {
		return true
	}
	prev, _ := utf8.DecodeLastRuneInString(text[:byteIndex])
	if prev == ':' || prev == '/' || prev == '@' {
		return false
	}
	return !serviceBotCommandChar(prev)
}

func serviceBotCommandChar(r rune) bool {
	return r == '_' || ('0' <= r && r <= '9') || ('A' <= r && r <= 'Z') || ('a' <= r && r <= 'z')
}

func utf16Range(text string, startByte, endByte int) (int, int) {
	offset := 0
	for _, r := range text[:startByte] {
		offset += utf16RuneLen(r)
	}
	length := 0
	for _, r := range text[startByte:endByte] {
		length += utf16RuneLen(r)
	}
	return offset, length
}

func utf16RuneLen(r rune) int {
	if r > 0xFFFF {
		return 2
	}
	return 1
}

func serviceBotSpanOverlaps(span serviceBotEntitySpan, occupied []serviceBotEntitySpan) bool {
	for _, other := range occupied {
		if span.start < other.end && other.start < span.end {
			return true
		}
	}
	return false
}
