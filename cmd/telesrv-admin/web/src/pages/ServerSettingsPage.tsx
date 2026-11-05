import { ChevronDown, CircleCheck, CircleOff, CircleX, Container, Database, ImagePlus, Layers, Loader2, RefreshCw, Search, Server, Trash2, Upload, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState, type Dispatch, type ReactNode, type SetStateAction } from "react";
import { createPortal } from "react-dom";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { Alert, LoadingSurface, PageFrame, SectionHead } from "../components/ui";
import { useI18n } from "../i18n";
import type { DockerService, EnvGroup, ServerIdentity, ServerStatus } from "../types";
import { withBase } from "../lib/base";

// Server Settings: a single settings screen (owpengram layout) — a section
// list on the left plus a search across every .env field, one section on the
// right. Deep-linkable with ?tab or &focus.
type SettingsSection = "status" | "identity" | "messages" | `env:${string}`;

export function ServerSettingsPage({ search }: { search?: URLSearchParams }) {
  const { t } = useI18n();
  const [section, setSection] = useState<SettingsSection>("status");
  const [groups, setGroups] = useState<EnvGroup[]>([]);
  const [values, setValues] = useState<Record<string, string>>({});
  const [envError, setEnvError] = useState("");
  const [needle, setNeedle] = useState("");

  const loadEnv = useCallback(async () => {
    setEnvError("");
    try {
      const list = await api.serverEnv();
      const next: Record<string, string> = {};
      for (const group of list) {
        for (const field of group.fields) {
          next[field.key] = field.value;
        }
      }
      setGroups(Array.isArray(list) ? list : []);
      setValues(next);
    } catch (err) {
      setEnvError(errorMessage(err));
    }
  }, []);

  useEffect(() => { void loadEnv(); }, [loadEnv]);

  // ?focus=identity|login|env jumps to a section on load.
  const focus = search?.get("focus");
  useEffect(() => {
    if (focus === "identity") setSection("identity");
    else if (focus === "login") setSection("messages");
  }, [focus]);
  // ?focus=env lands on the first .env group once they are loaded. The jump is
  // applied once, so picking another section afterwards (Status included)
  // sticks while the URL still says ?focus=env.
  const envFocusApplied = useRef(false);
  useEffect(() => {
    if (focus !== "env") {
      envFocusApplied.current = false;
      return;
    }
    if (envFocusApplied.current || groups.length === 0) return;
    envFocusApplied.current = true;
    const first = `env:${groups[0].title}` as SettingsSection;
    setSection((current) => (current === "status" ? first : current));
  }, [focus, groups]);

  const q = needle.trim().toLowerCase();
  // A search looks through every .env field (key, description, group title) and
  // shows the matches together, whatever group they live in.
  const matches = useMemo(() => {
    if (!q) return [];
    return groups
      .map((group) => ({
        ...group,
        fields: group.fields.filter(
          (field) =>
            field.key.toLowerCase().includes(q) ||
            (field.description ?? "").toLowerCase().includes(q) ||
            group.title.toLowerCase().includes(q)
        )
      }))
      .filter((group) => group.fields.length > 0);
  }, [groups, q]);

  const activeGroup = section.startsWith("env:") ? groups.find((group) => `env:${group.title}` === section) : undefined;

  return (
    <PageFrame title={t("route.serverSettings")} eyebrow={t("serverSettings.eyebrow")}>
      <div className="settings-layout">
        <nav className="settings-nav" aria-label={t("serverSettings.tabsLabel")}>
          <label className="searchbox settings-search">
            <Search size={15} />
            <input value={needle} onChange={(event) => setNeedle(event.target.value)} placeholder={t("serverSettings.searchSettings")} aria-label={t("serverSettings.searchSettings")} />
          </label>
          {!q && (
            <>
              <SettingsNavItem label={t("serverSettings.statusLabel")} active={section === "status"} onClick={() => setSection("status")} />
              <SettingsNavItem label={t("serverSettings.identityTitle")} active={section === "identity"} onClick={() => setSection("identity")} />
              <SettingsNavItem label={t("nav.loginMessages")} active={section === "messages"} onClick={() => setSection("messages")} />
              {groups.length > 0 && <div className="settings-nav-label">{t("nav.configuration")}</div>}
              {groups.map((group) => (
                <SettingsNavItem
                  key={group.title}
                  label={group.title}
                  count={group.fields.length}
                  active={section === `env:${group.title}`}
                  onClick={() => setSection(`env:${group.title}` as SettingsSection)}
                />
              ))}
            </>
          )}
        </nav>
        <div className="settings-content">
          {envError && <Alert>{envError}</Alert>}
          {q ? (
            matches.length === 0 ? (
              <p className="about-text">{`${t("serverSettings.noMatch")} "${needle.trim()}".`}</p>
            ) : (
              matches.map((group) => (
                <EnvGroupPanel key={group.title} group={group} values={values} setValues={setValues} onSaved={() => undefined} />
              ))
            )
          ) : section === "status" ? (
            <ServicesTab />
          ) : section === "identity" ? (
            <IdentitySection />
          ) : section === "messages" ? (
            <LoginNotificationsSection />
          ) : activeGroup ? (
            <EnvGroupPanel group={activeGroup} values={values} setValues={setValues} onSaved={() => undefined} />
          ) : (
            <LoadingSurface label={t("common.loading")} />
          )}
        </div>
      </div>
    </PageFrame>
  );
}

function SettingsNavItem({ label, count, active, onClick }: { label: string; count?: number; active: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      className={`settings-nav-item ${active ? "active" : ""}`.trim()}
      onClick={onClick}
      aria-current={active ? "page" : undefined}
      title={label}
    >
      <span className="settings-nav-text">{label}</span>
      {typeof count === "number" && <span className="settings-nav-count">{count}</span>}
    </button>
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

// --- .env editor -----------------------------------------------------

function EnvGroupPanel({
  group,
  values,
  setValues,
  onSaved
}: {
  group: EnvGroup;
  values: Record<string, string>;
  setValues: Dispatch<SetStateAction<Record<string, string>>>;
  onSaved: () => void;
}) {
  const { t } = useI18n();
  return (
    <section className="section-block settings-panel">
      <SectionHead
        title={group.title}
        text={group.description}
        action={
          <ActionButton
            tone="warn"
            label={t("serverSettings.saveEnv")}
            path="/api/actions/update-server-env"
            payload={() => {
              const changed: Record<string, string> = {};
              for (const field of group.fields) {
                changed[field.key] = values[field.key] ?? "";
              }
              return { values: changed };
            }}
            onDone={onSaved}
          />
        }
      />
      {group.fields.map((field) => (
        <label key={field.key} className="form-field env-field">
          <span className="mono">{field.key}</span>
          {field.description && <span className="env-field-desc">{field.description}</span>}
          <input
            type={field.sensitive ? "password" : "text"}
            value={values[field.key] ?? ""}
            placeholder={field.default_value}
            onChange={(event) => setValues((prev) => ({ ...prev, [field.key]: event.target.value }))}
          />
        </label>
      ))}
    </section>
  );
}

// --- Services tab (read-only status probes) ----------------------------

type LiveTone = "good" | "warn" | "danger" | "idle";

function liveDotIcon(tone: LiveTone) {
  switch (tone) {
    case "good": return <CircleCheck size={15} />;
    case "danger": return <CircleX size={15} />;
    case "idle": return <CircleOff size={15} />;
    default: return <Loader2 className="spin" size={15} />;
  }
}

function ServiceCard({
  icon,
  name,
  tone,
  statusLabel,
  detail
}: {
  icon: ReactNode;
  name: string;
  tone: LiveTone;
  statusLabel: string;
  detail?: string;
}) {
  return (
    <div className={`service-card tone-${tone}`}>
      <div className="service-card-icon">{icon}</div>
      <div className="service-card-body">
        <div className="service-card-name">{name}</div>
        <div className="service-card-detail">{detail ?? "\u00a0"}</div>
      </div>
      <div className="service-card-status">
        {liveDotIcon(tone)}
        <span>{statusLabel}</span>
      </div>
    </div>
  );
}

const dockerServiceIcon: Record<string, ReactNode> = {
  postgres: <Database size={18} />,
  redis: <Layers size={18} />
};

// dockerTone/dockerStatusLabel map `docker compose ps` output onto the same
// card tones the reachability probes use: a container that is not running (or
// is unhealthy) is a real problem; `starting` is transient, not a failure.
function dockerTone(service: DockerService): LiveTone {
  const state = service.state.toLowerCase();
  const health = service.health.toLowerCase();
  if (state !== "running") return "danger";
  if (health === "unhealthy") return "danger";
  if (health === "starting") return "warn";
  return "good";
}

function dockerStatusLabel(service: DockerService): string {
  const state = service.state.toLowerCase();
  if (state !== "running") return service.state || "stopped";
  if (service.health) return service.health.charAt(0).toUpperCase() + service.health.slice(1);
  return "Running";
}

// ServicesTab reports reachability of the pieces this deployment depends on.
// Unlike the reference there is no process/docker control to watch (restart is
// a deploy script), so the screen is deliberately static-but-refreshable and
// reads at a glance rather than being polled against an upcoming bounce. The
// Compose container list is best-effort: the admin console normally runs
// without a Docker socket, so an unavailable list is explained, not alarmed on.
function ServicesTab() {
  const { t } = useI18n();
  const [status, setStatus] = useState<ServerStatus | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function load(showSpinner: boolean) {
    if (showSpinner) setBusy(true);
    setError("");
    try {
      setStatus(await api.serverStatus());
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => { void load(false); }, []);

  const serviceState = (health?: { configured: boolean; ok: boolean; error?: string }): { tone: LiveTone; statusLabel: string; detail?: string } => {
    if (!health?.configured) return { tone: "idle", statusLabel: t("serverSettings.serviceUnconfigured") };
    if (health.ok) return { tone: "good", statusLabel: t("serverSettings.running") };
    return { tone: "danger", statusLabel: t("serverSettings.serviceDown"), detail: health.error };
  };

  return (
    <section id="serversettings-services" className="section-block scroll-anchor">
      <SectionHead
        title={t("serverSettings.servicesTitle")}
        text={t("serverSettings.servicesHint")}
        action={
          <button className="btn compact-btn icon-text" type="button" disabled={busy} onClick={() => void load(true)}>
            {busy ? <Loader2 size={15} className="spin" /> : <RefreshCw size={15} />}
            {t("serverSettings.refresh")}
          </button>
        }
      />
      {error && <Alert>{error}</Alert>}
      {!status ? (
        <LoadingSurface label={t("serverSettings.servicesLoading")} />
      ) : (
        <div className="service-grid">
          <ServiceCard
            icon={<Server size={18} />}
            name={t("serverSettings.host")}
            tone="good"
            statusLabel={t("serverSettings.running")}
            detail={`${status.host.distro || status.host.hostname} \u00b7 ${status.host.os} ${status.host.arch} \u00b7 go ${status.host.go_version}`}
          />
          <ServiceCard
            icon={<CircleOff size={18} />}
            name={t("serverSettings.mtproto")}
            {...serviceState(status.mtproto)}
          />
          {status.docker?.services?.map((service) => (
            <ServiceCard
              key={service.name}
              icon={dockerServiceIcon[service.name] ?? <Container size={18} />}
              name={service.name}
              tone={dockerTone(service)}
              statusLabel={dockerStatusLabel(service)}
            />
          ))}
        </div>
      )}
      {status && !status.docker?.available && (
        <p className="muted-lead">
          {t("serverSettings.dockerUnavailable", { reason: status.docker?.error || "docker not reachable" })}
        </p>
      )}
    </section>
  );
}