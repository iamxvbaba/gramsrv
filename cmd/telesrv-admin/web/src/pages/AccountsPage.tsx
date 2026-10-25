import { Check, ChevronLeft, ChevronRight, Copy, Loader2, RefreshCw, Search, Smartphone } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { Alert, Badge, EmptyRow, Metric, PageFrame, QueryPanel, UsernameCell } from "../components/ui";
import { ScamFakeBadges } from "../components/flags";
import { Avatar } from "../components/Avatar";
import { useI18n } from "../i18n";
import { displayName, displayPhone, formatDate, formatUnix } from "../lib/format";
import { accountMetrics } from "../lib/metrics";
import type { Navigate } from "../routing";
import type { AccountListResponse } from "../types";
import { SectionTabs } from "../components/SectionTabs";

type AccountCursor = { beforeID: number; beforeActiveUS: number };

const firstAccountPage: AccountCursor = { beforeID: 0, beforeActiveUS: 0 };

export function AccountsPage({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const [q, setQ] = useState("");
  const [limit, setLimit] = useState("50");
  const [data, setData] = useState<AccountListResponse | null>(null);
  const [filters, setFilters] = useState({ q: "", limit: "50" });
  const [cursor, setCursor] = useState<AccountCursor>(firstAccountPage);
  const [cursorHistory, setCursorHistory] = useState<AccountCursor[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [copiedID, setCopiedID] = useState<number | null>(null);

  async function copyUserID(userID: number) {
    try {
      await navigator.clipboard.writeText(String(userID));
      setCopiedID(userID);
      setTimeout(() => setCopiedID((current) => current === userID ? null : current), 1200);
    } catch {
      // Clipboard is best-effort.
    }
  }

  async function loadPage(target: AccountCursor, requestedFilters = filters, resetHistory = false) {
    setBusy(true);
    setError("");
    const params = new URLSearchParams({ limit: requestedFilters.limit });
    if (requestedFilters.q) {
      params.set("q", requestedFilters.q);
    } else if (target.beforeID > 0) {
      params.set("before_id", String(target.beforeID));
      params.set("before_active_us", String(target.beforeActiveUS));
    }
    try {
      const result = await api.accounts(params);
      setData(result);
      setFilters(requestedFilters);
      setCursor(target);
      if (resetHistory) setCursorHistory([]);
      return true;
    } catch (err) {
      setError(errorMessage(err));
      return false;
    } finally {
      setBusy(false);
    }
  }

  async function search() {
    const requestedFilters = { q: q.trim(), limit };
    await loadPage(firstAccountPage, requestedFilters, true);
  }

  async function nextPage() {
    if (!data?.listing || !data.has_more) return;
    const target = { beforeID: data.next_before_id, beforeActiveUS: data.next_before_active_us };
    if (await loadPage(target)) setCursorHistory((history) => [...history, cursor]);
  }

  async function previousPage() {
    const target = cursorHistory[cursorHistory.length - 1];
    if (!target) return;
    if (await loadPage(target)) setCursorHistory((history) => history.slice(0, -1));
  }

  useEffect(() => {
    void loadPage(firstAccountPage, { q: "", limit: "50" }, true);
  }, []);

  const metrics = accountMetrics(data?.rows ?? []);

  return (
    <PageFrame
      title={t("account.pageTitle")}
      eyebrow={data?.listing === false ? t("account.queryResults") : t("account.recentActive")}
      actions={
        <>
          <button className="btn icon-text" type="button" onClick={() => navigate("/accounts/shared-devices")}>
            <Smartphone size={15} /> {t("account.sharedDevices")}
          </button>
          <button className="btn" type="button" onClick={() => loadPage(cursor)} disabled={busy}>
            <RefreshCw size={15} /> {t("common.refresh")}
          </button>
        </>
      }
    >
      <SectionTabs sectionId="accounts" active="/accounts" navigate={navigate} />
      {error && <Alert>{error}</Alert>}
      <div className="metric-row">
        <Metric label={t("account.currentPage")} value={String(data?.rows.length ?? 0)} />
        <Metric label={t("account.onlineDevices")} value={String(metrics.devices)} />
        <Metric label={t("account.premium")} value={String(metrics.premium)} tone="good" />
        <Metric label={t("account.frozen")} value={String(metrics.frozen)} tone={metrics.frozen > 0 ? "danger" : "neutral"} />
      </div>
      <QueryPanel>
        <form className="toolbar" onSubmit={(event) => { event.preventDefault(); void search(); }}>
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
          {data?.listing && cursorHistory.length > 0 && (
            <button className="btn icon-text" type="button" onClick={() => previousPage()} disabled={busy}>
              <ChevronLeft size={15} /> {t("messages.previousPage")}
            </button>
          )}
          {data?.listing && data.has_more && (
            <button className="btn icon-text" type="button" onClick={() => nextPage()} disabled={busy}>
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
                <td className="mono">
                  <button
                    className="copy-id"
                    type="button"
                    title={copiedID === row.ID ? t("common.copied") : t("common.copy")}
                    onClick={() => void copyUserID(row.ID)}
                  >
                    <span>{row.ID}</span>
                    {copiedID === row.ID ? <Check size={12} /> : <Copy size={12} />}
                  </button>
                </td>
                <td>{displayPhone(row.Phone)}</td>
                <td><UsernameCell username={row.Username} collectibles={row.Collectibles} /></td>
                <td><span className="table-identity"><Avatar id={row.ID} label={displayName(row)} />{displayName(row)}</span></td>
                <td>{row.DeviceCount}</td>
                <td>{formatDate(row.LastActiveAt)}</td>
                <td>{row.PremiumUntil > 0 ? <Badge tone="good">{t("account.premium")} {formatUnix(row.PremiumUntil)}</Badge> : <Badge>{t("common.none")}</Badge>}</td>
                <td>{row.Verified ? <Badge tone="good">{t("common.verified")}</Badge> : <Badge>{t("account.notVerified")}</Badge>} <ScamFakeBadges scam={row.Scam} fake={row.Fake} /></td>
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
