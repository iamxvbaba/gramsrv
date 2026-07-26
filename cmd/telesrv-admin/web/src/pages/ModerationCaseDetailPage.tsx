import { ArrowLeft, CheckCircle2, RefreshCw, ShieldCheck } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { api, errorMessage } from "../api";
import { Alert, JsonBlock, LoadingSurface, PageFrame, SectionHead, SplitLayout, Summary } from "../components/ui";
import { useI18n, type TFunction } from "../i18n";
import { formatDate } from "../lib/format";
import type { Navigate } from "../routing";
import type { ModerationCaseDetail, ModerationReport } from "../types";
import {
  CaseSeverity,
  CaseStatus,
  moderationEnumLabel,
  moderationTargetLabel
} from "./ModerationCasesPage";

type DecisionPreset = "no_violation" | "scam" | "fake" | "freeze" | "scam_freeze" | "fake_freeze" | "delete_messages" | "delete_account";

export function ModerationCaseDetailPage({ id, navigate }: { id: number; navigate: Navigate }) {
  const { t } = useI18n();
  const [detail, setDetail] = useState<ModerationCaseDetail | null>(null);
  const [report, setReport] = useState<ModerationReport | null>(null);
  const [reason, setReason] = useState("");
  const [preset, setPreset] = useState<DecisionPreset>("no_violation");
  const [messageIDs, setMessageIDs] = useState("");
  const [ownerUserID, setOwnerUserID] = useState("");
  const [revokeMessages, setRevokeMessages] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  function selectReport(next: ModerationReport | null) {
    setReport(next);
    if (!next) return;
    const ids = next.Items
      .filter((item) => item.Kind === "message")
      .map((item) => Number(item.ItemID))
      .filter((value) => Number.isSafeInteger(value) && value > 0);
    setMessageIDs(ids.join(", "));
    setOwnerUserID(String(next.ReporterUserID));
  }

  async function load() {
    setError("");
    try {
      const next = await api.moderationCase(id);
      setDetail(next);
      const reportID = next.ReportIDs[0];
      selectReport(reportID ? await api.moderationReport(reportID) : null);
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  useEffect(() => {
    void load();
  }, [id]);

  const selectedActions = useMemo(
    () => actionsForPreset(
      preset,
      detail?.Case.Target.Type,
      parseMessageIDs(messageIDs),
      Number(ownerUserID),
      revokeMessages
    ),
    [preset, detail?.Case.Target.Type, messageIDs, ownerUserID, revokeMessages]
  );
  const appealRemedy = useMemo(
    () => detail ? requiredAppealRemedy(detail, t) : { actions: [], label: t("common.none"), blocked: false },
    [detail, t]
  );

  async function claim() {
    if (!detail) return;
    setBusy(true);
    setError("");
    try {
      await api.claimModerationCase(id, detail.Case.Version);
      await load();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function decide() {
    if (!detail || !reason.trim()) {
      setError(t("moderation.reasonRequired"));
      return;
    }
    if (preset === "delete_messages" && selectedActions.length === 0) {
      setError(detail.Case.Target.Type === "user"
        ? t("moderation.privateDeleteValidation")
        : t("moderation.channelDeleteValidation"));
      return;
    }
    if (!window.confirm(t("moderation.confirmDecision", { decision: decisionPresetLabel(t, preset) }))) return;
    setBusy(true);
    setError("");
    try {
      const result = await api.decideModerationCase(id, {
        expected_version: detail.Case.Version,
        reason: reason.trim(),
        kind: preset === "no_violation" ? "no_violation" : "violation",
        actions: selectedActions
      });
      setDetail(result.case);
      setReason("");
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function reviewAppeal(appealID: number, granted: boolean) {
    if (!detail || !reason.trim()) {
      setError(t("moderation.appealReasonRequired"));
      return;
    }
    if (!window.confirm(granted ? t("moderation.confirmGrantAppeal") : t("moderation.confirmDenyAppeal"))) return;
    setBusy(true);
    try {
      const result = await api.reviewModerationAppeal(id, appealID, {
        expected_version: detail.Case.Version,
        reason: reason.trim(),
        granted,
        actions: granted ? appealRemedy.actions : []
      });
      setDetail(result.case);
      setReason("");
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  if (error && !detail) return <Alert>{error}</Alert>;
  if (!detail) return <LoadingSurface label={t("moderation.loadingCase")} />;
  const item = detail.Case;
  const canClaim = item.Status === "open" || item.Status === "in_review" || item.Status === "appeal_review";
  const canDecide = (item.Status === "in_review" || item.Status === "action_failed") && Boolean(item.AssignedTo);
  const canSubmitDecision = canDecide && (item.Status !== "action_failed" || preset !== "no_violation");
  const pendingAppeal = detail.Appeals.find((appeal) => appeal.Status === "pending");

  return (
    <PageFrame
      title={t("moderation.caseDetailTitle", { id: item.ID })}
      eyebrow={t("moderation.caseDetailEyebrow")}
      actions={
        <>
          <button className="btn icon-text" onClick={() => navigate("/moderation")}>
            <ArrowLeft size={15} /> {t("moderation.backToQueue")}
          </button>
          <button className="btn icon-text" onClick={load}>
            <RefreshCw size={15} /> {t("common.refresh")}
          </button>
        </>
      }
    >
      {error && <Alert>{error}</Alert>}
      <SplitLayout
        main={
          <div className="stacked-sections">
            <section className="entity-head">
              <div>
                <div className="entity-title">{moderationTargetLabel(t, item.Target.Type, item.Target.ID)}</div>
                <div className="entity-subtitle">
                  {t("moderation.versionAndUpdated", { version: item.Version, time: formatDate(item.UpdatedAt) })}
                </div>
              </div>
              <div className="entity-badges">
                <CaseStatus status={item.Status} />
                <CaseSeverity value={item.Severity} />
              </div>
            </section>
            <div className="summary-grid">
              <Summary label={t("moderation.target")} value={moderationTargetLabel(t, item.Target.Type, item.Target.ID)} mono />
              <Summary
                label={t("moderation.reportCount")}
                value={t("moderation.reportCountValue", {
                  reports: item.ReportCount,
                  reporters: item.DistinctReporterCount
                })}
              />
              <Summary label={t("moderation.assignee")} value={item.AssignedTo || "-"} />
              <Summary
                label={t("moderation.firstAndLatestReport")}
                value={`${formatDate(item.FirstReportAt)} / ${formatDate(item.LastReportAt)}`}
              />
            </div>
            <section className="section-block">
              <SectionHead title={t("moderation.evidence")} text={t("moderation.evidenceHint")} />
              <div className="toolbar">
                {detail.ReportIDs.map((reportID) => (
                  <button className="btn" key={reportID} onClick={async () => selectReport(await api.moderationReport(reportID))}>
                    #{reportID}
                  </button>
                ))}
              </div>
              {report && (
                <>
                  <div className="summary-grid">
                    <Summary
                      label={t("moderation.sourceAndReason")}
                      value={`${moderationEnumLabel(t, "source", report.Source)} / ${moderationEnumLabel(t, "reason", report.Reason)}`}
                    />
                    <Summary label={t("moderation.reporter")} value={String(report.ReporterUserID)} mono />
                    <Summary label={t("moderation.option")} value={report.Option} mono />
                    <Summary label={t("common.time")} value={formatDate(report.CreatedAt)} />
                  </div>
                  {report.Comment && <p className="about-text">{report.Comment}</p>}
                  <JsonBlock value={JSON.stringify(report, null, 2)} />
                </>
              )}
            </section>
            <section className="section-block">
              <SectionHead title={t("moderation.decisionAudit")} text={t("moderation.decisionAuditHint")} />
              <JsonBlock value={JSON.stringify({ decisions: detail.Decisions, actions: detail.Actions }, null, 2)} />
            </section>
            {detail.Appeals.length > 0 && (
              <section className="section-block">
                <SectionHead title={t("moderation.appeals")} />
                <JsonBlock value={JSON.stringify(detail.Appeals, null, 2)} />
              </section>
            )}
          </div>
        }
        side={
          <section className="action-dock">
            <div className="dock-title">{t("moderation.caseActions")}</div>
            {canClaim && (
              <button className="btn primary icon-text" disabled={busy} onClick={claim}>
                <ShieldCheck size={15} /> {item.AssignedTo ? t("moderation.renewClaim") : t("moderation.claimCase")}
              </button>
            )}
            <label className="field">
              <span>{t("moderation.reviewReason")}</span>
              <textarea value={reason} onChange={(event) => setReason(event.target.value)} rows={5} />
            </label>
            <label className="field">
              <span>{t("moderation.decisionPreset")}</span>
              <select value={preset} onChange={(event) => setPreset(event.target.value as DecisionPreset)}>
                <option value="no_violation">{t("moderation.preset.noViolation")}</option>
                <option value="scam">{t("moderation.preset.scam")}</option>
                <option value="fake">{t("moderation.preset.fake")}</option>
                <option value="freeze">{t("moderation.preset.freeze")}</option>
                <option value="scam_freeze">{t("moderation.preset.scamFreeze")}</option>
                <option value="fake_freeze">{t("moderation.preset.fakeFreeze")}</option>
                <option value="delete_messages">{t("moderation.preset.deleteMessages")}</option>
                <option value="delete_account">{t("moderation.preset.deleteAccount")}</option>
              </select>
            </label>
            {preset === "delete_messages" && (
              <>
                <label className="field">
                  <span>{t("moderation.evidenceMessageIDs")}</span>
                  <input value={messageIDs} onChange={(event) => setMessageIDs(event.target.value)} placeholder="101, 102" />
                </label>
                {item.Target.Type === "user" && (
                  <>
                    <label className="field">
                      <span>{t("moderation.privateOwnerUserID")}</span>
                      <input value={ownerUserID} onChange={(event) => setOwnerUserID(event.target.value)} inputMode="numeric" />
                    </label>
                    <label className="field checkbox-field">
                      <input type="checkbox" checked={revokeMessages} onChange={(event) => setRevokeMessages(event.target.checked)} />
                      <span>{t("moderation.revokeForBoth")}</span>
                    </label>
                  </>
                )}
                <Alert>{t("moderation.evidenceValidationHint")}</Alert>
              </>
            )}
            {item.Status === "action_failed" && preset === "no_violation" && (
              <Alert>{t("moderation.failedActionHint")}</Alert>
            )}
            {canDecide && (
              <button className="btn danger icon-text" disabled={busy || !canSubmitDecision} onClick={decide}>
                <CheckCircle2 size={15} /> {item.Status === "action_failed"
                  ? t("moderation.retryAction")
                  : t("moderation.submitDecision")}
              </button>
            )}
            {pendingAppeal && item.AssignedTo && (
              <>
                <div className="dock-title">{t("moderation.appealReviewTitle", { id: pendingAppeal.ID })}</div>
                <Summary label={t("moderation.automaticRemedy")} value={appealRemedy.label} />
                {appealRemedy.blocked && (
                  <Alert>{t("moderation.irreversibleAppealHint")}</Alert>
                )}
                <button className="btn" disabled={busy} onClick={() => reviewAppeal(pendingAppeal.ID, false)}>
                  {t("moderation.denyAppeal")}
                </button>
                <button className="btn primary" disabled={busy || appealRemedy.blocked} onClick={() => reviewAppeal(pendingAppeal.ID, true)}>
                  {t("moderation.grantAppeal")}
                </button>
              </>
            )}
          </section>
        }
      />
    </PageFrame>
  );
}

function actionsForPreset(
  preset: DecisionPreset,
  targetType: string | undefined,
  messageIDs: number[],
  ownerUserID: number,
  revoke: boolean
): Array<{ kind: string; payload: Record<string, unknown> }> {
  switch (preset) {
    case "scam":
      return [{ kind: "mark_scam", payload: {} }];
    case "fake":
      return [{ kind: "mark_fake", payload: {} }];
    case "freeze":
      return [{ kind: "freeze_account", payload: {} }];
    case "scam_freeze":
      return [{ kind: "mark_scam", payload: {} }, { kind: "freeze_account", payload: {} }];
    case "fake_freeze":
      return [{ kind: "mark_fake", payload: {} }, { kind: "freeze_account", payload: {} }];
    case "delete_messages":
      if (messageIDs.length === 0) return [];
      if (targetType === "channel") {
        return [{ kind: "delete_channel_message", payload: { ids: messageIDs } }];
      }
      if (targetType === "user" && Number.isSafeInteger(ownerUserID) && ownerUserID > 0) {
        return [{ kind: "delete_private_message", payload: { owner_user_id: ownerUserID, ids: messageIDs, revoke } }];
      }
      return [];
    case "delete_account":
      return [{ kind: "delete_account", payload: {} }];
    default:
      return [];
  }
}

function parseMessageIDs(raw: string): number[] {
  const values = raw
    .split(/[,\s]+/)
    .filter(Boolean)
    .map(Number);
  if (values.length === 0 || values.some((value) => !Number.isSafeInteger(value) || value <= 0)) return [];
  return [...new Set(values)];
}

function requiredAppealRemedy(detail: ModerationCaseDetail, t: TFunction): {
  actions: Array<{ kind: string; payload: Record<string, unknown> }>;
  label: string;
  blocked: boolean;
} {
  let flagsActive = false;
  let freezeActive = false;
  let irreversible = false;
  for (const action of [...detail.Actions].sort((left, right) => left.ID - right.ID)) {
    if (action.Status !== "succeeded") continue;
    switch (action.Kind) {
      case "mark_scam":
      case "mark_fake":
        flagsActive = true;
        break;
      case "clear_peer_flags":
        flagsActive = false;
        break;
      case "freeze_account":
        freezeActive = true;
        break;
      case "unfreeze_account":
        freezeActive = false;
        break;
      case "delete_private_message":
      case "delete_channel_message":
      case "delete_account":
        irreversible = true;
        break;
    }
  }
  const actions: Array<{ kind: string; payload: Record<string, unknown> }> = [];
  const labels: string[] = [];
  if (flagsActive) {
    actions.push({ kind: "clear_peer_flags", payload: {} });
    labels.push(t("moderation.remedy.clearFlags"));
  }
  if (freezeActive) {
    actions.push({ kind: "unfreeze_account", payload: {} });
    labels.push(t("moderation.remedy.unfreeze"));
  }
  return { actions, label: labels.join(" + ") || t("moderation.remedy.none"), blocked: irreversible };
}

function decisionPresetLabel(t: TFunction, preset: DecisionPreset): string {
  const keys: Record<DecisionPreset, string> = {
    no_violation: "noViolation",
    scam: "scam",
    fake: "fake",
    freeze: "freeze",
    scam_freeze: "scamFreeze",
    fake_freeze: "fakeFreeze",
    delete_messages: "deleteMessages",
    delete_account: "deleteAccount"
  };
  return t(`moderation.preset.${keys[preset]}`);
}
