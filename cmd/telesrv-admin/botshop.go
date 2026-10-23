package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// The numbers bot shop price list: read and saved through the admin API.

func (s *server) handleBotShopAPI(w http.ResponseWriter, r *http.Request) {
	s.proxyAdminJSONNoStore(w, r, "/v1/bot-shop", 1<<20)
}

func (s *server) handleSaveBotShopAPI(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Config json.RawMessage `json:"config"`
	}
	if !decodeAction(w, r, &body) {
		return
	}
	payload, err := json.Marshal(map[string]any{"actor": actorFromContext(r.Context()), "config": body.Config})
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.cfg.AdminAPIURL+"/v1/bot-shop", bytes.NewReader(payload))
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.cfg.AdminAPIToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = string(raw)
		}
		writeAPIError(w, resp.StatusCode, e.Error)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}
