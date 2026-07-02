import { ArrowLeft, BadgeCheck, CircleAlert, Sparkles } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { AuthorizationTable } from "../components/AuthorizationTable";
import { Alert, AuditTable, Badge, LoadingSurface, PageFrame, SectionHead, SplitLayout, Summary } from "../components/ui";
import { useI18n } from "../i18n";
import { displayName, displayPhone, displayUsername, formatDate, formatUnix, toInt } from "../lib/format";
import type { Navigate } from "../routing";
import type { AccountDetail } from "../types";

export function AccountDetailPage({ id, navigate }: { id: number; navigate: Navigate }) {
  const { t } = useI18n();
  const [detail, setDetail] = useState<AccountDetail | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [months, setMonths] = useState("1");

  async function load() {
    setBusy(true);
    setError("");
    try {
      setDetail(await api.account(id));
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    void load();
  }, [id]);

  if (error) {
    return <Alert>{error}</Alert>;
  }
  if (!detail) {
    return <LoadingSurface label={busy ? t("account.loadingDetail") : t("account.waitingData")} />;
  }

  const account = detail.Account;
  return (
    <PageFrame
      title={t("account.detailTitle", { id: account.ID })}
      eyebrow={t("account.profile")}
      actions={<button className="btn icon-text" onClick={() => navigate("/accounts")}><ArrowLeft size={15} /> {t("common.backToList")}</button>}
    >
      <SplitLayout
        main={
          <div className="stacked-sections">
            <section className="entity-head">
              <div>
                <div className="entity-title">{displayName(account)}</div>
                <div className="entity-subtitle">{displayUsername(account.Username) || t("account.noUsername")} · {displayPhone(account.Phone) || t("account.noPhone")}</div>
              </div>
              <div className="entity-badges">
                {account.PremiumUntil > 0 ? <Badge tone="good">{t("account.premium")}</Badge> : <Badge>{t("account.notPremium")}</Badge>}
                {detail.Verified ? <Badge tone="good">{t("common.verified")}</Badge> : <Badge>{t("account.notVerified")}</Badge>}
                {account.Frozen ? <Badge tone="danger">{t("account.sendFrozen")}</Badge> : <Badge>{t("account.sendNormal")}</Badge>}
              </div>
            </section>
            <div className="summary-grid">
              <Summary label={t("account.userID")} value={String(account.ID)} mono />
              <Summary label={t("account.lastActive")} value={formatUnix(detail.LastSeenAt) || "-"} />
              <Summary label={t("account.premiumUntil")} value={account.PremiumUntil > 0 ? formatUnix(account.PremiumUntil) : t("common.none")} />
              <Summary label={t("common.updatedAt")} value={formatDate(account.UpdatedAt) || "-"} />
              <Summary label={t("account.activeSessions")} value={String(detail.Authorizations.length)} />
              <Summary label={t("account.accountFlags")} value={`support=${detail.Support} bot=${detail.Bot}`} />
              <Summary label={t("account.restriction")} value={detail.HasRestriction ? detail.Restriction.Reason || t("account.restricted") : t("common.none")} />
              <Summary label={t("account.createdAt")} value={formatDate(account.CreatedAt) || "-"} />
            </div>
            {detail.About && <p className="about-text">{detail.About}</p>}
            <section className="section-block">
              <SectionHead title={t("account.authorizationsTitle")} text={t("account.authorizationsCount", { count: detail.Authorizations.length })} />
              <AuthorizationTable rows={detail.Authorizations} userID={account.ID} onDone={load} />
            </section>
            <section className="section-block">
              <SectionHead title={t("account.recentAdminOps")} text={t("account.recent30Audit")} />
              <AuditTable rows={detail.AuditLogs} />
            </section>
          </div>
        }
        side={
          <section className="action-dock">
            <div className="dock-title">{t("account.actionDock")}</div>
            <ActionButton
              label={account.Frozen ? t("account.unfreezeSend") : t("account.freezeSend")}
              icon={<CircleAlert size={15} />}
              path="/api/actions/freeze-send"
              payload={() => ({ user_id: account.ID, frozen: !account.Frozen })}
              onDone={load}
            />
            <label className="duration-field">
              <span>{t("account.premiumMonths")}</span>
              <input
                aria-label={t("account.premiumMonthsAria")}
                value={months}
                onChange={(event) => setMonths(event.target.value)}
                type="number"
                min="1"
                max="120"
              />
            </label>
            <div className="action-stack">
              <ActionButton
                label={t("account.setPremium")}
                icon={<Sparkles size={15} />}
                tone="warn"
                path="/api/actions/grant-premium"
                payload={() => ({ user_id: account.ID, months: toInt(months) })}
                onDone={load}
              />
              <ActionButton
                label={t("account.clearPremium")}
                icon={<Sparkles size={15} />}
                tone="warn"
                path="/api/actions/grant-premium"
                payload={() => ({ user_id: account.ID, months: 0 })}
                onDone={load}
              />
              <ActionButton
                label={detail.Verified ? t("account.clearVerified") : t("account.setVerified")}
                icon={<BadgeCheck size={15} />}
                tone="warn"
                path="/api/actions/set-verified"
                payload={() => ({ user_id: account.ID, verified: !detail.Verified })}
                onDone={load}
              />
            </div>
          </section>
        }
      />
    </PageFrame>
  );
}
