package botapi

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"telesrv/internal/domain"
)

// The official Bot API rejects the raw formatted-text input above 32 KiB before
// parsing; the post-parse message/caption character limit is checked separately.
const maxBotAPIFormattedTextBytes = 1 << 15

// botAPIFormattedTextRaw applies the Bot API parse_mode/entities precedence and
// returns the plain text plus UTF-16 based entities that can cross the domain
// boundary. A non-empty parse_mode (except "none") deliberately wins over an
// entities payload, matching the official Bot API server.
func botAPIFormattedTextRaw(text, parseMode, rawEntities string, maxLength int, requireText bool) (string, []domain.MessageEntity, error) {
	mode, enabled, err := botAPIParseMode(parseMode)
	if err != nil {
		return "", nil, err
	}
	if enabled && text != "" {
		return botAPIFormattedText(text, mode, nil, maxLength, requireText)
	}
	entities, err := botAPIMessageEntities(rawEntities)
	if err != nil {
		return "", nil, err
	}
	return validateBotAPIFormattedText(text, entities, maxLength, requireText)
}

func botAPIFormattedText(text, parseMode string, inputEntities []apiMessageEntity, maxLength int, requireText bool) (string, []domain.MessageEntity, error) {
	mode, enabled, err := botAPIParseMode(parseMode)
	if err != nil {
		return "", nil, err
	}
	if !utf8.ValidString(text) {
		return "", nil, errors.New("ENTITY_INVALID")
	}
	if len(text) > maxBotAPIFormattedTextBytes {
		return "", nil, errors.New("MESSAGE_TOO_LONG")
	}
	if enabled {
		var parsed string
		var entities []domain.MessageEntity
		switch mode {
		case "html":
			parsed, entities, err = parseBotAPIHTML(text)
		case "markdown":
			parsed, entities, err = parseBotAPIMarkdown(text)
		case "markdownv2":
			parsed, entities, err = parseBotAPIMarkdownV2(text)
		default:
			panic("normalized Bot API parse mode is not handled")
		}
		if err != nil {
			return "", nil, err
		}
		return validateBotAPIFormattedText(parsed, entities, maxLength, requireText)
	}
	entities, err := messageEntitiesFromAPI(inputEntities)
	if err != nil {
		return "", nil, err
	}
	return validateBotAPIFormattedText(text, entities, maxLength, requireText)
}

func botAPIParseMode(raw string) (mode string, enabled bool, err error) {
	mode = strings.ToLower(strings.TrimSpace(raw))
	switch mode {
	case "", "none", "null":
		return "", false, nil
	case "html", "markdown", "markdownv2":
		return mode, true, nil
	default:
		return "", false, errors.New("Unsupported parse_mode")
	}
}

func validateBotAPIFormattedText(text string, entities []domain.MessageEntity, maxLength int, requireText bool) (string, []domain.MessageEntity, error) {
	if !utf8.ValidString(text) {
		return "", nil, errors.New("ENTITY_INVALID")
	}
	if requireText && text == "" {
		return "", nil, errors.New("MESSAGE_EMPTY")
	}
	if maxLength > 0 && utf8.RuneCountInString(text) > maxLength {
		return "", nil, errors.New("MESSAGE_TOO_LONG")
	}
	if len(entities) > domain.MaxMessageEntityCount {
		return "", nil, errors.New("ENTITIES_TOO_LONG")
	}
	textLength := utf16StringLength(text)
	boundaries := make(map[int]struct{}, utf8.RuneCountInString(text)+1)
	boundaries[0] = struct{}{}
	position := 0
	for _, r := range text {
		position++
		if r > 0xffff {
			position++
		}
		boundaries[position] = struct{}{}
	}
	for _, entity := range entities {
		if entity.Type == "" || entity.Offset < 0 || entity.Length <= 0 || entity.Offset > textLength || entity.Length > textLength-entity.Offset {
			return "", nil, errors.New("ENTITY_BOUNDS_INVALID")
		}
		if _, ok := boundaries[entity.Offset]; !ok {
			return "", nil, errors.New("ENTITY_BOUNDS_INVALID")
		}
		if _, ok := boundaries[entity.Offset+entity.Length]; !ok {
			return "", nil, errors.New("ENTITY_BOUNDS_INVALID")
		}
	}
	sortBotAPIEntities(entities)
	ends := make([]int, 0, len(entities))
	for _, entity := range entities {
		for len(ends) > 0 && entity.Offset >= ends[len(ends)-1] {
			ends = ends[:len(ends)-1]
		}
		end := entity.Offset + entity.Length
		if len(ends) > 0 && end > ends[len(ends)-1] {
			return "", nil, errors.New("ENTITY_BOUNDS_INVALID")
		}
		ends = append(ends, end)
	}
	return text, entities, nil
}

func utf16StringLength(text string) int {
	length := 0
	for _, r := range text {
		length++
		if r > 0xffff {
			length++
		}
	}
	return length
}

type formattedTextBuilder struct {
	text  bytes.Buffer
	utf16 int
}

func (b *formattedTextBuilder) appendString(value string) {
	b.text.WriteString(value)
	b.utf16 += utf16StringLength(value)
}

func (b *formattedTextBuilder) appendRune(r rune) {
	b.text.WriteRune(r)
	b.utf16++
	if r > 0xffff {
		b.utf16++
	}
}

func (b *formattedTextBuilder) string() string { return b.text.String() }
func (b *formattedTextBuilder) byteLen() int   { return b.text.Len() }

func parseEntityError(format string, args ...any) error {
	return fmt.Errorf("Can't parse entities: "+format, args...)
}

type htmlEntityFrame struct {
	tag        string
	typ        domain.MessageEntityType
	offset     int
	outputByte int
	argument   string
	language   string
	documentID int64
	date       int
	collapsed  bool
	relative   bool
	shortTime  bool
	longTime   bool
	shortDate  bool
	longDate   bool
	dayOfWeek  bool
}

func parseBotAPIHTML(input string) (string, []domain.MessageEntity, error) {
	var out formattedTextBuilder
	entities := make([]domain.MessageEntity, 0)
	stack := make([]htmlEntityFrame, 0)
	for i := 0; i < len(input); {
		switch input[i] {
		case '&':
			decoded, next, err := decodeBotAPIHTMLEntity(input, i)
			if err != nil {
				return "", nil, err
			}
			out.appendString(decoded)
			i = next
		case '<':
			closing, tag, attrs, booleans, next, err := scanBotAPIHTMLTag(input, i)
			if err != nil {
				return "", nil, err
			}
			if !closing {
				frame, frameErr := botAPIHTMLFrame(tag, attrs, booleans, out.utf16, out.byteLen())
				if frameErr != nil {
					return "", nil, frameErr
				}
				stack = append(stack, frame)
			} else {
				if len(stack) == 0 {
					return "", nil, parseEntityError("unexpected end tag at byte offset %d", i)
				}
				frame := stack[len(stack)-1]
				if tag != "" && tag != frame.tag {
					return "", nil, parseEntityError("unmatched end tag at byte offset %d, expected </%s>, found </%s>", i, frame.tag, tag)
				}
				stack = stack[:len(stack)-1]
				length := out.utf16 - frame.offset
				if length > 0 {
					if frame.tag == "tg-time" && frame.date <= 0 {
						i = next
						continue
					}
					entity := domain.MessageEntity{
						Type: frame.typ, Offset: frame.offset, Length: length, Language: frame.language,
						DocumentID: frame.documentID, Date: frame.date, Collapsed: frame.collapsed,
						Relative: frame.relative, ShortTime: frame.shortTime, LongTime: frame.longTime,
						ShortDate: frame.shortDate, LongDate: frame.longDate, DayOfWeek: frame.dayOfWeek,
					}
					switch frame.tag {
					case "a":
						link := frame.argument
						if link == "" {
							link = out.string()[frame.outputByte:]
						}
						resolved, ok := botAPITextLinkEntity(link, frame.offset, length)
						if ok {
							entities = append(entities, resolved)
						}
					case "pre":
						if len(entities) > 0 {
							last := &entities[len(entities)-1]
							if last.Type == domain.MessageEntityCode && last.Offset == frame.offset && last.Length == length && last.Language != "" {
								last.Type = domain.MessageEntityPre
								break
							}
						}
						entities = append(entities, entity)
					default:
						entities = append(entities, entity)
					}
				}
			}
			i = next
		default:
			j := i
			for j < len(input) && input[j] != '<' && input[j] != '&' {
				j++
			}
			out.appendString(input[i:j])
			i = j
		}
	}
	if len(stack) > 0 {
		return "", nil, parseEntityError("can't find end tag corresponding to start tag <%s>", stack[len(stack)-1].tag)
	}
	for i := range entities {
		if entities[i].Type == domain.MessageEntityCode {
			entities[i].Language = ""
		}
	}
	sortBotAPIEntities(entities)
	return out.string(), entities, nil
}

// scanBotAPIHTMLTag parses only the deliberately small HTML dialect accepted by
// the Bot API. It does not apply browser error recovery: malformed and unmatched
// tags must fail before a message state transition.
func scanBotAPIHTMLTag(input string, start int) (closing bool, tag string, attrs map[string]string, booleans map[string]bool, next int, err error) {
	if start < 0 || start >= len(input) || input[start] != '<' {
		return false, "", nil, nil, start, parseEntityError("invalid tag at byte offset %d", start)
	}
	i := start + 1
	if i < len(input) && input[i] == '/' {
		closing = true
		i++
	}
	nameStart := i
	for i < len(input) && !isHTMLSpace(input[i]) && input[i] != '>' {
		i++
	}
	if i >= len(input) {
		return false, "", nil, nil, start, parseEntityError("unclosed tag at byte offset %d", start)
	}
	if i == nameStart && !closing {
		return false, "", nil, nil, start, parseEntityError("empty tag at byte offset %d", start)
	}
	tag = strings.ToLower(input[nameStart:i])
	if tag != "" && !supportedBotAPIHTMLTag(tag) {
		return false, "", nil, nil, start, parseEntityError("unsupported tag %q at byte offset %d", tag, start)
	}
	attrs = make(map[string]string)
	booleans = make(map[string]bool)
	if closing {
		for i < len(input) && isHTMLSpace(input[i]) {
			i++
		}
		if i >= len(input) || input[i] != '>' {
			return false, "", nil, nil, start, parseEntityError("unclosed end tag at byte offset %d", start)
		}
		return true, tag, attrs, booleans, i + 1, nil
	}
	for {
		for i < len(input) && isHTMLSpace(input[i]) {
			i++
		}
		if i >= len(input) {
			return false, "", nil, nil, start, parseEntityError("unclosed start tag <%s>", tag)
		}
		if input[i] == '>' {
			return false, tag, attrs, booleans, i + 1, nil
		}
		if input[i] == '/' {
			return false, "", nil, nil, start, parseEntityError("self-closing tag <%s/> is unsupported", tag)
		}
		attributeStart := i
		for i < len(input) && !isHTMLSpace(input[i]) && !strings.ContainsRune("=>/\"'", rune(input[i])) {
			i++
		}
		if i == attributeStart {
			return false, "", nil, nil, start, parseEntityError("empty attribute name in tag <%s>", tag)
		}
		name := strings.ToLower(input[attributeStart:i])
		for i < len(input) && isHTMLSpace(input[i]) {
			i++
		}
		if i >= len(input) {
			return false, "", nil, nil, start, parseEntityError("unclosed start tag <%s>", tag)
		}
		if input[i] != '=' {
			booleans[name] = true
			continue
		}
		i++
		for i < len(input) && isHTMLSpace(input[i]) {
			i++
		}
		if i >= len(input) {
			return false, "", nil, nil, start, parseEntityError("unclosed attribute %q", name)
		}
		var raw string
		if input[i] == '\'' || input[i] == '"' {
			quote := input[i]
			i++
			valueStart := i
			for i < len(input) && input[i] != quote {
				i++
			}
			if i >= len(input) {
				return false, "", nil, nil, start, parseEntityError("unclosed attribute %q", name)
			}
			raw = input[valueStart:i]
			i++
		} else {
			valueStart := i
			for i < len(input) && (isASCIIAlphaNumeric(input[i]) || input[i] == '.' || input[i] == '-') {
				i++
			}
			if i == valueStart || (i < len(input) && !isHTMLSpace(input[i]) && input[i] != '>') {
				return false, "", nil, nil, start, parseEntityError("invalid unquoted attribute %q", name)
			}
			raw = strings.ToLower(input[valueStart:i])
		}
		value, decodeErr := decodeBotAPIHTMLString(raw)
		if decodeErr != nil {
			return false, "", nil, nil, start, decodeErr
		}
		attrs[name] = value
	}
}

func botAPIHTMLFrame(tag string, attrs map[string]string, booleans map[string]bool, offset, outputByte int) (htmlEntityFrame, error) {
	frame := htmlEntityFrame{tag: tag, offset: offset, outputByte: outputByte}
	switch tag {
	case "b", "strong":
		frame.typ = domain.MessageEntityBold
	case "i", "em":
		frame.typ = domain.MessageEntityItalic
	case "u", "ins":
		frame.typ = domain.MessageEntityUnderline
	case "s", "strike", "del":
		frame.typ = domain.MessageEntityStrike
	case "tg-spoiler":
		frame.typ = domain.MessageEntitySpoiler
	case "span":
		if attrs["class"] != "tg-spoiler" {
			return htmlEntityFrame{}, parseEntityError("tag <span> must have class \"tg-spoiler\"")
		}
		frame.typ = domain.MessageEntitySpoiler
	case "a":
		frame.typ, frame.argument = domain.MessageEntityTextURL, attrs["href"]
	case "code":
		frame.typ = domain.MessageEntityCode
		if class := attrs["class"]; strings.HasPrefix(class, "language-") {
			frame.language = strings.TrimPrefix(class, "language-")
		}
	case "pre":
		frame.typ = domain.MessageEntityPre
	case "blockquote":
		frame.typ = domain.MessageEntityBlockquote
		_, hasExpandable := attrs["expandable"]
		frame.collapsed = booleans["expandable"] || hasExpandable
	case "tg-emoji":
		frame.typ = domain.MessageEntityCustomEmoji
		id, err := strconv.ParseInt(attrs["emoji-id"], 10, 64)
		if err != nil || id <= 0 {
			return htmlEntityFrame{}, parseEntityError("invalid custom emoji identifier")
		}
		frame.documentID = id
	case "tg-time":
		frame.typ = domain.MessageEntityFormattedDate
		date, err := strconv.ParseInt(attrs["unix"], 10, 32)
		if err != nil {
			date = 0
		}
		formatted, err := botAPIFormattedDate(1, attrs["format"])
		if err != nil {
			return htmlEntityFrame{}, err
		}
		frame.date = int(date)
		frame.relative = formatted.Relative
		frame.shortTime = formatted.ShortTime
		frame.longTime = formatted.LongTime
		frame.shortDate = formatted.ShortDate
		frame.longDate = formatted.LongDate
		frame.dayOfWeek = formatted.DayOfWeek
	default:
		return htmlEntityFrame{}, parseEntityError("unsupported tag <%s>", tag)
	}
	return frame, nil
}

func supportedBotAPIHTMLTag(tag string) bool {
	switch tag {
	case "a", "b", "strong", "i", "em", "s", "strike", "del", "u", "ins", "tg-spoiler", "tg-emoji", "tg-time", "span", "pre", "code", "blockquote":
		return true
	default:
		return false
	}
}

func isHTMLSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' }

func isASCIIAlphaNumeric(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func decodeBotAPIHTMLString(input string) (string, error) {
	if !strings.Contains(input, "&") {
		return input, nil
	}
	var out strings.Builder
	for i := 0; i < len(input); {
		if input[i] != '&' {
			j := strings.IndexByte(input[i:], '&')
			if j < 0 {
				out.WriteString(input[i:])
				break
			}
			out.WriteString(input[i : i+j])
			i += j
			continue
		}
		decoded, next, err := decodeBotAPIHTMLEntity(input, i)
		if err != nil {
			return "", err
		}
		out.WriteString(decoded)
		i = next
	}
	return out.String(), nil
}

func decodeBotAPIHTMLEntity(input string, start int) (string, int, error) {
	endRelative := strings.IndexByte(input[start:], ';')
	if endRelative < 0 {
		return "&", start + 1, nil
	}
	end := start + endRelative
	name := input[start+1 : end]
	switch name {
	case "lt":
		return "<", end + 1, nil
	case "gt":
		return ">", end + 1, nil
	case "amp":
		return "&", end + 1, nil
	case "quot":
		return "\"", end + 1, nil
	}
	if !strings.HasPrefix(name, "#") {
		return "&", start + 1, nil
	}
	base, digits := 10, name[1:]
	if strings.HasPrefix(digits, "x") || strings.HasPrefix(digits, "X") {
		base, digits = 16, digits[1:]
	}
	value, err := strconv.ParseInt(digits, base, 32)
	if err != nil || value <= 0 || value > utf8.MaxRune || value >= 0xd800 && value <= 0xdfff {
		return "&", start + 1, nil
	}
	return string(rune(value)), end + 1, nil
}

func parseBotAPIMarkdown(input string) (string, []domain.MessageEntity, error) {
	var out formattedTextBuilder
	entities := make([]domain.MessageEntity, 0)
	for i := 0; i < len(input); {
		if input[i] == '\\' && i+1 < len(input) && strings.ContainsRune("_*`[", rune(input[i+1])) {
			out.appendRune(rune(input[i+1]))
			i += 2
			continue
		}
		marker := input[i]
		if marker != '_' && marker != '*' && marker != '`' && marker != '[' {
			r, size := utf8.DecodeRuneInString(input[i:])
			out.appendRune(r)
			i += size
			continue
		}
		begin := i
		typ := domain.MessageEntityItalic
		delimiter := string(marker)
		language := ""
		switch marker {
		case '*':
			typ = domain.MessageEntityBold
			i++
		case '[':
			typ, delimiter = domain.MessageEntityTextURL, "]"
			i++
		case '`':
			typ = domain.MessageEntityCode
			if strings.HasPrefix(input[i:], "```") {
				typ, delimiter = domain.MessageEntityPre, "```"
				i += 3
				languageEnd := i
				for languageEnd < len(input) && !isHTMLSpace(input[languageEnd]) && input[languageEnd] != '`' {
					languageEnd++
				}
				if languageEnd > i && languageEnd < len(input) && input[languageEnd] != '`' {
					language = input[i:languageEnd]
					i = languageEnd
				}
				i = skipSingleLeadingNewline(input, i)
			} else {
				i++
			}
		default:
			i++
		}
		offset := out.utf16
		for i < len(input) && !strings.HasPrefix(input[i:], delimiter) {
			r, size := utf8.DecodeRuneInString(input[i:])
			out.appendRune(r)
			i += size
		}
		if i >= len(input) {
			return "", nil, parseEntityError("can't find end of entity starting at byte offset %d", begin)
		}
		length := out.utf16 - offset
		i += len(delimiter)
		if length <= 0 {
			continue
		}
		if typ == domain.MessageEntityTextURL {
			// Derive the visible slice by walking back over exactly the entity's
			// UTF-16 range; legacy Markdown doesn't allow nested entities.
			visibleStart := outputByteOffsetForUTF16Suffix(out.string(), length)
			link := out.string()[visibleStart:]
			if i < len(input) && input[i] == '(' {
				urlStart := i + 1
				urlEnd := strings.IndexByte(input[urlStart:], ')')
				if urlEnd < 0 {
					link = input[urlStart:]
					i = len(input)
				} else {
					link = input[urlStart : urlStart+urlEnd]
					i = urlStart + urlEnd + 1
				}
			}
			if entity, ok := botAPITextLinkEntity(link, offset, length); ok {
				entities = append(entities, entity)
			}
			continue
		}
		entities = append(entities, domain.MessageEntity{Type: typ, Offset: offset, Length: length, Language: language})
	}
	sortBotAPIEntities(entities)
	return out.string(), entities, nil
}

func skipSingleLeadingNewline(input string, i int) int {
	if i >= len(input) || input[i] != '\n' && input[i] != '\r' {
		return i
	}
	first := input[i]
	i++
	if i < len(input) && (input[i] == '\n' || input[i] == '\r') && input[i] != first {
		i++
	}
	return i
}

func outputByteOffsetForUTF16Suffix(text string, suffixLength int) int {
	need := suffixLength
	for i := len(text); i > 0; {
		r, size := utf8.DecodeLastRuneInString(text[:i])
		need--
		if r > 0xffff {
			need--
		}
		i -= size
		if need == 0 {
			return i
		}
	}
	return 0
}

type markdownV2Frame struct {
	typ        domain.MessageEntityType
	offset     int
	inputByte  int
	outputByte int
	language   string
}

func parseBotAPIMarkdownV2(input string) (string, []domain.MessageEntity, error) {
	var out formattedTextBuilder
	entities := make([]domain.MessageEntity, 0)
	stack := make([]markdownV2Frame, 0)
	haveBlockquote, canStartBlockquote := false, true
	for i := 0; i < len(input); {
		if input[i] == '\\' && i+1 < len(input) && input[i+1] > 0 && input[i+1] <= 126 {
			literal := input[i+1]
			out.appendRune(rune(literal))
			if literal != '\r' {
				canStartBlockquote = literal == '\n'
			}
			i += 2
			continue
		}

		reserved := "_*[]()~`>#+-=|{}.!\n"
		if len(stack) > 0 && (stack[len(stack)-1].typ == domain.MessageEntityCode || stack[len(stack)-1].typ == domain.MessageEntityPre) {
			reserved = "`"
		}
		if !strings.ContainsRune(reserved, rune(input[i])) {
			r, size := utf8.DecodeRuneInString(input[i:])
			out.appendRune(r)
			if r != '\r' {
				canStartBlockquote = false
			}
			i += size
			continue
		}

		c := input[i]
		endQuote := haveBlockquote && c == '\n' && (i+1 == len(input) || input[i+1] != '>')
		isEnd := endQuote || markdownV2ClosesTop(input, i, stack)
		if !isEnd {
			frame := markdownV2Frame{offset: out.utf16, inputByte: i, outputByte: out.byteLen()}
			switch c {
			case '_':
				frame.typ = domain.MessageEntityItalic
				i++
				if i < len(input) && input[i] == '_' {
					frame.typ = domain.MessageEntityUnderline
					i++
				}
			case '*':
				frame.typ = domain.MessageEntityBold
				i++
			case '~':
				frame.typ = domain.MessageEntityStrike
				i++
			case '|':
				if i+1 >= len(input) || input[i+1] != '|' {
					return "", nil, markdownV2ReservedError(c)
				}
				frame.typ = domain.MessageEntitySpoiler
				i += 2
			case '[':
				frame.typ = domain.MessageEntityTextURL
				i++
			case '!':
				if i+1 >= len(input) || input[i+1] != '[' {
					return "", nil, markdownV2ReservedError(c)
				}
				frame.typ = domain.MessageEntityCustomEmoji
				i += 2
			case '`':
				frame.typ = domain.MessageEntityCode
				if strings.HasPrefix(input[i:], "```") {
					frame.typ = domain.MessageEntityPre
					i += 3
					languageEnd := i
					for languageEnd < len(input) && !isHTMLSpace(input[languageEnd]) && input[languageEnd] != '`' {
						languageEnd++
					}
					if languageEnd > i && languageEnd < len(input) && input[languageEnd] != '`' {
						frame.language = input[i:languageEnd]
						i = languageEnd
					}
					i = skipSingleLeadingNewline(input, i)
				} else {
					i++
				}
			case '\n':
				out.appendRune('\n')
				canStartBlockquote = true
				i++
				continue
			case '>':
				if !canStartBlockquote {
					return "", nil, markdownV2ReservedError(c)
				}
				if haveBlockquote {
					i++
					continue
				}
				frame.typ = domain.MessageEntityBlockquote
				haveBlockquote = true
				i++
			default:
				return "", nil, markdownV2ReservedError(c)
			}
			stack = append(stack, frame)
			continue
		}

		if len(stack) == 0 {
			return "", nil, markdownV2ReservedError(c)
		}
		collapsed := false
		if endQuote {
			quoteStart := i
			if len(stack) > 0 {
				quoteStart = stack[len(stack)-1].inputByte
			}
			if stack[len(stack)-1].typ == domain.MessageEntitySpoiler && out.utf16 == stack[len(stack)-1].offset {
				stack = stack[:len(stack)-1]
				collapsed = true
			}
			if len(stack) == 0 || stack[len(stack)-1].typ != domain.MessageEntityBlockquote {
				return "", nil, parseEntityError("can't find end of entity starting at byte offset %d", quoteStart)
			}
			frame := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			out.appendRune('\n')
			length := out.utf16 - frame.offset
			if length > 0 {
				entities = append(entities, domain.MessageEntity{Type: domain.MessageEntityBlockquote, Offset: frame.offset, Length: length, Collapsed: collapsed})
			}
			haveBlockquote, canStartBlockquote = false, true
			i++
			continue
		}

		frame := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		length := out.utf16 - frame.offset
		switch frame.typ {
		case domain.MessageEntityBold, domain.MessageEntityItalic, domain.MessageEntityStrike, domain.MessageEntityCode:
			i++
		case domain.MessageEntityUnderline, domain.MessageEntitySpoiler:
			i += 2
		case domain.MessageEntityPre:
			i += 3
		case domain.MessageEntityTextURL, domain.MessageEntityCustomEmoji:
			i++ // closing ]
			link := out.string()[frame.outputByte:]
			if i < len(input) && input[i] == '(' {
				parsedURL, next, parseErr := parseMarkdownV2URL(input, i+1)
				if parseErr != nil {
					return "", nil, parseErr
				}
				link, i = parsedURL, next
			} else if frame.typ == domain.MessageEntityCustomEmoji {
				return "", nil, parseEntityError("custom emoji entity must contain a tg://emoji or tg://time URL")
			}
			if length > 0 {
				if frame.typ == domain.MessageEntityTextURL {
					if entity, ok := botAPITextLinkEntity(link, frame.offset, length); ok {
						entities = append(entities, entity)
					}
				} else {
					entity, resolveErr := botAPICustomLinkEntity(link, frame.offset, length)
					if resolveErr != nil {
						return "", nil, resolveErr
					}
					entities = append(entities, entity)
				}
			}
			continue
		default:
			return "", nil, parseEntityError("invalid MarkdownV2 entity")
		}
		if length > 0 {
			entities = append(entities, domain.MessageEntity{Type: frame.typ, Offset: frame.offset, Length: length, Language: frame.language})
		}
	}

	if haveBlockquote {
		collapsed := false
		if len(stack) > 0 && stack[len(stack)-1].typ == domain.MessageEntitySpoiler && out.utf16 == stack[len(stack)-1].offset {
			stack = stack[:len(stack)-1]
			collapsed = true
		}
		if len(stack) > 0 && stack[len(stack)-1].typ == domain.MessageEntityBlockquote {
			frame := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if length := out.utf16 - frame.offset; length > 0 {
				entities = append(entities, domain.MessageEntity{Type: domain.MessageEntityBlockquote, Offset: frame.offset, Length: length, Collapsed: collapsed})
			}
			haveBlockquote = false
		}
	}
	if len(stack) > 0 {
		frame := stack[len(stack)-1]
		return "", nil, parseEntityError("can't find end of entity starting at byte offset %d", frame.inputByte)
	}
	sortBotAPIEntities(entities)
	return out.string(), entities, nil
}

func markdownV2ClosesTop(input string, i int, stack []markdownV2Frame) bool {
	if len(stack) == 0 {
		return false
	}
	c := input[i]
	switch stack[len(stack)-1].typ {
	case domain.MessageEntityBold:
		return c == '*'
	case domain.MessageEntityItalic:
		return c == '_' && (i+1 >= len(input) || input[i+1] != '_')
	case domain.MessageEntityUnderline:
		return c == '_' && i+1 < len(input) && input[i+1] == '_'
	case domain.MessageEntityStrike:
		return c == '~'
	case domain.MessageEntitySpoiler:
		return c == '|' && i+1 < len(input) && input[i+1] == '|'
	case domain.MessageEntityCode:
		return c == '`'
	case domain.MessageEntityPre:
		return strings.HasPrefix(input[i:], "```")
	case domain.MessageEntityTextURL, domain.MessageEntityCustomEmoji:
		return c == ']'
	case domain.MessageEntityBlockquote:
		return false
	default:
		return false
	}
}

func markdownV2ReservedError(c byte) error {
	return parseEntityError("character %q is reserved and must be escaped with a preceding backslash", c)
}

func parseMarkdownV2URL(input string, start int) (string, int, error) {
	var out strings.Builder
	for i := start; i < len(input); {
		if input[i] == ')' {
			return out.String(), i + 1, nil
		}
		if input[i] == '\\' && i+1 < len(input) && input[i+1] > 0 && input[i+1] <= 126 {
			out.WriteByte(input[i+1])
			i += 2
			continue
		}
		r, size := utf8.DecodeRuneInString(input[i:])
		out.WriteRune(r)
		i += size
	}
	return "", start, parseEntityError("can't find end of URL at byte offset %d", start)
}

func botAPITextLinkEntity(raw string, offset, length int) (domain.MessageEntity, bool) {
	if userID, ok := botAPITGUserID(raw); ok {
		return domain.MessageEntity{Type: domain.MessageEntityMentionName, Offset: offset, Length: length, UserID: userID}, true
	}
	if parsed, err := url.Parse(raw); err == nil && strings.EqualFold(parsed.Scheme, "tg") && strings.EqualFold(parsed.Host, "user") {
		return domain.MessageEntity{}, false
	}
	if !validBotAPITextURL(raw) {
		return domain.MessageEntity{}, false
	}
	return domain.MessageEntity{Type: domain.MessageEntityTextURL, Offset: offset, Length: length, URL: raw}, true
}

func botAPITGUserID(raw string) (int64, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(parsed.Scheme, "tg") || !strings.EqualFold(parsed.Host, "user") {
		return 0, false
	}
	id, err := strconv.ParseInt(parsed.Query().Get("id"), 10, 64)
	return id, err == nil && id > 0
}

func validBotAPITextURL(raw string) bool {
	if raw == "" || strings.ContainsAny(raw, " \t\r\n") {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return parsed.Host != ""
	case "tg":
		return parsed.Host != ""
	case "mailto", "tel":
		return parsed.Opaque != "" || parsed.Path != ""
	case "":
		return strings.Contains(parsed.Path, ".")
	default:
		return false
	}
}

func botAPICustomLinkEntity(raw string, offset, length int) (domain.MessageEntity, error) {
	parsed, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(parsed.Scheme, "tg") {
		return domain.MessageEntity{}, parseEntityError("invalid tg://emoji or tg://time URL")
	}
	switch strings.ToLower(parsed.Host) {
	case "emoji":
		id, parseErr := strconv.ParseInt(parsed.Query().Get("id"), 10, 64)
		if parseErr != nil || id <= 0 {
			return domain.MessageEntity{}, parseEntityError("invalid custom emoji identifier")
		}
		return domain.MessageEntity{Type: domain.MessageEntityCustomEmoji, Offset: offset, Length: length, DocumentID: id}, nil
	case "time":
		date, parseErr := strconv.ParseInt(parsed.Query().Get("unix"), 10, 32)
		if parseErr != nil || date <= 0 {
			return domain.MessageEntity{}, parseEntityError("invalid date-time unix value")
		}
		entity, formatErr := botAPIFormattedDate(int(date), parsed.Query().Get("format"))
		if formatErr != nil {
			return domain.MessageEntity{}, formatErr
		}
		entity.Offset, entity.Length = offset, length
		return entity, nil
	default:
		return domain.MessageEntity{}, parseEntityError("invalid tg://emoji or tg://time URL")
	}
}

func botAPIFormattedDate(date int, format string) (domain.MessageEntity, error) {
	if date <= 0 || int64(date) > 1<<31-1 {
		return domain.MessageEntity{}, parseEntityError("invalid date-time unix value")
	}
	entity := domain.MessageEntity{Type: domain.MessageEntityFormattedDate, Date: date}
	if format == "" {
		return entity, nil
	}
	if format == "r" || format == "R" {
		entity.Relative = true
		return entity, nil
	}
	for _, part := range format {
		switch part {
		case 't':
			entity.ShortTime = true
		case 'T':
			entity.LongTime = true
		case 'd':
			entity.ShortDate = true
		case 'D':
			entity.LongDate = true
		case 'w', 'W':
			entity.DayOfWeek = true
		default:
			return domain.MessageEntity{}, parseEntityError("invalid date-time format %q", format)
		}
	}
	return entity, nil
}

func botAPIFormattedDateFormat(entity domain.MessageEntity) string {
	if entity.Relative {
		return "r"
	}
	var out strings.Builder
	if entity.DayOfWeek {
		out.WriteByte('w')
	}
	if entity.ShortDate {
		out.WriteByte('d')
	} else if entity.LongDate {
		out.WriteByte('D')
	}
	if entity.ShortTime {
		out.WriteByte('t')
	} else if entity.LongTime {
		out.WriteByte('T')
	}
	return out.String()
}

func sortBotAPIEntities(entities []domain.MessageEntity) {
	sort.SliceStable(entities, func(i, j int) bool {
		if entities[i].Offset != entities[j].Offset {
			return entities[i].Offset < entities[j].Offset
		}
		if entities[i].Length != entities[j].Length {
			return entities[i].Length > entities[j].Length
		}
		return entities[i].Type < entities[j].Type
	})
}
