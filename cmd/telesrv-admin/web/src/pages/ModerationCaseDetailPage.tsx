import { ArrowLeft, CheckCircle2, ExternalLink, RefreshCw, ShieldCheck } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { api, errorMessage } from "../api";
import { Alert, Badge, EmptyRow, JsonBlock, LoadingSurface, PageFrame, SectionHead, SplitLayout, Summary } from "../components/ui";
import { useI18n, type TFunction } from "../i18n";
import { permissionMessagesRead, useCan } from "../permissions";
import { formatDate } from "../lib/format";
import type { Navigate } from "../routing";
import type { ModerationCaseDetail, ModerationReport, ModerationReportItem } from "../types";
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
              <div className="detail-stack">
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
                    <ReportEvidence t={t} report={report} navigate={navigate} />
                  </>
                )}
              </div>
            </section>
            <section className="section-block">
              <SectionHead title={t("moderation.decisionAudit")} text={t("moderation.decisionAuditHint")} />
              <div className="detail-stack">
                <DecisionAudit t={t} detail={detail} />
              </div>
            </section>
            {detail.Appeals.length > 0 && (
              <section className="section-block">
                <SectionHead title={t("moderation.appeals")} />
                {detail.Appeals.map((appeal) => (
                  <div className="stacked-sections" key={appeal.ID}>
                    <div className="summary-grid">
                      <Summary label={t("moderation.appellant")} value={String(appeal.AppellantUserID)} mono />
                      <Summary
                        label={t("moderation.appealStatus")}
                        value={moderationEnumLabel(t, "appealStatus", appeal.Status)}
                      />
                      <Summary
                        label={t("moderation.previousCaseStatus")}
                        value={moderationEnumLabel(t, "status", appeal.PreviousCaseStatus)}
                      />
                      <Summary label={t("moderation.reviewer")} value={appeal.Reviewer || "-"} />
                      <Summary label={t("common.time")} value={formatDate(appeal.CreatedAt)} />
                      <Summary
                        label={t("moderation.reviewedAt")}
                        value={appeal.ReviewedAt ? formatDate(appeal.ReviewedAt) : "-"}
                      />
                    </div>
                    {appeal.Text && <p className="about-text">{appeal.Text}</p>}
                    {appeal.ReviewReason && <p className="about-text">{appeal.ReviewReason}</p>}
                  </div>
                ))}
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

// ReportEvidence renders the report's attached evidence as a table (kind,
// peer, ids, author) with each frozen snapshot tucked behind a disclosure.
// Reported messages are shown in full -- body wrapped, media hinted -- and the
// message itself is one click away in the Messages console when the operator
// has messages.read. Media held as evidence gets its own small table so an
// operator can act on it.
function ReportEvidence({ t, report, navigate }: { t: TFunction; report: ModerationReport; navigate: Navigate }) {
  const canReadMessages = useCan(permissionMessagesRead);
  return (
    <div className="detail-stack">
      <div className="table-wrap">
        <table className="data-table">
          <thead><tr>
            <th>{t("moderation.itemKind")}</th>
            <th>{t("moderation.peer")}</th>
            <th>{t("moderation.itemID")}</th>
            <th>{t("moderation.secondaryID")}</th>
            <th>{t("audit.actor")}</th>
            <th>{t("messages.body")}</th>
            <th>{t("moderation.snapshot")}</th>
          </tr></thead>
          <tbody>
            {report.Items.map((evidence, index) => {
              const body = messageEvidenceBody(evidence.Evidence);
              const media = messageEvidenceMedia(evidence.Evidence);
              const openLink = messageConsoleLink(report, evidence);
              return (
              <tr key={`${evidence.Kind}-${index}`}>
                <td><Badge>{moderationEnumLabel(t, "itemKind", evidence.Kind)}</Badge></td>
                <td className="mono">{evidence.Peer ? moderationTargetLabel(t, evidence.Peer.Type, evidence.Peer.ID) : "-"}</td>
                <td className="mono">{evidence.ItemID || "-"}</td>
                <td className="mono">{evidence.SecondaryID || "-"}</td>
                <td className="mono">{evidence.AuthorUserID || "-"}</td>
                <td className="evidence-message-cell">
                  <div className="evidence-message" title={body ?? undefined}>
                    {body || (media ? `[${media}]` : "-")}
                  </div>
                  {openLink && canReadMessages && (
                    <button className="evidence-open" type="button" onClick={() => navigate(openLink)}>
                      <ExternalLink size={12} /> {t("moderation.openMessage")}
                    </button>
                  )}
                </td>
                <td>
                  {evidence.Evidence != null
                    ? (
                      <details>
                        <summary>{t("moderation.snapshot")}</summary>
                        <JsonBlock value={JSON.stringify(evidence.Evidence, null, 2)} />
                      </details>
                    )
                    : "-"}
                </td>
              </tr>
              );
            })}
            {report.Items.length === 0 && <EmptyRow colSpan={7} />}
          </tbody>
        </table>
      </div>
      {report.MediaHolds.length > 0 && (
        <>
          <div className="dock-title">{t("moderation.mediaHolds")}</div>
          <div className="table-wrap">
            <table className="data-table">
              <thead><tr>
                <th>{t("moderation.mediaItem")}</th>
                <th>{t("moderation.mediaKind")}</th>
                <th>{t("moderation.storageKey")}</th>
              </tr></thead>
              <tbody>
                {report.MediaHolds.map((hold, index) => (
                  <tr key={`${hold.StorageKey}-${index}`}>
                    <td>#{hold.ItemIndex}</td>
                    <td>{moderationEnumLabel(t, "mediaKind", hold.Kind)}</td>
                    <td className="mono">{hold.StorageKey}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
    </div>
  );
}

// messageEvidenceBody pulls the reported content out of a frozen snapshot so
// the operator sees what was actually reported instead of having to open the
// raw JSON disclosure: message items expose their text in `body`,
// reaction items nest the message under `message.body`, and stories carry the
// text as `caption`. Anything else has no displayable message.
function messageEvidenceBody(evidence: unknown): string | null {
  if (!evidence || typeof evidence !== "object") return null;
  const snapshot = evidence as Record<string, unknown>;
  const candidates: unknown[] = [snapshot.body];
  const message = snapshot.message;
  if (message && typeof message === "object") {
    candidates.push((message as Record<string, unknown>).body);
  }
  candidates.push(snapshot.caption);
  for (const candidate of candidates) {
    if (typeof candidate === "string" && candidate.trim()) return candidate;
  }
  return null;
}

// messageEvidenceMedia returns the media kind hint (photo, document, poll, ...)
// when the snapshot carries media but no body, so an attachment-only reported
// message still reads as a message instead of an empty cell.
function messageEvidenceMedia(evidence: unknown): string | null {
  if (!evidence || typeof evidence !== "object") return null;
  const snapshot = evidence as Record<string, unknown>;
  if (snapshot.media && typeof snapshot.media === "object") {
    const kind = (snapshot.media as Record<string, unknown>).kind;
    if (typeof kind === "string" && kind) return kind;
  }
  return null;
}

// messageConsoleLink deep-links a reported message into the Messages console
// (both gated by messages.read): private messages were loaded from the
// reporter's box, so the report's reporter id is the box owner; channel
// messages address the detail page with the channel peer id. Anything that is
// not a message item cannot be opened there.
function messageConsoleLink(report: ModerationReport, evidence: ModerationReportItem): string | null {
  if (evidence.Kind !== "message" || !evidence.Peer || !evidence.ItemID) return null;
  if (evidence.Peer.Type === "channel") {
    return `/messages/groups/detail?channel_id=${evidence.Peer.ID}&msg_id=${evidence.ItemID}`;
  }
  if (evidence.Peer.Type === "user" && report.ReporterUserID > 0) {
    return `/messages/private/detail?owner_user_id=${report.ReporterUserID}&msg_id=${evidence.ItemID}`;
  }
  return null;
}

// DecisionAudit replaces the decisions/actions JSON dump with two tables: the
// decision record (who decided what, and why) and the durable action queue
// (status, attempts, last error, and the payload behind a disclosure).
function DecisionAudit({ t, detail }: { t: TFunction; detail: ModerationCaseDetail }) {
  return (
    <div className="detail-stack">
      <div className="table-wrap">
        <table className="data-table">
          <thead><tr>
            <th>{t("moderation.decisionKind")}</th>
            <th>{t("audit.actor")}</th>
            <th>{t("audit.reason")}</th>
            <th>{t("common.time")}</th>
          </tr></thead>
          <tbody>
            {detail.Decisions.map((decision) => (
              <tr key={decision.ID}>
                <td>
                  <Badge tone={decision.Kind === "violation" ? "warn" : "good"}>
                    {moderationEnumLabel(t, "kind", decision.Kind)}
                  </Badge>
                </td>
                <td>{decision.Actor || "-"}</td>
                <td>{decision.Reason || "-"}</td>
                <td>{formatDate(decision.CreatedAt)}</td>
              </tr>
            ))}
            {detail.Decisions.length === 0 && <EmptyRow colSpan={4} />}
          </tbody>
        </table>
      </div>
      <div className="dock-title">{t("moderation.actionKind")}</div>
      <div className="table-wrap">
        <table className="data-table">
          <thead><tr>
            <th>{t("moderation.actionKind")}</th>
            <th>{t("common.status")}</th>
            <th>{t("moderation.attempts")}</th>
            <th>{t("moderation.lastError")}</th>
            <th>{t("moderation.payload")}</th>
            <th>{t("common.time")}</th>
          </tr></thead>
          <tbody>
            {detail.Actions.map((action) => (
              <tr key={action.ID}>
                <td className="mono">{action.Kind}</td>
                <td>
                  <Badge tone={action.Status === "succeeded" ? "good" : action.Status === "failed" ? "danger" : "warn"}>
                    {moderationEnumLabel(t, "actionStatus", action.Status)}
                  </Badge>
                </td>
                <td>{action.Attempts}</td>
                <td>{action.LastError || "-"}</td>
                <td>
                  {action.Payload && Object.keys(action.Payload).length > 0
                    ? (
                      <details>
                        <summary>{t("moderation.payload")}</summary>
                        <JsonBlock value={JSON.stringify(action.Payload, null, 2)} />
                      </details>
                    )
                    : "-"}
                </td>
                <td>{formatDate(action.CreatedAt)}</td>
              </tr>
            ))}
            {detail.Actions.length === 0 && <EmptyRow colSpan={6} />}
          </tbody>
        </table>
      </div>
    </div>
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
