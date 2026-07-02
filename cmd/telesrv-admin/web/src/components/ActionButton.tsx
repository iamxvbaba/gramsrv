import { CheckCircle2, CircleAlert, FileJson, Loader2, Play, X } from "lucide-react";
import type { ReactNode } from "react";
import { useMemo, useState } from "react";
import { createPortal } from "react-dom";
import { api, errorMessage } from "../api";
import { useI18n } from "../i18n";
import type { CommandResult } from "../types";
import { Alert, JsonBlock } from "./ui";

type ActionTone = "neutral" | "warn" | "danger";

export function ActionButton({
  label,
  path,
  payload,
  icon,
  compact = false,
  tone = "danger",
  onDone
}: {
  label: string;
  path: string;
  payload: () => Record<string, unknown>;
  icon?: ReactNode;
  compact?: boolean;
  tone?: ActionTone;
  onDone?: () => void;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState("");
  const [result, setResult] = useState<CommandResult | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  function reset() {
    setReason("");
    setResult(null);
    setError("");
  }

  async function run(confirm: boolean) {
    if (!reason.trim()) {
      setError(t("action.reasonRequired"));
      return;
    }
    setBusy(true);
    setError("");
    try {
      const body = { ...payload(), reason, confirm };
      const commandResult = await api.action(path, body);
      setResult(commandResult);
      if (confirm) {
        onDone?.();
      }
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  const canConfirm = result?.dry_run && !result.error;
  const triggerClass = `btn ${tone === "danger" ? "danger" : tone === "warn" ? "warn" : ""} ${compact ? "compact-btn" : ""}`;
  const previewPayload = useMemo(() => {
    try {
      return payload();
    } catch (err) {
      return { payload_error: errorMessage(err) };
    }
  }, [open, payload]);

  return (
    <>
      <button
        className={triggerClass}
        type="button"
        onClick={() => {
          reset();
          setOpen(true);
        }}
      >
        {icon}
        {label}
      </button>
      {open && createPortal(
        <div className="modal-backdrop" role="presentation">
          <section className="modal command-modal" role="dialog" aria-modal="true" aria-label={label}>
            <div className="modal-head">
              <div>
                <div className="eyebrow">{t("action.flow")}</div>
                <h2>{label}</h2>
              </div>
              <button className="icon-btn" type="button" onClick={() => setOpen(false)} aria-label={t("action.close")}><X size={15} /></button>
            </div>
            <div className="command-body">
              <div className="command-steps">
                <div className={`command-step ${reason.trim() ? "done" : "active"}`}>
                  <span>1</span><strong>{t("action.stepReason")}</strong>
                </div>
                <div className={`command-step ${result?.dry_run ? "done" : reason.trim() ? "active" : ""}`}>
                  <span>2</span><strong>{t("action.stepDryRun")}</strong>
                </div>
                <div className={`command-step ${result && !result.dry_run && !result.error ? "done" : canConfirm ? "active" : ""}`}>
                  <span>3</span><strong>{t("action.stepConfirm")}</strong>
                </div>
              </div>
              <label className="form-field">
                <span>{t("action.reason")}</span>
                <textarea value={reason} onChange={(event) => setReason(event.target.value)} rows={3} placeholder={t("action.reasonPlaceholder")} />
              </label>
              <div className="command-preview">
                <div className="preview-head"><FileJson size={14} /> {t("action.requestPreview")}</div>
                <JsonBlock value={JSON.stringify(previewPayload, null, 2)} />
              </div>
              {error && <Alert>{error}</Alert>}
              {result && (
                <div className="result-box">
                <div className="result-title">
                    {result.error ? <CircleAlert size={16} /> : <CheckCircle2 size={16} />}
                  <strong>{result.message || result.error || t("action.result")}</strong>
                </div>
                <div className="result-line"><span>{t("action.commandID")}</span><strong>{result.command_id}</strong></div>
                <div className="result-line"><span>{t("action.status")}</span><strong>{result.status}</strong></div>
                <div className="result-line"><span>{t("action.dryRun")}</span><strong>{result.dry_run ? t("common.yes") : t("common.no")}</strong></div>
                  <div className="result-message">{result.message || result.error}</div>
                  {result.details && <JsonBlock value={JSON.stringify(result.details, null, 2)} />}
                </div>
              )}
            </div>
            <div className="modal-actions">
              <button className="btn" type="button" onClick={() => setOpen(false)}>{t("common.close")}</button>
              <button className="btn icon-text" type="button" onClick={() => run(false)} disabled={busy}>
                {busy ? <Loader2 size={15} className="spin" /> : <Play size={15} />}
                {result ? t("action.runAgain") : t("action.runDry")}
              </button>
              <button className="btn danger icon-text" type="button" onClick={() => run(true)} disabled={busy || !canConfirm}>
                <CheckCircle2 size={15} />
                {t("action.confirm")}
              </button>
            </div>
          </section>
        </div>,
        document.body
      )}
    </>
  );
}
