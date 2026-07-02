import { ArrowLeft, BadgeCheck } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { Alert, AuditTable, Badge, JsonBlock, LoadingSurface, PageFrame, SectionHead, SplitLayout, Summary } from "../components/ui";
import { useI18n } from "../i18n";
import { channelKind, displayUsername, formatDate, formatUnix } from "../lib/format";
import type { Navigate } from "../routing";
import type { ChannelDetail } from "../types";

export function ChannelDetailPage({ id, navigate }: { id: number; navigate: Navigate }) {
  const { t } = useI18n();
  const [detail, setDetail] = useState<ChannelDetail | null>(null);
  const [error, setError] = useState("");

  async function load() {
    setError("");
    try {
      setDetail(await api.channel(id));
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  useEffect(() => {
    void load();
  }, [id]);

  if (error) {
    return <Alert>{error}</Alert>;
  }
  if (!detail) {
    return <LoadingSurface label={t("channel.loadingDetail")} />;
  }

  const ch = detail.Channel;
  return (
    <PageFrame
      title={`${channelKind(ch, t)} #${ch.ID}`}
      eyebrow={t("channel.detailProfile")}
      actions={<button className="btn icon-text" onClick={() => navigate("/channels")}><ArrowLeft size={15} /> {t("common.backToList")}</button>}
    >
      <SplitLayout
        main={
          <div className="stacked-sections">
            <section className="entity-head">
              <div>
                <div className="entity-title">{ch.Title || "-"}</div>
                <div className="entity-subtitle">{displayUsername(ch.Username) || t("account.noUsername")} · {t("channel.creator", { id: ch.CreatorUserID })}</div>
              </div>
              <div className="entity-badges">
                <Badge>{channelKind(ch, t)}</Badge>
                {ch.Verified ? <Badge tone="good">{t("common.verified")}</Badge> : <Badge>{t("account.notVerified")}</Badge>}
                {ch.Deleted ? <Badge tone="danger">{t("common.deleted")}</Badge> : <Badge>{t("common.valid")}</Badge>}
              </div>
            </section>
            <div className="summary-grid">
              <Summary label={t("channel.channelID")} value={String(ch.ID)} mono />
              <Summary label="access_hash" value={String(ch.AccessHash)} mono />
              <Summary label={t("common.members")} value={`${ch.ParticipantsCount} / ${t("common.admins")} ${ch.AdminsCount}`} />
              <Summary label={t("channel.governance")} value={t("channel.governanceValue", { banned: ch.BannedCount, kicked: ch.KickedCount })} />
              <Summary label={t("channel.flags")} value={`broadcast=${ch.Broadcast} megagroup=${ch.Megagroup} forum=${ch.Forum}`} />
              <Summary label="top / pinned / PTS" value={`${ch.TopMessageID} / ${ch.PinnedMessageID} / ${ch.PTS}`} />
              <Summary label={t("account.createdAt")} value={formatUnix(ch.Date) || "-"} />
              <Summary label={t("common.updatedAt")} value={formatDate(ch.UpdatedAt) || "-"} />
            </div>
            {ch.About && <p className="about-text">{ch.About}</p>}
            <section className="section-block">
              <SectionHead title={t("account.recentAdminOps")} text={t("account.recent30Audit")} />
              <AuditTable rows={detail.AuditLogs} />
            </section>
            <section className="section-block">
              <SectionHead title={t("channel.rawRow")} text={t("channel.rawRowText")} />
              <JsonBlock value={detail.ChannelJSON} />
            </section>
          </div>
        }
        side={
          <section className="action-dock">
            <div className="dock-title">{t("channel.actionDock")}</div>
            <ActionButton
              label={ch.Verified ? t("channel.clearVerified") : t("channel.setVerified")}
              icon={<BadgeCheck size={15} />}
              tone="warn"
              path="/api/actions/set-channel-verified"
              payload={() => ({ channel_id: ch.ID, verified: !ch.Verified })}
              onDone={load}
            />
          </section>
        }
      />
    </PageFrame>
  );
}
