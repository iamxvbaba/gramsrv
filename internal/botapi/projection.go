package botapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"telesrv/internal/domain"
)

func apiInt(raw string, fallback int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}

func botAPIMessageEntities(raw string) ([]domain.MessageEntity, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var payload []apiMessageEntity
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, errors.New("ENTITY_INVALID")
	}
	return messageEntitiesFromAPI(payload)
}

func allowedUpdates(raw string) map[string]struct{} {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var items []string
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil
	}
	out := make(map[string]struct{}, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			out[item] = struct{}{}
		}
	}
	return out
}

func apiUpdates(events []domain.UpdateEvent, allowed map[string]struct{}, limit int) []map[string]any {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	out := make([]map[string]any, 0, min(len(events), limit))
	for _, event := range events {
		item, kind, ok := apiUpdate(event)
		if !ok || !updateAllowed(kind, allowed) {
			continue
		}
		out = append(out, item)
		if len(out) >= limit {
			break
		}
	}
	if out == nil {
		return []map[string]any{}
	}
	return out
}

func updateAllowed(kind string, allowed map[string]struct{}) bool {
	if len(allowed) == 0 {
		return true
	}
	_, ok := allowed[kind]
	return ok
}

func apiUpdate(event domain.UpdateEvent) (map[string]any, string, bool) {
	if event.Pts <= 0 {
		return nil, "", false
	}
	switch event.Type {
	case domain.UpdateEventNewMessage:
		if !apiMessageProjectable(event.Message) {
			return nil, "", false
		}
		return map[string]any{
			"update_id": event.Pts,
			"message":   apiMessage(event.Message, event.Users),
		}, "message", true
	case domain.UpdateEventEditMessage:
		if !apiMessageProjectable(event.Message) {
			return nil, "", false
		}
		return map[string]any{
			"update_id":      event.Pts,
			"edited_message": apiMessage(event.Message, event.Users),
		}, "edited_message", true
	default:
		return nil, "", false
	}
}

func apiMessageProjectable(msg domain.Message) bool {
	if msg.Out || msg.ID <= 0 {
		return false
	}
	return msg.Body != "" || len(apiMessageMedia(msg.Media)) > 0
}

func apiUser(u domain.User) map[string]any {
	first := u.FirstName
	if strings.TrimSpace(first) == "" {
		first = "User " + strconv.FormatInt(u.ID, 10)
	}
	out := map[string]any{
		"id":         u.ID,
		"is_bot":     u.Bot,
		"first_name": first,
	}
	if u.LastName != "" {
		out["last_name"] = u.LastName
	}
	if u.Username != "" {
		out["username"] = u.Username
	}
	return out
}

func apiMessage(msg domain.Message, users []domain.User) map[string]any {
	userByID := map[int64]domain.User{}
	for _, u := range users {
		userByID[u.ID] = u
	}
	out := map[string]any{
		"message_id": msg.ID,
		"date":       msg.Date,
		"chat":       apiChat(msg.Peer, userByID),
	}
	if msg.From.Type == domain.PeerTypeUser && msg.From.ID != 0 {
		from := userByID[msg.From.ID]
		if from.ID == 0 {
			from = domain.User{ID: msg.From.ID}
		}
		if msg.Out && msg.From.ID == msg.OwnerUserID {
			from.Bot = true
		}
		out["from"] = apiUser(from)
	}
	media := apiMessageMedia(msg.Media)
	if msg.Body != "" {
		if len(media) > 0 {
			out["caption"] = msg.Body
		} else {
			out["text"] = msg.Body
		}
	}
	if entities := apiMessageEntities(msg.Entities, userByID); len(entities) > 0 {
		if len(media) > 0 {
			out["caption_entities"] = entities
		} else {
			out["entities"] = entities
		}
	}
	if msg.EditDate > 0 {
		out["edit_date"] = msg.EditDate
	}
	if msg.ReplyTo != nil && msg.ReplyTo.MessageID > 0 {
		out["reply_to_message"] = map[string]any{
			"message_id": msg.ReplyTo.MessageID,
			"date":       0,
			"chat":       apiChat(msg.ReplyTo.Peer, userByID),
		}
	}
	if markup := apiReplyMarkup(msg.ReplyMarkup); markup != nil {
		out["reply_markup"] = markup
	}
	if len(media) > 0 {
		for k, v := range media {
			out[k] = v
		}
	}
	return out
}

func apiChat(peer domain.Peer, users map[int64]domain.User) map[string]any {
	switch peer.Type {
	case domain.PeerTypeUser:
		out := map[string]any{
			"id":   peer.ID,
			"type": "private",
		}
		if u := users[peer.ID]; u.ID != 0 {
			out["first_name"] = apiUserFirstName(u)
			if u.LastName != "" {
				out["last_name"] = u.LastName
			}
			if u.Username != "" {
				out["username"] = u.Username
			}
		}
		return out
	case domain.PeerTypeChannel:
		return map[string]any{
			"id":   -1000000000000 - peer.ID,
			"type": "supergroup",
		}
	default:
		return map[string]any{
			"id":   peer.ID,
			"type": "private",
		}
	}
}

func apiUserFirstName(u domain.User) string {
	if strings.TrimSpace(u.FirstName) != "" {
		return u.FirstName
	}
	return "User " + strconv.FormatInt(u.ID, 10)
}

func apiMessageEntities(in []domain.MessageEntity, users map[int64]domain.User) []map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(in))
	for _, entity := range in {
		typ, ok := botAPIEntityType(entity.Type)
		if !ok || entity.Offset < 0 || entity.Length <= 0 {
			continue
		}
		item := map[string]any{
			"type":   typ,
			"offset": entity.Offset,
			"length": entity.Length,
		}
		if entity.URL != "" {
			item["url"] = entity.URL
		}
		if entity.Language != "" {
			item["language"] = entity.Language
		}
		if entity.UserID != 0 {
			u := users[entity.UserID]
			if u.ID == 0 {
				u = domain.User{ID: entity.UserID}
			}
			item["user"] = apiUser(u)
		}
		if entity.DocumentID != 0 {
			item["custom_emoji_id"] = strconv.FormatInt(entity.DocumentID, 10)
		}
		out = append(out, item)
	}
	return out
}

func botAPIEntityType(in domain.MessageEntityType) (string, bool) {
	switch in {
	case domain.MessageEntityBold:
		return "bold", true
	case domain.MessageEntityItalic:
		return "italic", true
	case domain.MessageEntityUnderline:
		return "underline", true
	case domain.MessageEntityStrike:
		return "strikethrough", true
	case domain.MessageEntityCode:
		return "code", true
	case domain.MessageEntityPre:
		return "pre", true
	case domain.MessageEntityTextURL:
		return "text_link", true
	case domain.MessageEntityMentionName:
		return "text_mention", true
	case domain.MessageEntitySpoiler:
		return "spoiler", true
	case domain.MessageEntityBlockquote:
		return "blockquote", true
	case domain.MessageEntityCustomEmoji:
		return "custom_emoji", true
	case domain.MessageEntityMention:
		return "mention", true
	case domain.MessageEntityHashtag:
		return "hashtag", true
	case domain.MessageEntityCashtag:
		return "cashtag", true
	case domain.MessageEntityBotCommand:
		return "bot_command", true
	case domain.MessageEntityURL:
		return "url", true
	case domain.MessageEntityEmail:
		return "email", true
	case domain.MessageEntityPhone:
		return "phone_number", true
	default:
		return "", false
	}
}

func apiReplyMarkup(markup *domain.MessageReplyMarkup) map[string]any {
	if markup.IsZero() {
		return nil
	}
	rows := make([][]map[string]any, 0, len(markup.Inline))
	for _, row := range markup.Inline {
		if len(row) == 0 {
			continue
		}
		apiRow := make([]map[string]any, 0, len(row))
		for _, button := range row {
			item := map[string]any{"text": button.Text}
			switch button.Type {
			case domain.MarkupButtonURL:
				item["url"] = button.URL
			case domain.MarkupButtonCallback:
				item["callback_data"] = string(button.Data)
			default:
				continue
			}
			apiRow = append(apiRow, item)
		}
		if len(apiRow) > 0 {
			rows = append(rows, apiRow)
		}
	}
	if len(rows) == 0 {
		return nil
	}
	return map[string]any{"inline_keyboard": rows}
}

func apiMessageMedia(media *domain.MessageMedia) map[string]any {
	if media.IsZero() {
		return nil
	}
	switch media.Kind {
	case domain.MessageMediaKindPhoto:
		if media.Photo == nil {
			return nil
		}
		photos := apiPhotoSizes(*media.Photo)
		if len(photos) == 0 {
			return nil
		}
		return map[string]any{"photo": photos}
	case domain.MessageMediaKindDocument:
		if media.Document == nil {
			return nil
		}
		return map[string]any{"document": apiDocument(*media.Document)}
	default:
		return nil
	}
}

func apiPhotoSizes(photo domain.Photo) []map[string]any {
	return apiPhotoSizesWithPrefix(photo.Sizes, "photo:"+strconv.FormatInt(photo.ID, 10)+":")
}

func apiPhotoSizesWithPrefix(sizes []domain.PhotoSize, locationPrefix string) []map[string]any {
	out := make([]map[string]any, 0, len(sizes))
	for _, size := range sizes {
		if !size.Downloadable() || size.Type == "" {
			continue
		}
		fileID := encodeBotAPIFileID(locationPrefix + size.Type)
		item := map[string]any{
			"file_id":        fileID,
			"file_unique_id": fileID,
			"width":          size.W,
			"height":         size.H,
		}
		if size.Size > 0 {
			item["file_size"] = size.Size
		}
		out = append(out, item)
	}
	return out
}

func apiDocument(doc domain.Document) map[string]any {
	fileID := encodeBotAPIFileID("doc:" + strconv.FormatInt(doc.ID, 10))
	out := map[string]any{
		"file_id":        fileID,
		"file_unique_id": fileID,
	}
	if doc.MimeType != "" {
		out["mime_type"] = doc.MimeType
	}
	if doc.Size > 0 {
		out["file_size"] = doc.Size
	}
	for _, attr := range doc.Attributes {
		if attr.Kind == domain.DocAttrFilename && strings.TrimSpace(attr.FileName) != "" {
			out["file_name"] = attr.FileName
			break
		}
	}
	if thumbs := apiPhotoSizesWithPrefix(doc.Thumbs, "doc:"+strconv.FormatInt(doc.ID, 10)+":"); len(thumbs) > 0 {
		out["thumbnail"] = thumbs[len(thumbs)-1]
	}
	return out
}

func encodeBotAPIFileID(locationKey string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(locationKey))
}

func decodeBotAPIFileID(fileID string) (string, bool) {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return "", false
	}
	data, err := base64.RawURLEncoding.DecodeString(fileID)
	if err != nil {
		return "", false
	}
	locationKey := string(data)
	if strings.HasPrefix(locationKey, "doc:") || strings.HasPrefix(locationKey, "photo:") {
		return locationKey, true
	}
	return "", false
}
