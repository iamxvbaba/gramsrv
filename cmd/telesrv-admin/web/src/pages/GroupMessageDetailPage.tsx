import { ArrowLeft } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { MessagePreview, type PreviewMessage } from "../components/MessagePreview";
import { Alert, Badge, EmptyRow, JsonBlock, LoadingSurface, PageFrame, SectionHead, Summary } from "../components/ui";
import { useI18n } from "../i18n";
import { formatUnix } from "../lib/format";
import { mediaFields } from "../lib/media";
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

  // Live conversation preview ending at this message (inclusive): the 50
  // messages up to and including it, from the same route the Messages console
  // uses. The cursor is strict, (date, id) < (before_date, before_id), so
  // id + 1 keeps the message itself in the page.
  useEffect(() => {
    if (!detail) return;
    let cancelled = false;
    api
      .groupMessages(new URLSearchParams({
        channel_id: String(detail.Message.ChannelID),
        before_date: String(detail.Message.Date),
        before_id: String(detail.Message.ID + 1),
        limit: "50"
      }))
      .then((data) => {
        if (cancelled) return;
        setPreview(
          data.rows.map((row) => ({
            id: row.ID,
            displayID: row.ID,
            date: row.Date,
            from: row.SenderUserID,
            body: row.Body,
            deleted: row.Deleted,
            media: Boolean(row.Media && row.Media !== "{}"),
            ...mediaFields(row.Media),
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
        <MessagePreview title={t("messages.groupTitle")} emptyLabel={t("messages.noMessages")} messages={preview} focusID={msg.ID} />
        <section className="section-block">
          <SectionHead title={t("messages.channelUpdateEvents")} text={t("messages.channelEventsSource")} />
          <div className="table-wrap">
            <table className="data-table">
              <thead><tr><th>PTS</th><th>{t("common.count")}</th><th>{t("common.type")}</th><th>{t("common.messageId")}</th><th>{t("common.sender")}</th><th>{t("common.time")}</th></tr></thead>
              <tbody>
                {detail.UpdateEvents.map((row) => (
                  <tr key={`${row.PTS}-${row.Type}-${row.MessageID}`}>
                    <td>{row.PTS}</td>
                    <td>{row.PTSCount}</td>
                    <td>{row.Type}</td>
                    <td>{row.MessageID}</td>
                    <td>{row.SenderUserID}</td>
                    <td>{formatUnix(row.Date)}</td>
                  </tr>
                ))}
                {detail.UpdateEvents.length === 0 && <EmptyRow colSpan={6} />}
              </tbody>
            </table>
          </div>
        </section>
        <details className="section-block">
          <summary>{t("messages.rawRecords")}</summary>
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
