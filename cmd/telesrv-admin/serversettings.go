package main

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"telesrv/internal/admin"
	"telesrv/internal/domain"
	"telesrv/internal/identity"
)

// serverManage gates the whole Server Settings surface -- see
// permissionServerManage's doc comment in security.go for why this is one
// right rather than split review/manage like other sections.
func (s *server) serverManage(handler http.HandlerFunc) http.Handler {
	return s.requireAuthAPI(s.requirePermission(permissionServerManage, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if operatorIDFromContext(r.Context()) != 0 {
			writeAPIError(w, http.StatusForbidden, "server settings require the break-glass operator")
			return
		}
		handler.ServeHTTP(w, r)
	})))
}

// serverCommandResult builds the same admin.CommandResult shape every other
// action returns, without going through internal/admin's runCommand +
// Postgres audit log: everything in this file operates on local files (the
// identity.json store and .env) or probes local services, so there is no
// admin_commands row to write. The actor/reason are still in meta for
// structured logging if that's ever added; today they are simply not
// persisted anywhere.
func serverCommandResult(meta admin.CommandMeta, action string, err error, message string, details map[string]any) admin.CommandResult {
	status := "completed"
	errText := ""
	if err != nil {
		status = "failed"
		errText = err.Error()
		if message == "" {
			message = "command failed"
		}
	}
	return admin.CommandResult{
		CommandID: meta.CommandID,
		Action:    action,
		Status:    status,
		DryRun:    meta.DryRun,
		Message:   message,
		Details:   details,
		Error:     errText,
	}
}

// --- identity (name/description/icon) ---------------------------------

// serverIdentityAPIResponse extends identity.Info's raw fields with the three
// *effective* fallback templates -- s.cfg's WelcomeMessage{Phone,Email}Default
// and LoginCodeMessageDefault, i.e. this admin process's own reading of the
// TELESRV_WELCOME_MESSAGE_*_TEMPLATE / TELESRV_LOGIN_CODE_MESSAGE_TEMPLATE
// env vars (themselves defaulting to the compiled-in copies), which matches
// what the telesrv server falls back to whenever the panel override is unset,
// as long as both processes share the same .env (see
// uiConfig.WelcomeMessagePhoneDefault's doc comment). The panel needs both:
// the raw override (possibly empty) to know whether a field is "explicitly
// set", and the default text to show as "(using default: ...)" / to restore
// on Reset.
type serverIdentityAPIResponse struct {
	identity.Info
	DefaultWelcomeMessagePhoneTemplate string `json:"default_welcome_message_phone_template"`
	DefaultWelcomeMessageEmailTemplate string `json:"default_welcome_message_email_template"`
	// DefaultLoginCodeMessageTemplate is the effective fallback text for
	// the login-code delivery message (s.cfg.LoginCodeMessageDefault) --
	// same "raw override + effective default" contract as the two fields
	// above, see their doc comment.
	DefaultLoginCodeMessageTemplate string `json:"default_login_code_message_template"`
}

func (s *server) handleServerIdentityAPI(w http.ResponseWriter, r *http.Request) {
	info, err := s.identity.Get()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, serverIdentityAPIResponse{
		Info:                               info,
		DefaultWelcomeMessagePhoneTemplate: s.cfg.WelcomeMessagePhoneDefault,
		DefaultWelcomeMessageEmailTemplate: s.cfg.WelcomeMessageEmailDefault,
		DefaultLoginCodeMessageTemplate:    s.cfg.LoginCodeMessageDefault,
	})
}

// handleServerIconAPI serves the icon's raw bytes for the panel's own
// preview. The same icon is also public: the server publishes it next to the
// RSA key on the MTProto port at /owpengram/server-icon (internal/serverinfo),
// so nothing uploaded here may be private.
func (s *server) handleServerIconAPI(w http.ResponseWriter, r *http.Request) {
	data, ext, ok := s.identity.Icon()
	if !ok {
		writeAPIError(w, http.StatusNotFound, "no icon configured")
		return
	}
	contentType := map[string]string{
		".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
		".webp": "image/webp", ".gif": "image/gif",
	}[ext]
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// handlePublicServerIconAPI serves the brand mark to the unauthenticated login
// screen. See the route comment in server.go for why this one is public and
// what it is allowed to disclose. It shares the Content-Type table with the
// gated variant so the login page gets the same bytes either way.
func (s *server) handlePublicServerIconAPI(w http.ResponseWriter, _ *http.Request) {
	data, ext, ok := s.identity.Icon()
	if !ok {
		writeAPIError(w, http.StatusNotFound, "no icon configured")
		return
	}
	contentType := map[string]string{
		".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
		".webp": "image/webp", ".gif": "image/gif",
	}[ext]
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

type setServerIdentityAPIRequest struct {
	CommandID   string `json:"command_id"`
	Reason      string `json:"reason"`
	Confirm     bool   `json:"confirm"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (s *server) handleSetServerIdentityAPI(w http.ResponseWriter, r *http.Request) {
	var body setServerIdentityAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	meta := s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "set-server-identity")
	details := map[string]any{"name": body.Name, "description": body.Description}
	if meta.DryRun {
		writeJSON(w, http.StatusOK, serverCommandResult(meta, "server.set_identity", nil, "server identity validated", details))
		return
	}
	err := s.identity.SetText(body.Name, body.Description)
	writeJSON(w, http.StatusOK, serverCommandResult(meta, "server.set_identity", err, "server identity updated", details))
}

// --- login-notification templates ---------------------------------------

type setWelcomeMessageTemplatesAPIRequest struct {
	CommandID     string `json:"command_id"`
	Reason        string `json:"reason"`
	Confirm       bool   `json:"confirm"`
	PhoneTemplate string `json:"phone_template"`
	EmailTemplate string `json:"email_template"`
}

// handleSetWelcomeMessageTemplatesAPI sets (or, with an empty string,
// clears) the admin-panel override for the 777000 login-notification
// message's phone/email template -- see identity.Store.SetWelcomeMessageTemplates
// and domain.ResolveWelcomeMessageTemplate. Deliberately a separate endpoint
// from set-server-identity: brand identity (name/description/icon) and
// login-notification copy are different concerns that happen to share the
// same on-disk identity.json, and keeping them as separate actions/buttons
// means editing one never risks silently blanking the other.
func (s *server) handleSetWelcomeMessageTemplatesAPI(w http.ResponseWriter, r *http.Request) {
	var body setWelcomeMessageTemplatesAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	meta := s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "set-welcome-message-templates")
	details := map[string]any{
		"phone_template_set": strings.TrimSpace(body.PhoneTemplate) != "",
		"email_template_set": strings.TrimSpace(body.EmailTemplate) != "",
	}
	if meta.DryRun {
		writeJSON(w, http.StatusOK, serverCommandResult(meta, "server.set_welcome_message_templates", nil, "login-notification templates validated", details))
		return
	}
	err := s.identity.SetWelcomeMessageTemplates(body.PhoneTemplate, body.EmailTemplate)
	writeJSON(w, http.StatusOK, serverCommandResult(meta, "server.set_welcome_message_templates", err, "login-notification templates updated", details))
}

// --- login-code delivery message template --------------------------------

type setLoginCodeMessageTemplateAPIRequest struct {
	CommandID string `json:"command_id"`
	Reason    string `json:"reason"`
	Confirm   bool   `json:"confirm"`
	Template  string `json:"template"`
}

// handleSetLoginCodeMessageTemplateAPI sets (or, with an empty string,
// clears) the admin-panel override for the 777000 login-code delivery
// message -- see identity.Store.SetLoginCodeMessageTemplate and
// domain.ResolveLoginCodeMessageTemplate. A dedicated endpoint (not folded
// into set-welcome-message-templates): this message embeds the actual OTP
// code via the {{code}} placeholder, so a save here carries an extra,
// security-relevant validation the login-notification templates don't
// need -- a template missing {{code}} (or containing it more than once)
// would either silently drop the code from the message or leave it
// ambiguous which occurrence carries it, so it is rejected outright with a
// 422 rather than saved. Clearing the override (empty string) is exempt --
// it always resolves to a valid built-in/env default.
func (s *server) handleSetLoginCodeMessageTemplateAPI(w http.ResponseWriter, r *http.Request) {
	var body setLoginCodeMessageTemplateAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	if t := strings.TrimSpace(body.Template); t != "" {
		if err := domain.ValidateLoginCodeMessageTemplate(t); err != nil {
			writeAPIError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	}
	meta := s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "set-login-code-message-template")
	details := map[string]any{"template_set": strings.TrimSpace(body.Template) != ""}
	if meta.DryRun {
		writeJSON(w, http.StatusOK, serverCommandResult(meta, "server.set_login_code_message_template", nil, "login-code message template validated", details))
		return
	}
	err := s.identity.SetLoginCodeMessageTemplate(body.Template)
	writeJSON(w, http.StatusOK, serverCommandResult(meta, "server.set_login_code_message_template", err, "login-code message template updated", details))
}

// --- server icon upload/remove -------------------------------------------

var allowedServerIconExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true,
}

const maxServerIconBytes = 2 << 20 // 2 MiB

type uploadServerIconAPIRequest struct {
	CommandID string `json:"command_id"`
	Reason    string `json:"reason"`
	Confirm   bool   `json:"confirm"`
}

// handleUploadServerIconAPI takes multipart/form-data (a "metadata" JSON
// field + a "file" field), the same shape handleSetAccountAvatarAPI uses --
// deliberately not JSON+base64 like the other Server Settings actions:
// base64 inflates a file ~33%, and decodeAction's plain io.LimitReader caps
// the request body at 1MiB regardless of maxServerIconBytes, so a real
// multi-hundred-KB icon would fail decoding ("unexpected EOF" from the
// truncated body) before this handler ever saw it. Multipart sidesteps that
// entirely -- the size cap below is enforced on the actual file bytes.
func (s *server) handleUploadServerIconAPI(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, maxServerIconBytes+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid multipart form: "+err.Error())
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	var body uploadServerIconAPIRequest
	dec := json.NewDecoder(strings.NewReader(r.FormValue("metadata")))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid metadata: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "icon file is required")
		return
	}
	defer file.Close()
	ext := strings.ToLower(filepath.Ext(header.Filename))
	if !allowedServerIconExts[ext] {
		writeAPIError(w, http.StatusBadRequest, "unsupported icon extension")
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, maxServerIconBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxServerIconBytes {
		writeAPIError(w, http.StatusBadRequest, "icon file is empty or too large (max 2MiB)")
		return
	}
	meta := s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "upload-server-icon")
	details := map[string]any{"bytes": len(data), "ext": ext}
	if meta.DryRun {
		writeJSON(w, http.StatusOK, serverCommandResult(meta, "server.upload_icon", nil, "server icon validated", details))
		return
	}
	setErr := s.identity.SetIcon(data, ext)
	writeJSON(w, http.StatusOK, serverCommandResult(meta, "server.upload_icon", setErr, "server icon updated", details))
}

type removeServerIconAPIRequest struct {
	CommandID string `json:"command_id"`
	Reason    string `json:"reason"`
	Confirm   bool   `json:"confirm"`
}

func (s *server) handleRemoveServerIconAPI(w http.ResponseWriter, r *http.Request) {
	var body removeServerIconAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	meta := s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "remove-server-icon")
	if meta.DryRun {
		writeJSON(w, http.StatusOK, serverCommandResult(meta, "server.remove_icon", nil, "server icon removal validated", nil))
		return
	}
	err := s.identity.RemoveIcon()
	writeJSON(w, http.StatusOK, serverCommandResult(meta, "server.remove_icon", err, "server icon removed", nil))
}
