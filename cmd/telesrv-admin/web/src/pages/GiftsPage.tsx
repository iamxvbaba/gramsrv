import { CheckCircle2, FileJson2, Gem, Loader2, Pause, Play, Plus, RefreshCw, Search, ShieldCheck, Upload, X } from "lucide-react";
import lottie from "lottie-web/build/player/lottie_light_canvas";
import { useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { Alert, Badge, EmptyRow, Metric, PageFrame, QueryPanel } from "../components/ui";
import { useI18n } from "../i18n";
import { formatDate, toUnixSeconds } from "../lib/format";
import type { CommandResult, OfficialStarGiftRow, StarGiftRow } from "../types";
import { GiftCollectiblesModal } from "./GiftCollectiblesModal";

type OfficialGiftCategory = "all" | "upgrade" | "craft" | "basic";

// "auction" runs the gift through the timed-round bidding engine; "drop" keeps it
// locked behind a countdown until locked_until_date passes. An auction needs an
// animation of its own, so it is authored on the upload path only; a drop is just a
// release time and applies to a snapshot import as well.
type LifecycleMode = "regular" | "auction" | "drop";

function officialGiftAttributeCount(gift: OfficialStarGiftRow) {
  return gift.model_count + gift.pattern_count + gift.backdrop_count;
}

function formatBytes(value: number | string) {
	const bytes = Number(value);
	if (bytes < 1024) return `${bytes} B`;
	if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
	return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

export function LottiePreview({ giftID, revision, compact = false }: { giftID: string; revision: number; compact?: boolean }) {
  const host = useRef<HTMLDivElement>(null);
  const animation = useRef<ReturnType<typeof lottie.loadAnimation> | null>(null);
  const [playing, setPlaying] = useState(true);
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    api.giftAnimation(giftID).then((data) => {
      if (cancelled || !host.current) return;
      animation.current?.destroy();
      animation.current = lottie.loadAnimation({
        container: host.current,
        renderer: "canvas",
        loop: true,
        autoplay: true,
        animationData: structuredClone(data)
      });
    }).catch((err) => setError(errorMessage(err)));
    return () => {
      cancelled = true;
      animation.current?.destroy();
      animation.current = null;
    };
  }, [giftID, revision]);

  function toggle() {
    if (!animation.current) return;
    if (playing) animation.current.pause();
    else animation.current.play();
    setPlaying(!playing);
  }

  return (
    <div className={`gift-animation-shell ${compact ? "compact" : ""}`}>
      <div className="gift-animation" ref={host}>{error && <span>{error}</span>}</div>
      <button className="gift-play" type="button" onClick={toggle} aria-label={playing ? "Pause" : "Play"}>
        {playing ? <Pause size={14} /> : <Play size={14} />}
      </button>
    </div>
  );
}

function OfficialLottiePreview({ sourceGiftID }: { sourceGiftID: string }) {
  const host = useRef<HTMLDivElement>(null);
  useEffect(() => {
    let cancelled = false;
    let player: ReturnType<typeof lottie.loadAnimation> | null = null;
    api.officialGiftAnimation(sourceGiftID).then((data) => {
      if (cancelled || !host.current) return;
      player = lottie.loadAnimation({ container: host.current, renderer: "canvas", loop: true, autoplay: true, animationData: structuredClone(data) });
    }).catch(() => undefined);
    return () => { cancelled = true; player?.destroy(); };
  }, [sourceGiftID]);
  return <div className="gift-animation-shell"><div className="gift-animation" ref={host} /></div>;
}

export function GiftsPage() {
  const { t } = useI18n();
  const [gifts, setGifts] = useState<StarGiftRow[]>([]);
  const [query, setQuery] = useState("");
  const [importOpen, setImportOpen] = useState(false);
  const [collectibleGift, setCollectibleGift] = useState<StarGiftRow | null>(null);
  const [file, setFile] = useState<File | null>(null);
  const [importSource, setImportSource] = useState<"official" | "file">("official");
  const [officialGifts, setOfficialGifts] = useState<OfficialStarGiftRow[]>([]);
  const [officialQuery, setOfficialQuery] = useState("");
  const [officialCategory, setOfficialCategory] = useState<OfficialGiftCategory>("all");
  const [sourceGiftID, setSourceGiftID] = useState("");
  const [includeCollectible, setIncludeCollectible] = useState(true);
  const [upgradeStars, setUpgradeStars] = useState("0");
  const [supplyTotal, setSupplyTotal] = useState("0");
  const [slugPrefix, setSlugPrefix] = useState("");
	const [giftID, setGiftID] = useState("0");
  const [title, setTitle] = useState("");
  const [stars, setStars] = useState("50");
  const [convertStars, setConvertStars] = useState("50");
  const [sortOrder, setSortOrder] = useState("0");
  const [lifecycleMode, setLifecycleMode] = useState<LifecycleMode>("regular");
  const [auctionSlug, setAuctionSlug] = useState("");
  const [auctionSupply, setAuctionSupply] = useState("10");
  const [giftsPerRound, setGiftsPerRound] = useState("1");
  const [roundDuration, setRoundDuration] = useState("3600");
  const [auctionStartAt, setAuctionStartAt] = useState("");
  const [unlockAt, setUnlockAt] = useState("");
  // "Rare": a plain (non-collectible) gift's own sales cap -- independent of
  // both auction (its own inventory) and a collectible pool's supply. Total
  // is Y, issued is X ("already sold" at authoring time; 0 for a fresh drop).
  const [limitedEnabled, setLimitedEnabled] = useState(false);
  const [limitedTotal, setLimitedTotal] = useState("100");
  const [limitedIssued, setLimitedIssued] = useState("0");
  const [releasedByUsername, setReleasedByUsername] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [reason, setReason] = useState("");
  const [preview, setPreview] = useState<CommandResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [importError, setImportError] = useState("");

  async function load() {
    setError("");
    try {
      setGifts((await api.gifts()).Gifts ?? []);
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  useEffect(() => { void load(); }, []);

  useEffect(() => {
    if (!importOpen || importSource !== "official" || officialGifts.length > 0) return;
    api.officialGifts().then((value) => setOfficialGifts(value.gifts ?? [])).catch((err) => setImportError(errorMessage(err)));
  }, [importOpen, importSource, officialGifts.length]);

  const selectedOfficial = useMemo(() => officialGifts.find((gift) => gift.source_gift_id === sourceGiftID) ?? null, [officialGifts, sourceGiftID]);
  const officialCategoryCounts = useMemo(() => ({
    all: officialGifts.length,
    upgrade: officialGifts.filter((gift) => gift.can_upgrade).length,
    craft: officialGifts.filter((gift) => gift.can_craft).length,
    basic: officialGifts.filter((gift) => !gift.can_upgrade).length
  }), [officialGifts]);
  const visibleOfficial = useMemo(() => {
    const normalized = officialQuery.trim().toLowerCase();
    return officialGifts.filter((gift) => {
      const categoryMatches = officialCategory === "all" ||
        (officialCategory === "upgrade" && gift.can_upgrade) ||
        (officialCategory === "craft" && gift.can_craft) ||
        (officialCategory === "basic" && !gift.can_upgrade);
      return categoryMatches && (!normalized || gift.source_gift_id.includes(normalized) || gift.title.toLowerCase().includes(normalized));
    });
  }, [officialGifts, officialQuery, officialCategory]);

  // The official-gift picker can hold 150+ cards; mounting all of them at
  // once is what made the import dialog stutter. Only the rows actually
  // inside the scrollable window (plus a small overscan) are rendered --
  // the rest collapse into two spacer cells so scrollHeight/scrollbar stay
  // correct. Columns and row height are measured off the real grid instead
  // of hard-coded, so this keeps working across the mobile 1-column layout.
  const officialListRef = useRef<HTMLDivElement | null>(null);
  const officialRowRef = useRef<HTMLButtonElement | null>(null);
  const [officialViewport, setOfficialViewport] = useState({ scrollTop: 0, height: 314 });
  const [officialColumns, setOfficialColumns] = useState(2);
  const [officialRowHeight, setOfficialRowHeight] = useState(112);

  useEffect(() => {
    const el = officialListRef.current;
    if (!el) return;
    let frame = 0;
    const measure = () => {
      frame = 0;
      setOfficialViewport({ scrollTop: el.scrollTop, height: el.clientHeight });
      const rowEl = officialRowRef.current;
      if (rowEl && rowEl.offsetWidth > 0) {
        setOfficialColumns(Math.max(1, Math.round(el.clientWidth / rowEl.offsetWidth)));
        setOfficialRowHeight(rowEl.offsetHeight + 8 /* grid gap, see .official-gift-list */);
      }
    };
    const onScroll = () => { if (!frame) frame = requestAnimationFrame(measure); };
    measure();
    el.addEventListener("scroll", onScroll, { passive: true });
    const observer = new ResizeObserver(onScroll);
    observer.observe(el);
    return () => { el.removeEventListener("scroll", onScroll); observer.disconnect(); if (frame) cancelAnimationFrame(frame); };
  }, [visibleOfficial.length]);

  const officialWindow = useMemo(() => {
    const rowCount = Math.ceil(visibleOfficial.length / officialColumns);
    const overscanRows = 3;
    const firstRow = Math.max(0, Math.floor(officialViewport.scrollTop / officialRowHeight) - overscanRows);
    const lastRow = Math.min(rowCount, Math.ceil((officialViewport.scrollTop + officialViewport.height) / officialRowHeight) + overscanRows);
    const start = firstRow * officialColumns;
    const end = Math.min(visibleOfficial.length, lastRow * officialColumns);
    return {
      items: visibleOfficial.slice(start, end),
      topSpacer: firstRow * officialRowHeight,
      bottomSpacer: Math.max(0, (rowCount - lastRow) * officialRowHeight)
    };
  }, [visibleOfficial, officialViewport, officialColumns, officialRowHeight]);

  const auctionRounds = useMemo(() => {
    const supply = Number(auctionSupply);
    const perRound = Number(giftsPerRound);
    if (!(supply > 0) || !(perRound > 0)) return 0;
    return Math.ceil(supply / perRound);
  }, [auctionSupply, giftsPerRound]);

  const visibleGifts = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    if (!normalized) return gifts;
    return gifts.filter((gift) =>
      String(gift.GiftID).includes(normalized) ||
      gift.Title.toLowerCase().includes(normalized) ||
      gift.SourceFormat.toLowerCase().includes(normalized)
    );
  }, [gifts, query]);

  // Mirrors domain.StarGiftCatalogWrite.ValidateLifecycleAuthoring so the operator
  // sees the problem before the round-trip. Only the keys belonging to the selected
  // mode are sent — the server rejects auction fields on a plain gift. Shared by
  // both import sources: the server now authors auctions/scheduled drops for an
  // official snapshot import the same way it always could for a file upload.
  function lifecyclePayload() {
    const now = Math.floor(Date.now() / 1000);
    if (lifecycleMode === "auction") {
      const supply = Number(auctionSupply);
      const perRound = Number(giftsPerRound);
      const duration = Number(roundDuration);
      const startDate = toUnixSeconds(auctionStartAt);
      if (!auctionSlug.trim()) throw new Error(t("gifts.lifecycle.slugRequired"));
      if (!(supply > 0)) throw new Error(t("gifts.lifecycle.supplyRequired"));
      if (!(perRound > 0)) throw new Error(t("gifts.lifecycle.perRoundRequired"));
      if (perRound > supply) throw new Error(t("gifts.lifecycle.perRoundTooLarge"));
      if (!(duration > 0)) throw new Error(t("gifts.lifecycle.roundDurationRequired"));
      if (startDate !== 0 && startDate < now) throw new Error(t("gifts.lifecycle.startPast"));
      return {
        auction: true,
        auction_slug: auctionSlug.trim().toLowerCase(),
        availability_total: supply,
        gifts_per_round: perRound,
        auction_round_duration: duration,
        auction_start_date: startDate
      };
    }
    if (lifecycleMode === "drop") {
      const unlock = toUnixSeconds(unlockAt);
      if (unlock <= now) throw new Error(t("gifts.lifecycle.unlockRequired"));
      return { locked_until_date: unlock };
    }
    return {};
  }

  // "Rare": a plain gift's own "X of Y sold" cap. Orthogonal to lifecycleMode
  // except auction, which owns its own inventory (the toggle is hidden for
  // that mode -- see the JSX -- so this only ever fires for regular/drop).
  function limitedPayload() {
    if (!limitedEnabled) return {};
    const total = Number(limitedTotal);
    const issued = Number(limitedIssued);
    if (!(total > 0)) throw new Error(t("gifts.limited.totalRequired"));
    if (issued < 0 || issued > total) throw new Error(t("gifts.limited.issuedInvalid"));
    return { limited: true, availability_total: total, availability_issued: issued };
  }

  function uploadForm(confirm: boolean, commandID = "") {
    if (!file) throw new Error(t("gifts.fileRequired"));
    if (!reason.trim()) throw new Error(t("action.reasonRequired"));
    const form = new FormData();
    form.set("metadata", JSON.stringify({
      command_id: commandID,
      reason: reason.trim(),
      confirm,
			gift_id: giftID,
			title: title.trim(),
			stars,
			convert_stars: convertStars,
      enabled,
      sort_order: Number(sortOrder),
      released_by_username: releasedByUsername.trim(),
      ...lifecyclePayload(),
      ...limitedPayload()
    }));
    form.set("file", file, file.name);
    return form;
  }

  function officialPayload(confirm: boolean, commandID = "") {
    if (!sourceGiftID) throw new Error(t("gifts.officialRequired"));
    if (!reason.trim()) throw new Error(t("action.reasonRequired"));
    return {
      command_id: commandID, reason: reason.trim(), confirm,
		source_gift_id: sourceGiftID, gift_id: giftID, title: title.trim(),
		stars, convert_stars: convertStars, enabled, sort_order: Number(sortOrder),
		include_collectible: includeCollectible, upgrade_stars: upgradeStars,
      supply_total: includeCollectible ? Number(supplyTotal) : 0, slug_prefix: slugPrefix.trim().toLowerCase(),
      released_by_username: releasedByUsername.trim(),
      ...lifecyclePayload(),
      ...limitedPayload()
    };
  }

  function chooseOfficial(gift: OfficialStarGiftRow) {
    setSourceGiftID(gift.source_gift_id);
    setTitle(gift.title || t("gifts.officialUnnamed", { id: gift.source_gift_id }));
    setStars(String(gift.stars));
    setConvertStars(String(gift.convert_stars));
    setIncludeCollectible(gift.can_upgrade);
		setUpgradeStars(gift.upgrade_stars);
    // Саплай — не догадка по счётчику вариантов каталога (upgrade_variants к
    // реальному тиражу отношения не имеет), а отдельное поле: администратор
    // заполняет его сам под конкретный релиз. Не указано -- бэкенд сам
    // подставит availability_total снапшота (см. ImportOfficialStarGift).
    setSupplyTotal("");
    setSlugPrefix(gift.title ? gift.title.trim().toLowerCase().replace(/\s+/g, "-") : `official-${gift.source_gift_id}`);
    setPreview(null);
  }

  async function validateImport() {
    setBusy(true); setImportError(""); setPreview(null);
    try {
      setPreview(importSource === "official" ? await api.importOfficialGift(officialPayload(false)) : await api.importGift(uploadForm(false)));
    } catch (err) {
      setImportError(errorMessage(err));
    } finally { setBusy(false); }
  }

  async function confirmImport() {
    if (!preview) return;
    setBusy(true); setImportError("");
    try {
      if (importSource === "official") await api.importOfficialGift(officialPayload(true, preview.command_id));
      else await api.importGift(uploadForm(true, preview.command_id));
		setPreview(null); setFile(null); setGiftID("0"); setTitle(""); setSourceGiftID("");
      await load();
      setImportOpen(false);
    } catch (err) {
      setImportError(errorMessage(err));
    } finally { setBusy(false); }
  }

  function startImport() {
	setGiftID("0"); setTitle(""); setStars("50"); setConvertStars("50"); setSortOrder("0");
    setEnabled(true); setReason(""); setFile(null); setPreview(null); setImportError("");
    setImportSource("official"); setSourceGiftID(""); setOfficialQuery(""); setOfficialCategory("all"); setImportOpen(true);
  }

  function startRevision(gift: StarGiftRow) {
    setGiftID(gift.GiftID); setTitle(gift.Title); setStars(String(gift.Stars));
    setConvertStars(String(gift.ConvertStars)); setSortOrder(String(gift.SortOrder)); setEnabled(gift.Enabled);
    setReason(""); setFile(null); setPreview(null); setImportError("");
    setImportSource("official"); setSourceGiftID(""); setOfficialQuery(""); setOfficialCategory("all"); setImportOpen(true);
  }

  return (
    <PageFrame title={t("gifts.pageTitle")} eyebrow={t("gifts.eyebrow")} actions={<>
      <button className="btn" type="button" onClick={() => load()} disabled={busy}><RefreshCw size={15} /> {t("common.refresh")}</button>
      <button className="btn primary" type="button" onClick={startImport}><Plus size={15} /> {t("gifts.add")}</button>
    </>}>
      {error && <Alert>{error}</Alert>}
      <div className="metric-row gift-metrics">
        <Metric label={t("gifts.total")} value={String(gifts.length)} />
        <Metric label={t("gifts.enabled")} value={String(gifts.filter((gift) => gift.Enabled).length)} tone="good" />
		<Metric label={t("gifts.received")} value={gifts.reduce((sum, gift) => sum + BigInt(gift.ReceivedCount), 0n).toString()} />
        <Metric label={t("gifts.formats")} value="TGS / Lottie" />
      </div>
      <QueryPanel>
        <div className="toolbar">
          <label className="searchbox"><Search size={15} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("gifts.searchPlaceholder")} /></label>
          <span className="gift-list-summary">{t("gifts.listSummary", { shown: visibleGifts.length, total: gifts.length })}</span>
        </div>
      </QueryPanel>
      <div className="table-wrap gift-table-wrap">
        <table className="data-table gift-table">
          <thead><tr><th>{t("gifts.animation")}</th><th>{t("gifts.idRevision")}</th><th>{t("gifts.title")}</th><th>{t("gifts.price")}</th><th>{t("gifts.source")}</th><th>{t("gifts.received")}</th><th>{t("common.status")}</th><th>{t("common.updatedAt")}</th><th>{t("common.actions")}</th></tr></thead>
          <tbody>
            {visibleGifts.map((gift) => (
              <tr className={gift.Enabled ? "" : "gift-row-disabled"} key={gift.GiftID}>
                <td><LottiePreview giftID={gift.GiftID} revision={gift.Revision} compact /></td>
                <td className="mono">{gift.GiftID} / {gift.Revision}</td>
                <td><strong className="gift-table-title">{gift.Title || `Gift #${gift.GiftID}`}</strong><span className="gift-sort-order">{t("gifts.sortOrder")}: {gift.SortOrder}</span></td>
                <td><strong className="gift-table-price">⭐ {gift.Stars}</strong><span className="gift-convert-price">→ {gift.ConvertStars}</span>{gift.Limited && <span className="gift-limited-badge">{t("gifts.limited.badge", { remains: gift.AvailabilityRemains, total: gift.AvailabilityTotal })}</span>}</td>
                <td><Badge>{gift.SourceFormat}</Badge><span className="gift-source-size">{formatBytes(gift.AnimationSize)}</span></td>
                <td>{gift.ReceivedCount}</td>
                <td><Badge tone={gift.Enabled ? "good" : "neutral"}>{gift.Enabled ? t("common.enabled") : t("common.disabled")}</Badge></td>
                <td>{formatDate(gift.UpdatedAt)}</td>
                <td><div className="gift-table-actions"><button className="btn compact-btn collectible-button" type="button" onClick={() => setCollectibleGift(gift)}><Gem size={13} />{t("collectibles.manage")}</button><button className="btn compact-btn" type="button" onClick={() => startRevision(gift)}>{t("gifts.replace")}</button><ActionButton compact tone="neutral" label={gift.Enabled ? t("gifts.disable") : t("gifts.enable")} path="/api/actions/set-gift-enabled" payload={() => ({ gift_id: gift.GiftID, enabled: !gift.Enabled })} onDone={() => void load()} /></div></td>
              </tr>
            ))}
            {visibleGifts.length === 0 && <EmptyRow colSpan={9} />}
          </tbody>
        </table>
      </div>

      {importOpen && createPortal(
        <div className="modal-backdrop" role="presentation">
			<section className="modal command-modal gift-import-modal" role="dialog" aria-modal="true" aria-label={giftID !== "0" ? t("gifts.newRevision", { id: giftID }) : t("gifts.importTitle")}>
            <div className="modal-head">
				<div><div className="eyebrow">{t("gifts.importEyebrow")}</div><h2>{giftID !== "0" ? t("gifts.newRevision", { id: giftID }) : t("gifts.importTitle")}</h2></div>
              <button className="icon-btn" type="button" onClick={() => setImportOpen(false)} disabled={busy} aria-label={t("action.close")}><X size={15} /></button>
            </div>
            <div className="command-body gift-import-modal-body">
              <div className="command-steps">
                <div className={`command-step ${(importSource === "official" ? sourceGiftID : file) ? "done" : "active"}`}><span>1</span><strong>{t("gifts.stepDetails")}</strong></div>
                <div className={`command-step ${preview ? "done" : (importSource === "official" ? sourceGiftID : file) ? "active" : ""}`}><span>2</span><strong>{t("gifts.stepValidate")}</strong></div>
                <div className={`command-step ${preview ? "active" : ""}`}><span>3</span><strong>{t("gifts.stepImport")}</strong></div>
              </div>
              <div className="gift-source-tabs">
                <button className={`btn ${importSource === "official" ? "primary" : ""}`} type="button" onClick={() => { setImportSource("official"); setPreview(null); }}>{t("gifts.officialSource")}</button>
                <button className={`btn ${importSource === "file" ? "primary" : ""}`} type="button" onClick={() => { setImportSource("file"); setPreview(null); }}>{t("gifts.fileSource")}</button>
              </div>
              {importSource === "official" ? <section className="official-gift-picker">
                <div className="gift-import-note"><span>{t("gifts.officialHint")}</span><div className="gift-format-chips"><span>{officialGifts.length}</span><span>SHA-256</span></div></div>
                <div className="official-gift-tools">
                  <label className="searchbox"><Search size={15} /><input value={officialQuery} onChange={(e) => setOfficialQuery(e.target.value)} placeholder={t("gifts.officialSearch")} /></label>
                  <span>{t("gifts.officialResults", { shown: visibleOfficial.length, total: officialGifts.length })}</span>
                </div>
                <div className="official-gift-categories" role="group" aria-label={t("gifts.officialCategoryLabel")}>
                  {(["all", "upgrade", "craft", "basic"] as const).map((category) => (
                    <button key={category} className={officialCategory === category ? "active" : ""} type="button"
                      aria-pressed={officialCategory === category} onClick={() => setOfficialCategory(category)}>
                      {t(`gifts.officialCategory.${category}`)}<span>{officialCategoryCounts[category]}</span>
                    </button>
                  ))}
                </div>
                <div className="official-gift-list" ref={officialListRef} role="listbox" aria-label={t("gifts.officialSelect")}>
                  {officialWindow.topSpacer > 0 && <div aria-hidden style={{ gridColumn: "1 / -1", height: officialWindow.topSpacer }} />}
                  {officialWindow.items.map((gift, index) => {
                    const selected = gift.source_gift_id === sourceGiftID;
                    return <button key={gift.source_gift_id} ref={index === 0 ? officialRowRef : undefined}
                      className={`official-gift-option ${selected ? "selected" : ""}`}
                      type="button" role="option" aria-selected={selected} onClick={() => chooseOfficial(gift)}>
                      <span className="official-gift-option-head">
                        <strong>{gift.title || t("gifts.officialUnnamed", { id: gift.source_gift_id })}</strong>
                        <span className="mono">#{gift.source_gift_id}</span>
                      </span>
                      <span className="official-gift-option-meta">
                        <span>⭐ {gift.stars}</span>
                        <span>{t("gifts.officialAttributes", { count: officialGiftAttributeCount(gift) })}</span>
                      </span>
                      <span className="official-gift-capabilities">
                        <span className={gift.can_upgrade ? "yes" : "no"}>{gift.can_upgrade ? t("gifts.canUpgrade") : t("gifts.cannotUpgrade")}</span>
                        <span className={gift.can_craft ? "craft" : "no"}>{gift.can_craft ? t("gifts.canCraft") : t("gifts.cannotCraft")}</span>
                      </span>
                    </button>;
                  })}
                  {officialWindow.bottomSpacer > 0 && <div aria-hidden style={{ gridColumn: "1 / -1", height: officialWindow.bottomSpacer }} />}
                  {visibleOfficial.length === 0 && <div className="official-gift-empty">{t("gifts.officialEmpty")}</div>}
                </div>
                {selectedOfficial && <div className="official-gift-selected">
                  <OfficialLottiePreview sourceGiftID={selectedOfficial.source_gift_id} />
                  <div><strong>{selectedOfficial.title || t("gifts.officialUnnamed", { id: selectedOfficial.source_gift_id })}</strong><span className="mono">{selectedOfficial.source_gift_id}</span><small>{selectedOfficial.model_count} {t("collectibles.models")} · {selectedOfficial.pattern_count} {t("collectibles.patterns")} · {selectedOfficial.backdrop_count} {t("collectibles.backdrops")}</small><span className="official-gift-capabilities"><span className={selectedOfficial.can_upgrade ? "yes" : "no"}>{selectedOfficial.can_upgrade ? t("gifts.canUpgrade") : t("gifts.cannotUpgrade")}</span><span className={selectedOfficial.can_craft ? "craft" : "no"}>{selectedOfficial.can_craft ? t("gifts.canCraft") : t("gifts.cannotCraft")}</span></span></div>
                </div>}
                {selectedOfficial?.can_upgrade && <>
                  <label className="gift-switch"><input type="checkbox" checked={includeCollectible} onChange={(e) => { setIncludeCollectible(e.target.checked); setPreview(null); }} /><span className="gift-switch-track" aria-hidden="true"><span /></span><span>{t("gifts.includeCollectible")}</span></label>
                  {includeCollectible && <div className="gift-fields-grid">
                    <label><span>{t("collectibles.upgradeStars")}</span><input type="number" min="1" value={upgradeStars} onChange={(e) => { setUpgradeStars(e.target.value); setPreview(null); }} /></label>
                    <label><span>{t("collectibles.supply")}</span><input type="number" min="1" value={supplyTotal} placeholder={t("collectibles.supplyPlaceholder")} onChange={(e) => { setSupplyTotal(e.target.value); setPreview(null); }} /></label>
                    <label><span>{t("collectibles.slug")}</span><input value={slugPrefix} maxLength={48} onChange={(e) => { setSlugPrefix(e.target.value.toLowerCase()); setPreview(null); }} /></label>
                  </div>}
                  {includeCollectible && <div className="gift-import-note"><span>{t("gifts.officialSupplyHint")}</span></div>}
                </>}
              </section> : <>
                <div className="gift-import-note"><span>{t("gifts.importHint")}</span><div className="gift-format-chips" aria-label={t("gifts.formats")}><span>TGS</span><span>Lottie JSON</span></div></div>
                <label className={`gift-file-picker ${file ? "has-file" : ""}`}>
                  <input type="file" accept=".tgs,.json,.lottie,application/json,application/x-tgsticker" onChange={(e) => { setFile(e.target.files?.[0] ?? null); setPreview(null); }} />
                  <span className="gift-file-icon"><FileJson2 size={22} /></span>
                  <span className="gift-file-copy"><span className="gift-field-label">{t("gifts.animation")}</span><strong>{file ? file.name : t("gifts.filePrompt")}</strong><small>{file ? formatBytes(file.size) : t("gifts.fileHint")}</small></span>
                  <span className="gift-file-action">{file ? t("gifts.changeFile") : t("gifts.chooseFile")}</span>
                </label>
              </>}
              <div className="gift-fields-grid">
                <label><span>{t("gifts.title")}</span><input value={title} maxLength={128} placeholder={t("gifts.titlePlaceholder")} onChange={(e) => { setTitle(e.target.value); setPreview(null); }} /></label>
                <label><span>{t("gifts.stars")}</span><input type="number" min="1" value={stars} onChange={(e) => { setStars(e.target.value); setPreview(null); }} /></label>
                <label><span>{t("gifts.convertStars")}</span><input type="number" min="0" value={convertStars} onChange={(e) => { setConvertStars(e.target.value); setPreview(null); }} /></label>
                <label><span>{t("gifts.sortOrder")}</span><input type="number" value={sortOrder} onChange={(e) => { setSortOrder(e.target.value); setPreview(null); }} /></label>
              </div>
              <section className="gift-lifecycle">
                <span className="gift-field-label">{t("gifts.lifecycle.label")}</span>
                <div className="gift-source-tabs" role="group" aria-label={t("gifts.lifecycle.label")}>
                  {(["regular", "auction", "drop"] as const).map((mode) => (
                    <button key={mode} className={`btn ${lifecycleMode === mode ? "primary" : ""}`} type="button"
                      aria-pressed={lifecycleMode === mode} onClick={() => { setLifecycleMode(mode); setPreview(null); }}>
                      {t(`gifts.lifecycle.${mode}`)}
                    </button>
                  ))}
                </div>
                <div className="gift-import-note"><span>{lifecycleMode === "auction" ? t("gifts.lifecycle.hintAuction") : lifecycleMode === "drop" ? t("gifts.lifecycle.hintDrop") : t("gifts.lifecycle.hintRegular")}</span></div>
                {lifecycleMode === "auction" && <>
                  <div className="gift-fields-grid">
                    <label><span>{t("gifts.auction.slug")}</span><input value={auctionSlug} maxLength={48} placeholder={t("gifts.auction.slugPlaceholder")} onChange={(e) => { setAuctionSlug(e.target.value.toLowerCase()); setPreview(null); }} /></label>
                    <label><span>{t("gifts.auction.supply")}</span><input type="number" min="1" value={auctionSupply} onChange={(e) => { setAuctionSupply(e.target.value); setPreview(null); }} /></label>
                    <label><span>{t("gifts.auction.perRound")}</span><input type="number" min="1" value={giftsPerRound} onChange={(e) => { setGiftsPerRound(e.target.value); setPreview(null); }} /></label>
                    <label><span>{t("gifts.auction.roundDuration")}</span><input type="number" min="1" value={roundDuration} onChange={(e) => { setRoundDuration(e.target.value); setPreview(null); }} /></label>
                    <label><span>{t("gifts.auction.startAt")}</span><input type="datetime-local" value={auctionStartAt} onChange={(e) => { setAuctionStartAt(e.target.value); setPreview(null); }} /></label>
                  </div>
                  {auctionRounds > 0 && <div className="gift-import-note"><span>{t("gifts.auction.plan", { rounds: auctionRounds, perRound: Number(giftsPerRound), minutes: Math.max(1, Math.round(Number(roundDuration) / 60)) })}</span></div>}
                </>}
                {lifecycleMode === "drop" && <div className="gift-fields-grid">
                  <label><span>{t("gifts.drop.unlockAt")}</span><input type="datetime-local" value={unlockAt} onChange={(e) => { setUnlockAt(e.target.value); setPreview(null); }} /></label>
                </div>}
              </section>
              {lifecycleMode !== "auction" && <section className="gift-lifecycle">
                <label className="gift-switch"><input type="checkbox" checked={limitedEnabled} onChange={(e) => { setLimitedEnabled(e.target.checked); setPreview(null); }} /><span className="gift-switch-track" aria-hidden="true"><span /></span><span>{t("gifts.limited.label")}</span></label>
                {limitedEnabled && <>
                  <div className="gift-import-note"><span>{t("gifts.limited.hint")}</span></div>
                  <div className="gift-fields-grid">
                    <label><span>{t("gifts.limited.total")}</span><input type="number" min="1" value={limitedTotal} onChange={(e) => { setLimitedTotal(e.target.value); setPreview(null); }} /></label>
                    <label><span>{t("gifts.limited.issued")}</span><input type="number" min="0" max={limitedTotal} value={limitedIssued} onChange={(e) => { setLimitedIssued(e.target.value); setPreview(null); }} /></label>
                  </div>
                </>}
              </section>}
              <label className="gift-reason-field"><span>{t("gifts.releasedBy")}</span><input value={releasedByUsername} placeholder={t("gifts.releasedByPlaceholder")} onChange={(e) => { setReleasedByUsername(e.target.value); setPreview(null); }} /></label>
              <label className="gift-reason-field"><span>{t("gifts.reason")}</span><input value={reason} placeholder={t("gifts.reasonPlaceholder")} onChange={(e) => setReason(e.target.value)} /></label>
              <label className="gift-switch"><input type="checkbox" checked={enabled} onChange={(e) => { setEnabled(e.target.checked); setPreview(null); }} /><span className="gift-switch-track" aria-hidden="true"><span /></span><span>{t("gifts.enableAfterImport")}</span></label>
              {importError && <Alert>{importError}</Alert>}
              {preview && <div className="gift-validation"><div className="gift-validation-head"><CheckCircle2 size={17} /><div><strong>{t("gifts.validationReady")}</strong><span>{t("gifts.validationHint")}</span></div></div><pre>{JSON.stringify(preview.details, null, 2)}</pre></div>}
            </div>
            <div className="modal-actions">
              <button className="btn" type="button" onClick={() => setImportOpen(false)} disabled={busy}>{t("common.close")}</button>
              <button className="btn" type="button" onClick={validateImport} disabled={busy}>{busy ? <Loader2 className="spin" size={15} /> : <ShieldCheck size={15} />}{t("gifts.validate")}</button>
              <button className="btn primary" type="button" onClick={confirmImport} disabled={busy || !preview}><Upload size={15} />{t("gifts.confirmImport")}</button>
            </div>
          </section>
        </div>,
        document.body
      )}
      {collectibleGift && <GiftCollectiblesModal gift={collectibleGift} onClose={() => setCollectibleGift(null)} onPublished={() => void load()} />}
    </PageFrame>
  );
}
