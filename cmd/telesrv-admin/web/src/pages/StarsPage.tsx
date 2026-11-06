import { ChevronDown, ChevronRight, Coins, Loader2, RefreshCw, Search } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { Alert, Badge, EmptyRow, Metric, PageFrame, QueryPanel } from "../components/ui";
import { useI18n } from "../i18n";
import { displayName, displayPhone, displayUsername, formatDate, formatQuantity } from "../lib/format";
import type { Navigate } from "../routing";
import type { StarsAccountRow } from "../types";

export function StarsPage({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const [search, setSearch] = useState("");
  const [limit, setLimit] = useState("50");
  const [rows, setRows] = useState<StarsAccountRow[]>([]);
  const [hasMore, setHasMore] = useState(false);
  const [cursor, setCursor] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function load(next = false) {
    // One free-text field: the backend matches a username prefix (editable or
    // collectible), a first/last name or phone prefix, and a bare number as the
    // user id. An empty query is the leaderboard (positive balances only); once
    // an operator types a search every account that ever held stars is matchable,
    // so a balance spent down to zero stays findable for an audit.
    const wanted = search.trim();
    setBusy(true);
    setError("");
    const params = new URLSearchParams({ limit });
    if (wanted) params.set("q", wanted);
    if (next && cursor) params.set("before_id", cursor);
    try {
      const result = await api.starsTop(params);
      const page = result.rows ?? [];
      setRows((current) => (next ? [...current, ...page] : page));
      setCursor(result.next_before_id ?? "");
      setHasMore(Boolean(result.has_more));
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    void load(false);
  }, []);

  const totalBalance = rows.reduce((sum, row) => sum + Number(row.Balance), 0);
  const topBalance = rows.reduce((max, row) => Math.max(max, Number(row.Balance)), 0);
  const grantedCount = rows.filter((row) => row.Granted).length;

  return (
    <PageFrame
      title={t("stars.pageTitle")}
      eyebrow={t("stars.eyebrow")}
      actions={
        <button className="btn icon-text" type="button" onClick={() => load(false)} disabled={busy}>
          <RefreshCw size={15} className={busy ? "spin" : ""} /> {t("common.refresh")}
        </button>
      }
    >
      {error && <Alert>{error}</Alert>}
      <div className="metric-row">
        <Metric label={t("stars.metricLoaded")} value={String(rows.length)} />
        <Metric label={t("stars.metricTotal")} value={formatQuantity(String(totalBalance))} mono />
        <Metric label={t("stars.metricTop")} value={formatQuantity(String(topBalance))} tone="good" mono />
        <Metric label={t("stars.metricGranted")} value={String(grantedCount)} tone={grantedCount ? "warn" : "neutral"} />
      </div>

      <QueryPanel>
        <form className="toolbar" onSubmit={(event) => { event.preventDefault(); void load(false); }}>
          <label className="searchbox">
            <Search size={15} />
            <input value={search} onChange={(event) => setSearch(event.target.value)} placeholder={t("stars.searchPlaceholder")} />
          </label>
          <label className="field-inline">
            <span>{t("common.limit")}</span>
            <input className="small-input" value={limit} onChange={(event) => setLimit(event.target.value)} type="number" min="1" max="200" />
          </label>
          <button className="btn primary icon-text" type="submit" disabled={busy}>
            {busy ? <Loader2 size={15} className="spin" /> : <Search size={15} />} {t("common.search")}
          </button>
        </form>
      </QueryPanel>

      <div className="table-wrap">
        <table className="data-table">
          <thead>
            <tr>
              <th>{t("stars.userID")}</th>
              <th>{t("common.username")}</th>
              <th>{t("stars.name")}</th>
              <th>{t("stars.phone")}</th>
              <th>{t("stars.balance")}</th>
              <th>{t("stars.granted")}</th>
              <th>{t("stars.ledgerEntries")}</th>
              <th>{t("stars.updatedAt")}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.UserID}>
                <td className="mono">{row.UserID}</td>
                <td>{displayUsername(row.Username) || "-"}</td>
                <td>{displayName(row) === "-" ? "-" : displayName(row)}</td>
                <td className="mono">{displayPhone(row.Phone) || "-"}</td>
                <td className="mono">{formatQuantity(row.Balance)}</td>
                <td><GrantedBadge granted={row.Granted} /></td>
                <td className="mono">{formatQuantity(row.TxnCount)}</td>
                <td>{formatDate(row.UpdatedAt) || "-"}</td>
                <td>
                  <button className="row-link" type="button" onClick={() => navigate(`/stars/${row.UserID}`)}>
                    <Coins size={14} /> {t("common.detail")} <ChevronRight size={14} />
                  </button>
                </td>
              </tr>
            ))}
            {rows.length === 0 && <EmptyRow colSpan={9} />}
          </tbody>
        </table>
      </div>
      {hasMore && (
        <div className="toolbar">
          <button className="btn icon-text" type="button" onClick={() => load(true)} disabled={busy}>
            {busy ? <Loader2 size={15} className="spin" /> : <ChevronDown size={15} />} {t("common.loadMore")}
          </button>
        </div>
      )}
    </PageFrame>
  );
}

export function GrantedBadge({ granted }: { granted: boolean }) {
  const { t } = useI18n();
  return <Badge tone={granted ? "good" : "neutral"}>{t(granted ? "stars.grantedYes" : "stars.grantedNo")}</Badge>;
}