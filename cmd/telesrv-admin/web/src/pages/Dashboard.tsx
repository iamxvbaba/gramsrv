import {
  Activity,
  AlertTriangle,
  BadgeCheck,
  Bot,
  Cpu,
  Database,
  Film,
  Flag,
  HardDrive,
  MemoryStick,
  Radio,
  RefreshCw,
  Smile,
  Sticker,
  Users,
  UsersRound
} from "lucide-react";
import { type ReactNode, useEffect, useState } from "react";
import { api } from "../api";
import { Alert } from "../components/ui";
import { useI18n, type TFunction } from "../i18n";
import { formatBytes, formatQuantity } from "../lib/format";
import type { Navigate } from "../routing";
import type { DashboardResponse, StorageStatsResponse } from "../types";
import { StatsCharts, StatsEconomy, useStats } from "./StatisticsPanel";
import { withBase } from "../lib/base";

// The overview is the one page an operator is guaranteed to be able to open, so
// it answers everything at a glance and nothing that needs a click: the queues
// that want attention, the size of the place, the economy, and whether the box
// itself is healthy.
//
// Layout is a fixed two-column grid that fills the viewport height rather than
// stacking sections down the page. The dashboard used to be four full-width
// bands and the statistics page two more; an operator on a laptop scrolled past
// half of it to reach the charts. Each panel here scrolls inside itself if its
// content outgrows the space, so the page as a whole never does. Below 1100px
// the grid collapses to one column and the page scrolls normally again -- on a
// phone there is no honest way to fit this, and pinning the viewport height
// would only trade page scroll for squeezed charts.
export function Dashboard({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const [data, setData] = useState<DashboardResponse | null>(null);
  const [error, setError] = useState("");
  const [nonce, setNonce] = useState(0);
  const stats = useStats();

  useEffect(() => {
    let cancelled = false;
    async function load() {
      try {
        const res = await api.dashboard();
        if (!cancelled) {
          setData(res);
          setError("");
        }
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : t("dashboard.loadError"));
      }
    }
    void load();
    const timer = window.setInterval(() => void load(), 15000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [t, nonce]);

  const counts = data?.counts;
  const storage = data?.storage;
  const host = data?.host;

  return (
    <div className="overview">
      {(error || stats.error) && (
        <div className="overview-alerts">
          {error && <Alert>{error}</Alert>}
          {stats.error && <Alert>{stats.error}</Alert>}
        </div>
      )}

      <div className="overview-summary">
        <section className="overview-panel overview-panel--queues">
          <PanelHead title={t("dashboard.needsAttention")}>
            <button
              type="button"
              className="btn ghost icon-text"
              onClick={() => setNonce((value) => value + 1)}
              aria-label={t("common.refresh")}
              title={t("common.refresh")}
            >
              <RefreshCw size={14} className={data ? "" : "spin"} />
            </button>
          </PanelHead>
          <div className="overview-tiles">
            <StatTile
              icon={<Flag />}
              label={t("dashboard.pendingReports")}
              value={counts ? formatQuantity(String(counts.PendingReports)) : "…"}
              tone={counts && counts.PendingReports > 0 ? "warn" : "good"}
              href="/moderation"
              navigate={navigate}
            />
            <StatTile
              icon={<BadgeCheck />}
              label={t("dashboard.verificationRequests")}
              value={counts ? formatQuantity(String(counts.PendingVerifications)) : "…"}
              tone={counts && counts.PendingVerifications > 0 ? "warn" : "good"}
              href="/verification"
              navigate={navigate}
            />
          </div>
        </section>

        <section className="overview-panel overview-panel--economy">
          <StatsEconomy stats={stats} />
        </section>

        <section className="overview-panel overview-panel--health">
          <PanelHead title={t("dashboard.serverHealth")} hint={host?.Ready ? undefined : t("dashboard.waitingForSample")} />
          {/* Not --tight: these three tiles keep their "of 32.0 GiB"
              qualifiers, which are what make a bare 9.0 GiB readable. */}
          <div className="overview-tiles overview-tiles--health">
            <UsageTile
              icon={<Cpu />}
              label={t("dashboard.cpuLoad")}
              percent={host?.Ready ? host.CPUPercent : undefined}
              valueText={host?.Ready ? `${host.CPUPercent.toFixed(0)}%` : "…"}
            />
            <UsageTile
              icon={<MemoryStick />}
              label={t("dashboard.ramUsed")}
              percent={host?.Ready && host.MemTotalBytes > 0 ? (host.MemUsedBytes / host.MemTotalBytes) * 100 : undefined}
              valueText={host?.Ready ? formatBytes(String(host.MemUsedBytes)) : "…"}
              sub={host?.Ready ? t("dashboard.ofTotal", { total: formatBytes(String(host.MemTotalBytes)) }) : undefined}
            />
            <UsageTile
              icon={<HardDrive />}
              label={t("dashboard.diskFree")}
              percent={
                host?.Ready && host.DiskTotalBytes > 0
                  ? ((host.DiskTotalBytes - host.DiskFreeBytes) / host.DiskTotalBytes) * 100
                  : undefined
              }
              valueText={host?.Ready ? formatBytes(String(host.DiskFreeBytes)) : "…"}
              sub={host?.Ready ? t("dashboard.ofTotal", { total: formatBytes(String(host.DiskTotalBytes)) }) : undefined}
              warnAbove={85}
            />
          </div>
        </section>
      </div>

      <div className="overview-main">
        <section className="overview-panel overview-panel--people">
          <PanelHead title={t("dashboard.peopleChats")} />
          <div className="overview-tiles overview-tiles--tight">
            <StatTile compact icon={<Users />} label={t("dashboard.users")} value={counts ? formatQuantity(String(counts.Users)) : "…"} href="/accounts" navigate={navigate} />
            <StatTile
              compact
              icon={<Activity />}
              label={t("dashboard.onlineNow")}
              value={counts ? formatQuantity(String(counts.OnlineUsers)) : "…"}
              sub={t("dashboard.lastFiveMinutes")}
              href="/accounts"
              navigate={navigate}
            />
            <StatTile compact icon={<Bot />} label={t("dashboard.bots")} value={counts ? formatQuantity(String(counts.Bots)) : "…"} href="/bots" navigate={navigate} />
            <StatTile compact icon={<Radio />} label={t("dashboard.channels")} value={counts ? formatQuantity(String(counts.BroadcastChannels)) : "…"} href="/channels" navigate={navigate} />
            <StatTile compact icon={<UsersRound />} label={t("dashboard.supergroups")} value={counts ? formatQuantity(String(counts.Supergroups)) : "…"} href="/channels" navigate={navigate} />
            <StatTile compact icon={<Sticker />} label={t("dashboard.stickerPacks")} value={counts ? formatQuantity(String(counts.StickerSets)) : "…"} href="/stickers" navigate={navigate} />
            <StatTile compact icon={<Smile />} label={t("dashboard.emojiPacks")} value={counts ? formatQuantity(String(counts.EmojiSets)) : "…"} href="/emoji" navigate={navigate} />
            <StatTile
              compact
              icon={<Film />}
              label={t("dashboard.gifs")}
              value={counts ? formatQuantity(String(counts.Gifs)) : "…"}
              sub={t("dashboard.savedByUsers")}
              href="/gif-catalog"
              navigate={navigate}
            />
            <StatTile
              compact
              icon={<Database />}
              label={t("dashboard.mediaStorageUsed")}
              value={storage ? formatBytes(storage.PhysicalBytes) : "…"}
              sub={storage ? storageBackendLabel(storage, t) : undefined}
              href="/storage"
              navigate={navigate}
            />
          </div>
        </section>

        <section className="overview-panel overview-panel--charts">
          <StatsCharts stats={stats} />
        </section>

      </div>
    </div>
  );
}

function PanelHead({ title, hint, children }: { title: string; hint?: string; children?: ReactNode }) {
  return (
    <div className="dashboard-section-title">
      {title}
      {hint && <span>{hint}</span>}
      {children}
    </div>
  );
}

function storageBackendLabel(storage: StorageStatsResponse, t: TFunction): string {
  const backends = storage.Backends ?? [];
  if (backends.length === 1) return t("dashboard.singleBackend", { backend: backends[0].Backend });
  if (backends.length > 1) return t("dashboard.multipleBackends", { count: backends.length });
  return t("dashboard.noMediaObjects");
}

type Tone = "neutral" | "good" | "warn" | "danger";

// compact drops the tile to icon/value/label at a third less height. The two
// queues in "Needs attention" keep the roomy tile on purpose -- they are the
// only figures on the page an operator is expected to act on, and they are
// only two, so they can afford the space. Everything else is context, and nine
// context tiles in the roomy variant push the host-health row off a laptop
// screen.
function StatTile({
  icon,
  label,
  value,
  sub,
  tone = "neutral",
  compact = false,
  href,
  navigate
}: {
  icon: ReactNode;
  label: string;
  value: string;
  sub?: string;
  tone?: Tone;
  compact?: boolean;
  href?: string;
  navigate?: Navigate;
}) {
  const toneClass = tone === "neutral" ? "" : ` ${tone}`;
  const boxClass = compact ? "stat-tile stat-tile--compact" : "stat-tile";
  const body = (
    <>
      <div className="stat-tile-head">
        <span className="stat-tile-icon">{icon}</span>
        {tone === "warn" && <AlertTriangle size={15} className="stat-tile-open" />}
      </div>
      <div className="stat-tile-value">{value}</div>
      <div className="stat-tile-label">{label}</div>
      {sub && <div className="stat-tile-sub">{sub}</div>}
    </>
  );
  if (href && navigate) {
    return (
      <a
        className={`${boxClass} clickable${toneClass}`}
        href={withBase(href)}
        onClick={(event) => {
          event.preventDefault();
          navigate(href);
        }}
      >
        {body}
      </a>
    );
  }
  return <div className={`${boxClass}${toneClass}`}>{body}</div>;
}

function UsageTile({
  icon,
  label,
  percent,
  valueText,
  sub,
  warnAbove = 90
}: {
  icon: ReactNode;
  label: string;
  percent?: number;
  valueText: string;
  sub?: string;
  warnAbove?: number;
}) {
  const clamped = percent === undefined ? 0 : Math.max(0, Math.min(100, percent));
  const tone: Tone = percent === undefined ? "neutral" : percent >= warnAbove ? "danger" : percent >= warnAbove - 15 ? "warn" : "neutral";
  const toneClass = tone === "neutral" ? "" : ` ${tone}`;
  return (
    <div className={`stat-tile stat-tile--compact${toneClass}`}>
      <div className="stat-tile-head">
        <span className="stat-tile-icon">{icon}</span>
      </div>
      <div className="stat-tile-value">{valueText}</div>
      <div className="stat-tile-label">{label}</div>
      {sub && <div className="stat-tile-sub">{sub}</div>}
      <div className="stat-tile-bar">
        <span style={{ width: `${clamped}%` }} />
      </div>
    </div>
  );
}