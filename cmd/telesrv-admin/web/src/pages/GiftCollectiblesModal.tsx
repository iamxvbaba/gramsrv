import { CheckCircle2, FileJson2, Gem, Loader2, Plus, ShieldCheck, Sparkles, Trash2, Upload, X } from "lucide-react";
import lottie from "lottie-web/build/player/lottie_light_canvas";
import { useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { api, errorMessage } from "../api";
import { Alert, Badge } from "../components/ui";
import { useI18n } from "../i18n";
import type { CommandResult, StarGiftCollectibleAttributeRow, StarGiftCollectiblePreview, StarGiftRow } from "../types";

type AnimationData = Record<string, unknown>;
type AnimatedDraft = {
  key: string;
  name: string;
  rarity: string;
  sortOrder: string;
  file: File | null;
  animation: AnimationData | null;
  fileError: string;
};
type BackdropDraft = {
  key: string;
  name: string;
  backdropID: string;
  rarity: string;
  sortOrder: string;
  center: string;
  edge: string;
  pattern: string;
  text: string;
};

let draftSequence = 0;
const nextKey = (kind: string) => `${kind}-${++draftSequence}`;
const newAnimated = (kind: string): AnimatedDraft => ({ key: nextKey(kind), name: "", rarity: "1000", sortOrder: "0", file: null, animation: null, fileError: "" });
const newBackdrop = (): BackdropDraft => ({ key: nextKey("backdrop"), name: "", backdropID: "1", rarity: "1000", sortOrder: "0", center: "#6f5bea", edge: "#34278f", pattern: "#a89df5", text: "#ffffff" });

function AnimationPreview({ data, compact = false }: { data: AnimationData; compact?: boolean }) {
  const host = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!host.current) return;
    const player = lottie.loadAnimation({ container: host.current, renderer: "canvas", loop: true, autoplay: true, animationData: structuredClone(data) });
    return () => player.destroy();
  }, [data]);
  return <div className={`collectible-animation ${compact ? "compact" : ""}`} ref={host} />;
}

function RemoteAnimation({ giftID, attribute }: { giftID: number; attribute: StarGiftCollectibleAttributeRow }) {
  const [data, setData] = useState<AnimationData | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let cancelled = false;
    setFailed(false);
    api.giftCollectibleAnimation(giftID, attribute.kind as "model" | "pattern", attribute.id)
      .then((value) => { if (!cancelled) setData(value); })
      .catch(() => { if (!cancelled) setFailed(true); });
    return () => { cancelled = true; };
  }, [giftID, attribute.id, attribute.kind]);
  if (failed) return <div className="collectible-animation compact failed">!</div>;
  if (!data) return <div className="collectible-animation compact loading"><Loader2 className="spin" size={15} /></div>;
  return <AnimationPreview data={data} compact />;
}

async function parseAnimationFile(file: File): Promise<AnimationData> {
  const bytes = new Uint8Array(await file.arrayBuffer());
  let raw: Uint8Array = bytes;
  if (bytes.length >= 2 && bytes[0] === 0x1f && bytes[1] === 0x8b) {
    if (!("DecompressionStream" in window)) throw new Error("This browser cannot preview TGS files");
    const stream = new Blob([bytes]).stream().pipeThrough(new DecompressionStream("gzip"));
    raw = new Uint8Array(await new Response(stream).arrayBuffer());
  }
  const parsed: unknown = JSON.parse(new TextDecoder().decode(raw));
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("Invalid Lottie JSON");
  return parsed as AnimationData;
}

const colorNumber = (value: string) => Number.parseInt(value.replace("#", ""), 16);

export function GiftCollectiblesModal({ gift, onClose, onPublished }: { gift: StarGiftRow; onClose: () => void; onPublished: () => void }) {
  const { t } = useI18n();
  const [active, setActive] = useState<StarGiftCollectiblePreview | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [preview, setPreview] = useState<CommandResult | null>(null);
  const [upgradeStars, setUpgradeStars] = useState("100");
  const [supplyTotal, setSupplyTotal] = useState("1000");
  const [slugPrefix, setSlugPrefix] = useState(`gift-${gift.GiftID}`);
  const [reason, setReason] = useState("");
  const [models, setModels] = useState<AnimatedDraft[]>([newAnimated("model")]);
  const [patterns, setPatterns] = useState<AnimatedDraft[]>([newAnimated("pattern")]);
  const [backdrops, setBackdrops] = useState<BackdropDraft[]>([newBackdrop()]);

  useEffect(() => {
    let cancelled = false;
    api.giftCollectibles(gift.GiftID).then((value) => {
      if (cancelled) return;
      setActive(value);
      if (value.found) {
        setUpgradeStars(String(value.upgrade_stars ?? 100));
        setSupplyTotal(String(value.supply_total ?? 1000));
        setSlugPrefix(value.slug_prefix ?? `gift-${gift.GiftID}`);
      }
    }).catch((err) => setError(errorMessage(err))).finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [gift.GiftID]);

  const rarityTotals = useMemo(() => ({
    models: models.reduce((sum, value) => sum + Number(value.rarity || 0), 0),
    patterns: patterns.reduce((sum, value) => sum + Number(value.rarity || 0), 0),
    backdrops: backdrops.reduce((sum, value) => sum + Number(value.rarity || 0), 0)
  }), [models, patterns, backdrops]);

  const invalidate = () => setPreview(null);
  const updateAnimated = (kind: "models" | "patterns", key: string, patch: Partial<AnimatedDraft>) => {
    const setter = kind === "models" ? setModels : setPatterns;
    setter((rows) => rows.map((row) => row.key === key ? { ...row, ...patch } : row));
    invalidate();
  };

  async function chooseFile(kind: "models" | "patterns", row: AnimatedDraft, file: File | null) {
    updateAnimated(kind, row.key, { file, animation: null, fileError: "" });
    if (!file) return;
    try {
      const animation = await parseAnimationFile(file);
      updateAnimated(kind, row.key, { animation, fileError: "" });
    } catch (err) {
      updateAnimated(kind, row.key, { animation: null, fileError: errorMessage(err) });
    }
  }

  function buildForm(confirm: boolean, commandID = "") {
    if (!reason.trim()) throw new Error(t("action.reasonRequired"));
    for (const row of [...models, ...patterns]) if (!row.file) throw new Error(t("collectibles.fileRequired"));
    const form = new FormData();
    const animatedMetadata = (rows: AnimatedDraft[]) => rows.map((row) => ({ name: row.name.trim(), rarity_permille: Number(row.rarity), sort_order: Number(row.sortOrder), file_key: row.key }));
    form.set("metadata", JSON.stringify({
      command_id: commandID, reason: reason.trim(), confirm,
      upgrade_stars: Number(upgradeStars), supply_total: Number(supplyTotal), slug_prefix: slugPrefix.trim().toLowerCase(),
      models: animatedMetadata(models), patterns: animatedMetadata(patterns),
      backdrops: backdrops.map((row) => ({
        name: row.name.trim(), backdrop_id: Number(row.backdropID), rarity_permille: Number(row.rarity), sort_order: Number(row.sortOrder),
        center_color: colorNumber(row.center), edge_color: colorNumber(row.edge), pattern_color: colorNumber(row.pattern), text_color: colorNumber(row.text)
      }))
    }));
    for (const row of [...models, ...patterns]) form.set(row.key, row.file as File, (row.file as File).name);
    return form;
  }

  async function validate() {
    setBusy(true); setError(""); setPreview(null);
    try { setPreview(await api.publishGiftCollectibles(gift.GiftID, buildForm(false))); }
    catch (err) { setError(errorMessage(err)); }
    finally { setBusy(false); }
  }

  async function publish() {
    if (!preview) return;
    setBusy(true); setError("");
    try {
      await api.publishGiftCollectibles(gift.GiftID, buildForm(true, preview.command_id));
      onPublished(); onClose();
    } catch (err) { setError(errorMessage(err)); }
    finally { setBusy(false); }
  }

  const renderAnimatedRows = (kind: "models" | "patterns", rows: AnimatedDraft[], setRows: (rows: AnimatedDraft[]) => void) => (
    <section className="collectible-section">
      <div className="collectible-section-head">
        <div><strong>{t(`collectibles.${kind}`)}</strong><span>{t("collectibles.rarityHint")}</span></div>
        <div className="collectible-section-tools"><Badge tone={rarityTotals[kind] === 1000 ? "good" : "neutral"}>{rarityTotals[kind]} / 1000</Badge><button className="btn compact-btn" type="button" onClick={() => { setRows([...rows, newAnimated(kind === "models" ? "model" : "pattern")]); invalidate(); }}><Plus size={13} />{t("collectibles.addAttribute")}</button></div>
      </div>
      <div className="collectible-rows">
        {rows.map((row, index) => <div className="collectible-row animated" key={row.key}>
          <div className="collectible-row-index">{index + 1}</div>
          <label><span>{t("common.name")}</span><input value={row.name} maxLength={128} onChange={(e) => updateAnimated(kind, row.key, { name: e.target.value })} /></label>
          <label><span>{t("collectibles.rarity")}</span><input type="number" min="1" max="1000" value={row.rarity} onChange={(e) => updateAnimated(kind, row.key, { rarity: e.target.value })} /></label>
          <label><span>{t("gifts.sortOrder")}</span><input type="number" value={row.sortOrder} onChange={(e) => updateAnimated(kind, row.key, { sortOrder: e.target.value })} /></label>
          <label className="collectible-file"><span>{t("gifts.animation")}</span><input type="file" accept=".tgs,.json,.lottie,application/json,application/x-tgsticker" onChange={(e) => void chooseFile(kind, row, e.target.files?.[0] ?? null)} /><em><FileJson2 size={13} />{row.file?.name ?? t("gifts.chooseFile")}</em></label>
          <div className="collectible-inline-preview">{row.animation ? <AnimationPreview data={row.animation} compact /> : <Sparkles size={16} />}</div>
          <button className="icon-btn danger" type="button" disabled={rows.length === 1} onClick={() => { setRows(rows.filter((value) => value.key !== row.key)); invalidate(); }} aria-label={t("collectibles.remove")}><Trash2 size={14} /></button>
          {row.fileError && <span className="collectible-file-error">{row.fileError}</span>}
        </div>)}
      </div>
    </section>
  );

  return createPortal(<div className="modal-backdrop" role="presentation">
    <section className="modal command-modal collectible-modal" role="dialog" aria-modal="true" aria-label={t("collectibles.title", { id: gift.GiftID })}>
      <div className="modal-head">
        <div><div className="eyebrow">{t("collectibles.eyebrow")}</div><h2>{t("collectibles.title", { id: gift.GiftID })}</h2><p>{gift.Title || `Gift #${gift.GiftID}`}</p></div>
        <button className="icon-btn" type="button" onClick={onClose} disabled={busy} aria-label={t("action.close")}><X size={15} /></button>
      </div>
      <div className="command-body collectible-modal-body">
        {loading ? <div className="collectible-loading"><Loader2 className="spin" />{t("common.loading")}</div> : active?.found ? <section className="collectible-active">
          <div className="collectible-active-head"><div><Gem size={18} /><div><strong>{t("collectibles.activeRevision", { revision: active.revision ?? 0 })}</strong><span>{active.slug_prefix} · ⭐ {active.upgrade_stars} · {active.issued} / {active.supply_total}</span></div></div><Badge tone="good">{t("collectibles.published")}</Badge></div>
          <div className="collectible-active-grid">
            {[...(active.models ?? []), ...(active.patterns ?? [])].map((attribute) => <article key={`${attribute.kind}-${attribute.id}`}><RemoteAnimation giftID={gift.GiftID} attribute={attribute} /><div><strong>{attribute.name}</strong><span>{t(`collectibles.${attribute.kind}`)} · {attribute.rarity_permille}‰</span></div></article>)}
            {(active.backdrops ?? []).map((attribute) => <article key={`backdrop-${attribute.id}`}><div className="collectible-backdrop-preview" style={{ background: `radial-gradient(circle, #${(attribute.center_color ?? 0).toString(16).padStart(6, "0")}, #${(attribute.edge_color ?? 0).toString(16).padStart(6, "0")})`, color: `#${(attribute.text_color ?? 0xffffff).toString(16).padStart(6, "0")}` }}>Aa</div><div><strong>{attribute.name}</strong><span>{t("collectibles.backdrop")} · {attribute.rarity_permille}‰</span></div></article>)}
          </div>
        </section> : <div className="collectible-empty"><Gem size={22} /><div><strong>{t("collectibles.noPool")}</strong><span>{t("collectibles.noPoolHint")}</span></div></div>}

        <section className="collectible-definition">
          <div className="collectible-definition-head"><div><strong>{t("collectibles.publishNew")}</strong><span>{t("collectibles.immutableHint")}</span></div><div className="gift-format-chips"><span>TGS</span><span>Lottie JSON</span></div></div>
          <div className="gift-fields-grid collectible-main-fields">
            <label><span>{t("collectibles.upgradeStars")}</span><input type="number" min="1" value={upgradeStars} onChange={(e) => { setUpgradeStars(e.target.value); invalidate(); }} /></label>
            <label><span>{t("collectibles.supply")}</span><input type="number" min="1" value={supplyTotal} onChange={(e) => { setSupplyTotal(e.target.value); invalidate(); }} /></label>
            <label><span>{t("collectibles.slug")}</span><input value={slugPrefix} maxLength={48} onChange={(e) => { setSlugPrefix(e.target.value.toLowerCase()); invalidate(); }} /></label>
            <label><span>{t("gifts.reason")}</span><input value={reason} maxLength={1000} placeholder={t("gifts.reasonPlaceholder")} onChange={(e) => setReason(e.target.value)} /></label>
          </div>
          {renderAnimatedRows("models", models, setModels)}
          {renderAnimatedRows("patterns", patterns, setPatterns)}
          <section className="collectible-section">
            <div className="collectible-section-head"><div><strong>{t("collectibles.backdrops")}</strong><span>{t("collectibles.colorHint")}</span></div><div className="collectible-section-tools"><Badge tone={rarityTotals.backdrops === 1000 ? "good" : "neutral"}>{rarityTotals.backdrops} / 1000</Badge><button className="btn compact-btn" type="button" onClick={() => { setBackdrops([...backdrops, newBackdrop()]); invalidate(); }}><Plus size={13} />{t("collectibles.addAttribute")}</button></div></div>
            <div className="collectible-rows">{backdrops.map((row, index) => <div className="collectible-row backdrop" key={row.key}>
              <div className="collectible-row-index">{index + 1}</div>
              <label><span>{t("common.name")}</span><input value={row.name} maxLength={128} onChange={(e) => { setBackdrops(backdrops.map((value) => value.key === row.key ? { ...value, name: e.target.value } : value)); invalidate(); }} /></label>
              <label><span>{t("collectibles.backdropID")}</span><input type="number" min="1" value={row.backdropID} onChange={(e) => { setBackdrops(backdrops.map((value) => value.key === row.key ? { ...value, backdropID: e.target.value } : value)); invalidate(); }} /></label>
              <label><span>{t("collectibles.rarity")}</span><input type="number" min="1" max="1000" value={row.rarity} onChange={(e) => { setBackdrops(backdrops.map((value) => value.key === row.key ? { ...value, rarity: e.target.value } : value)); invalidate(); }} /></label>
              <label><span>{t("gifts.sortOrder")}</span><input type="number" value={row.sortOrder} onChange={(e) => { setBackdrops(backdrops.map((value) => value.key === row.key ? { ...value, sortOrder: e.target.value } : value)); invalidate(); }} /></label>
              {(["center", "edge", "pattern", "text"] as const).map((field) => <label className="collectible-color" key={field}><span>{t(`collectibles.color.${field}`)}</span><input type="color" value={row[field]} onChange={(e) => { setBackdrops(backdrops.map((value) => value.key === row.key ? { ...value, [field]: e.target.value } : value)); invalidate(); }} /></label>)}
              <div className="collectible-backdrop-preview" style={{ background: `radial-gradient(circle, ${row.center}, ${row.edge})`, color: row.text }}>Aa</div>
              <button className="icon-btn danger" type="button" disabled={backdrops.length === 1} onClick={() => { setBackdrops(backdrops.filter((value) => value.key !== row.key)); invalidate(); }} aria-label={t("collectibles.remove")}><Trash2 size={14} /></button>
            </div>)}</div>
          </section>
        </section>
        {error && <Alert>{error}</Alert>}
        {preview && <div className="gift-validation"><div className="gift-validation-head"><CheckCircle2 size={17} /><div><strong>{t("collectibles.validationReady")}</strong><span>{t("collectibles.validationHint")}</span></div></div><pre>{JSON.stringify(preview.details, null, 2)}</pre></div>}
      </div>
      <div className="modal-actions">
        <button className="btn" type="button" onClick={onClose} disabled={busy}>{t("common.close")}</button>
        <button className="btn" type="button" onClick={validate} disabled={busy}>{busy ? <Loader2 className="spin" size={15} /> : <ShieldCheck size={15} />}{t("gifts.validate")}</button>
        <button className="btn primary" type="button" onClick={publish} disabled={busy || !preview}><Upload size={15} />{t("collectibles.publish")}</button>
      </div>
    </section>
  </div>, document.body);
}
