import { CheckCircle2, Gavel, Loader2, RefreshCw, ShieldCheck, Timer, Upload, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { createPortal } from "react-dom";
import { api, errorMessage } from "../api";
import { Alert, Badge, EmptyRow, Metric, PageFrame } from "../components/ui";
import { useI18n, type TFunction } from "../i18n";
import { formatUnix, localInputValue, titleFromFilename, toUnixSeconds } from "../lib/format";
import type { Navigate } from "../routing";
import type { CommandResult, StarGiftAuctionRow, StarGiftRow, StarGiftCollectiblePreview } from "../types";
import { LottiePreview } from "./GiftsPage";

// The tab authors both lifecycle kinds the engine understands: an auction runs the
// supply through timed bidding rounds, a scheduled drop keeps a plain gift locked
// behind a countdown until locked_until_date passes.
type AuthorMode = "auction" | "drop";

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
  const [authorOpen, setAuthorOpen] = useState(false);
  const [authorMode, setAuthorMode] = useState<AuthorMode>("auction");
  const now = useNow(rows.length > 0);

  async function load() {
    setBusy(true); setError("");
    try { setRows(((await api.auctions()).Auctions ?? []).filter((row) => row.Enabled)); }
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

  function openAuthor(mode: AuthorMode) {
    setAuthorMode(mode);
    setAuthorOpen(true);
  }

  return (
    <PageFrame title={t("auctions.title")} eyebrow={t("auctions.eyebrow")}
      actions={<>
        <button className="btn" type="button" disabled={busy} onClick={() => void load()}><RefreshCw size={15} />{t("common.refresh")}</button>
        <button className="btn" type="button" onClick={() => openAuthor("drop")}><Timer size={15} />{t("auctions.newDrop")}</button>
        <button className="btn primary" type="button" onClick={() => openAuthor("auction")}><Gavel size={15} />{t("auctions.newAuction")}</button>
      </>}>
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
      {authorOpen && <AuthorModal mode={authorMode} onClose={() => setAuthorOpen(false)} onCreated={() => void load()} />}
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

// AuthorModal posts to the same admin import endpoint the gift catalog uses: an
// auction or a drop is a catalog revision with lifecycle fields set, so there is no
// second write path to keep in step. The dry run is the server's own validation.
function AuthorModal({ mode, onClose, onCreated }: { mode: AuthorMode; onClose: () => void; onCreated: () => void }) {
  const { t } = useI18n();
  const [source, setSource] = useState<"imported" | "upload">(mode === "auction" ? "imported" : "upload");
  const [imported, setImported] = useState<StarGiftRow[]>([]);
  const [selected, setSelected] = useState<StarGiftRow | null>(null);
  const [search, setSearch] = useState("");
  const [pool, setPool] = useState<StarGiftCollectiblePreview | null>(null);
  const [catalogLoading, setCatalogLoading] = useState(false);
  const [poolLoading, setPoolLoading] = useState(false);
  const sourceLoading = catalogLoading || poolLoading;
  const [sourceError, setSourceError] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [title, setTitle] = useState("");
  const [stars, setStars] = useState("50");
  const [convertStars, setConvertStars] = useState("50");
  const [sortOrder, setSortOrder] = useState("0");
  const [slug, setSlug] = useState("");
  const [supply, setSupply] = useState("10");
  const [perRound, setPerRound] = useState("2");
  const [roundDuration, setRoundDuration] = useState("3600");
  const [startAt, setStartAt] = useState(() => localInputValue(600));
  const [unlockAt, setUnlockAt] = useState(() => localInputValue(3600));
  const [reason, setReason] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [supportOnly, setSupportOnly] = useState(false);
  const [releasedBy, setReleasedBy] = useState("");
  const [perUserTotal, setPerUserTotal] = useState("0");
  const [premium, setPremium] = useState(false);
  const [birthday, setBirthday] = useState(false);
  const [upgradeStars, setUpgradeStars] = useState("");
  const [preview, setPreview] = useState<CommandResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (source !== "imported") return;
    let cancelled = false;
    setCatalogLoading(true); setSourceError("");
    api.gifts(true).then((value) => { if (!cancelled) setImported(value.Gifts ?? []); })
      .catch((err) => { if (!cancelled) setSourceError(errorMessage(err)); })
      .finally(() => { if (!cancelled) setCatalogLoading(false); });
    return () => { cancelled = true; };
  }, [source]);

  useEffect(() => {
    setPool(null);
    if (!selected || source !== "imported") { setPoolLoading(false); return; }
    let cancelled = false;
    setPoolLoading(true); setSourceError("");
    api.giftCollectibles(selected.GiftID).then((value) => { if (!cancelled) setPool(value); })
      .catch((err) => { if (!cancelled) setSourceError(errorMessage(err)); })
      .finally(() => { if (!cancelled) setPoolLoading(false); });
    return () => { cancelled = true; };
  }, [source, selected?.GiftID]);

  const matches = useMemo(() => imported.filter((gift) =>
    !search.trim() || gift.Title.toLowerCase().includes(search.trim().toLowerCase()) || gift.GiftID.includes(search.trim())
  ), [imported, search]);

  function chooseGift(gift: StarGiftRow) {
    setSelected(gift); setTitle(gift.Title); setStars(gift.Stars); setConvertStars(gift.ConvertStars);
    setSlug(`auction-${gift.GiftID}-${Date.now().toString(36)}`);
    setReleasedBy(gift.ReleasedByPeerID && gift.ReleasedByPeerID !== "0" ? gift.ReleasedByPeerID : "");
    setPerUserTotal(String(gift.PerUserTotal ?? 0)); setPremium(gift.RequirePremium ?? false); setBirthday(gift.Birthday ?? false);
    setSupportOnly(gift.SupportOnly ?? false); setSortOrder(String(gift.SortOrder ?? 0)); setUpgradeStars("");
    setPreview(null); setError("");
  }

  // Any field change invalidates a dry run: the confirm step replays the command id
  // the server issued for the payload it actually validated.
  function edit<T>(setter: (next: T) => void) {
    return (next: T) => { setter(next); setPreview(null); };
  }
  const rounds = useMemo(() => {
    const total = Number(supply); const per = Number(perRound);
    return total > 0 && per > 0 ? Math.ceil(total / per) : 0;
  }, [supply, perRound]);

  // Mirrors domain.StarGiftCatalogWrite.ValidateLifecycleAuthoring so the operator
  // sees a bad value before the upload round-trip.
  function lifecyclePayload() {
    const nowSec = Math.floor(Date.now() / 1000);
    if (mode === "auction") {
      const total = Number(supply); const per = Number(perRound); const duration = Number(roundDuration);
      const start = toUnixSeconds(startAt);
      if (!slug.trim()) throw new Error(t("gifts.lifecycle.slugRequired"));
      if (!Number.isSafeInteger(total) || total <= 0 || total > 2147483647) throw new Error(t("gifts.lifecycle.supplyRequired"));
      if (!Number.isSafeInteger(per) || per <= 0) throw new Error(t("gifts.lifecycle.perRoundRequired"));
      if (per > total) throw new Error(t("gifts.lifecycle.perRoundTooLarge"));
      if (!Number.isSafeInteger(duration) || duration <= 0 || duration > 2147483647) throw new Error(t("gifts.lifecycle.roundDurationRequired"));
      if (start !== 0 && start < nowSec) throw new Error(t("gifts.lifecycle.startPast"));
      return {
        auction: true,
        auction_slug: slug.trim().toLowerCase(),
        availability_total: total,
        gifts_per_round: per,
        auction_round_duration: duration,
        auction_start_date: start
      };
    }
    const unlock = toUnixSeconds(unlockAt);
    if (unlock <= nowSec) throw new Error(t("gifts.lifecycle.unlockRequired"));
    return { locked_until_date: unlock };
  }

  function uploadForm(confirm: boolean, commandID = "") {
    if (!file) throw new Error(t("gifts.fileRequired"));
    if (!reason.trim()) throw new Error(t("action.reasonRequired"));
    const form = new FormData();
    form.set("metadata", JSON.stringify({
      command_id: commandID,
      reason: reason.trim(),
      confirm,
      gift_id: "0",
      title: title.trim(),
      stars,
      convert_stars: convertStars,
      enabled,
      support_only: supportOnly,
      released_by_peer: releasedBy.trim(), per_user_total: Number(perUserTotal), require_premium: premium, birthday,
      sort_order: Number(sortOrder),
      ...lifecyclePayload()
    }));
    form.set("file", file, file.name);
    return form;
  }

  async function submit(confirm: boolean) {
    if (source === "upload") return api.importGift(uploadForm(confirm, confirm ? `exec-${preview?.command_id}` : ""));
    if (!selected || sourceLoading || sourceError) throw new Error(t("auctions.selectImported"));
    if (!reason.trim()) throw new Error(t("action.reasonRequired"));
    return api.action("/api/actions/create-gift-auction", {
      source_gift_id: selected.GiftID, source_revision_id: selected.RevisionID,
      source_sha256: confirm ? preview?.details?.source_sha256 : "",
      title: title.trim(), stars, convert_stars: convertStars, enabled, support_only: supportOnly,
      released_by_peer: releasedBy.trim(), per_user_total: Number(perUserTotal), require_premium: premium, birthday, sort_order: Number(sortOrder),
      ...(pool?.found && upgradeStars.trim() ? { upgrade_stars: upgradeStars } : {}),
      reason: reason.trim(), confirm,
      // A stable execution ID makes a retry safe if the success response was lost.
      command_id: confirm ? `exec-${preview?.command_id}` : "",
      ...Object.fromEntries(Object.entries(lifecyclePayload()).filter(([key]) => key !== "auction"))
    });
  }

  async function validate() {
    setBusy(true); setError(""); setPreview(null);
    try { const result = await submit(false); if (result.error) throw new Error(result.error); setPreview(result); }
    catch (err) { setError(errorMessage(err)); }
    finally { setBusy(false); }
  }

  async function confirm() {
    if (!preview?.dry_run || preview.error) return;
    setBusy(true); setError("");
    try {
      const result = await submit(true);
      if (result.error || result.status !== "completed") throw new Error(result.error || result.message || result.status);
      onCreated();
      onClose();
    } catch (err) { setError(errorMessage(err)); }
    finally { setBusy(false); }
  }

  return createPortal(
    <div className="modal-backdrop" role="presentation">
      <section className="modal command-modal auction-author-modal" role="dialog" aria-modal="true" aria-label={mode === "auction" ? t("auctions.newAuction") : t("auctions.newDrop")}>
        <div className="modal-head">
          <div>
            <div className="eyebrow">{t("auctions.eyebrow")}</div>
            <h2>{mode === "auction" ? t("auctions.newAuction") : t("auctions.newDrop")}</h2>
          </div>
          <button className="icon-btn" type="button" onClick={onClose} disabled={busy} aria-label={t("common.close")}><X size={15} /></button>
        </div>
        <div className="command-body auction-author-scroll"><fieldset className="auction-author-fields" disabled={busy}>
          <div className="gift-import-note"><span>{mode === "auction" ? t("gifts.lifecycle.hintAuction") : t("gifts.lifecycle.hintDrop")}</span></div>
          {mode === "auction" && <div className="gift-source-tabs" role="group" aria-label={t("auctions.giftSource")}>
            <button className={`btn ${source === "imported" ? "primary" : ""}`} type="button" disabled={busy} aria-pressed={source === "imported"} onClick={() => { setSource("imported"); setPreview(null); }}>{t("auctions.fromImported")}</button>
            <button className={`btn ${source === "upload" ? "primary" : ""}`} type="button" disabled={busy} aria-pressed={source === "upload"} onClick={() => { setSource("upload"); setPreview(null); }}>{t("gifts.fileSource")}</button>
          </div>}
          {source === "imported" && <section className="auction-source-panel">
            <p className="field-hint">{t("auctions.copyHint")}</p>
            <input aria-label={t("gifts.searchPlaceholder")} placeholder={t("gifts.searchPlaceholder")} value={search} onChange={(e) => setSearch(e.target.value)} disabled={busy} />
            <div className="auction-source-grid" role="group" aria-label={t("auctions.selectImported")}>
              {matches.map((gift) => <button key={gift.GiftID} type="button" className={`auction-source-card ${selected?.GiftID === gift.GiftID ? "selected" : ""}`} disabled={busy} aria-pressed={selected?.GiftID === gift.GiftID} onClick={() => chooseGift(gift)}>
                <span><strong>{gift.Title || `#${gift.GiftID}`}</strong><small className="mono">#{gift.GiftID}</small></span>
                <Badge tone={gift.Enabled ? "good" : "neutral"}>{gift.Enabled ? t("common.enabled") : t("common.disabled")}</Badge>
              </button>)}
              {!sourceLoading && matches.length === 0 && <p>{t("auctions.noImported")}</p>}
            </div>
            {selected && <div className="auction-selected-gift">
              <LottiePreview giftID={selected.GiftID} revision={Number(selected.Revision)} compact />
              <div><strong>{selected.Title}</strong><p className="field-hint">{sourceLoading ? t("common.loading") : pool?.found ? t("auctions.poolSummary", { models: pool.models?.length ?? 0, patterns: pool.patterns?.length ?? 0, backdrops: pool.backdrops?.length ?? 0, stars: pool.upgrade_stars ?? "0" }) : t("auctions.noUpgrades")}</p></div>
            </div>}
            {sourceError && <Alert>{sourceError}</Alert>}
          </section>}
          {source === "upload" && <label className={`gift-file-picker ${file ? "has-file" : ""}`}>

            <input type="file" accept=".tgs,.json,application/json" onChange={(e) => {
              const next = e.target.files?.[0] ?? null;
              setFile(next);
              setPreview(null);
              if (next && !title) setTitle(titleFromFilename(next.name));
            }} />
            <span className="gift-file-copy"><strong>{file?.name ?? t("gifts.chooseFile")}</strong></span>
            <span className="gift-file-action">{file ? t("gifts.changeFile") : t("common.choose")}</span>
          </label>}
          <div className="gift-fields-grid">
            <label><span>{t("gifts.title")}</span><input value={title} maxLength={128} placeholder={t("gifts.titlePlaceholder")} onChange={(e) => edit(setTitle)(e.target.value)} /></label>
            <label><span>{mode === "auction" ? t("auctions.minBid") : t("gifts.stars")}</span><input type="number" min="1" value={stars} onChange={(e) => edit(setStars)(e.target.value)} /></label>
            <label><span>{t("gifts.convertStars")}</span><input type="number" min="0" value={convertStars} onChange={(e) => edit(setConvertStars)(e.target.value)} /></label>
            <label><span>{t("gifts.sortOrder")}</span><input type="number" value={sortOrder} onChange={(e) => edit(setSortOrder)(e.target.value)} /></label>
          </div>
          {mode === "auction" ? <>
            <div className="gift-fields-grid">
              <label><span>{t("gifts.auction.slug")}</span><input value={slug} maxLength={48} placeholder={t("gifts.auction.slugPlaceholder")} onChange={(e) => edit(setSlug)(e.target.value.toLowerCase())} /></label>
              <label><span>{t("gifts.auction.supply")}</span><input type="number" min="1" value={supply} onChange={(e) => edit(setSupply)(e.target.value)} /></label>
              <label><span>{t("gifts.auction.perRound")}</span><input type="number" min="1" value={perRound} onChange={(e) => edit(setPerRound)(e.target.value)} /></label>
              <label><span>{t("gifts.auction.roundDuration")}</span><input type="number" min="1" value={roundDuration} onChange={(e) => edit(setRoundDuration)(e.target.value)} /></label>
              <label><span>{t("gifts.auction.startAt")}</span><input type="datetime-local" value={startAt} onChange={(e) => edit(setStartAt)(e.target.value)} /></label>
            </div>
            {rounds > 0 && <div className="gift-import-note"><span>{t("gifts.auction.plan", { rounds, perRound: Number(perRound), minutes: Math.max(1, Math.round(Number(roundDuration) / 60)) })}</span></div>}
          </> : <div className="gift-fields-grid">
            <label><span>{t("gifts.drop.unlockAt")}</span><input type="datetime-local" value={unlockAt} onChange={(e) => edit(setUnlockAt)(e.target.value)} /></label>
          </div>}
          <div className="gift-fields-grid">
            <label><span>{t("gifts.releasedBy")}</span><input value={releasedBy} placeholder="@username / ID" onChange={(e) => edit(setReleasedBy)(e.target.value)} /></label>
            <label><span>{t("gifts.perUserLimit")}</span><input type="number" min="0" step="1" value={perUserTotal} onChange={(e) => edit(setPerUserTotal)(e.target.value)} /></label>
            {source === "imported" && pool?.found && <label><span>{t("collectibles.upgradeStars")}</span><input type="number" min="1" value={upgradeStars} placeholder={pool.upgrade_stars} onChange={(e) => edit(setUpgradeStars)(e.target.value)} /></label>}
          </div>
          <label className="gift-switch"><input type="checkbox" checked={premium} onChange={(e) => edit(setPremium)(e.target.checked)} /><span className="gift-switch-track" aria-hidden="true"><span /></span><span>{t("gifts.premium")}</span></label>
          <label className="gift-switch"><input type="checkbox" checked={birthday} onChange={(e) => edit(setBirthday)(e.target.checked)} /><span className="gift-switch-track" aria-hidden="true"><span /></span><span>{t("gifts.birthday")}</span></label>
          <label className="gift-reason-field"><span>{t("gifts.reason")}</span><input value={reason} placeholder={t("gifts.reasonPlaceholder")} onChange={(e) => edit(setReason)(e.target.value)} /></label>
          <label className="gift-switch">
            <input type="checkbox" checked={enabled} onChange={(e) => edit(setEnabled)(e.target.checked)} />
            <span className="gift-switch-track" aria-hidden="true"><span /></span>
            <span>{t("auctions.enableHint")}</span>
          </label>
          <label className="gift-switch">
            <input type="checkbox" checked={supportOnly} onChange={(e) => edit(setSupportOnly)(e.target.checked)} />
            <span className="gift-switch-track" aria-hidden="true"><span /></span>
            <span>{t("auctions.supportOnly")}</span>
          </label>
          {error && <Alert>{error}</Alert>}
          {preview && <div className="gift-validation">
            <div className="gift-validation-head"><CheckCircle2 size={17} /><div><strong>{t("gifts.validationReady")}</strong><span>{t("gifts.validationHint")}</span></div></div>
            <div className="auction-release-summary">
              <strong>{title || selected?.Title || file?.name}</strong>
              <span>{t("gifts.auction.plan", { rounds, perRound: Number(perRound), minutes: Math.max(1, Math.round(Number(roundDuration) / 60)) })}</span>
              {source === "imported" && <span>{t("auctions.copyHint")}</span>}
            </div>
          </div>}
        </fieldset></div>
        <div className="modal-actions">
          <button className="btn" type="button" onClick={onClose} disabled={busy}>{t("common.close")}</button>
          <button className="btn" type="button" onClick={() => void validate()} disabled={busy}>{busy ? <Loader2 className="spin" size={15} /> : <ShieldCheck size={15} />}{t("gifts.validate")}</button>
          <button className="btn primary" type="button" onClick={() => void confirm()} disabled={busy || !preview?.dry_run || !!preview.error || (source === "imported" && (sourceLoading || !!sourceError || !selected))}><Upload size={15} />{source === "imported" ? t("auctions.createRelease") : t("gifts.confirmImport")}</button>
        </div>
      </section>
    </div>,
    document.body
  );
}
