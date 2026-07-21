package botapi

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"telesrv/internal/domain"
	"telesrv/internal/store"
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

func parseAllowedUpdates(raw string) ([]domain.BotAPIUpdateKind, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("ALLOWED_UPDATES_INVALID")
	}
	var items []string
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, errors.New("ALLOWED_UPDATES_INVALID")
	}
	if len(items) > 100 {
		return nil, errors.New("ALLOWED_UPDATES_INVALID")
	}
	seen := make(map[domain.BotAPIUpdateKind]struct{}, len(items))
	out := make([]domain.BotAPIUpdateKind, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || len(item) > 64 {
			return nil, errors.New("ALLOWED_UPDATES_INVALID")
		}
		kind := domain.BotAPIUpdateKind(item)
		if _, ok := seen[kind]; !ok {
			seen[kind] = struct{}{}
			out = append(out, kind)
		}
	}
	return out, nil
}

func apiUpdates(events []domain.UpdateEvent, limit int) []map[string]any {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	out := make([]map[string]any, 0, min(len(events), limit))
	for _, event := range events {
		item, _, ok := apiUpdate(event)
		if !ok {
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

func apiUpdate(event domain.UpdateEvent) (map[string]any, string, bool) {
	updateID := event.BotAPIUpdateID
	if updateID <= 0 {
		updateID = int64(event.Pts)
	}
	if updateID <= 0 {
		return nil, "", false
	}
	switch event.Type {
	case domain.UpdateEventNewMessage:
		if event.EphemeralMessage != nil {
			message, ok := apiEphemeralMessage(*event.EphemeralMessage, event.Users, event.Channels)
			if !ok {
				return nil, "", false
			}
			return map[string]any{"update_id": updateID, "message": message}, "message", true
		}
		if !apiMessageProjectable(event.Message) {
			return nil, "", false
		}
		return map[string]any{
			"update_id": updateID,
			"message":   apiMessage(event.Message, event.Users, event.Channels),
		}, "message", true
	case domain.UpdateEventEditMessage:
		if event.EphemeralMessage != nil {
			message, ok := apiEphemeralMessage(*event.EphemeralMessage, event.Users, event.Channels)
			if !ok {
				return nil, "", false
			}
			return map[string]any{"update_id": updateID, "edited_message": message}, "edited_message", true
		}
		if !apiMessageProjectable(event.Message) {
			return nil, "", false
		}
		return map[string]any{
			"update_id":      updateID,
			"edited_message": apiMessage(event.Message, event.Users, event.Channels),
		}, "edited_message", true
	case domain.UpdateEventBotCallbackQuery:
		callback := event.BotCallbackQuery
		if callback == nil || callback.ID == 0 || callback.UserID == 0 {
			return nil, "", false
		}
		var from domain.User
		for _, user := range event.Users {
			if user.ID == callback.UserID {
				from = user
				break
			}
		}
		if from.ID == 0 {
			from = domain.User{ID: callback.UserID}
		}
		query := map[string]any{
			"id":            strconv.FormatInt(callback.ID, 10),
			"from":          apiUser(from),
			"chat_instance": strconv.FormatInt(callback.ChatInstance, 10),
			"data":          string(callback.Data),
		}
		if callback.InlineMessage != nil {
			inlineMessageID, ok := encodeBotAPIInlineMessageID(*callback.InlineMessage)
			if !ok || callback.MessageID != 0 || callback.Peer != (domain.Peer{}) {
				return nil, "", false
			}
			query["inline_message_id"] = inlineMessageID
		} else if event.EphemeralMessage != nil {
			if callback.MessageID <= 0 || event.EphemeralMessage.ID != callback.MessageID || event.EphemeralMessage.Peer != callback.Peer {
				return nil, "", false
			}
			message, ok := apiEphemeralMessage(*event.EphemeralMessage, event.Users, event.Channels)
			if !ok {
				return nil, "", false
			}
			query["message"] = message
		} else {
			if callback.MessageID <= 0 || event.Message.ID != callback.MessageID {
				return nil, "", false
			}
			query["message"] = apiMessage(event.Message, event.Users, event.Channels)
		}
		return map[string]any{
			"update_id":      updateID,
			"callback_query": query,
		}, "callback_query", true
	default:
		return nil, "", false
	}
}

func apiEphemeralMessage(message domain.EphemeralMessage, users []domain.User, channels []domain.Channel) (map[string]any, bool) {
	return apiEphemeralMessageDepth(message, users, channels, 0)
}

func apiEphemeralMessageDepth(message domain.EphemeralMessage, users []domain.User, channels []domain.Channel, depth int) (map[string]any, bool) {
	if message.ID <= 0 || message.Peer.Type != domain.PeerTypeChannel || message.Peer.ID <= 0 ||
		message.SenderUserID <= 0 || message.ReceiverUserID <= 0 || message.Date <= 0 || message.Deleted {
		return nil, false
	}
	if message.Content.Message == "" && (message.Content.Media == nil || message.Content.Media.IsZero()) {
		return nil, false
	}
	projected := apiMessage(domain.Message{
		ID: 0, Peer: message.Peer, From: domain.Peer{Type: domain.PeerTypeUser, ID: message.SenderUserID},
		Date: message.Date, EditDate: message.EditDate, Body: message.Content.Message,
		Entities: message.Content.Entities, Media: message.Content.Media, ReplyMarkup: message.Content.ReplyMarkup,
	}, users, channels)
	projected["message_id"] = 0
	projected["ephemeral_message_id"] = message.ID
	receiver := domain.User{ID: message.ReceiverUserID}
	for _, user := range users {
		if user.ID == message.ReceiverUserID {
			receiver = user
			break
		}
	}
	projected["receiver_user"] = apiUser(receiver)
	if message.ReplyToEphemeralID > 0 {
		if depth != 0 || message.BotAPIReply == nil || message.BotAPIReply.ID != message.ReplyToEphemeralID {
			return nil, false
		}
		reply, ok := apiEphemeralMessageDepth(*message.BotAPIReply, users, channels, depth+1)
		if !ok {
			return nil, false
		}
		projected["reply_to_message"] = reply
	}
	return projected, true
}

const botAPIInlineMessageIDVersion byte = 1

// encodeBotAPIInlineMessageID exposes the signed MTProto inline-message identity as an
// opaque, fixed-size Bot API token. AccessHash remains the authorization boundary; the
// version byte lets us reject rather than reinterpret future shapes.
func encodeBotAPIInlineMessageID(id domain.BotInlineMessageID) (string, bool) {
	if id.DCID <= 0 || id.OwnerID == 0 || id.ID <= 0 || id.AccessHash == 0 {
		return "", false
	}
	buf := make([]byte, 1+4+8+4+8)
	buf[0] = botAPIInlineMessageIDVersion
	binary.LittleEndian.PutUint32(buf[1:5], uint32(id.DCID))
	binary.LittleEndian.PutUint64(buf[5:13], uint64(id.OwnerID))
	binary.LittleEndian.PutUint32(buf[13:17], uint32(id.ID))
	binary.LittleEndian.PutUint64(buf[17:25], uint64(id.AccessHash))
	return base64.RawURLEncoding.EncodeToString(buf), true
}

func decodeBotAPIInlineMessageID(raw string) (domain.BotInlineMessageID, error) {
	buf, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(buf) != 25 || buf[0] != botAPIInlineMessageIDVersion {
		return domain.BotInlineMessageID{}, errors.New("INLINE_MESSAGE_ID_INVALID")
	}
	id := domain.BotInlineMessageID{
		DCID:       int(binary.LittleEndian.Uint32(buf[1:5])),
		OwnerID:    int64(binary.LittleEndian.Uint64(buf[5:13])),
		ID:         int(binary.LittleEndian.Uint32(buf[13:17])),
		AccessHash: int64(binary.LittleEndian.Uint64(buf[17:25])),
	}
	if id.DCID <= 0 || id.OwnerID == 0 || id.ID <= 0 || id.AccessHash == 0 {
		return domain.BotInlineMessageID{}, errors.New("INLINE_MESSAGE_ID_INVALID")
	}
	return id, nil
}

func apiMessageProjectable(msg domain.Message) bool {
	if msg.Out || msg.ID <= 0 {
		return false
	}
	return msg.Body != "" || (msg.RichMessage != nil && len(msg.RichMessage.BotAPIProjection) > 0) || len(apiMessageMedia(msg.Media, nil, nil)) > 0
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

func apiMessage(msg domain.Message, users []domain.User, channelLists ...[]domain.Channel) map[string]any {
	userByID := map[int64]domain.User{}
	for _, u := range users {
		userByID[u.ID] = u
	}
	channelByID := map[int64]domain.Channel{}
	if len(channelLists) > 0 {
		for _, channel := range channelLists[0] {
			channelByID[channel.ID] = channel
		}
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
	media := apiMessageMedia(msg.Media, userByID, channelByID)
	if msg.Body != "" {
		if apiMediaUsesCaption(media) {
			out["caption"] = msg.Body
		} else if poll, ok := media["poll"].(map[string]any); ok {
			poll["description"] = msg.Body
		} else {
			out["text"] = msg.Body
		}
	}
	if entities := apiMessageEntities(msg.Entities, userByID); len(entities) > 0 {
		if apiMediaUsesCaption(media) {
			out["caption_entities"] = entities
		} else if poll, ok := media["poll"].(map[string]any); ok && msg.Body != "" {
			poll["description_entities"] = entities
		} else {
			out["entities"] = entities
		}
	}
	if msg.RichMessage != nil && len(msg.RichMessage.BotAPIProjection) > 0 {
		var richMessage any
		if json.Unmarshal(msg.RichMessage.BotAPIProjection, &richMessage) == nil && richMessage != nil {
			out["rich_message"] = richMessage
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

func apiMediaUsesCaption(media map[string]any) bool {
	for _, key := range []string{"photo", "live_photo", "animation", "audio", "document", "video", "voice"} {
		if _, ok := media[key]; ok {
			return true
		}
	}
	return false
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
		if entity.Type == domain.MessageEntityBlockquote && entity.Collapsed {
			item["type"] = "expandable_blockquote"
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
		if entity.Type == domain.MessageEntityFormattedDate {
			item["unix_time"] = entity.Date
			item["date_time_format"] = botAPIFormattedDateFormat(entity)
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
	case domain.MessageEntityBankCard:
		return "bank_card_number", true
	case domain.MessageEntityFormattedDate:
		return "date_time", true
	default:
		return "", false
	}
}

func apiReplyMarkup(markup *domain.MessageReplyMarkup) map[string]any {
	if markup.IsZero() {
		return nil
	}
	// Bot API Message.reply_markup is InlineKeyboardMarkup only. ReplyKeyboardMarkup,
	// ReplyKeyboardRemove and ForceReply are send parameters, not message response fields.
	if markup.Kind() != domain.MessageReplyMarkupInline {
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
			if button.Style != "" {
				item["style"] = string(button.Style)
			}
			if button.IconCustomEmojiID > 0 {
				item["icon_custom_emoji_id"] = strconv.FormatInt(button.IconCustomEmojiID, 10)
			}
			switch button.Type {
			case domain.MarkupButtonURL:
				item["url"] = button.URL
			case domain.MarkupButtonLoginURL:
				login := map[string]any{"url": button.URL}
				if button.ForwardText != "" {
					login["forward_text"] = button.ForwardText
				}
				if button.LoginBotUsername != "" {
					login["bot_username"] = button.LoginBotUsername
				}
				if button.RequestWriteAccess {
					login["request_write_access"] = true
				}
				item["login_url"] = login
			case domain.MarkupButtonCallback:
				item["callback_data"] = string(button.Data)
			case domain.MarkupButtonWebView:
				item["web_app"] = map[string]any{"url": button.URL}
			case domain.MarkupButtonSwitchInline:
				switch {
				case button.SamePeer:
					item["switch_inline_query_current_chat"] = button.Query
				case len(button.PeerTypes) > 0:
					chosen := map[string]any{"query": button.Query}
					for _, peerType := range button.PeerTypes {
						switch peerType {
						case store.InlineQueryPeerTypePM:
							chosen["allow_user_chats"] = true
						case store.InlineQueryPeerTypeBotPM:
							chosen["allow_bot_chats"] = true
						case store.InlineQueryPeerTypeChat, store.InlineQueryPeerTypeMegagroup:
							chosen["allow_group_chats"] = true
						case store.InlineQueryPeerTypeBroadcast:
							chosen["allow_channel_chats"] = true
						}
					}
					item["switch_inline_query_chosen_chat"] = chosen
				default:
					item["switch_inline_query"] = button.Query
				}
			case domain.MarkupButtonCopy:
				item["copy_text"] = map[string]any{"text": button.CopyText}
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

func apiMessageMedia(media *domain.MessageMedia, users map[int64]domain.User, channels map[int64]domain.Channel) map[string]any {
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
		if media.LivePhotoVideo != nil {
			live := apiDocument(*media.LivePhotoVideo)
			live["photo"] = photos
			for _, attribute := range media.LivePhotoVideo.Attributes {
				if attribute.Kind == domain.DocAttrVideo {
					live["width"], live["height"], live["duration"] = attribute.W, attribute.H, int(attribute.Duration)
					break
				}
			}
			return map[string]any{"live_photo": live}
		}
		return map[string]any{"photo": photos}
	case domain.MessageMediaKindDocument:
		if media.Document == nil {
			return nil
		}
		return apiDocumentMedia(*media.Document)
	case domain.MessageMediaKindContact:
		if media.Contact == nil {
			return nil
		}
		contact := map[string]any{
			"phone_number": media.Contact.PhoneNumber,
			"first_name":   media.Contact.FirstName,
		}
		if media.Contact.LastName != "" {
			contact["last_name"] = media.Contact.LastName
		}
		if media.Contact.Vcard != "" {
			contact["vcard"] = media.Contact.Vcard
		}
		if media.Contact.UserID != 0 {
			contact["user_id"] = media.Contact.UserID
		}
		return map[string]any{"contact": contact}
	case domain.MessageMediaKindGeo:
		if media.Geo == nil {
			return nil
		}
		return map[string]any{"location": apiLocation(*media.Geo, nil)}
	case domain.MessageMediaKindVenue:
		if media.Venue == nil {
			return nil
		}
		return map[string]any{"venue": apiVenue(*media.Venue)}
	case domain.MessageMediaKindGeoLive:
		if media.GeoLive == nil {
			return nil
		}
		return map[string]any{"location": apiLocation(media.GeoLive.Geo, media.GeoLive)}
	case domain.MessageMediaKindPoll:
		if media.Poll == nil {
			return nil
		}
		return map[string]any{"poll": apiPoll(*media.Poll, users)}
	case domain.MessageMediaKindService:
		if media.ServiceAction == nil {
			return nil
		}
		switch media.ServiceAction.Kind {
		case domain.MessageServiceActionWebViewDataSent:
			if media.ServiceAction.WebViewData == nil {
				return nil
			}
			return map[string]any{"web_app_data": map[string]any{
				"data": media.ServiceAction.WebViewData.Data, "button_text": media.ServiceAction.WebViewData.ButtonText,
			}}
		case domain.MessageServiceActionRequestedPeer:
			return apiRequestedPeer(media.ServiceAction.RequestedPeer, users, channels)
		default:
			return nil
		}
	default:
		return nil
	}
}

func apiDocumentMedia(document domain.Document) map[string]any {
	base := apiDocument(document)
	for _, attribute := range document.Attributes {
		switch attribute.Kind {
		case domain.DocAttrSticker:
			sticker := cloneAPIMap(base)
			sticker["type"], sticker["width"], sticker["height"] = "regular", attribute.W, attribute.H
			sticker["is_animated"] = hasDocumentAttribute(document, domain.DocAttrAnimated)
			sticker["is_video"] = hasDocumentAttribute(document, domain.DocAttrVideo)
			if attribute.Alt != "" {
				sticker["emoji"] = attribute.Alt
			}
			return map[string]any{"sticker": sticker}
		case domain.DocAttrAudio:
			audio := cloneAPIMap(base)
			audio["duration"] = attribute.AudioDuration
			if attribute.Voice {
				return map[string]any{"voice": audio}
			}
			if attribute.Title != "" {
				audio["title"] = attribute.Title
			}
			if attribute.Performer != "" {
				audio["performer"] = attribute.Performer
			}
			return map[string]any{"audio": audio}
		case domain.DocAttrVideo:
			video := cloneAPIMap(base)
			video["width"], video["height"], video["duration"] = attribute.W, attribute.H, int(attribute.Duration)
			if attribute.RoundMessage {
				video["length"] = attribute.W
				delete(video, "width")
				delete(video, "height")
				return map[string]any{"video_note": video}
			}
			if hasDocumentAttribute(document, domain.DocAttrAnimated) {
				return map[string]any{"animation": video, "document": base}
			}
			return map[string]any{"video": video}
		}
	}
	return map[string]any{"document": base}
}

func hasDocumentAttribute(document domain.Document, kind domain.DocumentAttributeKind) bool {
	for _, attribute := range document.Attributes {
		if attribute.Kind == kind {
			return true
		}
	}
	return false
}

func cloneAPIMap(input map[string]any) map[string]any {
	out := make(map[string]any, len(input)+4)
	for key, value := range input {
		out[key] = value
	}
	return out
}

func apiLocation(geo domain.MessageGeoPoint, live *domain.MessageGeoLive) map[string]any {
	out := map[string]any{"latitude": geo.Lat, "longitude": geo.Long}
	if geo.AccuracyRadius > 0 {
		out["horizontal_accuracy"] = float64(geo.AccuracyRadius)
	}
	if live != nil {
		if live.Period > 0 {
			out["live_period"] = live.Period
		}
		if live.Heading > 0 {
			out["heading"] = live.Heading
		}
		if live.ProximityNotificationRadius > 0 {
			out["proximity_alert_radius"] = live.ProximityNotificationRadius
		}
	}
	return out
}

func apiVenue(venue domain.MessageVenue) map[string]any {
	out := map[string]any{
		"location": apiLocation(venue.Geo, nil), "title": venue.Title, "address": venue.Address,
	}
	switch strings.ToLower(venue.Provider) {
	case "foursquare":
		if venue.VenueID != "" {
			out["foursquare_id"] = venue.VenueID
		}
		if venue.VenueType != "" {
			out["foursquare_type"] = venue.VenueType
		}
	case "gplaces", "google":
		if venue.VenueID != "" {
			out["google_place_id"] = venue.VenueID
		}
		if venue.VenueType != "" {
			out["google_place_type"] = venue.VenueType
		}
	}
	return out
}

func apiPoll(poll domain.MessagePoll, users map[int64]domain.User) map[string]any {
	resultByOption := make(map[string]domain.MessagePollAnswerVoters)
	totalVoters := 0
	if poll.Results != nil {
		totalVoters = poll.Results.TotalVoters
		for _, result := range poll.Results.Voters {
			resultByOption[string(result.Option)] = result
		}
	}
	options := make([]map[string]any, 0, len(poll.Answers))
	correct := make([]int, 0, len(poll.Answers))
	for index, answer := range poll.Answers {
		persistentID := base64.RawURLEncoding.EncodeToString(answer.Option)
		if persistentID == "" {
			persistentID = strconv.Itoa(index)
		}
		result := resultByOption[string(answer.Option)]
		option := map[string]any{
			"persistent_id": persistentID, "text": answer.Text, "voter_count": result.Voters,
		}
		if entities := apiMessageEntities(answer.Entities, users); len(entities) > 0 {
			option["text_entities"] = entities
		}
		if answer.Media != nil {
			if projected := apiPollMedia(answer.Media); len(projected) > 0 {
				option["media"] = projected
			}
		}
		if result.Correct {
			correct = append(correct, index)
		}
		options = append(options, option)
	}
	pollType := "regular"
	if poll.Quiz {
		pollType = "quiz"
	}
	out := map[string]any{
		"id": strconv.FormatInt(poll.ID, 10), "question": poll.Question,
		"options": options, "total_voter_count": totalVoters, "is_closed": poll.Closed,
		"is_anonymous": !poll.PublicVoters, "type": pollType,
		"allows_multiple_answers": poll.MultipleChoice, "allows_revoting": !poll.RevotingDisabled,
	}
	if entities := apiMessageEntities(poll.QuestionEntities, users); len(entities) > 0 {
		out["question_entities"] = entities
	}
	if len(correct) > 0 {
		out["correct_option_ids"] = correct
	}
	if poll.Results != nil && poll.Results.Solution != "" {
		out["explanation"] = poll.Results.Solution
		if entities := apiMessageEntities(poll.Results.SolutionEntities, users); len(entities) > 0 {
			out["explanation_entities"] = entities
		}
	}
	if poll.ClosePeriod > 0 {
		out["open_period"] = poll.ClosePeriod
	}
	if poll.CloseDate > 0 {
		out["close_date"] = poll.CloseDate
	}
	if poll.AttachedMedia != nil {
		if projected := apiPollMedia(poll.AttachedMedia); len(projected) > 0 {
			out["media"] = projected
		}
	}
	return out
}

func apiPollMedia(media *domain.MessageMedia) map[string]any {
	if media.IsZero() {
		return nil
	}
	switch media.Kind {
	case domain.MessageMediaKindPhoto:
		if media.Photo != nil {
			if sizes := apiPhotoSizes(*media.Photo); len(sizes) > 0 {
				return map[string]any{"photo": sizes}
			}
		}
	case domain.MessageMediaKindDocument:
		if media.Document != nil {
			return map[string]any{"document": apiDocument(*media.Document)}
		}
	case domain.MessageMediaKindGeo:
		if media.Geo != nil {
			return map[string]any{"location": apiLocation(*media.Geo, nil)}
		}
	case domain.MessageMediaKindVenue:
		if media.Venue != nil {
			return map[string]any{"venue": apiVenue(*media.Venue)}
		}
	}
	return nil
}

func apiRequestedPeer(action *domain.MessageRequestedPeerAction, _ map[int64]domain.User, _ map[int64]domain.Channel) map[string]any {
	if action == nil || action.ButtonID == 0 || len(action.Peers) == 0 {
		return nil
	}
	allUsers := true
	details := make(map[domain.Peer]domain.MessageRequestedPeerDetails, len(action.Details))
	for _, detail := range action.Details {
		details[detail.Peer] = detail
	}
	for _, peer := range action.Peers {
		if peer.ID == 0 || (peer.Type != domain.PeerTypeUser && peer.Type != domain.PeerTypeChannel) {
			return nil
		}
		allUsers = allUsers && peer.Type == domain.PeerTypeUser
	}
	if allUsers {
		shared := make([]map[string]any, 0, len(action.Peers))
		for _, peer := range action.Peers {
			item := map[string]any{"user_id": peer.ID}
			detail := details[peer]
			if action.NameRequested {
				if detail.FirstName != "" {
					item["first_name"] = detail.FirstName
				}
				if detail.LastName != "" {
					item["last_name"] = detail.LastName
				}
			}
			if action.UsernameRequested && detail.Username != "" {
				item["username"] = detail.Username
			}
			if action.PhotoRequested && detail.Photo != nil {
				if photo := apiPhotoSizes(*detail.Photo); len(photo) > 0 {
					item["photo"] = photo
				}
			}
			shared = append(shared, item)
		}
		return map[string]any{"users_shared": map[string]any{"request_id": action.ButtonID, "users": shared}}
	}
	if len(action.Peers) != 1 || action.Peers[0].Type != domain.PeerTypeChannel {
		return nil
	}
	peer := action.Peers[0]
	shared := map[string]any{"request_id": action.ButtonID, "chat_id": -1000000000000 - peer.ID}
	detail := details[peer]
	if action.NameRequested && detail.Title != "" {
		shared["title"] = detail.Title
	}
	if action.UsernameRequested && detail.Username != "" {
		shared["username"] = detail.Username
	}
	if action.PhotoRequested && detail.Photo != nil {
		if photo := apiPhotoSizes(*detail.Photo); len(photo) > 0 {
			shared["photo"] = photo
		}
	}
	return map[string]any{"chat_shared": shared}
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
