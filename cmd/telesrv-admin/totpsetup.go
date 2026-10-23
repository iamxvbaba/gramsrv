package main

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type totpSetupState struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *server) handleTOTPSetupQR(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" || s.cfg.TOTPSecret == "" {
		http.NotFound(w, r)
		return
	}
	base := filepath.Join(s.cfg.RepoRoot, "data")
	raw, err := os.ReadFile(filepath.Join(base, "totp-setup.json"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var state totpSetupState
	if err := json.Unmarshal(raw, &state); err != nil || state.Token == "" {
		http.NotFound(w, r)
		return
	}
	if time.Now().After(state.ExpiresAt) || subtle.ConstantTimeCompare([]byte(token), []byte(state.Token)) != 1 {
		http.NotFound(w, r)
		return
	}
	png, err := os.ReadFile(filepath.Join(base, "totp-setup.png"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(png)
}
