import { ImageOff, Loader2, Plus, RefreshCw, Upload, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { createPortal } from "react-dom";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { SectionTabs } from "../components/SectionTabs";
import { Alert, Badge, EmptyRow, Metric, PageFrame } from "../components/ui";
import { useI18n } from "../i18n";
import type { Navigate } from "../routing";
import type { GifCatalogRow } from "../types";

const gifCategories = ["reaction", "humor", "animals", "sports", "celebration", "other"] as const;

function GifPreview({ documentID }: { documentID: string }) {
  const [broken, setBroken] = useState(false);
  if (broken) return <div className="sticker-list-thumb-empty"><ImageOff size={14} /></div>;
  return <video className="gif-catalog-thumb" src={api.gifCatalogDocumentPreviewURL(documentID)} muted loop autoPlay playsInline onError={() => setBroken(true)} />;
}

export function GifCatalogPage({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const [rows, setRows] = useState<GifCatalogRow[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const [orderDrafts, setOrderDrafts] = useState<Record<string, string>>({});
  const [categoryDrafts, setCategoryDrafts] = useState<Record<string, string>>({});
  const [categoryFilter, setCategoryFilter] = useState("");

  async function load() {
    setBusy(true); setError("");
    try { setRows((await api.gifCatalog()).rows ?? []); }
    catch (err) { setError(errorMessage(err)); }
    finally { setBusy(false); }
  }
  useEffect(() => { void load(); }, []);
  const enabled = useMemo(() => rows.filter((row) => row.Enabled).length, [rows]);
  const visibleRows = useMemo(() => rows.filter((row) => !categoryFilter || row.Category === categoryFilter), [rows, categoryFilter]);

  return (
    <PageFrame title={t("gifCatalog.title")} eyebrow={t("gifCatalog.eyebrow")}
      actions={<><button className="btn" type="button" disabled={busy} onClick={() => void load()}><RefreshCw size={15} />{t("common.refresh")}</button><button className="btn primary" type="button" onClick={() => setCreateOpen(true)}><Plus size={15} />{t("gifCatalog.add")}</button></>}>
      <SectionTabs sectionId="media" active="/gif-catalog" navigate={navigate} />
      {error && <Alert>{error}</Alert>}
      <div className="metric-row"><Metric label={t("gifCatalog.entries")} value={`${rows.length} / 50`} /><Metric label={t("common.enabled")} value={String(enabled)} tone="good" /></div>
      <div className="toolbar"><label>{t("gifCatalog.filterCategory")} <select value={categoryFilter} onChange={(event) => setCategoryFilter(event.target.value)}><option value="">{t("gifCatalog.allCategories")}</option>{gifCategories.map((category) => <option key={category} value={category}>{t(`gifCatalog.category.${category}`)}</option>)}</select></label></div>
      <div className="table-wrap">
        <table className="data-table"><thead><tr><th>{t("common.preview")}</th><th>{t("common.title")}</th><th>{t("gifCatalog.category")}</th><th>{t("common.document")}</th><th>{t("common.source")}</th><th>{t("common.status")}</th><th>{t("common.order")}</th><th>{t("common.actions")}</th></tr></thead>
          <tbody>{visibleRows.map((row) => <tr key={row.ID} className={row.Enabled ? "" : "gift-row-disabled"}>
            <td><GifPreview documentID={row.DocumentID} /></td><td>{row.Title}</td>
            <td><div>{t(`gifCatalog.category.${row.Category || "other"}`)} · {row.CategoryManual ? t("gifCatalog.manual") : t("gifCatalog.auto")}</div><div className="sort-order-editor"><select value={categoryDrafts[row.ID] ?? (row.CategoryManual ? row.Category : "")} onChange={(event) => setCategoryDrafts((prev) => ({ ...prev, [row.ID]: event.target.value }))}><option value="">{t("gifCatalog.auto")}</option>{gifCategories.map((category) => <option key={category} value={category}>{t(`gifCatalog.category.${category}`)}</option>)}</select><ActionButton compact tone="neutral" label={t("common.save")} path="/api/actions/set-gif-catalog-category" payload={() => ({ id: row.ID, category: categoryDrafts[row.ID] ?? (row.CategoryManual ? row.Category : "") })} disabled={categoryDrafts[row.ID] === undefined || categoryDrafts[row.ID] === (row.CategoryManual ? row.Category : "")} onDone={() => void load()} /></div></td>
            <td className="mono">{row.DocumentID}</td><td>{row.SourceFilename || row.FileName || t("gifCatalog.adminUpload")}</td>
            <td>{row.Enabled ? <Badge tone="good">{t("common.enabled")}</Badge> : <Badge tone="danger">{t("common.disabled")}</Badge>}</td>
            <td><div className="sort-order-editor"><input className="small-input" type="number" value={orderDrafts[row.ID] ?? String(row.SortOrder)} onChange={(event) => setOrderDrafts((prev) => ({ ...prev, [row.ID]: event.target.value }))} />
              <ActionButton compact tone="neutral" label={t("common.save")} path="/api/actions/set-gif-catalog-sort-order" payload={() => ({ id: row.ID, sort_order: Number(orderDrafts[row.ID] ?? row.SortOrder) })} onDone={() => void load()} /></div></td>
            <td><div className="gift-table-actions"><ActionButton compact tone="neutral" label={row.Enabled ? t("common.disable") : t("common.enable")} path="/api/actions/set-gif-catalog-enabled" payload={() => ({ id: row.ID, enabled: !row.Enabled })} onDone={() => void load()} />
              <ActionButton compact tone="danger" label={t("common.delete")} path="/api/actions/delete-gif-catalog-entry" payload={() => ({ id: row.ID })} onDone={() => void load()} /></div></td>
          </tr>)}{visibleRows.length === 0 && <EmptyRow colSpan={8} />}</tbody>
        </table>
      </div>
      {createOpen && <AddGifModal onClose={() => setCreateOpen(false)} onCreated={() => void load()} />}
    </PageFrame>
  );
}

function AddGifModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const { t } = useI18n();
  const [title, setTitle] = useState("");
  const [reason, setReason] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [previewURL, setPreviewURL] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  function choose(next: File | null) {
    if (previewURL) URL.revokeObjectURL(previewURL);
    setFile(next); setPreviewURL(next ? URL.createObjectURL(next) : "");
  }
  async function submit() {
    if (!title.trim() || !reason.trim() || !file) { setError(t("gifCatalog.required")); return; }
    setBusy(true); setError("");
    try {
      const form = new FormData(); form.set("metadata", JSON.stringify({ command_id: "", reason: reason.trim(), confirm: true, title: title.trim() })); form.set("file", file, file.name);
      await api.createGifCatalogEntry(form); onCreated(); onClose();
    } catch (err) { setError(errorMessage(err)); } finally { setBusy(false); }
  }
  return createPortal(<div className="modal-backdrop" role="presentation"><section className="modal command-modal" role="dialog" aria-modal="true">
    <div className="modal-head"><div><div className="eyebrow">{t("gifCatalog.newMaterial")}</div><h2>{t("gifCatalog.add")}</h2></div><button className="icon-btn" type="button" onClick={onClose} disabled={busy} aria-label={t("common.close")}><X size={15} /></button></div>
    <div className="command-body"><label><span>{t("common.title")}</span><input value={title} maxLength={128} onChange={(event) => setTitle(event.target.value)} /></label>
      <label className={`gift-file-picker ${file ? "has-file" : ""}`}><input type="file" accept=".gif,.mp4,image/gif,video/mp4" onChange={(event) => choose(event.target.files?.[0] ?? null)} /><span className="gift-file-copy"><strong>{file?.name ?? t("gifCatalog.chooseMedia")}</strong></span><span className="gift-file-action">{t("common.choose")}</span></label>
      {previewURL && <div className="gif-catalog-preview">{file?.type === "video/mp4" ? <video src={previewURL} autoPlay loop muted playsInline /> : <img src={previewURL} alt="" />}</div>}
      <label><span>{t("action.reason")}</span><input value={reason} onChange={(event) => setReason(event.target.value)} /></label>{error && <Alert>{error}</Alert>}
    </div><div className="modal-actions"><button className="btn" type="button" onClick={onClose} disabled={busy}>{t("common.close")}</button><button className="btn primary" type="button" onClick={() => void submit()} disabled={busy}>{busy ? <Loader2 className="spin" size={15} /> : <Upload size={15} />}{t("gifCatalog.add")}</button></div>
  </section></div>, document.body);
}
