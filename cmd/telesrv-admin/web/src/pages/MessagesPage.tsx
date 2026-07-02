import { ChevronRight, History, Search, Trash2 } from "lucide-react";
import { useState } from "react";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { UserPicker } from "../components/EntityPicker";
import { Alert, Badge, EmptyRow, Metric, PageFrame, QueryPanel } from "../components/ui";
import { useI18n } from "../i18n";
import { displayName, formatUnix, parseIDs, toInt } from "../lib/format";
import type { Navigate } from "../routing";
import type { AccountRow, MessageListResponse } from "../types";

export function MessagesPage({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const [owner, setOwner] = useState<AccountRow | null>(null);
  const [peer, setPeer] = useState<AccountRow | null>(null);
  const [beforeDate, setBeforeDate] = useState("");
  const [beforeID, setBeforeID] = useState("");
  const [limit, setLimit] = useState("100");
  const [ids, setIDs] = useState("");
  const [revoke, setRevoke] = useState(true);
  const [justClear, setJustClear] = useState(false);
  const [maxID, setMaxID] = useState("");
  const [maxBatches, setMaxBatches] = useState("1");
  const [data, setData] = useState<MessageListResponse | null>(null);
  const [error, setError] = useState("");

  async function load(next = false) {
    setError("");
    if (!owner || !peer) {
      setError(t("messages.selectPrivatePeers"));
      return;
    }
    const params = new URLSearchParams({
      owner_user_id: String(owner.ID),
      peer_id: String(peer.ID),
      limit
    });
    if (next && data?.rows.length) {
      const last = data.rows[data.rows.length - 1];
      params.set("before_date", String(last.Date));
      params.set("before_id", String(last.BoxID));
      setBeforeDate(String(last.Date));
      setBeforeID(String(last.BoxID));
    } else {
      if (beforeDate) params.set("before_date", beforeDate);
      if (beforeID) params.set("before_id", beforeID);
    }
    try {
      setData(await api.messages(params));
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  function changeOwner(row: AccountRow | null) {
    setOwner(row);
    setBeforeDate("");
    setBeforeID("");
    setData(null);
  }

  function changePeer(row: AccountRow | null) {
    setPeer(row);
    setBeforeDate("");
    setBeforeID("");
    setData(null);
  }

  return (
    <PageFrame title={t("messages.privateTitle")} eyebrow={t("messages.privateEyebrow")}>
      {error && <Alert>{error}</Alert>}
      <QueryPanel>
        <div className="message-selector-grid">
          <UserPicker label={t("messages.ownerUser")} value={owner} onChange={changeOwner} />
          <UserPicker label={t("messages.peerUser")} value={peer} onChange={changePeer} />
        </div>
        <form className="toolbar message-query" onSubmit={(event) => { event.preventDefault(); void load(false); }}>
          <input value={beforeDate} onChange={(event) => setBeforeDate(event.target.value)} placeholder={t("messages.beforeDatePlaceholder")} />
          <input value={beforeID} onChange={(event) => setBeforeID(event.target.value)} placeholder={t("messages.beforeIDPlaceholder")} />
          <input className="small-input" value={limit} onChange={(event) => setLimit(event.target.value)} placeholder={t("messages.limitPlaceholder")} />
          <button className="btn primary icon-text" type="submit"><Search size={15} /> {t("messages.searchMessages")}</button>
          {data?.rows.length ? <button className="btn icon-text" type="button" onClick={() => load(true)}><ChevronRight size={15} /> {t("messages.nextPage")}</button> : null}
        </form>
      </QueryPanel>
      <div className="metric-row">
        <Metric label={t("messages.currentPage")} value={String(data?.rows.length ?? 0)} />
        <Metric label={t("messages.deleted")} value={String((data?.rows ?? []).filter((row) => row.Deleted).length)} tone="danger" />
        <Metric label={t("messages.outgoing")} value={String((data?.rows ?? []).filter((row) => row.Outgoing).length)} />
        <Metric label={t("messages.ownerPeer")} value={owner && peer ? `${displayName(owner)} / ${displayName(peer)}` : "-"} />
      </div>
      <div className="operation-row">
        <div className="operation-box">
          <div className="operation-title"><Trash2 size={15} /> {t("messages.deleteSelected")}</div>
          <input value={ids} onChange={(event) => setIDs(event.target.value)} placeholder={t("messages.idsPlaceholder")} />
          <label className="checkline"><input type="checkbox" checked={revoke} onChange={(event) => setRevoke(event.target.checked)} /> {t("messages.revoke")}</label>
          <ActionButton path="/api/actions/delete-messages" label={t("messages.previewDelete")} payload={() => ({
            owner_user_id: owner?.ID ?? 0,
            peer_id: peer?.ID ?? 0,
            ids: parseIDs(ids, t("messages.msgIDsInvalid")),
            revoke
          })} />
        </div>
        <div className="operation-box">
          <div className="operation-title"><History size={15} /> {t("messages.clearHistory")}</div>
          <input value={maxID} onChange={(event) => setMaxID(event.target.value)} placeholder={t("messages.maxIDPlaceholder")} />
          <input value={maxBatches} onChange={(event) => setMaxBatches(event.target.value)} placeholder={t("messages.maxBatchesPlaceholder")} />
          <label className="checkline"><input type="checkbox" checked={revoke} onChange={(event) => setRevoke(event.target.checked)} /> {t("messages.revoke")}</label>
          <label className="checkline"><input type="checkbox" checked={justClear} onChange={(event) => setJustClear(event.target.checked)} /> {t("messages.justClear")}</label>
          <ActionButton path="/api/actions/delete-history" label={t("messages.previewClearHistory")} payload={() => ({
            owner_user_id: owner?.ID ?? 0,
            peer_id: peer?.ID ?? 0,
            max_id: toInt(maxID),
            max_batches: toInt(maxBatches),
            just_clear: justClear,
            revoke
          })} />
        </div>
      </div>
      <div className="table-wrap">
        <table className="data-table">
          <thead>
            <tr>
              <th>{t("common.messageId")}</th>
              <th>{t("common.time")}</th>
              <th>{t("common.sender")}</th>
              <th>{t("messages.direction")}</th>
              <th>PTS</th>
              <th>{t("common.status")}</th>
              <th>{t("messages.body")}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {data?.rows.map((row) => (
              <tr key={`${row.OwnerUserID}-${row.BoxID}`}>
                <td className="mono">{row.BoxID}</td>
                <td>{formatUnix(row.Date)}</td>
                <td className="mono">{row.FromUserID}</td>
                <td>{row.Outgoing ? t("messages.outgoing") : t("messages.incoming")}</td>
                <td>{row.PTS}</td>
                <td>{row.Deleted ? <Badge tone="danger">{t("common.deleted")}</Badge> : <Badge>{t("common.survived")}</Badge>}</td>
                <td className="truncate">{row.Body}</td>
                <td>
                  <button
                    className="row-link"
                    onClick={() => navigate(`/messages/private/detail?owner_user_id=${row.OwnerUserID}&msg_id=${row.BoxID}`)}
                  >
                    {t("common.detail")} <ChevronRight size={14} />
                  </button>
                </td>
              </tr>
            ))}
            {(!data || data.rows.length === 0) && <EmptyRow colSpan={8} />}
          </tbody>
        </table>
      </div>
    </PageFrame>
  );
}
