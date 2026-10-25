package botapi

import (
	"net/http"
	"strings"

	"telesrv/internal/domain"
)

// sendChatAction implements the Bot API sendChatAction: a transient typing-style
// status in a private chat or a channel/supergroup the bot belongs to.
func (h *handler) sendChatAction(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, ok := h.gateway.(MessageActionGatewayService)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	if strings.TrimSpace(values["business_connection_id"]) != "" {
		writeAPIError(w, http.StatusBadRequest, "BUSINESS_CONNECTION_INVALID")
		return
	}
	chatID, ok := parsePositiveOrNegativeID(values["chat_id"])
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "CHAT_ID_INVALID")
		return
	}
	action := domain.BotAPIChatAction(strings.TrimSpace(values["action"]))
	if !action.Valid() {
		writeAPIError(w, http.StatusBadRequest, "ACTION_INVALID")
		return
	}
	threadID := apiInt(values["message_thread_id"], 0)
	if threadID < 0 || threadID > domain.MaxMessageBoxID {
		writeAPIError(w, http.StatusBadRequest, "MESSAGE_THREAD_ID_INVALID")
		return
	}
	sent, err := gateway.BotAPISendChatAction(r.Context(), botID, chatID, action, threadID)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, sent)
}

// editMessageCaption implements the Bot API editMessageCaption for a message
// identified by chat_id + message_id. An omitted caption removes it, as in the
// official Bot API.
func (h *handler) editMessageCaption(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, ok := h.gateway.(MessageActionGatewayService)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	chatID, messageID, ok := messageActionTarget(w, values)
	if !ok {
		return
	}
	caption, entities, err := botAPIFormattedTextRaw(values["caption"], values["parse_mode"], values["caption_entities"], domain.MaxEphemeralCaptionLength, false)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	markup, setMarkup, err := optionalInlineReplyMarkup(values)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	msg, err := gateway.BotAPIEditMessageCaption(r.Context(), botID, chatID, messageID, caption, entities, setMarkup, markup)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	h.writeEditedMessage(w, r, botID, msg)
}

// editMessageReplyMarkup implements the Bot API editMessageReplyMarkup for a
// message identified by chat_id + message_id. An omitted reply_markup removes
// the inline keyboard, as in the official Bot API.
func (h *handler) editMessageReplyMarkup(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, ok := h.gateway.(MessageActionGatewayService)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	chatID, messageID, ok := messageActionTarget(w, values)
	if !ok {
		return
	}
	markup, _, err := optionalInlineReplyMarkup(values)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	msg, err := gateway.BotAPIEditMessageReplyMarkup(r.Context(), botID, chatID, messageID, markup)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	h.writeEditedMessage(w, r, botID, msg)
}

// messageActionTarget reads chat_id + message_id. inline_message_id is not
// supported by these two edit methods yet, so it is refused rather than
// ignored.
func messageActionTarget(w http.ResponseWriter, values map[string]string) (int64, int, bool) {
	if strings.TrimSpace(values["inline_message_id"]) != "" {
		writeAPIError(w, http.StatusBadRequest, "INLINE_MESSAGE_ID_UNSUPPORTED")
		return 0, 0, false
	}
	if strings.TrimSpace(values["business_connection_id"]) != "" {
		writeAPIError(w, http.StatusBadRequest, "BUSINESS_CONNECTION_INVALID")
		return 0, 0, false
	}
	chatID, ok := parsePositiveOrNegativeID(values["chat_id"])
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "CHAT_ID_INVALID")
		return 0, 0, false
	}
	messageID := apiInt(values["message_id"], 0)
	if messageID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "MESSAGE_ID_INVALID")
		return 0, 0, false
	}
	return chatID, messageID, true
}

func (h *handler) writeEditedMessage(w http.ResponseWriter, r *http.Request, botID int64, msg domain.Message) {
	users := []domain.User(nil)
	if self, err := h.gateway.BotAPISelf(r.Context(), botID); err == nil && self.ID != 0 {
		users = append(users, self)
	}
	writeAPIOK(w, apiMessage(msg, users))
}
