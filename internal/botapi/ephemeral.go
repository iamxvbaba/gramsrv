package botapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"telesrv/internal/domain"
)

type ephemeralSendTarget struct {
	receiverUserID     int64
	callbackQueryID    int64
	replyToEphemeralID int
	topMessageID       int
}

func parseEphemeralSendTarget(values map[string]string) (ephemeralSendTarget, bool, error) {
	var result ephemeralSendTarget
	receiverRaw := strings.TrimSpace(values["receiver_user_id"])
	callbackRaw := strings.TrimSpace(values["callback_query_id"])
	var reply struct {
		MessageID          int `json:"message_id"`
		EphemeralMessageID int `json:"ephemeral_message_id"`
	}
	if raw := strings.TrimSpace(values["reply_parameters"]); raw != "" {
		if json.Unmarshal([]byte(raw), &reply) != nil || reply.MessageID < 0 || reply.EphemeralMessageID < 0 ||
			(reply.MessageID != 0 && reply.EphemeralMessageID != 0) {
			return result, false, errors.New("REPLY_PARAMETERS_INVALID")
		}
	}
	if receiverRaw == "" {
		if callbackRaw != "" || reply.EphemeralMessageID != 0 {
			return result, false, errors.New("USER_ID_INVALID")
		}
		return result, false, nil
	}
	receiver, err := strconv.ParseInt(receiverRaw, 10, 64)
	if err != nil || receiver <= 0 {
		return result, false, errors.New("USER_ID_INVALID")
	}
	result.receiverUserID = receiver
	result.replyToEphemeralID = reply.EphemeralMessageID
	if reply.MessageID != 0 {
		return result, false, errors.New("REPLY_PARAMETERS_INVALID")
	}
	if callbackRaw != "" {
		result.callbackQueryID, err = strconv.ParseInt(callbackRaw, 10, 64)
		if err != nil || result.callbackQueryID == 0 {
			return result, false, errors.New("QUERY_ID_INVALID")
		}
	}
	if result.callbackQueryID != 0 && result.replyToEphemeralID != 0 {
		return result, false, errors.New("REPLY_PARAMETERS_INVALID")
	}
	if raw := strings.TrimSpace(values["message_thread_id"]); raw != "" {
		result.topMessageID, err = strconv.Atoi(raw)
		if err != nil || result.topMessageID <= 0 || result.topMessageID > domain.MaxMessageBoxID {
			return result, false, errors.New("MESSAGE_THREAD_ID_INVALID")
		}
	}
	return result, true, nil
}

func botAPIFileInput(raw string, files map[string]uploadedFile, field string, values map[string]string) (domain.BotAPIFileInput, bool) {
	locationKey, remoteURL, fileName, mimeType, fileBytes, ok := mediaInput(raw, files, field)
	if !ok {
		return domain.BotAPIFileInput{}, false
	}
	return domain.BotAPIFileInput{
		LocationKey: locationKey, RemoteURL: remoteURL, FileName: fileName, MimeType: mimeType, Bytes: fileBytes,
		Width: apiInt(values["width"], 0), Height: apiInt(values["height"], 0), Duration: apiInt(values["duration"], 0),
		Title: values["title"], Performer: values["performer"], Emoji: values["emoji"],
	}, true
}

func (h *handler) writeEphemeralMessage(w http.ResponseWriter, r *http.Request, botID int64, message domain.EphemeralMessage) {
	users := make([]domain.User, 0, 1)
	if self, err := h.gateway.BotAPISelf(r.Context(), botID); err == nil && self.ID != 0 {
		users = append(users, self)
	}
	projected, ok := apiEphemeralMessage(message, users, nil)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR")
		return
	}
	writeAPIOK(w, projected)
}

func (h *handler) sendEphemeralContact(w http.ResponseWriter, r *http.Request, botID int64) {
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	target, ephemeral, err := parseEphemeralSendTarget(values)
	if err != nil || !ephemeral {
		if err == nil {
			err = errors.New("EPHEMERAL_TARGET_REQUIRED")
		}
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	chatID, ok := parsePositiveOrNegativeID(values["chat_id"])
	if !ok || strings.TrimSpace(values["phone_number"]) == "" || strings.TrimSpace(values["first_name"]) == "" || len(values["vcard"]) > 2048 {
		writeAPIError(w, http.StatusBadRequest, "MEDIA_INVALID")
		return
	}
	markup, _, err := optionalInlineReplyMarkup(values)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	gateway, ok := h.gateway.(EphemeralGatewayService)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	message, err := gateway.BotAPISendEphemeral(r.Context(), domain.BotAPIEphemeralSendInput{
		BotUserID: botID, ChatID: chatID, ReceiverUserID: target.receiverUserID,
		CallbackQueryID: target.callbackQueryID, ReplyToEphemeralID: target.replyToEphemeralID, TopMessageID: target.topMessageID,
		Kind: "contact", ReplyMarkup: markup, DirectMedia: &domain.MessageMedia{Kind: domain.MessageMediaKindContact, Contact: &domain.MessageContact{
			PhoneNumber: values["phone_number"], FirstName: values["first_name"], LastName: values["last_name"], Vcard: values["vcard"],
		}},
	})
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	h.writeEphemeralMessage(w, r, botID, message)
}

func (h *handler) sendEphemeralLocation(w http.ResponseWriter, r *http.Request, botID int64, venue bool) {
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	target, ephemeral, err := parseEphemeralSendTarget(values)
	if err != nil || !ephemeral {
		if err == nil {
			err = errors.New("EPHEMERAL_TARGET_REQUIRED")
		}
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	chatID, ok := parsePositiveOrNegativeID(values["chat_id"])
	latitude, latErr := strconv.ParseFloat(strings.TrimSpace(values["latitude"]), 64)
	longitude, longErr := strconv.ParseFloat(strings.TrimSpace(values["longitude"]), 64)
	accuracy, accuracyErr := strconv.ParseFloat(defaultString(values["horizontal_accuracy"], "0"), 64)
	if !ok || latErr != nil || longErr != nil || accuracyErr != nil || latitude < -90 || latitude > 90 || longitude < -180 || longitude > 180 || accuracy < 0 || accuracy > 1500 || apiInt(values["live_period"], 0) != 0 {
		writeAPIError(w, http.StatusBadRequest, "MEDIA_INVALID")
		return
	}
	markup, _, err := optionalInlineReplyMarkup(values)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	geo := domain.MessageGeoPoint{Lat: latitude, Long: longitude, AccuracyRadius: int(accuracy)}
	media := &domain.MessageMedia{Kind: domain.MessageMediaKindGeo, Geo: &geo}
	if venue {
		if strings.TrimSpace(values["title"]) == "" || strings.TrimSpace(values["address"]) == "" {
			writeAPIError(w, http.StatusBadRequest, "MEDIA_INVALID")
			return
		}
		provider, venueID, venueType := "", "", ""
		if values["foursquare_id"] != "" || values["foursquare_type"] != "" {
			provider, venueID, venueType = "foursquare", values["foursquare_id"], values["foursquare_type"]
		} else if values["google_place_id"] != "" || values["google_place_type"] != "" {
			provider, venueID, venueType = "gplaces", values["google_place_id"], values["google_place_type"]
		}
		media = &domain.MessageMedia{Kind: domain.MessageMediaKindVenue, Venue: &domain.MessageVenue{
			Geo: geo, Title: values["title"], Address: values["address"], Provider: provider, VenueID: venueID, VenueType: venueType,
		}}
	}
	gateway, ok := h.gateway.(EphemeralGatewayService)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	message, err := gateway.BotAPISendEphemeral(r.Context(), domain.BotAPIEphemeralSendInput{
		BotUserID: botID, ChatID: chatID, ReceiverUserID: target.receiverUserID,
		CallbackQueryID: target.callbackQueryID, ReplyToEphemeralID: target.replyToEphemeralID, TopMessageID: target.topMessageID,
		Kind: "location", ReplyMarkup: markup, DirectMedia: media,
	})
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	h.writeEphemeralMessage(w, r, botID, message)
}

func (h *handler) editEphemeralMessage(w http.ResponseWriter, r *http.Request, botID int64, mode string) {
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	chatID, ok := parsePositiveOrNegativeID(values["chat_id"])
	receiverID, receiverErr := strconv.ParseInt(strings.TrimSpace(values["receiver_user_id"]), 10, 64)
	messageID := apiInt(values["ephemeral_message_id"], 0)
	if !ok || receiverErr != nil || receiverID <= 0 || messageID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "EPHEMERAL_MESSAGE_ID_INVALID")
		return
	}
	input := domain.BotAPIEphemeralEditInput{
		BotUserID: botID, ChatID: chatID, ReceiverUserID: receiverID, MessageID: messageID,
		Mode: domain.EphemeralEditMode(mode),
	}
	markup, markupSet, err := optionalInlineReplyMarkup(values)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	input.Fields.SetReplyMarkup, input.Fields.ReplyMarkup = markupSet, markup
	switch mode {
	case "text":
		text, entities, err := botAPIFormattedTextRaw(values["text"], values["parse_mode"], values["entities"], domain.MaxMessageTextLength, true)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		input.Fields.SetMessage, input.Fields.Message, input.Fields.Entities = true, text, entities
	case "caption":
		caption, entities, err := botAPIFormattedTextRaw(values["caption"], values["parse_mode"], values["caption_entities"], domain.MaxEphemeralCaptionLength, false)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		input.Fields.SetMessage, input.Fields.Message, input.Fields.Entities = true, caption, entities
	case "reply_markup":
		input.Fields.SetReplyMarkup = true
	case "media":
		if err := parseEphemeralEditMedia(values["media"], &input); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
	default:
		writeAPIError(w, http.StatusNotFound, "METHOD_NOT_FOUND")
		return
	}
	gateway, ok := h.gateway.(EphemeralGatewayService)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	result, err := gateway.BotAPIEditEphemeral(r.Context(), input)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, result)
}

func parseEphemeralEditMedia(raw string, input *domain.BotAPIEphemeralEditInput) error {
	var media struct {
		Type            string          `json:"type"`
		Media           string          `json:"media"`
		Photo           string          `json:"photo"`
		Caption         string          `json:"caption"`
		ParseMode       string          `json:"parse_mode"`
		CaptionEntities json.RawMessage `json:"caption_entities"`
		Width           int             `json:"width"`
		Height          int             `json:"height"`
		Duration        int             `json:"duration"`
		Title           string          `json:"title"`
		Performer       string          `json:"performer"`
	}
	if input == nil || json.Unmarshal([]byte(raw), &media) != nil || media.Type == "" {
		return errors.New("MEDIA_INVALID")
	}
	allowed := map[string]bool{"animation": true, "audio": true, "document": true, "live_photo": true, "photo": true, "video": true}
	if !allowed[media.Type] {
		return errors.New("MEDIA_INVALID")
	}
	primaryRaw := media.Media
	if media.Type == "live_photo" {
		primaryRaw = media.Photo
	}
	primary, ok := botAPIFileInput(primaryRaw, nil, "", map[string]string{
		"width": strconv.Itoa(media.Width), "height": strconv.Itoa(media.Height), "duration": strconv.Itoa(media.Duration),
		"title": media.Title, "performer": media.Performer,
	})
	if !ok || len(primary.Bytes) != 0 {
		return errors.New("FILE_ID_INVALID")
	}
	input.MediaKind, input.File = media.Type, primary
	if media.Type == "live_photo" {
		secondary, ok := botAPIFileInput(media.Media, nil, "", map[string]string{"duration": strconv.Itoa(media.Duration)})
		if !ok || len(secondary.Bytes) != 0 {
			return errors.New("FILE_ID_INVALID")
		}
		input.SecondaryFile = secondary
	}
	caption, entities, err := botAPIFormattedTextRaw(media.Caption, media.ParseMode, string(media.CaptionEntities), domain.MaxEphemeralCaptionLength, false)
	if err != nil {
		return err
	}
	input.Fields.SetMessage, input.Fields.Message, input.Fields.Entities = true, caption, entities
	return nil
}

func (h *handler) deleteEphemeralMessage(w http.ResponseWriter, r *http.Request, botID int64) {
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	chatID, ok := parsePositiveOrNegativeID(values["chat_id"])
	receiverID, receiverErr := strconv.ParseInt(strings.TrimSpace(values["receiver_user_id"]), 10, 64)
	messageID := apiInt(values["ephemeral_message_id"], 0)
	if !ok || receiverErr != nil || receiverID <= 0 || messageID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "EPHEMERAL_MESSAGE_ID_INVALID")
		return
	}
	gateway, ok := h.gateway.(EphemeralGatewayService)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	result, err := gateway.BotAPIDeleteEphemeral(r.Context(), botID, chatID, receiverID, messageID)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, result)
}

func optionalInlineReplyMarkup(values map[string]string) (*domain.MessageReplyMarkup, bool, error) {
	raw, exists := values["reply_markup"]
	if !exists || strings.TrimSpace(raw) == "" {
		return nil, exists, nil
	}
	markup, err := inlineReplyMarkupFromAPI(json.RawMessage(raw))
	return markup, true, err
}

func parsePositiveOrNegativeID(raw string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return id, err == nil && id != 0
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
