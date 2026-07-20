package botapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"telesrv/internal/domain"
)

const maxBotAPICommands = 100

func validateDefaultBotCommandScope(values map[string]string) error {
	if strings.TrimSpace(values["language_code"]) != "" {
		return errors.New("BOT_COMMAND_SCOPE_UNSUPPORTED")
	}
	raw := strings.TrimSpace(values["scope"])
	if raw == "" {
		return nil
	}
	var scope struct {
		Type string `json:"type"`
	}
	if json.Unmarshal([]byte(raw), &scope) != nil || scope.Type != "default" {
		return errors.New("BOT_COMMAND_SCOPE_UNSUPPORTED")
	}
	return nil
}

func (h *handler) setMyCommands(w http.ResponseWriter, r *http.Request, botID int64) {
	values, err := requestValues(r)
	if err != nil || h.bots == nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	if err := validateDefaultBotCommandScope(values); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	var input []struct {
		Command     string `json:"command"`
		Description string `json:"description"`
		IsEphemeral bool   `json:"is_ephemeral"`
	}
	if json.Unmarshal([]byte(values["commands"]), &input) != nil || len(input) > maxBotAPICommands {
		writeAPIError(w, http.StatusBadRequest, "BOT_COMMAND_INVALID")
		return
	}
	commands := make([]domain.BotCommand, 0, len(input))
	for _, command := range input {
		commands = append(commands, domain.BotCommand{
			Command: command.Command, Description: command.Description, Ephemeral: command.IsEphemeral,
		})
	}
	if _, err := h.bots.SetBotCommands(r.Context(), botID, commands); err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, true)
}

func (h *handler) deleteMyCommands(w http.ResponseWriter, r *http.Request, botID int64) {
	values, err := requestValues(r)
	if err != nil || h.bots == nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	if err := validateDefaultBotCommandScope(values); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := h.bots.SetBotCommands(r.Context(), botID, nil); err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, true)
}

func (h *handler) getMyCommands(w http.ResponseWriter, r *http.Request, botID int64) {
	values, err := requestValues(r)
	if err != nil || h.bots == nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	if err := validateDefaultBotCommandScope(values); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	commands, err := h.bots.GetBotCommands(r.Context(), botID)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	out := make([]map[string]any, 0, len(commands))
	for _, command := range commands {
		item := map[string]any{"command": command.Command, "description": command.Description}
		if command.Ephemeral {
			item["is_ephemeral"] = true
		}
		out = append(out, item)
	}
	writeAPIOK(w, out)
}
