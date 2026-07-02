import { ChevronRight, Loader2, RefreshCw, Search } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { Alert, Badge, EmptyRow, Metric, PageFrame, QueryPanel } from "../components/ui";
import { useI18n } from "../i18n";
import { channelKind, displayUsername, formatDate } from "../lib/format";
import { channelMetrics } from "../lib/metrics";
import type { Navigate } from "../routing";
import type { ChannelListResponse } from "../types";

export function ChannelsPage({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const [q, setQ] = useState("");
  const [limit, setLimit] = useState("50");
  const [data, setData] = useState<ChannelListResponse | null>(null);
  const [cursor, setCursor] = useState({ beforeID: 0, beforeUpdatedUS: 0 });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function load(next = false) {
    setBusy(true);
    setError("");
    const params = new URLSearchParams({ limit });
    if (q.trim()) {
      params.set("q", q.trim());
    } else if (next) {
      params.set("before_id", String(cursor.beforeID));
      params.set("before_updated_us", String(cursor.beforeUpdatedUS));
    }
    try {
      const result = await api.channels(params);
      setData(result);
      setCursor({
        beforeID: result.next_before_id,
        beforeUpdatedUS: result.next_before_updated_us
      });
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    void load(false);
  }, []);

  const metrics = channelMetrics(data?.rows ?? []);

  return (
    <PageFrame
      title={t("channel.pageTitle")}
      eyebrow={data?.listing === false ? t("account.queryResults") : t("channel.recentUpdated")}
      actions={
        <button className="btn" type="button" onClick={() => load(false)} disabled={busy}>
          <RefreshCw size={15} /> {t("common.refresh")}
        </button>
      }
    >
      {error && <Alert>{error}</Alert>}
      <div className="metric-row">
        <Metric label={t("channel.currentPage")} value={String(data?.rows.length ?? 0)} />
        <Metric label={t("channel.megagroups")} value={String(metrics.megagroups)} />
        <Metric label={t("channel.broadcasts")} value={String(metrics.broadcasts)} />
        <Metric label={t("channel.verifiedCount")} value={String(metrics.verified)} tone="good" />
      </div>
      <QueryPanel>
        <form className="toolbar" onSubmit={(event) => { event.preventDefault(); void load(false); }}>
          <label className="searchbox">
            <Search size={15} />
            <input value={q} onChange={(event) => setQ(event.target.value)} placeholder={t("channel.searchPlaceholder")} />
          </label>
          <label className="field-inline">
            <span>{t("common.limit")}</span>
            <input className="small-input" value={limit} onChange={(event) => setLimit(event.target.value)} type="number" min="1" max="100" />
          </label>
          <button className="btn primary icon-text" type="submit" disabled={busy}>
            {busy ? <Loader2 size={15} className="spin" /> : <Search size={15} />} {t("common.search")}
          </button>
          {data?.listing && data.has_more && (
            <button className="btn icon-text" type="button" onClick={() => load(true)} disabled={busy}>
              <ChevronRight size={15} /> {t("messages.nextPage")}
            </button>
          )}
        </form>
      </QueryPanel>
      <div className="table-wrap">
        <table className="data-table">
          <thead>
            <tr>
              <th>{t("channel.channelID")}</th>
              <th>{t("channel.kind")}</th>
              <th>{t("common.username")}</th>
              <th>{t("channel.title")}</th>
              <th>{t("common.members")}</th>
              <th>{t("common.admins")}</th>
              <th>PTS</th>
              <th>{t("common.verified")}</th>
              <th>{t("common.updatedAt")}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {data?.rows.map((row) => (
              <tr key={row.ID}>
                <td className="mono">{row.ID}</td>
                <td>{channelKind(row, t)}</td>
                <td>{displayUsername(row.Username)}</td>
                <td>{row.Title}</td>
                <td>{row.ParticipantsCount}</td>
                <td>{row.AdminsCount}</td>
                <td>{row.PTS}</td>
                <td>{row.Verified ? <Badge tone="good">{t("common.verified")}</Badge> : <Badge>{t("account.notVerified")}</Badge>}</td>
                <td>{formatDate(row.UpdatedAt)}</td>
                <td><button className="row-link" onClick={() => navigate(`/channels/${row.ID}`)}>{t("common.detail")} <ChevronRight size={14} /></button></td>
              </tr>
            ))}
            {(!data || data.rows.length === 0) && <EmptyRow colSpan={10} />}
          </tbody>
        </table>
      </div>
    </PageFrame>
  );
}
