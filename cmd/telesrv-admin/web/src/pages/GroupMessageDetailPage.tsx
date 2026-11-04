import { ArrowLeft } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { MessagePreview, type PreviewMessage } from "../components/MessagePreview";
import { Alert, Badge, EmptyRow, JsonBlock, LoadingSurface, PageFrame, SectionHead, Summary } from "../components/ui";
import { useI18n } from "../i18n";
import { formatUnix } from "../lib/format";
import { mediaLocationKey } from "../lib/media";
import type { Navigate } from "../routing";
import type { GroupMessageDetail } from "../types";

export function GroupMessageDetailPage({ channelID, msgID, navigate }: { channelID: number; msgID: number; navigate: Navigate }) {
  const { t } = useI18n();
  const [detail, setDetail] = useState<GroupMessageDetail | null>(null);
  const [preview, setPreview] = useState<PreviewMessage[]>([]);
  const [error, setError] = useState("");

  async function load() {
    setError("");
    try {
      setDetail(await api.groupMessage(channelID, msgID));
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  useEffect(() => {
    void load();
  }, [channelID, msgID]);

  // Live conversation preview for the channel, from the same route the
  // Messages console uses.
  useEffect(() => {
    if (!detail) return;
    let cancelled = false;
    api
      .groupMessages(new URLSearchParams({ channel_id: String(detail.Message.ChannelID), limit: "50" }))
      .then((data) => {
        if (cancelled) return;
        setPreview(
          data.rows.map((row) => ({
            id: row.ID,
            date: row.Date,
            from: row.SenderUserID,
            body: row.Body,
            deleted: row.Deleted,
            media: Boolean(row.Media && row.Media !== "{}"),
            mediaURL: (() => { const key = mediaLocationKey(row.Media); return key ? api.mediaURL(key) : undefined; })(),
            post: row.Post
          }))
        );
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [detail]);

  if (error) {
    return <Alert>{error}</Alert>;
  }
  if (!detail) {
    return <LoadingSurface label={t("common.loading")} />;
  }

  const msg = detail.Message;
  return (
    <PageFrame
      title={t("messages.groupDetailTitle", { id: msg.ID })}
      eyebrow={t("messages.detailEyebrow")}
      actions={<button className="btn icon-text" onClick={() => navigate("/messages/groups")}><ArrowLeft size={15} /> {t("messages.backGroup")}</button>}
    >
      <div className="stacked-sections">
        <section className="entity-head">
          <div>
            <div className="entity-title">{t("messages.channelGroupTitle", { id: msg.ChannelID })}</div>
            <div className="entity-subtitle">{t("messages.senderSubtitle", { sender: msg.SenderUserID, date: formatUnix(msg.Date) })}</div>
          </div>
          <div className="entity-badges">
            {msg.Deleted ? <Badge tone="danger">{t("common.deleted")}</Badge> : <Badge>{t("common.survived")}</Badge>}
            {msg.Pinned && <Badge tone="warn">{t("messages.pinned")}</Badge>}
            {msg.Post && <Badge>{t("messages.channelPost")}</Badge>}
            <Badge>pts {msg.PTS}</Badge>
          </div>
        </section>
        <div className="summary-grid">
          <Summary label={t("common.messageId")} value={String(msg.ID)} mono />
          <Summary label={t("messages.channelGroup")} value={String(msg.ChannelID)} mono />
          <Summary label={t("common.fromPeer")} value={`${msg.FromPeerType}:${msg.FromPeerID}`} mono />
          <Summary label={t("common.views")} value={String(msg.ViewsCount)} />
        </div>
        <MessagePreview title={t("messages.groupTitle")} emptyLabel={"No messages"} messages={preview} focusID={msg.ID} />
        <details className="section-block">
          <summary>{"Raw records"}</summary>
          <section className="section-block">
            <SectionHead title={t("messages.channelMessageRow")} text={t("messages.channelMessagesSnapshot")} />
            <JsonBlock value={detail.MessageJSON} />
          </section>
          <section className="section-block">
            <SectionHead title={t("messages.channelRow")} text={t("messages.channelSnapshot")} />
            <JsonBlock value={detail.ChannelJSON} />
          </section>
          <section className="section-block">
            <SectionHead title={t("messages.eventJson")} />
            <div className="raw-grid">
              {detail.UpdateEvents.map((row) => (
                <JsonBlock key={`${row.PTS}-${row.Type}-json`} value={row.JSON} />
              ))}
              {detail.UpdateEvents.length === 0 && <div className="empty-panel">{t("common.noResults")}</div>}
            </div>
          </section>
        </details>
      </div>
    </PageFrame>
  );
}
