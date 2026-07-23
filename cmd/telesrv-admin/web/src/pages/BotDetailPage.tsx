import { ArrowLeft, BadgeCheck, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { Alert, AuditTable, Badge, LoadingSurface, PageFrame, SectionHead, SplitLayout, Summary } from "../components/ui";
import { ScamFakeActions, ScamFakeBadges } from "../components/flags";
import { ColorAction, EmojiStatusAction, UsernameAction } from "../components/attributes";
import { useI18n } from "../i18n";
import { displayUsername, formatDate } from "../lib/format";
import type { Navigate } from "../routing";
import type { BotDetail } from "../types";

export function BotDetailPage({ id, navigate }: { id: number; navigate: Navigate }) {
  const { t } = useI18n();
  const [detail, setDetail] = useState<BotDetail | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function load() {
    setBusy(true);
    setError("");
    try {
      setDetail(await api.bot(id));
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
    return <LoadingSurface label={busy ? t("bots.loadingDetail") : t("account.waitingData")} />;
  }

  const bot = detail.Bot;
  return (
    <PageFrame
      title={t("bots.detailTitle", { id: bot.ID })}
      eyebrow={t("bots.profile")}
      actions={<button className="btn icon-text" onClick={() => navigate("/bots")}><ArrowLeft size={15} /> {t("common.backToList")}</button>}
    >
      <SplitLayout
        main={
          <div className="stacked-sections">
            <section className="entity-head">
              <div>
                <div className="entity-title">{bot.FirstName || t("bots.unnamed")}</div>
                <div className="entity-subtitle">{displayUsername(bot.Username) || t("account.noUsername")}</div>
              </div>
              <div className="entity-badges">
                <Badge tone={bot.System ? "warn" : "neutral"}>{bot.System ? t("bots.system") : t("bots.user")}</Badge>
                {bot.Verified ? <Badge tone="good">{t("common.verified")}</Badge> : <Badge>{t("account.notVerified")}</Badge>}
                <ScamFakeBadges scam={bot.Scam} fake={bot.Fake} />
              </div>
            </section>
            <div className="summary-grid">
              <Summary label={t("bots.botID")} value={String(bot.ID)} mono />
              <Summary label={t("bots.owner")} value={bot.OwnerUserID > 0 ? `${bot.OwnerUserID} ${displayUsername(detail.OwnerUsername)}`.trim() : t("common.none")} />
              <Summary label={t("bots.type")} value={bot.System ? t("bots.system") : t("bots.user")} />
              <Summary label={t("common.updatedAt")} value={formatDate(bot.UpdatedAt) || "-"} />
              <Summary label={t("account.createdAt")} value={formatDate(bot.CreatedAt) || "-"} />
            </div>
            {detail.About && <p className="about-text">{detail.About}</p>}
            {detail.Description && detail.Description.trim() !== detail.About.trim() && <p className="about-text">{detail.Description}</p>}
            <section className="section-block">
              <SectionHead title={t("account.recentAdminOps")} text={t("account.recent30Audit")} />
              <AuditTable rows={detail.AuditLogs} />
            </section>
          </div>
        }
        side={
          <section className="action-dock">
            <div className="dock-title">{t("bots.actionDock")}</div>
            <div className="action-stack">
              <ActionButton
                label={bot.Verified ? t("account.clearVerified") : t("account.setVerified")}
                icon={<BadgeCheck size={15} />}
                tone="neutral"
                path="/api/actions/set-verified"
                payload={() => ({ user_id: bot.ID, verified: !bot.Verified })}
                onDone={load}
              />
            </div>
            <ScamFakeActions idKey="user_id" id={bot.ID} path="/api/actions/set-account-flags" scam={bot.Scam} fake={bot.Fake} onDone={load} />
            <div className="dock-title">{t("attr.attributes")}</div>
            <UsernameAction idKey="user_id" id={bot.ID} path="/api/actions/set-account-username" current={bot.Username} onDone={load} />
            <ColorAction idKey="user_id" id={bot.ID} path="/api/actions/set-account-color" onDone={load} />
            <EmojiStatusAction idKey="user_id" id={bot.ID} path="/api/actions/set-account-emoji-status" onDone={load} />
            {bot.System ? (
              <p className="bot-create-note">{t("bots.systemHint")}</p>
            ) : (
              <div className="danger-zone">
                <ActionButton
                  label={t("bots.delete")}
                  icon={<Trash2 size={15} />}
                  tone="danger"
                  path="/api/actions/delete-bot"
                  payload={() => ({ bot_user_id: bot.ID })}
                  onDone={() => navigate("/bots")}
                />
                <p className="bot-create-note">{t("bots.deleteHint")}</p>
              </div>
            )}
          </section>
        }
      />
    </PageFrame>
  );
}
