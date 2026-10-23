package main

import (
	"net/http"

	"telesrv/internal/admin"
)

func (s *server) handleAutoSubscribeChannelsAPI(w http.ResponseWriter, r *http.Request) {
	s.proxyAdminJSON(w, r, "/v1/channels/auto-subscribe", 1<<20)
}

type addAutoSubscribeChannelAPIRequest struct {
	CommandID string    `json:"command_id"`
	Reason    string    `json:"reason"`
	Confirm   bool      `json:"confirm"`
	ChannelID flexInt64 `json:"channel_id"`
}

func (s *server) handleAddAutoSubscribeChannelAPI(w http.ResponseWriter, r *http.Request) {
	var body addAutoSubscribeChannelAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.AddAutoSubscribeChannelRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "add-auto-subscribe-channel"),
		ChannelID:   body.ChannelID.Int64(),
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/channels/auto-subscribe/add", req)
	writeCommandResultAPI(w, result, err)
}

type removeAutoSubscribeChannelAPIRequest struct {
	CommandID string    `json:"command_id"`
	Reason    string    `json:"reason"`
	Confirm   bool      `json:"confirm"`
	ChannelID flexInt64 `json:"channel_id"`
}

func (s *server) handleRemoveAutoSubscribeChannelAPI(w http.ResponseWriter, r *http.Request) {
	var body removeAutoSubscribeChannelAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.RemoveAutoSubscribeChannelRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "remove-auto-subscribe-channel"),
		ChannelID:   body.ChannelID.Int64(),
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/channels/auto-subscribe/remove", req)
	writeCommandResultAPI(w, result, err)
}
