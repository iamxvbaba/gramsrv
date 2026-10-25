import { CheckCircle2, CircleAlert, FileJson, Loader2, Play, X } from "lucide-react";
import { type ReactNode, useMemo, useState } from "react";
import { createPortal } from "react-dom";
import { APIError, api, errorMessage } from "../api";
import { useI18n } from "../i18n";
import type { CommandResult } from "../types";
import { Alert, JsonBlock } from "./ui";

// CommandFlow is the console's reason -> dry-run -> confirm modal, minus the
// trigger button.
//
// Every mutating action in the panel is an audited command, and every one of
// them is presented the same way: an operator types why, runs it as a dry run to
// see what the server would do, and only then commits. That presentation used
// to live inside ActionButton, which meant a modal that needed a *form* rather
// than a single click -- the gift-pack importer and the TON-wallet binding --
// had to reimplement the whole three-step dance by hand. They each grew their
// own slightly different version, with no step indicator, and the audit reason
// was the one thing a copy could quietly forget.
//
// This component is that presentation, with the request body supplied by the
// caller as a function, so a form can pass whatever it has collected. It owns
// the reason field, the dry run, the command id carried into the commit, the
// step indicator, the result box and the buttons -- nothing else.
export function CommandFlow({
  path,
  label,
  eyebrow,
  children,
  payload,
  onApplied,
  onResult,
  onClose,
  disabled = false,
  // Rendered under the reason field: the request preview is derived from the
  // payload, so a form with inputs wants the operator to see exactly what will
  // be sent before they run it.
  extra,
  form
}: {
  path: string;
  label: string;
  eyebrow?: string;
  children?: ReactNode;
  // What the request body contains, for the JSON path and for the preview.
  // Required even when `form` is set: it is what the operator sees in the
  // request preview before running, and a file cannot be rendered there.
  payload: () => Record<string, unknown>;
  onApplied?: () => void;
  // Every CommandResult this flow receives, dry run or commit. A command whose
  // outcome is a structured report rather than a message -- the gift pack
  // importer returns a per-gift table -- renders it from here instead of
  // reimplementing the request to get at it.
  onResult?: (result: CommandResult) => void;
  onClose: () => void;
  disabled?: boolean;
  extra?: ReactNode;
  // Form commands (an uploaded .zip, a directory of animation frames) post
  // multipart rather than JSON. When set, the flow builds the request body
  // itself from the reason/confirm/command-id it holds and this callback, and
  // the preview shows the non-file parts.
  form?: (fields: { reason: string; confirm: boolean; command_id: string }) => FormData;
}) {
  const { t } = useI18n();
  const [reason, setReason] = useState("");
  const [result, setResult] = useState<CommandResult | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  // The preview is recomputed on every render so it tracks the form live; a
  // throwing payload (a form that is not yet valid) shows the reason rather
  // than taking the modal down.
  const preview = useMemo(() => {
    try {
      return payload();
    } catch (err) {
      return { payload_error: errorMessage(err) };
    }
  }, [payload]);

  const canConfirm = Boolean(result?.dry_run) && !result?.error;

  async function run(confirm: boolean) {
    if (!reason.trim()) {
      setError(t("action.reasonRequired"));
      return;
    }
    setBusy(true);
    setError("");
    const fields = { reason, confirm, command_id: confirm ? (result?.command_id ?? "") : "" };
    try {
      const body: FormData | Record<string, unknown> = form
        ? form(fields)
        : { ...payload(), ...fields };
      const commandResult = await api.action(path, body);
      setResult(commandResult);
      onResult?.(commandResult);
      if (confirm && !commandResult.error) {
        onApplied?.();
      }
    } catch (err) {
      setError(errorMessage(err));
      // A command that reports a structured outcome can also fail as a
      // transport error carrying that outcome in its body -- the gift pack
      // importer does exactly this when some of the archive's gifts are bad.
      // Hand it to the caller rather than reducing it to a bare message.
      if (err instanceof APIError && err.result && typeof err.result === "object") {
        const carried = err.result as CommandResult;
        if (typeof carried.message === "string" || carried.details) {
          setResult(carried);
          onResult?.(carried);
        }
      }
    } finally {
      setBusy(false);
    }
  }

  return createPortal(
    <div className="modal-backdrop" role="presentation">
      <section className="modal command-modal" role="dialog" aria-modal="true" aria-label={label}>
        <div className="modal-head">
          <div>
            <div className="eyebrow">{eyebrow ?? t("action.flow")}</div>
            <h2>{label}</h2>
          </div>
          <button className="icon-btn" type="button" onClick={onClose} disabled={busy} aria-label={t("action.close")}>
            <X size={15} />
          </button>
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
          {children}
          <label className="form-field">
            <span>{t("action.reason")}</span>
            <textarea
              value={reason}
              onChange={(event) => setReason(event.target.value)}
              rows={3}
              placeholder={t("action.reasonPlaceholder")}
            />
          </label>
          {extra}
          <div className="command-preview">
            <div className="preview-head"><FileJson size={14} /> {t("action.requestPreview")}</div>
            <JsonBlock value={JSON.stringify(preview, null, 2)} />
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
              {result.message || result.error ? <div className="result-message">{result.message || result.error}</div> : null}
              {result.details && Object.keys(result.details).length > 0 && (
                <JsonBlock value={JSON.stringify(result.details, null, 2)} />
              )}
            </div>
          )}
        </div>
        <div className="modal-actions">
          <button className="btn" type="button" onClick={onClose} disabled={busy}>{t("common.close")}</button>
          <button className="btn icon-text" type="button" onClick={() => void run(false)} disabled={busy || disabled}>
            {busy ? <Loader2 size={15} className="spin" /> : <Play size={15} />}
            {result ? t("action.runAgain") : t("action.runDry")}
          </button>
          <button className="btn danger icon-text" type="button" onClick={() => void run(true)} disabled={busy || !canConfirm}>
            <CheckCircle2 size={15} />
            {t("action.confirm")}
          </button>
        </div>
      </section>
    </div>,
    document.body
  );
}