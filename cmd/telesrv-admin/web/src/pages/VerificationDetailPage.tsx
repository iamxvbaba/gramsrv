import {
  ArrowLeft,
  BadgeCheck,
  Ban,
  CheckCircle2,
  ExternalLink,
  Handshake,
  RefreshCw,
  ShieldOff,
  User,
  XCircle
} from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { api, APIError, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { Alert, Badge, EmptyRow, LoadingSurface, PageFrame, SectionHead, SplitLayout, Summary } from "../components/ui";
import { useI18n } from "../i18n";
import { displayUsername, formatDate, safeHttpURL } from "../lib/format";
import { permissionVerificationRevoke, usePermissions } from "../permissions";
import type { Navigate } from "../routing";
import type { VerificationApplicationDetail, VerificationEventKind } from "../types";
import { VerificationStatusBadge, targetHref, targetLabel } from "./VerificationPage";

export function VerificationDetailPage({ id, navigate }: { id: string; navigate: Navigate }) {
  const { t } = useI18n();
  const { can } = usePermissions();
  const [detail, setDetail] = useState<VerificationApplicationDetail | null>(null);
  const [note, setNote] = useState("");
  const [conflict, setConflict] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function load() {
    setBusy(true);
    setError("");
    try {
      setDetail(await api.verificationApplication(id));
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  function refresh() {
    setConflict(false);
    void load();
  }

  useEffect(() => {
    void load();
  }, [id]);

  // 409 is the one failure the operator cannot fix by editing the form: another
  // reviewer decided against the version this page read. The panel says so in
  // plain words and reloads, so the next attempt carries the current version.
  function handleActionError(err: unknown): string | undefined {
    if (err instanceof APIError && err.status === 409) {
      setConflict(true);
      void load();
      return t("verification.conflict");
    }
    return undefined;
  }

  if (error && !detail) {
    return <Alert>{error}</Alert>;
  }
  if (!detail) {
    return <LoadingSurface label={t("verification.loadingDetail")} />;
  }

  const app = detail.application;
  const events = detail.events ?? [];
  const controls = detail.applicant_controls_target;
  const verified = detail.target_verified;
  const canClaim = app.Status === "submitted";
  const canDecide = app.Status === "submitted" || app.Status === "in_review";
  const canRevoke = app.Status === "approved" && can(permissionVerificationRevoke);
  const trimmedNote = note.trim();

  // version is the optimistic-locking token: it goes with every decision, as the
  // decimal string it arrived as, so a stale page cannot overwrite a fresh one.
  function decisionPayload(): Record<string, unknown> {
    const payload: Record<string, unknown> = { version: app.Version };
    if (trimmedNote) payload.internal_note = trimmedNote;
    return payload;
  }

  function afterDecision() {
    setNote("");
    setConflict(false);
    void load();
  }

  return (
    <PageFrame
      title={t("verification.detailTitle", { id: app.ID })}
      eyebrow={t("verification.detailEyebrow")}
      actions={
        <>
          <button className="btn icon-text" type="button" onClick={() => navigate("/verification")}>
            <ArrowLeft size={15} /> {t("common.backToList")}
          </button>
          <button className="btn icon-text" type="button" onClick={refresh} disabled={busy}>
            <RefreshCw size={15} className={busy ? "spin" : ""} /> {t("common.refresh")}
          </button>
        </>
      }
    >
      {error && <Alert>{error}</Alert>}
      {conflict && <Alert>{t("verification.conflict")}</Alert>}
      <SplitLayout
        main={
          <div className="stacked-sections">
            <section className="entity-head">
              <div>
                <div className="entity-title">{targetLabel(app)}</div>
                <div className="entity-subtitle mono">
                  #{app.ID} · {t(`verification.type.${app.TargetType}`)}:{app.TargetID} · v{app.Version}
                </div>
              </div>
              <div className="entity-badges">
                <VerificationStatusBadge status={app.Status} />
                {verified && <Badge tone="good"><BadgeCheck size={12} /> {t("verification.alreadyVerified")}</Badge>}
                <Badge tone={controls ? "good" : "danger"}>
                  {controls ? t("verification.controlsOk") : t("verification.controlsLost")}
                </Badge>
              </div>
            </section>

            <section className="section-block">
              <SectionHead
                title={t("verification.targetSection")}
                text={t("verification.targetHint")}
                action={
                  <button className="btn icon-text" type="button" onClick={() => navigate(targetHref(app))}>
                    <ExternalLink size={15} /> {t("verification.openTarget")}
                  </button>
                }
              />
              <div className="summary-grid">
                <Summary label={t("common.type")} value={t(`verification.type.${app.TargetType}`)} />
                <Summary label={t("common.username")} value={displayUsername(app.TargetUsername) || "-"} />
                <Summary label={t("verification.targetTitle")} value={app.TargetTitle || "-"} />
                <Summary label={t("verification.targetID")} value={app.TargetID} mono />
              </div>
            </section>

            <section className="section-block">
              <SectionHead
                title={t("verification.applicantSection")}
                text={t("verification.applicantHint")}
                action={
                  <button className="btn icon-text" type="button" onClick={() => navigate(`/accounts/${app.ApplicantUserID}`)}>
                    <User size={15} /> {t("verification.openApplicant")}
                  </button>
                }
              />
              <div className="summary-grid">
                <Summary label={t("common.username")} value={displayUsername(app.ApplicantUsername) || "-"} />
                <Summary label={t("common.name")} value={app.ApplicantName || "-"} />
                <Summary label={t("verification.applicantID")} value={app.ApplicantUserID} mono />
                <Summary label={t("verification.submittedAt")} value={formatDate(app.SubmittedAt) || "-"} />
              </div>
              {controls
                ? <p className="bot-create-note">{t("verification.controlsOkHint")}</p>
                : <Alert>{t("verification.controlsLostHint")}</Alert>}
            </section>

            <section className="section-block">
              <SectionHead title={t("verification.applicationSection")} text={t("verification.applicationHint")} />
              <div className="stacked-sections">
                <div className="summary-grid">
                  <Summary label={t("verification.category")} value={app.Category || "-"} />
                  <Summary label={t("verification.correlationID")} value={app.CorrelationID || "-"} mono />
                  <Summary label={t("verification.createdAt")} value={formatDate(app.CreatedAt) || "-"} />
                  <Summary label={t("common.updatedAt")} value={formatDate(app.UpdatedAt) || "-"} />
                </div>
                <FieldBlock label={t("verification.description")}>
                  {app.Description
                    ? <p className="about-text">{app.Description}</p>
                    : <p className="bot-create-note">{t("verification.notProvided")}</p>}
                </FieldBlock>
                <FieldBlock label={t("verification.officialWebsite")}>
                  {app.OfficialWebsite
                    ? <div className="about-text"><SafeLink value={app.OfficialWebsite} /></div>
                    : <p className="bot-create-note">{t("verification.notProvided")}</p>}
                </FieldBlock>
                <FieldBlock label={t("verification.socialLinks")}>
                  <LinkList values={app.SocialLinks} />
                </FieldBlock>
                <FieldBlock label={t("verification.pressLinks")}>
                  <LinkList values={app.PressLinks} />
                </FieldBlock>
                <FieldBlock label={t("verification.additionalNote")}>
                  {app.AdditionalNote
                    ? <p className="about-text">{app.AdditionalNote}</p>
                    : <p className="bot-create-note">{t("verification.notProvided")}</p>}
                </FieldBlock>
                <p className="bot-create-note">{t("verification.linkSafetyHint")}</p>
              </div>
            </section>

            <section className="section-block">
              <SectionHead title={t("verification.decisionSection")} text={t("verification.decisionHint")} />
              <div className="stacked-sections">
                <div className="summary-grid">
                  <Summary label={t("verification.reviewer")} value={app.ReviewerAdminID || "-"} />
                  <Summary label={t("verification.reviewedAt")} value={formatDate(app.ReviewedAt) || "-"} />
                  <Summary label={t("common.status")} value={t(`verification.status.${app.Status}`)} />
                  <Summary label={t("verification.version")} value={app.Version} mono />
                </div>
                <FieldBlock label={t("verification.decisionReason")}>
                  {app.DecisionReason
                    ? <p className="about-text">{app.DecisionReason}</p>
                    : <p className="bot-create-note">{t("verification.noDecision")}</p>}
                </FieldBlock>
                {/* The internal note is the reviewer handover text and is labelled
                    as admin-only wherever it appears. */}
                <FieldBlock label={`${t("verification.internalNote")} · ${t("verification.adminOnly")}`}>
                  {app.InternalNote
                    ? <p className="about-text">{app.InternalNote}</p>
                    : <p className="bot-create-note">{t("verification.notProvided")}</p>}
                </FieldBlock>
              </div>
            </section>

            <section className="section-block">
              <SectionHead title={t("verification.eventsSection")} text={t("verification.eventsHint")} />
              <div className="table-wrap">
                <table className="data-table">
                  <thead>
                    <tr>
                      <th>{t("verification.eventKind")}</th>
                      <th>{t("verification.transition")}</th>
                      <th>{t("audit.actor")}</th>
                      <th>{t("audit.reason")}</th>
                      <th>{t("verification.eventNote")}</th>
                      <th>{t("common.time")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {events.map((row) => (
                      <tr key={row.ID}>
                        <td><EventKind kind={row.Kind} /></td>
                        <td className="mono">
                          {row.FromStatus || "-"} → {row.ToStatus || "-"}
                        </td>
                        <td>{row.Actor || "-"}</td>
                        <td className="truncate">{row.Reason || "-"}</td>
                        <td className="truncate">{row.Note || "-"}</td>
                        <td>{formatDate(row.CreatedAt) || "-"}</td>
                      </tr>
                    ))}
                    {events.length === 0 && <EmptyRow colSpan={6} />}
                  </tbody>
                </table>
              </div>
            </section>
          </div>
        }
        side={
          <section className="action-dock">
            <div className="dock-title">{t("verification.actionDock")}</div>
            {!canClaim && !canDecide && !canRevoke && (
              <p className="bot-create-note">{t("verification.noActions")}</p>
            )}
            {canClaim && (
              <>
                <div className="action-stack">
                  <ActionButton
                    label={t("verification.claim")}
                    icon={<Handshake size={15} />}
                    tone="neutral"
                    path={`/api/verification/applications/${app.ID}/claim`}
                    payload={() => ({ version: app.Version })}
                    onDone={afterDecision}
                    onError={handleActionError}
                  />
                </div>
                <p className="bot-create-note">{t("verification.claimHint")}</p>
              </>
            )}
            {/* One optional note field feeds every decision on this page,
                including a revoke. */}
            {(canDecide || canRevoke) && (
              <>
                <label className="duration-field">
                  <span>{t("verification.internalNote")}</span>
                  <textarea
                    value={note}
                    onChange={(event) => setNote(event.target.value)}
                    rows={3}
                    placeholder={t("verification.internalNotePlaceholder")}
                  />
                </label>
                <p className="bot-create-note">{t("verification.internalNoteHint")}</p>
              </>
            )}
            {canDecide && (
              <>
                {!controls && <Alert>{t("verification.controlsLostHint")}</Alert>}
                {verified && <p className="bot-create-note">{t("verification.alreadyVerifiedHint")}</p>}
                <div className="action-stack">
                  <ActionButton
                    label={t("verification.approve")}
                    icon={<CheckCircle2 size={15} />}
                    tone="neutral"
                    path={`/api/verification/applications/${app.ID}/approve`}
                    payload={decisionPayload}
                    onDone={afterDecision}
                    onError={handleActionError}
                  />
                  <ActionButton
                    label={t("verification.reject")}
                    icon={<XCircle size={15} />}
                    tone="warn"
                    path={`/api/verification/applications/${app.ID}/reject`}
                    payload={decisionPayload}
                    onDone={afterDecision}
                    onError={handleActionError}
                  />
                </div>
                <p className="bot-create-note">{t("verification.approveHint")}</p>
                <p className="bot-create-note">{t("verification.rejectHint")}</p>
              </>
            )}
            {canRevoke && (
              <>
                <div className="dock-title"><ShieldOff size={14} /> {t("verification.dangerZone")}</div>
                <div className="danger-zone">
                  <ActionButton
                    label={t("verification.revoke")}
                    icon={<Ban size={15} />}
                    tone="danger"
                    path="/api/actions/revoke-verification"
                    payload={() => {
                      // Revoke addresses the peer, not the application: the
                      // approved application stays approved as history.
                      const payload: Record<string, unknown> = {
                        target_type: app.TargetType,
                        target_id: app.TargetID
                      };
                      if (trimmedNote) payload.internal_note = trimmedNote;
                      return payload;
                    }}
                    onDone={afterDecision}
                    onError={handleActionError}
                  />
                  <p className="bot-create-note">{t("verification.revokeHint")}</p>
                  {!verified && <p className="bot-create-note">{t("verification.revokeNotVerified")}</p>}
                </div>
              </>
            )}
          </section>
        }
      />
    </PageFrame>
  );
}

function FieldBlock({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="duration-field">
      <span>{label}</span>
      {children}
    </div>
  );
}

// Applicant-supplied text is rendered as ordinary React children (escaped by
// React) and only ever linked when it is an http(s) URL. No markup from a
// submission reaches the DOM.
function SafeLink({ value }: { value: string }) {
  const href = safeHttpURL(value);
  if (!href) {
    return <span className="mono">{value}</span>;
  }
  return (
    <a className="row-link" href={href} target="_blank" rel="noopener noreferrer">
      {value} <ExternalLink size={13} />
    </a>
  );
}

function LinkList({ values }: { values: string[] | null }) {
  const { t } = useI18n();
  const links = (values ?? []).filter((item) => item.trim() !== "");
  if (links.length === 0) {
    return <p className="bot-create-note">{t("verification.notProvided")}</p>;
  }
  return (
    <div className="about-text">
      {links.map((item, index) => (
        <div key={`${index}-${item}`}><SafeLink value={item} /></div>
      ))}
    </div>
  );
}

function EventKind({ kind }: { kind: VerificationEventKind }) {
  const { t } = useI18n();
  const tone = kind === "approved"
    ? "good"
    : kind === "rejected" || kind === "revoked" || kind === "cancelled"
      ? "danger"
      : kind === "submitted" || kind === "claimed"
        ? "warn"
        : "neutral";
  return <Badge tone={tone}>{t(`verification.kind.${kind}`)}</Badge>;
}
