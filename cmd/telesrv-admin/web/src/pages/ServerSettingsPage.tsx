import { ImagePlus, Loader2, Server, Trash2, Upload, X } from "lucide-react";
import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { Alert, LoadingSurface, PageFrame, SectionHead } from "../components/ui";
import { useI18n } from "../i18n";
import type { ServerIdentity } from "../types";
import { withBase } from "../lib/base";

// Server Settings: admin-editable server name/description/icon and
// login-notification template overrides. See
// cmd/telesrv-admin/serversettings.go for the backend.
//
// v2 deliberately omits the main-branch .env editor and Services status tab:
// each v2 role reads its own YAML config, there is no repository-root
// .env/.env.example to edit, and the Admin container cannot probe the Core
// MTProto listener. ?focus=identity|login still jumps to a card.
export function ServerSettingsPage({ search }: { search?: URLSearchParams }) {
  const { t } = useI18n();

  // ?focus=identity|login rolls the matching card into view once the page is
  // mounted — a way to link an operator straight at "why didn't my login
  // message change" without making them hunt the accordion.
  const focus = search?.get("focus");
  useEffect(() => {
    if (!focus) return;
    const target = document.getElementById(`serversettings-${focus}`);
    if (target) {
      target.scrollIntoView({ behavior: "smooth", block: "start" });
    }
  }, [focus]);

  return (
    <PageFrame title={t("route.serverSettings")} eyebrow={t("serverSettings.eyebrow")}>
      <div className="stacked-sections">
        <IdentitySection />
      </div>
    </PageFrame>
  );
}

// --- Identity ---------------------------------------------------------

function IdentitySection() {
  const { t } = useI18n();
  const [identity, setIdentity] = useState<ServerIdentity | null>(null);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [iconModalOpen, setIconModalOpen] = useState(false);
  const [iconBust, setIconBust] = useState(0);
  const [iconFailed, setIconFailed] = useState(false);
  const [error, setError] = useState("");

  async function load() {
    setError("");
    try {
      const info = await api.serverIdentity();
      setIdentity(info);
      setName(info.name);
      setDescription(info.description);
      setIconFailed(false);
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  useEffect(() => { void load(); }, []);

  // Tells the shell to re-read identity so the sidebar/tab rebrand at once.
  function notifyIdentityChanged() {
    window.dispatchEvent(new Event("telesrv:identity-changed"));
  }

  return (
    <section id="serversettings-identity" className="section-block scroll-anchor">
      <SectionHead title={t("serverSettings.identityTitle")} />
      {error && <Alert>{error}</Alert>}
      {!identity ? (
        <LoadingSurface label={t("serverSettings.loadingIdentity")} />
      ) : (
        <div className="card-body identity-card">
          <div className="identity-layout">
            <div className="avatar-edit-slot">
              {identity.icon_ext && !iconFailed ? (
                <img
                  className="avatar-photo-img"
                  src={api.serverIconURL(iconBust)}
                  alt=""
                  style={{ width: 88, height: 88 }}
                  onError={() => setIconFailed(true)}
                />
              ) : (
                <img
                  className="avatar-photo-img server-icon-default"
                  src={withBase("/logo.png")}
                  alt=""
                  style={{ width: 88, height: 88 }}
                />
              )}
              <button
                className="icon-btn avatar-edit-btn"
                type="button"
                aria-label={t("serverSettings.changeIcon")}
                title={t("serverSettings.changeIcon")}
                onClick={() => setIconModalOpen(true)}
              >
                <ImagePlus size={14} />
              </button>
            </div>
            <div className="server-identity-fields">
              <label className="form-field"><span>{t("serverSettings.name")}</span><input value={name} maxLength={128} onChange={(event) => setName(event.target.value)} /></label>
              <label className="form-field"><span>{t("serverSettings.description")}</span><textarea rows={4} value={description} maxLength={512} onChange={(event) => setDescription(event.target.value)} /></label>
            </div>
          </div>
          <div className="gift-table-actions identity-save-row">
            <ActionButton
              tone="neutral"
              label={t("serverSettings.saveIdentity")}
              path="/api/actions/set-server-identity"
              payload={() => ({ name, description })}
              onDone={() => { notifyIdentityChanged(); void load(); }}
            />
          </div>
        </div>
      )}
      {iconModalOpen && (
        <ServerIconModal
          hasIcon={!!identity?.icon_ext}
          onClose={() => setIconModalOpen(false)}
          onDone={() => { notifyIdentityChanged(); setIconBust((n) => n + 1); setIconFailed(false); void load(); }}
        />
      )}
    </section>
  );
}

// --- Login notifications ------------------------------------------------

// LoginNotificationsSection edits the 777000 login-notification message's
// per-method (phone/email) template and the login-code delivery message — a
// different concern from brand identity above even though all three live in
// the same identity.json (see serversettings.go's
// handleSetWelcomeMessageTemplatesAPI / handleSetLoginCodeMessageTemplateAPI
// doc comments), so it gets its own card and its own save actions.
function LoginNotificationsSection() {
  const { t } = useI18n();
  const [identity, setIdentity] = useState<ServerIdentity | null>(null);
  const [phoneTemplate, setPhoneTemplate] = useState("");
  const [emailTemplate, setEmailTemplate] = useState("");
  const [codeTemplate, setCodeTemplate] = useState("");
  const [error, setError] = useState("");

  async function load() {
    setError("");
    try {
      const info = await api.serverIdentity();
      setIdentity(info);
      setPhoneTemplate(info.welcome_message_phone_template ?? "");
      setEmailTemplate(info.welcome_message_email_template ?? "");
      setCodeTemplate(info.login_code_message_template ?? "");
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  useEffect(() => { void load(); }, []);

  const phoneIsOverridden = phoneTemplate.trim() !== "";
  const emailIsOverridden = emailTemplate.trim() !== "";
  const codeIsOverridden = codeTemplate.trim() !== "";
  // Mirrors the server-side check in handleSetLoginCodeMessageTemplateAPI —
  // keep the save button closed instead of letting the operator submit a
  // template that would silently never deliver the actual OTP code.
  const codeOccurrences = (codeTemplate.match(/\{\{code\}\}/g) ?? []).length;
  const codeTemplateInvalid = codeIsOverridden && codeOccurrences !== 1;

  return (
    <section id="serversettings-login" className="section-block scroll-anchor">
      <SectionHead title={t("serverSettings.loginTitle")} />
      {error && <Alert>{error}</Alert>}
      {!identity ? (
        <LoadingSurface label={t("serverSettings.loadingTemplates")} />
      ) : (
        <div className="card-body">
          <p className="muted-lead">
            {t("serverSettings.loginDescBefore")}
            <code>{"{{server_name}}"}</code>
            {t("serverSettings.loginDescAfter")}
          </p>
          <label className="form-field">
            <span>
              {t("serverSettings.phoneTemplate")}
              {" "}
              {phoneIsOverridden ? <span className="badge good">{t("serverSettings.custom")}</span> : <span className="badge">{t("serverSettings.default")}</span>}
            </span>
            <textarea
              className="template-textarea"
              rows={8}
              value={phoneTemplate}
              onChange={(event) => setPhoneTemplate(event.target.value)}
              placeholder={identity.default_welcome_message_phone_template}
            />
          </label>
          <div className="gift-table-actions template-reset-row">
            <ActionButton
              tone="neutral"
              compact
              label={t("serverSettings.resetDefault")}
              path="/api/actions/set-welcome-message-templates"
              payload={() => ({ phone_template: "", email_template: emailTemplate })}
              disabled={!phoneIsOverridden}
              onDone={() => { setPhoneTemplate(""); void load(); }}
            />
          </div>
          <label className="form-field">
            <span>
              {t("serverSettings.emailTemplate")}
              {" "}
              {emailIsOverridden ? <span className="badge good">{t("serverSettings.custom")}</span> : <span className="badge">{t("serverSettings.default")}</span>}
            </span>
            <textarea
              className="template-textarea"
              rows={8}
              value={emailTemplate}
              onChange={(event) => setEmailTemplate(event.target.value)}
              placeholder={identity.default_welcome_message_email_template}
            />
          </label>
          <div className="gift-table-actions template-reset-row">
            <ActionButton
              tone="neutral"
              compact
              label={t("serverSettings.resetDefault")}
              path="/api/actions/set-welcome-message-templates"
              payload={() => ({ phone_template: phoneTemplate, email_template: "" })}
              disabled={!emailIsOverridden}
              onDone={() => { setEmailTemplate(""); void load(); }}
            />
          </div>
          <div className="gift-table-actions identity-save-row">
            <ActionButton
              tone="neutral"
              label={t("serverSettings.saveTemplates")}
              path="/api/actions/set-welcome-message-templates"
              payload={() => ({ phone_template: phoneTemplate, email_template: emailTemplate })}
              onDone={() => void load()}
            />
          </div>
          <p className="muted-lead code-lead">
            {t("serverSettings.codeDescBefore")}
            <code>{"{{code}}"}</code>
            {t("serverSettings.codeDescMid")}
            <code>{"{{server_name}}"}</code>
            {t("serverSettings.codeDescAfter")}
          </p>
          <label className="form-field">
            <span>
              {t("serverSettings.codeTemplate")}
              {" "}
              {codeIsOverridden ? <span className="badge good">{t("serverSettings.custom")}</span> : <span className="badge">{t("serverSettings.default")}</span>}
            </span>
            <textarea
              className="template-textarea"
              rows={5}
              value={codeTemplate}
              onChange={(event) => setCodeTemplate(event.target.value)}
              placeholder={identity.default_login_code_message_template}
            />
            {codeTemplateInvalid && (
              <span className="field-warning">
                {codeOccurrences === 0
                  ? t("serverSettings.codeMissing")
                  : t("serverSettings.codeCount", { count: codeOccurrences })}
              </span>
            )}
          </label>
          <div className="gift-table-actions template-reset-row">
            <ActionButton
              tone="neutral"
              compact
              label={t("serverSettings.resetDefault")}
              path="/api/actions/set-login-code-message-template"
              payload={() => ({ template: "" })}
              disabled={!codeIsOverridden}
              onDone={() => { setCodeTemplate(""); void load(); }}
            />
          </div>
          <div className="gift-table-actions identity-save-row">
            <ActionButton
              tone="neutral"
              label={t("serverSettings.saveCodeTemplate")}
              path="/api/actions/set-login-code-message-template"
              payload={() => ({ template: codeTemplate })}
              disabled={codeTemplateInvalid}
              onDone={() => void load()}
            />
          </div>
        </div>
      )}
    </section>
  );
}

// ServerIconModal swaps the identity icon: multipart upload (the same shape
// handleUploadServerIconAPI expects) or removal. Operates directly with
// confirm=true — selecting a file is itself the operator's explicit intent.
export function ServerIconModal({ hasIcon, onClose, onDone }: { hasIcon: boolean; onClose: () => void; onDone: () => void }) {
  const { t } = useI18n();
  const [file, setFile] = useState<File | null>(null);
  const [previewURL, setPreviewURL] = useState("");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!file) {
      setPreviewURL("");
      return;
    }
    const url = URL.createObjectURL(file);
    setPreviewURL(url);
    return () => URL.revokeObjectURL(url);
  }, [file]);

  async function submitUpload() {
    if (!file) {
      setError(t("serverSettings.chooseIconFirst"));
      return;
    }
    if (!reason.trim()) {
      setError(t("action.reasonRequired"));
      return;
    }
    setBusy(true);
    setError("");
    try {
      const form = new FormData();
      form.set("metadata", JSON.stringify({ command_id: "", reason: reason.trim(), confirm: true }));
      form.set("file", file, file.name);
      const result = await api.uploadServerIcon(form);
      if (result.error) {
        setError(result.error);
        return;
      }
      onDone();
      onClose();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function submitRemove() {
    if (!reason.trim()) {
      setError(t("action.reasonRequired"));
      return;
    }
    setBusy(true);
    setError("");
    try {
      const result = await api.action("/api/actions/remove-server-icon", { command_id: "", reason: reason.trim(), confirm: true });
      if (result.error) {
        setError(result.error);
        return;
      }
      onDone();
      onClose();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return createPortal(
    <div className="modal-backdrop" role="presentation">
      <section className="modal command-modal" role="dialog" aria-modal="true" aria-label={t("serverSettings.changeIcon")}>
        <div className="modal-head">
          <div>
            <div className="eyebrow">{t("serverSettings.iconModalEyebrow")}</div>
            <h2>{t("serverSettings.changeIcon")}</h2>
          </div>
          <button className="icon-btn" type="button" onClick={onClose} disabled={busy} aria-label={t("common.close")}><X size={15} /></button>
        </div>
        <div className="command-body">
          <label className={`gift-file-picker ${file ? "has-file" : ""}`}>
            <input type="file" accept=".png,.jpg,.jpeg,.webp,.gif,image/png,image/jpeg,image/webp,image/gif" onChange={(event) => setFile(event.target.files?.[0] ?? null)} />
            {previewURL ? <img className="gift-file-icon" src={previewURL} alt="" style={{ objectFit: "cover" }} /> : <ImagePlus size={22} />}
            <span className="gift-file-copy"><span className="gift-field-label">{t("serverSettings.newIcon")}</span><strong>{file ? file.name : t("serverSettings.chooseIcon")}</strong></span>
            <span className="gift-file-action">{file ? t("serverSettings.changeFile") : t("serverSettings.chooseFile")}</span>
          </label>
          <label className="gift-reason-field"><span>{t("action.reason")}</span><input value={reason} placeholder={t("serverSettings.iconReasonPlaceholder")} onChange={(event) => setReason(event.target.value)} /></label>
          {error && <Alert>{error}</Alert>}
        </div>
        <div className="modal-actions">
          <button className="btn" type="button" onClick={onClose} disabled={busy}>{t("common.close")}</button>
          {hasIcon && (
            <button className="btn danger icon-text" type="button" onClick={() => void submitRemove()} disabled={busy}>
              {busy ? <Loader2 className="spin" size={15} /> : <Trash2 size={15} />}
              {t("serverSettings.removeIcon")}
            </button>
          )}
          <button className="btn primary icon-text" type="button" onClick={() => void submitUpload()} disabled={busy}>
            {busy ? <Loader2 className="spin" size={15} /> : <Upload size={15} />}
            {t("serverSettings.uploadIcon")}
          </button>
        </div>
      </section>
    </div>,
    document.body
  );
}
