import { ChevronRight, Search } from "lucide-react";
import { useState } from "react";
import { api, errorMessage } from "../api";
import { ChannelPicker } from "../components/EntityPicker";
import { Alert, Badge, EmptyRow, Metric, PageFrame, QueryPanel } from "../components/ui";
import { useI18n } from "../i18n";
import { channelKind, formatUnix } from "../lib/format";
import type { Navigate } from "../routing";
import type { ChannelRow, GroupMessageListResponse } from "../types";

export function GroupMessagesPage({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const [channel, setChannel] = useState<ChannelRow | null>(null);
  const [beforeDate, setBeforeDate] = useState("");
  const [beforeID, setBeforeID] = useState("");
  const [limit, setLimit] = useState("100");
  const [data, setData] = useState<GroupMessageListResponse | null>(null);
  const [error, setError] = useState("");

  async function load(next = false) {
    setError("");
    if (!channel) {
      setError(t("messages.selectChannel"));
      return;
    }
    const params = new URLSearchParams({
      channel_id: String(channel.ID),
      limit
    });
    if (next && data?.rows.length) {
      const last = data.rows[data.rows.length - 1];
      params.set("before_date", String(last.Date));
      params.set("before_id", String(last.ID));
      setBeforeDate(String(last.Date));
      setBeforeID(String(last.ID));
    } else {
      if (beforeDate) params.set("before_date", beforeDate);
      if (beforeID) params.set("before_id", beforeID);
    }
    try {
      setData(await api.groupMessages(params));
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  function changeChannel(row: ChannelRow | null) {
    setChannel(row);
    setBeforeDate("");
    setBeforeID("");
    setData(null);
  }

  const rows = data?.rows ?? [];

  return (
    <PageFrame title={t("messages.groupTitle")} eyebrow={t("messages.groupEyebrow")}>
      {error && <Alert>{error}</Alert>}
      <QueryPanel>
        <div className="message-selector-grid single">
          <ChannelPicker label={t("messages.channelGroup")} value={channel} onChange={changeChannel} />
        </div>
        <form className="toolbar message-query" onSubmit={(event) => { event.preventDefault(); void load(false); }}>
          <input value={beforeDate} onChange={(event) => setBeforeDate(event.target.value)} placeholder={t("messages.beforeDatePlaceholder")} />
          <input value={beforeID} onChange={(event) => setBeforeID(event.target.value)} placeholder={t("messages.beforeIDPlaceholder")} />
          <input className="small-input" value={limit} onChange={(event) => setLimit(event.target.value)} placeholder={t("messages.limitPlaceholder")} />
          <button className="btn primary icon-text" type="submit"><Search size={15} /> {t("messages.searchMessages")}</button>
          {rows.length ? <button className="btn icon-text" type="button" onClick={() => load(true)}><ChevronRight size={15} /> {t("messages.nextPage")}</button> : null}
        </form>
      </QueryPanel>
      <div className="metric-row">
        <Metric label={t("messages.currentPage")} value={String(rows.length)} />
        <Metric label={t("messages.mediaCount")} value={String(rows.filter((row) => row.Media && row.Media !== "{}").length)} />
        <Metric label={t("messages.channelPosts")} value={String(rows.filter((row) => row.Post).length)} />
        <Metric label={t("messages.channelGroup")} value={channel ? `${channel.Title || channelKind(channel, t)} (${channel.ID})` : "-"} />
      </div>
      <div className="table-wrap">
        <table className="data-table">
          <thead>
            <tr>
              <th>{t("common.messageId")}</th>
              <th>{t("common.time")}</th>
              <th>{t("common.sender")}</th>
              <th>From Peer</th>
              <th>PTS</th>
              <th>{t("common.views")}</th>
              <th>{t("common.status")}</th>
              <th>{t("messages.body")}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={`${row.ChannelID}-${row.ID}`}>
                <td className="mono">{row.ID}</td>
                <td>{formatUnix(row.Date)}</td>
                <td className="mono">{row.SenderUserID}</td>
                <td className="mono">{row.FromPeerType}:{row.FromPeerID}</td>
                <td>{row.PTS}</td>
                <td>{row.ViewsCount}</td>
                <td>
                  {row.Deleted ? <Badge tone="danger">{t("common.deleted")}</Badge> : row.Pinned ? <Badge tone="warn">{t("messages.pinned")}</Badge> : <Badge>{t("common.survived")}</Badge>}
                </td>
                <td className="truncate">{row.Body}</td>
                <td>
                  <button
                    className="row-link"
                    onClick={() => navigate(`/messages/groups/detail?channel_id=${row.ChannelID}&msg_id=${row.ID}`)}
                  >
                    {t("common.detail")} <ChevronRight size={14} />
                  </button>
                </td>
              </tr>
            ))}
            {rows.length === 0 && <EmptyRow colSpan={9} />}
          </tbody>
        </table>
      </div>
    </PageFrame>
  );
}
