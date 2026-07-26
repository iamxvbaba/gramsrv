import { ChevronDown, ChevronRight, Loader2, RefreshCw, Search, Trophy } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { Alert, Badge, EmptyRow, Metric, PageFrame, QueryPanel } from "../components/ui";
import { useI18n } from "../i18n";
import { displayUsername, formatDate, formatQuantity, toNumeric } from "../lib/format";
import type { Navigate } from "../routing";
import type { AccountRatingRow } from "../types";

export function AccountRatingsPage({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const [minLevel, setMinLevel] = useState("");
  const [search, setSearch] = useState("");
  const [limit, setLimit] = useState("50");
  const [rows, setRows] = useState<AccountRatingRow[]>([]);
  const [hasMore, setHasMore] = useState(false);
  const [cursor, setCursor] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function load(next = false) {
    // One free-text field: the backend matches a username prefix (editable or
    // collectible), a first/last name prefix, and a bare number as the user id.
    const wanted = search.trim();
    setBusy(true);
    setError("");
    const params = new URLSearchParams({ limit });
    if (minLevel.trim()) params.set("min_level", minLevel.trim());
    if (wanted) params.set("q", wanted);
    if (next && cursor) params.set("before_id", cursor);
    try {
      const result = await api.accountRatings(params);
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

  const topLevel = rows.reduce((max, row) => Math.max(max, row.Level), 0);
  const pendingCount = rows.filter((row) => toNumeric(row.PendingStars) !== 0).length;
  const avgLevel = rows.length > 0
    ? (rows.reduce((sum, row) => sum + row.Level, 0) / rows.length).toFixed(1)
    : "0";

  return (
    <PageFrame
      title={t("rating.pageTitle")}
      eyebrow={t("rating.eyebrow")}
      actions={
        <button className="btn icon-text" type="button" onClick={() => load(false)} disabled={busy}>
          <RefreshCw size={15} className={busy ? "spin" : ""} /> {t("common.refresh")}
        </button>
      }
    >
      {error && <Alert>{error}</Alert>}
      <div className="metric-row">
        <Metric label={t("rating.metricLoaded")} value={String(rows.length)} />
        <Metric label={t("rating.metricTopLevel")} value={String(topLevel)} tone="good" />
        <Metric label={t("rating.metricAvgLevel")} value={avgLevel} />
        <Metric label={t("rating.metricPending")} value={String(pendingCount)} tone={pendingCount ? "warn" : "neutral"} />
      </div>

      <QueryPanel>
        <form className="toolbar" onSubmit={(event) => { event.preventDefault(); void load(false); }}>
          <label className="searchbox">
            <Search size={15} />
            <input value={search} onChange={(event) => setSearch(event.target.value)} placeholder={t("rating.searchPlaceholder")} />
          </label>
          <label className="field-inline">
            <span>{t("rating.minLevel")}</span>
            <input className="small-input" value={minLevel} onChange={(event) => setMinLevel(event.target.value)} type="number" min="0" placeholder="0" />
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
              <th>{t("rating.userID")}</th>
              <th>{t("common.username")}</th>
              <th>{t("rating.level")}</th>
              <th>{t("rating.stars")}</th>
              <th>{t("rating.progress")}</th>
              <th>{t("rating.pending")}</th>
              <th>{t("rating.computedAt")}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.UserID}>
                <td className="mono">{row.UserID}</td>
                <td>{displayUsername(row.Username) || row.FirstName || "-"}</td>
                <td><LevelBadge level={row.Level} /></td>
                <td className="mono">{formatQuantity(row.Stars)}</td>
                <td><RatingProgress row={row} /></td>
                <td className="mono">{toNumeric(row.PendingStars) !== 0 ? formatQuantity(row.PendingStars) : "-"}</td>
                <td>{formatDate(row.ComputedAt) || "-"}</td>
                <td>
                  <button className="row-link" type="button" onClick={() => navigate(`/account-ratings/${row.UserID}`)}>
                    <Trophy size={14} /> {t("common.detail")} <ChevronRight size={14} />
                  </button>
                </td>
              </tr>
            ))}
            {rows.length === 0 && <EmptyRow colSpan={8} />}
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

export function LevelBadge({ level }: { level: number }) {
  const { t } = useI18n();
  const tone = level >= 10 ? "good" : level >= 5 ? "warn" : "neutral";
  return <Badge tone={tone}>{t("rating.levelValue", { level })}</Badge>;
}

export function levelProgress(row: AccountRatingRow): { percent: number; remaining: number; target: number; stars: number } {
  const stars = toNumeric(row.Stars);
  const current = toNumeric(row.CurrentLevelStars);
  const target = toNumeric(row.NextLevelStars);
  const span = target - current;
  const percent = span > 0 ? Math.min(100, Math.max(0, ((stars - current) / span) * 100)) : 0;
  return { percent, remaining: Math.max(0, target - stars), target, stars };
}

export function RatingProgress({ row }: { row: AccountRatingRow }) {
  const { t } = useI18n();
  if (!row.HasNextLevel) {
    return <span className="progress-note">{t("rating.maxLevel")}</span>;
  }
  const { percent, remaining, target } = levelProgress(row);
  return (
    <div className="progress-cell">
      <div className="progress-bar" role="img" aria-label={`${Math.round(percent)}%`}>
        <span style={{ width: `${percent}%` }} />
      </div>
      <small>{t("rating.progressHint", { remaining: formatQuantity(String(remaining)), target: formatQuantity(String(target)) })}</small>
    </div>
  );
}
