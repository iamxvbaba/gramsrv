import { RefreshCw } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { api, errorMessage } from "../api";
import { SectionTabs } from "../components/SectionTabs";
import { Alert, Badge, EmptyRow, Metric, PageFrame } from "../components/ui";
import { useI18n, type TFunction } from "../i18n";
import { formatUnix } from "../lib/format";
import type { Navigate } from "../routing";
import type { StarGiftAuctionRow } from "../types";
import { LottiePreview } from "./GiftsPage";

// useNow drives the countdown columns. One shared tick per page keeps every row in
// step and avoids a timer per row.
function useNow(active: boolean) {
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000));
  useEffect(() => {
    if (!active) return;
    const id = window.setInterval(() => setNow(Math.floor(Date.now() / 1000)), 1000);
    return () => window.clearInterval(id);
  }, [active]);
  return now;
}

function formatDuration(seconds: number, t: TFunction) {
  if (seconds <= 0) return t("auctions.now");
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = seconds % 60;
  if (d > 0) return t("auctions.durationDH", { days: d, hours: h });
  if (h > 0) return t("auctions.durationHM", { hours: h, minutes: m });
  if (m > 0) return t("auctions.durationMS", { minutes: m, seconds: s });
  return t("auctions.durationS", { seconds: s });
}

// The catalog revision is the operator's intent; star_gift_auctions is the engine's
// state and is created lazily. Until it exists the row reports "awaiting first
// request" rather than a status the engine has not actually assigned yet.
function auctionStatus(row: StarGiftAuctionRow, now: number) {
  if (!row.IsAuction) {
    return row.LockedUntilDate > now
      ? { key: "auctions.status.scheduled", tone: "warn" as const }
      : { key: "auctions.status.released", tone: "good" as const };
  }
  if (!row.Materialized) {
    return row.AuctionStartDate > now
      ? { key: "auctions.status.pending", tone: "warn" as const }
      : { key: "auctions.status.awaiting", tone: "warn" as const };
  }
  switch (row.Status) {
    case "active":
      return { key: "auctions.status.active", tone: "good" as const };
    case "completed":
      return { key: "auctions.status.completed", tone: "neutral" as const };
    case "cancelled":
      return { key: "auctions.status.cancelled", tone: "danger" as const };
    default:
      return { key: "auctions.status.pending", tone: "warn" as const };
  }
}

export function AuctionsPage({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const [rows, setRows] = useState<StarGiftAuctionRow[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const now = useNow(rows.length > 0);

  async function load() {
    setBusy(true); setError("");
    try { setRows((await api.auctions()).Auctions ?? []); }
    catch (err) { setError(errorMessage(err)); }
    finally { setBusy(false); }
  }
  useEffect(() => { void load(); }, []);

  const stats = useMemo(() => {
    const auctions = rows.filter((row) => row.IsAuction);
    const drops = rows.filter((row) => !row.IsAuction);
    return {
      auctions: auctions.length,
      live: auctions.filter((row) => row.Materialized && row.Status === "active").length,
      drops: drops.length,
      pendingDrops: drops.filter((row) => row.LockedUntilDate > now).length,
      // Sums the Stars currently committed as active bids across every auction.
      committed: auctions.reduce((total, row) => total + Number(row.BidTotal || 0), 0)
    };
  }, [rows, now]);

  return (
    <PageFrame title={t("auctions.title")} eyebrow={t("auctions.eyebrow")}
      actions={<>
        <button className="btn" type="button" disabled={busy} onClick={() => void load()}><RefreshCw size={15} />{t("common.refresh")}</button>
      </>}>
      <SectionTabs sectionId="gifts" active="/auctions" navigate={navigate} />
      {error && <Alert>{error}</Alert>}
      <div className="metric-row">
        <Metric label={t("auctions.metricAuctions")} value={String(stats.auctions)} />
        <Metric label={t("auctions.metricLive")} value={String(stats.live)} tone={stats.live > 0 ? "good" : "neutral"} />
        <Metric label={t("auctions.metricDrops")} value={String(stats.drops)} />
        <Metric label={t("auctions.metricPendingDrops")} value={String(stats.pendingDrops)} tone={stats.pendingDrops > 0 ? "warn" : "neutral"} />
        <Metric label={t("auctions.metricCommitted")} value={stats.committed.toLocaleString()} mono />
      </div>
      <div className="table-wrap">
        <table className="data-table">
          <thead><tr>
            <th>{t("common.preview")}</th>
            <th>{t("auctions.colGift")}</th>
            <th>{t("auctions.colKind")}</th>
            <th>{t("common.status")}</th>
            <th>{t("auctions.colRounds")}</th>
            <th>{t("auctions.colSupply")}</th>
            <th>{t("auctions.colMinBid")}</th>
            <th>{t("auctions.colBids")}</th>
            <th>{t("auctions.colNext")}</th>
          </tr></thead>
          <tbody>
            {rows.map((row) => <AuctionRow key={row.GiftID} row={row} now={now} />)}
            {rows.length === 0 && !busy && <EmptyRow colSpan={9} />}
          </tbody>
        </table>
      </div>
      <div className="gift-import-note"><span>{t("auctions.lazyNote")}</span></div>
      <div className="gift-import-note"><span>{t("auctions.createElsewhere")}</span></div>
    </PageFrame>
  );
}

function AuctionRow({ row, now }: { row: StarGiftAuctionRow; now: number }) {
  const { t } = useI18n();
  const status = auctionStatus(row, now);
  // Rounds are only known once the engine has materialized the auction; before that
  // the plan is derivable from the authoring parameters alone.
  const plannedRounds = row.GiftsPerRound > 0 ? Math.ceil(row.AvailabilityTotal / row.GiftsPerRound) : 0;
  const totalRounds = row.Materialized ? row.TotalRounds : plannedRounds;
  const roundSeconds = row.Materialized ? row.RoundDuration : row.AuctionRoundDuration;
  // The next deadline is a round boundary for a live auction, the start for one that
  // has not begun, and the unlock time for a scheduled drop.
  const deadline = row.IsAuction
    ? (row.Materialized && row.Status === "active" ? row.NextRoundAt : (row.Materialized ? row.StartDate : row.AuctionStartDate))
    : row.LockedUntilDate;
  const done = row.Materialized && (row.Status === "completed" || row.Status === "cancelled");

  return (
    <tr className={row.Enabled ? "" : "gift-row-disabled"}>
      <td><LottiePreview giftID={row.GiftID} revision={0} compact /></td>
      <td>
        <div className="auction-cell">
          <strong>{row.Title || t("auctions.untitled")}</strong>
          <small className="mono">#{row.GiftID}{row.Slug ? ` · ${row.Slug}` : ""}</small>
        </div>
      </td>
      <td>{row.IsAuction ? <Badge tone="neutral">{t("auctions.kindAuction")}</Badge> : <Badge tone="neutral">{t("auctions.kindDrop")}</Badge>}</td>
      <td>
        <div className="auction-cell">
          <Badge tone={status.tone}>{t(status.key)}</Badge>
          {!row.Enabled && <small>{t("common.disabled")}</small>}
        </div>
      </td>
      <td>{row.IsAuction && totalRounds > 0
        ? <div className="auction-cell">
          <span>{t("auctions.roundOf", { current: row.Materialized ? row.CurrentRound : 0, total: totalRounds })}</span>
          {roundSeconds > 0 && <small>{t("auctions.perRoundShort", { minutes: Math.max(1, Math.round(roundSeconds / 60)) })}</small>}
        </div>
        : <small>—</small>}</td>
      <td>{row.IsAuction
        ? <div className="auction-cell">
          <span className="mono">{row.Materialized ? row.GiftsLeft : row.AvailabilityTotal} / {row.AvailabilityTotal}</span>
          <small>{t("auctions.awarded", { count: row.LastGiftNum })}</small>
        </div>
        : <small>{t("auctions.unlimited")}</small>}</td>
      <td className="mono">{row.IsAuction ? Number(row.Materialized ? row.MinBidAmount : row.Stars).toLocaleString() : Number(row.Stars).toLocaleString()}</td>
      <td>{row.IsAuction
        ? <div className="auction-cell">
          <span>{t("auctions.bidCount", { count: row.ActiveBids })}</span>
          {row.ActiveBids > 0 && <small className="mono">{t("auctions.topBid", { amount: Number(row.TopBid).toLocaleString() })}</small>}
        </div>
        : <small>—</small>}</td>
      <td>{done || deadline <= 0
        ? <small>—</small>
        : <div className="auction-cell">
          <strong>{formatDuration(deadline - now, t)}</strong>
          <small>{formatUnix(deadline)}</small>
        </div>}</td>
    </tr>
  );
}

