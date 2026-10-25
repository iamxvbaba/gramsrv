import {
  Database,
  Gift,
  Maximize2,
  Minimize2,
  RefreshCw,
  Star
} from "lucide-react";
import { type ReactNode, useEffect, useId, useMemo, useState } from "react";
import { api } from "../api";
import { Alert } from "../components/ui";
import { useI18n } from "../i18n";
import type { StatsDailyCountPoint, StatsDailyStarPoint, StatsResponse } from "../types";

// The statistics half of the overview. It was a page of its own at
// /statistics; folding it in here means one page to read instead of two that
// answered the same question ("how is the server doing") at different refresh
// rates, and it drops the duplicate Users/Online tiles the overview already
// showed.
//
// It keeps its own poller rather than sharing the dashboard's: /api/stats runs
// four generate_series aggregates and is refreshed once a minute, while
// /api/dashboard is a set of counters on a 15s tick. Merging the components
// must not merge their cost.

export const STATS_RANGES = [7, 14, 30, 90] as const;

type ChartKey = "users" | "active" | "stars";

export type StatsState = {
  data: StatsResponse | null;
  days: number;
  setDays: (days: number) => void;
  loading: boolean;
  error: string;
  refresh: () => void;
};

export function useStats(): StatsState {
  const { t } = useI18n();
  const [data, setData] = useState<StatsResponse | null>(null);
  const [days, setDays] = useState<number>(14);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [nonce, setNonce] = useState(0);

  useEffect(() => {
    let cancelled = false;
    async function load() {
      try {
        const res = await api.stats(days);
        if (!cancelled) {
          setData(res);
          setError("");
          setLoading(false);
        }
      } catch (err) {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : t("stats.loadError"));
          setLoading(false);
        }
      }
    }
    setLoading(true);
    void load();
    const timer = window.setInterval(() => void load(), 60000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [days, t, nonce]);

  return {
    data,
    days,
    setDays,
    loading,
    error,
    refresh: () => setNonce((value) => value + 1)
  };
}

// StatsStrip renders the economy figures and the three daily charts. It is a
// self-contained block so the overview page controls only the grid around it.
// The two halves are exported separately because the overview gives the economy
// row its own panel and lets the charts fill whatever height is left.
export function StatsStrip({ stats }: { stats: StatsState }) {
  return (
    <>
      <StatsEconomy stats={stats} />
      <StatsCharts stats={stats} />
    </>
  );
}

export function StatsEconomy({ stats }: { stats: StatsState }) {
  const { t } = useI18n();
  const data = stats.data;
  if (!data) {
    return (
      <>
        <div className="dashboard-section-title">{t("stats.sectionStars")}</div>
        <div className="overview-tiles overview-tiles--tight">
          <StatTile icon={<Star />} label={t("stats.starsToday")} value="…" />
          <StatTile icon={<Gift />} label={t("stats.giftsToday")} value="…" />
          <StatTile icon={<Database />} label={t("stats.balanceTotal")} value="…" />
          <StatTile icon={<Star />} label={t("stats.avgBalance")} value="…" />
        </div>
      </>
    );
  }
  return (
    <>
      <div className="dashboard-section-title">{t("stats.sectionStars")}</div>
      <div className="overview-tiles overview-tiles--tight">
        <StatTile icon={<Star />} label={t("stats.starsToday")} value={formatNumber(data.stars_given_today)} />
        <StatTile icon={<Gift />} label={t("stats.giftsToday")} value={formatNumber(data.gifts_sent_today)} />
        <StatTile icon={<Database />} label={t("stats.balanceTotal")} value={formatNumber(data.star_balance_total)} />
        <StatTile icon={<Star />} label={t("stats.avgBalance")} value={formatNumber(data.users_total > 0 ? Math.round(data.star_balance_total / data.users_total) : 0)} />
      </div>
    </>
  );
}

export function StatsCharts({ stats }: { stats: StatsState }) {
  const { t } = useI18n();
  const [expanded, setExpanded] = useState<ChartKey | null>(null);
  const data = stats.data;

  if (!data) {
    return <div className="stats-grid" />;
  }

  return (
    <>
      <div className="dashboard-section-title">
        {t("stats.sectionCharts")}
        <div className="stacked-toolbar stacked-toolbar--inline">
          <div className="segmented" role="group" aria-label={t("stats.daysLabel")}>
            {STATS_RANGES.map((d) => (
              <button
                key={d}
                type="button"
                className={`segmented-option ${d === stats.days ? "active" : ""}`}
                onClick={() => stats.setDays(d)}
              >
                {d}
              </button>
            ))}
          </div>
          <button
            type="button"
            className="btn ghost icon-text"
            onClick={stats.refresh}
            disabled={stats.loading}
            aria-label={t("stats.refresh")}
            title={t("stats.refresh")}
          >
            <RefreshCw size={14} className={stats.loading ? "spin" : undefined} />
          </button>
        </div>
      </div>
      {stats.error && <Alert>{stats.error}</Alert>}
      <div className={`stats-grid ${expanded ? "expanded" : ""}`}>
        {(!expanded || expanded === "users") && (
          <ChartCard
            title={t("stats.chartUsers")}
            expanded={expanded === "users"}
            onToggle={() => setExpanded(expanded === "users" ? null : "users")}
            line={<LineChart points={data.users_chart} color="var(--brand-2)" />}
          >
            <BarChart points={data.users_chart} color="var(--brand-2)" />
          </ChartCard>
        )}
        {(!expanded || expanded === "active") && (
          <ChartCard
            title={t("stats.chartActive")}
            expanded={expanded === "active"}
            onToggle={() => setExpanded(expanded === "active" ? null : "active")}
            line={<LineChart points={data.active_chart} color="var(--good)" />}
          >
            <BarChart points={data.active_chart} color="var(--good)" />
          </ChartCard>
        )}
        {(!expanded || expanded === "stars") && (
          <ChartCard
            title={t("stats.chartStars")}
            hint={t("stats.chartStarsHint")}
            expanded={expanded === "stars"}
            onToggle={() => setExpanded(expanded === "stars" ? null : "stars")}
            line={<StarsLineChart points={data.stars_chart} />}
          >
            <StarsChart points={data.stars_chart} />
          </ChartCard>
        )}
      </div>
    </>
  );
}

function formatNumber(value: number): string {
  return (value ?? 0).toLocaleString();
}

function StatTile({ icon, label, value }: { icon: ReactNode; label: string; value: string }) {
  return (
    <div className="stat-tile stat-tile--compact">
      <div className="stat-tile-head">
        <span className="stat-tile-icon">{icon}</span>
      </div>
      <div className="stat-tile-value">{value}</div>
      <div className="stat-tile-label">{label}</div>
    </div>
  );
}

function ChartCard({
  title,
  hint,
  expanded,
  onToggle,
  line,
  children
}: {
  title: string;
  hint?: string;
  expanded: boolean;
  onToggle: () => void;
  line: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className={`chart-card ${expanded ? "expanded" : ""}`}>
      <div className="chart-card-head">
        <div className="chart-card-titles">
          <span className="chart-card-title">{title}</span>
          {hint && <span className="chart-card-hint">{hint}</span>}
        </div>
        <button type="button" className="chart-expand-btn" onClick={onToggle} aria-label={title}>
          {expanded ? <Minimize2 size={16} /> : <Maximize2 size={16} />}
        </button>
      </div>
      {expanded ? line : children}
    </div>
  );
}

type HoverTip = { x: number; y: number; text: string } | null;

// BarChart is a lightweight zero-dependency column chart. Bars are proportional
// to the largest value; hovering a column shows the exact value in a tooltip.
function BarChart({ points, color }: { points: StatsDailyCountPoint[]; color: string }) {
  const { t } = useI18n();
  const max = useMemo(() => Math.max(1, ...points.map((p) => p.value)), [points]);
  const [tip, setTip] = useState<HoverTip>(null);

  return (
    <div className="chart">
      <div className="chart-bars">
        {points.map((p) => (
          <div
            key={p.date}
            className="chart-col"
            onMouseMove={(e) => {
              const bars = (e.currentTarget.closest(".chart-bars") as HTMLElement)?.getBoundingClientRect();
              if (!bars) return;
              setTip({ x: e.clientX - bars.left, y: e.clientY - bars.top, text: `${p.date} — ${formatNumber(p.value)} ${t("stats.valueLabel")}` });
            }}
            onMouseLeave={() => setTip(null)}
          >
            <div className="chart-col-track">
              <div
                className="chart-bar"
                style={{ height: `${Math.max(2, (p.value / max) * 100)}%`, background: color }}
              />
            </div>
          </div>
        ))}
        {tip && <div className="chart-tooltip" style={{ left: tip.x, top: tip.y }}>{tip.text}</div>}
      </div>
      <div className="chart-axis-label">
        <span>{points[0]?.date?.slice(5) ?? ""}</span>
        <span>{points[points.length - 1]?.date?.slice(5) ?? ""}</span>
      </div>
    </div>
  );
}

// StarsChart shows stars spent (bars) with a line for gifts sent on top, so the
// two series share one calendar but keep separate encodings.
function StarsChart({ points }: { points: StatsDailyStarPoint[] }) {
  const { t } = useI18n();
  const maxStars = useMemo(() => Math.max(1, ...points.map((p) => p.stars)), [points]);
  const maxGifts = useMemo(() => Math.max(1, ...points.map((p) => p.gifts)), [points]);
  const [tip, setTip] = useState<HoverTip>(null);

  const width = 100;
  const height = 100;
  const pad = 3;
  const n = points.length;
  const polyline = points
    .map((p, i) => {
      const x = n <= 1 ? width / 2 : pad + (i / (n - 1)) * (width - pad * 2);
      const y = height - pad - (p.gifts / maxGifts) * (height - pad * 2);
      return `${x.toFixed(2)},${y.toFixed(2)}`;
    })
    .join(" ");

  return (
    <div className="chart chart-dual">
      <div className="chart-bars">
        {points.map((p) => (
          <div
            key={p.date}
            className="chart-col"
            onMouseMove={(e) => {
              const bars = (e.currentTarget.closest(".chart-bars") as HTMLElement)?.getBoundingClientRect();
              if (!bars) return;
              setTip({
                x: e.clientX - bars.left,
                y: e.clientY - bars.top,
                text: `${p.date} — ${formatNumber(p.stars)} ${t("stats.stars")} / ${formatNumber(p.gifts)} ${t("stats.gifts")}`
              });
            }}
            onMouseLeave={() => setTip(null)}
          >
            <div className="chart-col-track">
              <div className="chart-bar" style={{ height: `${Math.max(2, (p.stars / maxStars) * 100)}%` }} />
            </div>
          </div>
        ))}
        <svg className="chart-line-overlay" viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none">
          <polyline points={polyline} fill="none" stroke="var(--danger)" strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
        </svg>
        {tip && <div className="chart-tooltip" style={{ left: tip.x, top: tip.y }}>{tip.text}</div>}
      </div>
      <div className="chart-legend">
        <span className="chart-legend-item"><i className="swatch swatch-stars" />{t("stats.stars")}</span>
        <span className="chart-legend-item"><i className="swatch swatch-gifts" />{t("stats.gifts")}</span>
      </div>
    </div>
  );
}

// chartXY is the one projection every line chart shares: a data point becomes a
// coordinate inside the 100x100 viewBox the SVGs are drawn in. The bar and line
// variants used to carry their own copy of this arithmetic, which is how the
// expanded line view and the collapsed bars could silently drift apart.
function chartXY(index: number, count: number, value: number, max: number, pad: number): [number, number] {
  const x = count <= 1 ? 50 : pad + (index / (count - 1)) * (100 - pad * 2);
  const y = 100 - pad - (value / max) * (100 - pad * 2);
  return [x, y];
}

function seriesLine<T>(points: T[], pick: (point: T) => number, max: number, pad: number): string {
  const n = points.length;
  return points
    .map((p, i) => chartXY(i, n, pick(p), max, pad).map((v) => v.toFixed(2)).join(","))
    .join(" ");
}

// LineChart renders a filled-area line chart (SVG) used for the expanded,
// full-width view of a single-series statistic (users / active).
function LineChart({ points, color }: { points: StatsDailyCountPoint[]; color: string }) {
  const { t } = useI18n();
  const gid = useId();
  const max = useMemo(() => Math.max(1, ...points.map((p) => p.value)), [points]);
  const [tip, setTip] = useState<HoverTip>(null);

  const P = 5;
  const line = seriesLine(points, (p) => p.value, max, P);
  const area = `${P},${100 - P} ${line} ${100 - P},${100 - P} ${P},${100 - P}`;

  return (
    <div className="chart">
      <div className="chart-bars">
        <svg className="chart-line-svg" viewBox="0 0 100 100" preserveAspectRatio="none">
          <defs>
            <linearGradient id={gid} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor={color} stopOpacity="0.28" />
              <stop offset="100%" stopColor={color} stopOpacity="0" />
            </linearGradient>
          </defs>
          <polygon points={area} fill={`url(#${gid})`} />
          <polyline points={line} fill="none" stroke={color} strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
        </svg>
        {points.map((p) => (
          <div
            key={p.date}
            className="chart-hit"
            onMouseMove={(e) => {
              const bars = (e.currentTarget.closest(".chart-bars") as HTMLElement)?.getBoundingClientRect();
              if (!bars) return;
              setTip({ x: e.clientX - bars.left, y: e.clientY - bars.top, text: `${p.date} — ${formatNumber(p.value)} ${t("stats.valueLabel")}` });
            }}
            onMouseLeave={() => setTip(null)}
          />
        ))}
        {tip && <div className="chart-tooltip" style={{ left: tip.x, top: tip.y }}>{tip.text}</div>}
      </div>
      <div className="chart-axis-label">
        <span>{points[0]?.date?.slice(5) ?? ""}</span>
        <span>{points[points.length - 1]?.date?.slice(5) ?? ""}</span>
      </div>
    </div>
  );
}

// StarsLineChart renders the stars (area+line) and gifts (line) series together,
// used for the expanded full-width view of the stars statistic.
function StarsLineChart({ points }: { points: StatsDailyStarPoint[] }) {
  const { t } = useI18n();
  const gid = useId();
  const maxStars = useMemo(() => Math.max(1, ...points.map((p) => p.stars)), [points]);
  const maxGifts = useMemo(() => Math.max(1, ...points.map((p) => p.gifts)), [points]);
  const [tip, setTip] = useState<HoverTip>(null);

  const P = 5;
  const starsLine = seriesLine(points, (p) => p.stars, maxStars, P);
  const starsArea = `${P},${100 - P} ${starsLine} ${100 - P},${100 - P} ${P},${100 - P}`;
  const giftsLine = seriesLine(points, (p) => p.gifts, maxGifts, P);

  return (
    <div className="chart chart-dual">
      <div className="chart-bars">
        <svg className="chart-line-svg" viewBox="0 0 100 100" preserveAspectRatio="none">
          <defs>
            <linearGradient id={gid} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor="var(--brand-2)" stopOpacity="0.28" />
              <stop offset="100%" stopColor="var(--brand-2)" stopOpacity="0" />
            </linearGradient>
          </defs>
          <polygon points={starsArea} fill={`url(#${gid})`} />
          <polyline points={starsLine} fill="none" stroke="var(--brand-2)" strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
          <polyline points={giftsLine} fill="none" stroke="var(--danger)" strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
        </svg>
        {points.map((p) => (
          <div
            key={p.date}
            className="chart-hit"
            onMouseMove={(e) => {
              const bars = (e.currentTarget.closest(".chart-bars") as HTMLElement)?.getBoundingClientRect();
              if (!bars) return;
              setTip({
                x: e.clientX - bars.left,
                y: e.clientY - bars.top,
                text: `${p.date} — ${formatNumber(p.stars)} ${t("stats.stars")} / ${formatNumber(p.gifts)} ${t("stats.gifts")}`
              });
            }}
            onMouseLeave={() => setTip(null)}
          />
        ))}
        {tip && <div className="chart-tooltip" style={{ left: tip.x, top: tip.y }}>{tip.text}</div>}
      </div>
      <div className="chart-legend">
        <span className="chart-legend-item"><i className="swatch swatch-stars" />{t("stats.stars")}</span>
        <span className="chart-legend-item"><i className="swatch swatch-gifts" />{t("stats.gifts")}</span>
      </div>
    </div>
  );
}