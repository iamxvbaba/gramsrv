import { ArrowLeft, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { Alert, Badge, EmptyRow, JsonBlock, LoadingSurface, PageFrame, SectionHead, SplitLayout, Summary } from "../components/ui";
import { useI18n } from "../i18n";
import { formatDate, formatUnix } from "../lib/format";
import type { Navigate } from "../routing";
import type { MessageDetail } from "../types";

export function MessageDetailPage({ ownerUserID, msgID, navigate }: { ownerUserID: number; msgID: number; navigate: Navigate }) {
  const { t } = useI18n();
  const [detail, setDetail] = useState<MessageDetail | null>(null);
  const [error, setError] = useState("");

  async function load() {
    setError("");
    try {
      setDetail(await api.message(ownerUserID, msgID));
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  useEffect(() => {
    void load();
  }, [ownerUserID, msgID]);

  if (error) {
    return <Alert>{error}</Alert>;
  }
  if (!detail) {
    return <LoadingSurface label={t("common.loading")} />;
  }

  const msg = detail.Message;
  return (
    <PageFrame
      title={t("messages.privateDetailTitle", { id: msg.BoxID })}
      eyebrow={t("messages.detailEyebrow")}
      actions={<button className="btn icon-text" onClick={() => navigate("/messages/private")}><ArrowLeft size={15} /> {t("messages.backPrivate")}</button>}
    >
      <SplitLayout
        main={
          <div className="stacked-sections">
            <section className="entity-head">
              <div>
                <div className="entity-title">{t("messages.ownerPeerTitle", { owner: msg.OwnerUserID, peer: msg.PeerID })}</div>
                <div className="entity-subtitle">{t("messages.senderSubtitle", { sender: msg.FromUserID, date: formatUnix(msg.Date) })}</div>
              </div>
              <div className="entity-badges">
                {msg.Deleted ? <Badge tone="danger">{t("common.deleted")}</Badge> : <Badge>{t("common.survived")}</Badge>}
                <Badge>pts {msg.PTS}</Badge>
                <Badge>{msg.Outgoing ? t("messages.outgoing") : t("messages.incoming")}</Badge>
              </div>
            </section>
            <div className="summary-grid">
              <Summary label={t("messages.boxID")} value={String(msg.BoxID)} mono />
              <Summary label={t("messages.privateMessageID")} value={String(msg.PrivateMessageID)} mono />
              <Summary label={t("messages.messageSender")} value={String(msg.MessageSenderID)} mono />
              <Summary label={t("common.time")} value={formatUnix(msg.Date)} />
            </div>
            <section className="section-block">
              <SectionHead title={t("messages.messageBox")} text={t("messages.messageBoxesSnapshot")} />
              <JsonBlock value={detail.MessageJSON} />
            </section>
            <div className="raw-grid">
              <section className="section-block">
                <SectionHead title={t("messages.dialogRow")} text={t("messages.dialogSnapshot")} />
                <JsonBlock value={detail.DialogJSON} />
              </section>
              <section className="section-block">
                <SectionHead title={t("messages.privateRow")} text={t("messages.privateSnapshot")} />
                <JsonBlock value={detail.PrivateJSON} />
              </section>
            </div>
            <section className="section-block">
              <SectionHead title={t("messages.userUpdateEvents")} text={t("messages.userEventsSource")} />
              <div className="table-wrap">
                <table className="data-table">
                  <thead><tr><th>PTS</th><th>{t("common.count")}</th><th>{t("common.type")}</th><th>{t("common.time")}</th></tr></thead>
                  <tbody>
                    {detail.UpdateEvents.map((row) => <tr key={`${row.PTS}-${row.Type}`}><td>{row.PTS}</td><td>{row.PTSCount}</td><td>{row.Type}</td><td>{formatUnix(row.Date)}</td></tr>)}
                    {detail.UpdateEvents.length === 0 && <EmptyRow colSpan={4} />}
                  </tbody>
                </table>
              </div>
            </section>
            <section className="section-block">
              <SectionHead title={t("messages.dispatchOutbox")} text={t("messages.outboxSource")} />
              <div className="table-wrap">
                <table className="data-table">
                  <thead><tr><th>ID</th><th>{t("account.userID")}</th><th>PTS</th><th>{t("common.type")}</th><th>{t("common.status")}</th><th>{t("messages.attempts")}</th><th>{t("common.updatedAt")}</th></tr></thead>
                  <tbody>
                    {detail.Outbox.map((row) => <tr key={row.ID}><td>{row.ID}</td><td>{row.TargetUserID}</td><td>{row.PTS}</td><td>{row.EventType}</td><td>{row.Status}</td><td>{row.Attempts}</td><td>{formatDate(row.UpdatedAt)}</td></tr>)}
                    {detail.Outbox.length === 0 && <EmptyRow colSpan={7} />}
                  </tbody>
                </table>
              </div>
            </section>
          </div>
        }
        side={
          <section className="action-dock">
            <div className="dock-title">{t("common.operations")}</div>
            <ActionButton
              label={t("messages.deleteThis")}
              icon={<Trash2 size={15} />}
              path="/api/actions/delete-messages"
              payload={() => ({ owner_user_id: msg.OwnerUserID, peer_id: msg.PeerID, ids: [msg.BoxID], revoke: true })}
              onDone={load}
            />
          </section>
        }
      />
    </PageFrame>
  );
}
