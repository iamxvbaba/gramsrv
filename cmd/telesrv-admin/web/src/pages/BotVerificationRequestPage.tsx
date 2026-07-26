import {
  ArrowLeft,
  BadgeCheck,
  Ban,
  Building2,
  CheckCircle2,
  ExternalLink,
  RefreshCw,
  ShieldOff,
  Stamp,
  User,
  XCircle
} from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { api, APIError, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { Alert, Badge, LoadingSurface, PageFrame, SectionHead, SplitLayout, Summary } from "../components/ui";
import { useI18n } from "../i18n";
import { displayUsername, formatDate } from "../lib/format";
import type { Navigate } from "../routing";
import type { BotVerifierRow, CustomVerificationRequestDetail } from "../types";
import { RequestStatusBadge, peerHref, peerLabel } from "./BotVerificationPage";

export function BotVerificationRequestPage({ id, navigate }: { id: string; navigate: Navigate }) {
  const { t } = useI18n();
  const [detail, setDetail] = useState<CustomVerificationRequestDetail | null>(null);
  const [note, setNote] = useState("");
  const [conflict, setConflict] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function load() {
    setBusy(true);
    setError("");
    try {
      setDetail(await api.customVerificationRequest(id));
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
  // admin decided against the version this page read. The panel says so in plain
  // words and reloads, so the next attempt carries the current version.
  function handleActionError(err: unknown): string | undefined {
    if (err instanceof APIError && err.status === 409) {
      setConflict(true);
      void load();
      return t("botverification.conflict");
    }
    return undefined;
  }

  if (error && !detail) {
    return <Alert>{error}</Alert>;
  }
  if (!detail) {
    return <LoadingSurface label={t("botverification.loadingDetail")} />;
  }

  const request = detail.request;
  const verifier = liveVerifier(detail.verifier);
  const markActive = detail.mark_active;
  const canDecide = request.Status === "pending";
  const canRevoke = request.Status === "approved";
  const trimmedNote = note.trim();
  // What the mark would actually say: the applicant's wording only when this
  // verifier is allowed to override its own default, otherwise the default. Same
  // rule the backend applies (BotVerifierSettings.DescriptionFor), shown here so a
  // reviewer is not surprised by the text that ends up in the profile.
  const requestedDescription = request.RequestedDescription.trim();
  const descriptionAllowed = Boolean(verifier?.CanModifyCustomDescription) && requestedDescription !== "";
  const effectiveDescription = descriptionAllowed
    ? requestedDescription
    : (verifier?.DefaultDescription ?? "").trim();

  // version is the optimistic-locking token: it goes with every decision, as the
  // decimal string it arrived as, so a stale page cannot overwrite a fresh one.
  function decisionPayload(): Record<string, unknown> {
    const payload: Record<string, unknown> = { version: request.Version };
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
      title={t("botverification.detailTitle", { id: request.ID })}
      eyebrow={t("botverification.detailEyebrow")}
      actions={
        <>
          <button className="btn icon-text" type="button" onClick={() => navigate("/bot-verification")}>
            <ArrowLeft size={15} /> {t("common.backToList")}
          </button>
          <button className="btn icon-text" type="button" onClick={refresh} disabled={busy}>
            <RefreshCw size={15} className={busy ? "spin" : ""} /> {t("common.refresh")}
          </button>
        </>
      }
    >
      {error && <Alert>{error}</Alert>}
      {conflict && <Alert>{t("botverification.conflict")}</Alert>}
      <SplitLayout
        main={
          <div className="stacked-sections">
            <section className="entity-head">
              <div>
                <div className="entity-title">{peerLabel(request)}</div>
                <div className="entity-subtitle mono">
                  #{request.ID} · {t(`botverification.peer.${request.PeerType}`)}:{request.PeerID} · v{request.Version}
                </div>
              </div>
              <div className="entity-badges">
                <RequestStatusBadge status={request.Status} />
                {markActive
                  ? <Badge tone="good"><BadgeCheck size={12} /> {t("botverification.markActive")}</Badge>
                  : <Badge tone="neutral">{t("botverification.markInactive")}</Badge>}
              </div>
            </section>

            {/* Repeated on the detail page on purpose: the decision an operator is
                about to take grants a company's icon, not the platform badge. */}
            <section className="section-block">
              <SectionHead title={t("botverification.explainTitle")} text={t("botverification.explainText")} />
              <p className="bot-create-note">{t("botverification.explainIcon")}</p>
            </section>

            <section className="section-block">
              <SectionHead
                title={t("botverification.verifierSection")}
                text={t("botverification.verifierHint")}
                action={
                  <button className="btn icon-text" type="button" onClick={() => navigate(`/bots/${request.VerifierBotID}`)}>
                    <Building2 size={15} /> {t("botverification.openVerifier")}
                  </button>
                }
              />
              <div className="summary-grid">
                <Summary label={t("botverification.company")} value={verifier?.CompanyName || "-"} />
                <Summary label={t("botverification.bot")} value={displayUsername(request.VerifierBotUsername) || "-"} />
                <Summary label={t("botverification.verifierID")} value={request.VerifierBotID} mono />
                <Summary label={t("botverification.iconDocument")} value={verifier?.IconDocumentID || "-"} mono />
                <Summary label={t("botverification.iconName")} value={verifier?.IconName || "-"} />
                <Summary
                  label={t("botverification.canModifyShort")}
                  value={verifier?.CanModifyCustomDescription ? t("common.yes") : t("common.no")}
                />
              </div>
              <FieldBlock label={t("botverification.defaultDescription")}>
                {verifier?.DefaultDescription
                  ? <p className="about-text">{verifier.DefaultDescription}</p>
                  : <p className="bot-create-note">{t("botverification.notProvided")}</p>}
              </FieldBlock>
              {!verifier && <Alert>{t("botverification.verifierMissing")}</Alert>}
              {verifier && !verifier.Enabled && <Alert>{t("botverification.verifierDisabledHint")}</Alert>}
            </section>

            <section className="section-block">
              <SectionHead
                title={t("botverification.targetSection")}
                text={t("botverification.targetHint")}
                action={
                  <button
                    className="btn icon-text"
                    type="button"
                    onClick={() => navigate(peerHref(request.PeerType, request.PeerID))}
                  >
                    <ExternalLink size={15} /> {t("botverification.openTarget")}
                  </button>
                }
              />
              <div className="summary-grid">
                <Summary label={t("common.type")} value={t(`botverification.peer.${request.PeerType}`)} />
                <Summary label={t("common.username")} value={displayUsername(request.PeerUsername) || "-"} />
                <Summary label={t("botverification.targetTitle")} value={request.PeerTitle || "-"} />
                <Summary label={t("botverification.targetID")} value={request.PeerID} mono />
              </div>
            </section>

            <section className="section-block">
              <SectionHead
                title={t("botverification.applicantSection")}
                text={t("botverification.applicantHint")}
                action={
                  <button
                    className="btn icon-text"
                    type="button"
                    onClick={() => navigate(`/accounts/${request.ApplicantUserID}`)}
                  >
                    <User size={15} /> {t("botverification.openApplicant")}
                  </button>
                }
              />
              <div className="summary-grid">
                <Summary label={t("common.username")} value={displayUsername(request.ApplicantUsername) || "-"} />
                <Summary label={t("botverification.applicantID")} value={request.ApplicantUserID} mono />
                <Summary label={t("botverification.createdAt")} value={formatDate(request.CreatedAt) || "-"} />
                <Summary label={t("common.updatedAt")} value={formatDate(request.UpdatedAt) || "-"} />
              </div>
            </section>

            <section className="section-block">
              <SectionHead title={t("botverification.requestSection")} text={t("botverification.requestHint")} />
              <div className="stacked-sections">
                <div className="summary-grid">
                  <Summary label={t("botverification.correlationID")} value={request.CorrelationID || "-"} mono />
                  <Summary label={t("common.status")} value={t(`botverification.status.${request.Status}`)} />
                </div>
                <FieldBlock label={t("botverification.reason")}>
                  {request.Reason
                    ? <p className="about-text">{request.Reason}</p>
                    : <p className="bot-create-note">{t("botverification.notProvided")}</p>}
                </FieldBlock>
                <FieldBlock label={t("botverification.requestedDescription")}>
                  {requestedDescription
                    ? <p className="about-text">{requestedDescription}</p>
                    : <p className="bot-create-note">{t("botverification.notProvided")}</p>}
                </FieldBlock>
                <FieldBlock label={t("botverification.markPreview")}>
                  {effectiveDescription
                    ? <p className="about-text">{effectiveDescription}</p>
                    : <p className="bot-create-note">{t("botverification.notProvided")}</p>}
                </FieldBlock>
                <p className="bot-create-note">{t("botverification.markPreviewHint")}</p>
                {requestedDescription !== "" && !descriptionAllowed && (
                  <p className="bot-create-note">{t("botverification.descriptionIgnoredHint")}</p>
                )}
              </div>
            </section>

            <section className="section-block">
              <SectionHead title={t("botverification.decisionSection")} text={t("botverification.decisionHint")} />
              <div className="stacked-sections">
                <div className="summary-grid">
                  <Summary label={t("botverification.decidedBy")} value={request.DecidedBy || "-"} />
                  <Summary label={t("botverification.approvedAt")} value={formatDate(request.ApprovedAt) || "-"} />
                  <Summary label={t("botverification.rejectedAt")} value={formatDate(request.RejectedAt) || "-"} />
                  <Summary label={t("botverification.version")} value={request.Version} mono />
                </div>
                <FieldBlock label={t("botverification.decisionReason")}>
                  {request.DecisionReason
                    ? <p className="about-text">{request.DecisionReason}</p>
                    : <p className="bot-create-note">{t("botverification.noDecision")}</p>}
                </FieldBlock>
                {/* The internal note is the operator handover text and is labelled as
                    admin-only wherever it appears. */}
                <FieldBlock label={`${t("botverification.internalNote")} · ${t("botverification.adminOnly")}`}>
                  {request.InternalNote
                    ? <p className="about-text">{request.InternalNote}</p>
                    : <p className="bot-create-note">{t("botverification.notProvided")}</p>}
                </FieldBlock>
              </div>
            </section>
          </div>
        }
        side={
          <section className="action-dock">
            <div className="dock-title"><Stamp size={14} /> {t("botverification.actionDock")}</div>
            {!canDecide && !canRevoke && <p className="bot-create-note">{t("botverification.noActions")}</p>}
            {(canDecide || canRevoke) && (
              <>
                <label className="duration-field">
                  <span>{t("botverification.internalNote")}</span>
                  <textarea
                    value={note}
                    onChange={(event) => setNote(event.target.value)}
                    rows={3}
                    placeholder={t("botverification.internalNotePlaceholder")}
                  />
                </label>
                <p className="bot-create-note">{t("botverification.internalNoteHint")}</p>
              </>
            )}
            {canDecide && (
              <>
                {!verifier && <Alert>{t("botverification.verifierMissing")}</Alert>}
                {verifier && !verifier.Enabled && <Alert>{t("botverification.verifierDisabledHint")}</Alert>}
                {markActive && <p className="bot-create-note">{t("botverification.markActiveHint")}</p>}
                <div className="action-stack">
                  <ActionButton
                    label={t("botverification.approve")}
                    icon={<CheckCircle2 size={15} />}
                    tone="neutral"
                    path={`/api/botverification/requests/${request.ID}/approve`}
                    payload={decisionPayload}
                    onDone={afterDecision}
                    onError={handleActionError}
                  />
                  <ActionButton
                    label={t("botverification.reject")}
                    icon={<XCircle size={15} />}
                    tone="warn"
                    path={`/api/botverification/requests/${request.ID}/reject`}
                    payload={decisionPayload}
                    onDone={afterDecision}
                    onError={handleActionError}
                  />
                </div>
                <p className="bot-create-note">{t("botverification.approveHint")}</p>
                <p className="bot-create-note">{t("botverification.rejectHint")}</p>
              </>
            )}
            {canRevoke && (
              <>
                <div className="dock-title"><ShieldOff size={14} /> {t("botverification.dangerZone")}</div>
                <div className="danger-zone">
                  <ActionButton
                    label={t("botverification.revokeRequest")}
                    icon={<Ban size={15} />}
                    tone="danger"
                    path={`/api/botverification/requests/${request.ID}/revoke`}
                    payload={decisionPayload}
                    onDone={afterDecision}
                    onError={handleActionError}
                  />
                  <p className="bot-create-note">{t("botverification.revokeRequestHint")}</p>
                  {!markActive && <p className="bot-create-note">{t("botverification.revokeNoMark")}</p>}
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

// A verifier whose row was revoked after the application was filed can come back as
// null or as a zeroed record, depending on how the backend renders "gone". Both mean
// the same thing to a reviewer, so they collapse into one absent value here.
function liveVerifier(row: BotVerifierRow | null): BotVerifierRow | null {
  if (!row) return null;
  if (!row.BotID || row.BotID === "0") return null;
  return row;
}
