import { ChevronRight, Loader2, RefreshCw, Search } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { Alert, Badge, EmptyRow, Metric, PageFrame, QueryPanel } from "../components/ui";
import { useI18n } from "../i18n";
import { displayName, displayPhone, displayUsername, formatDate, formatUnix } from "../lib/format";
import { accountMetrics } from "../lib/metrics";
import type { Navigate } from "../routing";
import type { AccountListResponse } from "../types";

export function AccountsPage({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const [q, setQ] = useState("");
  const [limit, setLimit] = useState("50");
  const [data, setData] = useState<AccountListResponse | null>(null);
  const [cursor, setCursor] = useState({ beforeID: 0, beforeActiveUS: 0 });
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
      params.set("before_active_us", String(cursor.beforeActiveUS));
    }
    try {
      const result = await api.accounts(params);
      setData(result);
      setCursor({
        beforeID: result.next_before_id,
        beforeActiveUS: result.next_before_active_us
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

  const metrics = accountMetrics(data?.rows ?? []);

  return (
    <PageFrame
      title={t("account.pageTitle")}
      eyebrow={data?.listing === false ? t("account.queryResults") : t("account.recentActive")}
      actions={
        <button className="btn" type="button" onClick={() => load(false)} disabled={busy}>
          <RefreshCw size={15} /> {t("common.refresh")}
        </button>
      }
    >
      {error && <Alert>{error}</Alert>}
      <div className="metric-row">
        <Metric label={t("account.currentPage")} value={String(data?.rows.length ?? 0)} />
        <Metric label={t("account.onlineDevices")} value={String(metrics.devices)} />
        <Metric label={t("account.premium")} value={String(metrics.premium)} tone="good" />
        <Metric label={t("account.frozen")} value={String(metrics.frozen)} tone={metrics.frozen > 0 ? "danger" : "neutral"} />
      </div>
      <QueryPanel>
        <form className="toolbar" onSubmit={(event) => { event.preventDefault(); void load(false); }}>
          <label className="searchbox">
            <Search size={15} />
            <input value={q} onChange={(event) => setQ(event.target.value)} placeholder={t("account.searchPlaceholder")} />
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
              <th>{t("account.userID")}</th>
              <th>{t("account.phone")}</th>
              <th>{t("common.username")}</th>
              <th>{t("common.name")}</th>
              <th>{t("common.device")}</th>
              <th>{t("account.lastActive")}</th>
              <th>{t("account.premium")}</th>
              <th>{t("common.verified")}</th>
              <th>{t("account.frozen")}</th>
              <th>{t("common.updatedAt")}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {data?.rows.map((row) => (
              <tr key={row.ID}>
                <td className="mono">{row.ID}</td>
                <td>{displayPhone(row.Phone)}</td>
                <td>{displayUsername(row.Username)}</td>
                <td>{displayName(row)}</td>
                <td>{row.DeviceCount}</td>
                <td>{formatDate(row.LastActiveAt)}</td>
                <td>{row.PremiumUntil > 0 ? <Badge tone="good">{t("account.premium")} {formatUnix(row.PremiumUntil)}</Badge> : <Badge>{t("common.none")}</Badge>}</td>
                <td>{row.Verified ? <Badge tone="good">{t("common.verified")}</Badge> : <Badge>{t("account.notVerified")}</Badge>}</td>
                <td>{row.Frozen ? <Badge tone="danger">{t("account.frozen")}</Badge> : <Badge>{t("common.normal")}</Badge>}</td>
                <td>{formatDate(row.UpdatedAt)}</td>
                <td><button className="row-link" onClick={() => navigate(`/accounts/${row.ID}`)}>{t("common.detail")} <ChevronRight size={14} /></button></td>
              </tr>
            ))}
            {(!data || data.rows.length === 0) && <EmptyRow colSpan={11} />}
          </tbody>
        </table>
      </div>
    </PageFrame>
  );
}
