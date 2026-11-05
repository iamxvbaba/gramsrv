import { ArrowLeft, Calculator, RefreshCw, SlidersHorizontal, User } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { Alert, Badge, EmptyRow, LoadingSurface, Metric, PageFrame, SectionHead, SplitLayout, Summary } from "../components/ui";
import { useI18n } from "../i18n";
import { displayUsername, formatDate, formatQuantity, formatSigned, toNumeric } from "../lib/format";
import type { Navigate } from "../routing";
import type { AccountRatingDetail, AccountRatingEventKind, AccountRatingRow } from "../types";
import { LevelBadge, RatingProgress, levelProgress } from "./AccountRatingsPage";

export function AccountRatingDetailPage({ userID, navigate }: { userID: string; navigate: Navigate }) {
  const { t } = useI18n();
  const [detail, setDetail] = useState<AccountRatingDetail | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [adjustment, setAdjustment] = useState("");

  async function load() {
    setBusy(true);
    setError("");
    try {
      setDetail(await api.accountRating(userID));
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    void load();
  }, [userID]);

  if (error && !detail) {
    return <Alert>{error}</Alert>;
  }
  if (!detail) {
    return <LoadingSurface label={busy ? t("rating.loadingDetail") : t("account.waitingData")} />;
  }

  const rating = detail.rating;
  const events = detail.events ?? [];
  const pending = toNumeric(rating.PendingStars);
  const progress = levelProgress(rating);
  // user_id / amount are `,string` int64 fields on the backend, so they stay
  // decimal strings and never pass through a float.
  const payloadUserID = rating.UserID || userID;

  return (
    <PageFrame
      title={t("rating.detailTitle", { user: displayUsername(rating.Username) || rating.FirstName || rating.UserID })}
      eyebrow={t("rating.detailEyebrow")}
      actions={
        <>
          <button className="btn icon-text" type="button" onClick={() => navigate("/account-ratings")}>
            <ArrowLeft size={15} /> {t("common.backToList")}
          </button>
          <button className="btn icon-text" type="button" onClick={load} disabled={busy}>
            <RefreshCw size={15} className={busy ? "spin" : ""} /> {t("common.refresh")}
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
                <div className="entity-title">{displayUsername(rating.Username) || rating.FirstName || t("bots.unnamed")}</div>
                <div className="entity-subtitle">{t("rating.userID")}: {rating.UserID}</div>
              </div>
              <div className="entity-badges">
                <LevelBadge level={rating.Level} />
                {pending !== 0 && <Badge tone="warn">{t("rating.pendingBadge", { amount: formatSigned(rating.PendingStars) })}</Badge>}
              </div>
            </section>

            <div className="metric-row">
              <Metric label={t("rating.stars")} value={formatQuantity(rating.Stars)} mono />
              <Metric label={t("rating.level")} value={String(rating.Level)} tone="good" />
              <Metric
                label={t("rating.nextLevel")}
                value={rating.HasNextLevel ? formatQuantity(rating.NextLevelStars) : t("rating.maxLevel")}
                mono={rating.HasNextLevel}
              />
              <Metric
                label={t("rating.toNextLevel")}
                value={rating.HasNextLevel ? formatQuantity(String(progress.remaining)) : "-"}
                mono
                tone={rating.HasNextLevel && progress.percent >= 80 ? "good" : "neutral"}
              />
            </div>

            <section className="section-block">
              <SectionHead title={t("rating.breakdownTitle")} text={t("rating.breakdownHint")} />
              <Breakdown rating={rating} />
              <div className="summary-grid">
                <Summary label={t("rating.currentLevelStars")} value={formatQuantity(rating.CurrentLevelStars)} mono />
                <Summary
                  label={t("rating.nextLevelStars")}
                  value={rating.HasNextLevel ? formatQuantity(rating.NextLevelStars) : t("rating.maxLevel")}
                  mono={rating.HasNextLevel}
                />
                <Summary label={t("rating.computedAt")} value={formatDate(rating.ComputedAt) || "-"} />
                <Summary label={t("common.updatedAt")} value={formatDate(rating.UpdatedAt) || "-"} />
              </div>
              <div className="progress-wide">
                <RatingProgress row={rating} />
              </div>
            </section>

            {pending !== 0 && (
              <section className="section-block">
                <SectionHead title={t("rating.pendingTitle")} text={t("rating.pendingHint")} />
                <div className="summary-grid">
                  <Summary label={t("rating.pending")} value={formatSigned(rating.PendingStars)} mono />
                  <Summary label={t("rating.pendingDate")} value={formatDate(rating.PendingDate) || "-"} />
                </div>
              </section>
            )}

            <section className="section-block">
              <SectionHead title={t("rating.eventsTitle")} text={t("rating.eventsHint")} />
              <div className="table-wrap">
                <table className="data-table">
                  <thead>
                    <tr>
                      <th>{t("common.id")}</th>
                      <th>{t("rating.eventKind")}</th>
                      <th>{t("rating.amount")}</th>
                      <th>{t("audit.reason")}</th>
                      <th>{t("audit.actor")}</th>
                      <th>{t("common.time")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {events.map((row) => (
                      <tr key={row.ID}>
                        <td className="mono">{row.ID}</td>
                        <td><EventKind kind={row.Kind} /></td>
                        <td className="mono">{formatSigned(row.Amount)}</td>
                        <td className="truncate">{row.Reason || "-"}</td>
                        <td>{row.Actor || "-"}</td>
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
            <div className="dock-title">{t("rating.actionDock")}</div>
            <button className="btn icon-text" type="button" onClick={() => navigate(`/accounts/${rating.UserID}`)}>
              <User size={15} /> {t("rating.openAccount")}
            </button>
            <div className="action-stack">
              <ActionButton
                label={t("rating.recompute")}
                icon={<Calculator size={15} />}
                tone="neutral"
                path="/api/actions/recompute-account-rating"
                payload={() => ({ user_id: payloadUserID })}
                onDone={load}
              />
            </div>
            <p className="bot-create-note">{t("rating.recomputeHint")}</p>
            <div className="dock-title">{t("rating.adjustTitle")}</div>
            <label className="duration-field">
              <span>{t("rating.adjustAmount")}</span>
              <input
                value={adjustment}
                onChange={(event) => setAdjustment(event.target.value)}
                type="number"
                step="1"
                placeholder="-500"
              />
            </label>
            <div className="action-stack">
              <ActionButton
                label={t("rating.adjust")}
                icon={<SlidersHorizontal size={15} />}
                tone="warn"
                path="/api/actions/adjust-account-rating"
                payload={() => ({
                  user_id: payloadUserID,
                  amount: String(Number.parseInt(adjustment.trim() || "0", 10) || 0)
                })}
                onDone={() => {
                  setAdjustment("");
                  void load();
                }}
              />
            </div>
            <p className="bot-create-note">{t("rating.adjustHint")}</p>
          </section>
        }
      />
    </PageFrame>
  );
}

function Breakdown({ rating }: { rating: AccountRatingRow }) {
  const { t } = useI18n();
  // PenaltyComponent is stored as a positive magnitude and subtracted by the
  // scorer, so it is shown (and summed) as a negative contribution.
  const components = [
    { key: "stars", label: t("rating.componentStars"), hint: t("rating.componentStarsHint"), value: toNumeric(rating.StarsComponent) },
    { key: "activity", label: t("rating.componentActivity"), hint: t("rating.componentActivityHint"), value: toNumeric(rating.ActivityComponent) },
    { key: "penalty", label: t("rating.componentPenalty"), hint: t("rating.componentPenaltyHint"), value: -toNumeric(rating.PenaltyComponent) },
    { key: "manual", label: t("rating.componentManual"), hint: t("rating.componentManualHint"), value: toNumeric(rating.ManualComponent) }
  ];
  const scale = Math.max(1, ...components.map((item) => Math.abs(item.value)));
  // The score is clamped at zero, and a delayed increase sits in PendingStars
  // instead of the score, so both cases are expected rather than drift.
  const sum = Math.max(0, components.reduce((total, item) => total + item.value, 0));
  const total = toNumeric(rating.Stars);
  const pending = toNumeric(rating.PendingStars);

  return (
    <>
      <div className="breakdown-list">
        {components.map((item) => {
          const percent = Math.min(100, (Math.abs(item.value) / scale) * 100);
          const tone = item.value < 0 ? "danger" : item.value > 0 ? "good" : "";
          return (
            <div className="breakdown-row" key={item.key}>
              <div className="breakdown-label">
                <strong>{item.label}</strong>
                <small>{item.hint}</small>
              </div>
              <div className={`progress-bar ${tone}`} role="img" aria-label={String(item.value)}>
                <span style={{ width: `${percent}%` }} />
              </div>
              <div className={`breakdown-value mono ${tone}`}>{formatSigned(String(item.value))}</div>
            </div>
          );
        })}
        <div className="breakdown-row total">
          <div className="breakdown-label"><strong>{t("rating.componentTotal")}</strong></div>
          <div className="breakdown-value mono">{formatQuantity(rating.Stars)}</div>
        </div>
      </div>
      {pending === 0 && sum !== total && (
        <Alert>{t("rating.breakdownMismatch", { sum: formatQuantity(String(sum)), total: formatQuantity(rating.Stars) })}</Alert>
      )}
      {pending !== 0 && <p className="bot-create-note">{t("rating.breakdownPending", { amount: formatSigned(rating.PendingStars) })}</p>}
    </>
  );
}

function EventKind({ kind }: { kind: AccountRatingEventKind }) {
  const { t } = useI18n();
  const tone = kind === "moderation" ? "danger" : kind === "manual" ? "warn" : kind === "recompute" ? "neutral" : "good";
  return <Badge tone={tone}>{t(`rating.kind.${kind}`)}</Badge>;
}
