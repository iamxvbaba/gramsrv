import { ChevronLeft, ChevronRight, Eye, Files, ImageOff, Plus, RefreshCw, Search } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { StickerDocumentPreview } from "../components/StickerDocumentPreview";
import { Alert, Badge, EmptyRow, Metric, PageFrame, QueryPanel } from "../components/ui";
import { useI18n } from "../i18n";
import type { StickerSetRow } from "../types";
import type { Navigate } from "../routing";
import { CreateStickerSetModal } from "./CreateStickerSetModal";
import { StickerSetPreviewModal } from "./StickerSetPreviewModal";

type StickerPageSize = 10 | 20 | 50 | 100 | "all";

export function StickerSetsPage({ kind, navigate }: { kind: "stickers" | "emoji"; navigate: Navigate }) {
  const { t } = useI18n();
  const [sets, setSets] = useState<StickerSetRow[]>([]);
  const [query, setQuery] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [pageSize, setPageSize] = useState<StickerPageSize>(10);
  const [page, setPage] = useState(1);
  const [orderDrafts, setOrderDrafts] = useState<Record<string, string>>({});
  const [titleDrafts, setTitleDrafts] = useState<Record<string, string>>({});
  const [previewSet, setPreviewSet] = useState<StickerSetRow | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [maxItems, setMaxItems] = useState(0);

  const pageTitle = kind === "emoji" ? t("emoji.pageTitle") : t("route.stickers");
  const eyebrow =
    kind === "emoji"
      ? t("stickers.emojiEyebrow")
      : t("stickers.stickerEyebrow");

  async function load() {
    setBusy(true);
    setError("");
    try {
      const result = await api.stickerSets(kind);
      setSets(result.rows ?? []);
      setMaxItems(result.max_items ?? 0);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    void load();
  }, [kind]);

  const visible = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    if (!normalized) return sets;
    return sets.filter(
      (set) => String(set.ID).includes(normalized) || set.ShortName.toLowerCase().includes(normalized) || set.Title.toLowerCase().includes(normalized)
    );
  }, [sets, query]);

  useEffect(() => {
    setPage(1);
  }, [query, pageSize, kind]);

  const totalPages = pageSize === "all" ? 1 : Math.max(1, Math.ceil(visible.length / pageSize));
  const currentPage = Math.min(page, totalPages);
  const paged = useMemo(() => {
    if (pageSize === "all") return visible;
    const start = (currentPage - 1) * pageSize;
    return visible.slice(start, start + pageSize);
  }, [visible, currentPage, pageSize]);
  const rangeStart = paged.length === 0 ? 0 : pageSize === "all" ? 1 : (currentPage - 1) * pageSize + 1;
  const rangeEnd = rangeStart === 0 ? 0 : rangeStart + paged.length - 1;

  const counts = useMemo(
    () => ({
      total: sets.length,
      official: sets.filter((set) => set.Official).length,
      archived: sets.filter((set) => set.Archived).length
    }),
    [sets]
  );

  return (
    <PageFrame
      title={pageTitle}
      eyebrow={eyebrow}
      actions={
        <>
          {kind === "emoji" && (
            <button className="btn" type="button" onClick={() => navigate("/emoji/documents")}>
              <Files size={15} /> {t("emoji.documentCatalog")}
            </button>
          )}
          <button className="btn" type="button" onClick={() => load()} disabled={busy}>
            <RefreshCw size={15} /> {t("common.refresh")}
          </button>
          <button className="btn primary" type="button" onClick={() => setCreateOpen(true)}>
            <Plus size={15} /> {kind === "emoji" ? t("stickers.createEmojiPack") : t("stickers.createStickerPack")}
          </button>
        </>
      }
    >
      {error && <Alert>{error}</Alert>}
      <div className="metric-row">
        <Metric label={t("stickers.totalSets")} value={String(counts.total)} />
        <Metric label={t("common.official")} value={String(counts.official)} tone="good" />
        <Metric label={t("common.archived")} value={String(counts.archived)} tone={counts.archived > 0 ? "warn" : "neutral"} />
      </div>
      <QueryPanel>
        <div className="toolbar">
          <label className="searchbox">
            <Search size={15} />
            <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("stickers.searchPlaceholder")} />
          </label>
          <label className="gift-page-size">
            <span>{t("common.perPage")}</span>
            <select
              value={String(pageSize)}
              onChange={(event) => setPageSize(event.target.value === "all" ? "all" : (Number(event.target.value) as StickerPageSize))}
            >
              <option value="10">10</option>
              <option value="20">20</option>
              <option value="50">50</option>
              <option value="100">100</option>
              <option value="all">{t("common.all")}</option>
            </select>
          </label>
          <span className="gift-list-summary">{t("common.showing", { visible: visible.length, total: sets.length })}</span>
        </div>
      </QueryPanel>
      <div className="table-wrap gift-table-wrap">
        <table className="data-table">
          <thead>
            <tr>
              <th>{t("common.logo")}</th>
              <th>ID</th>
              <th>{t("common.shortName")}</th>
              <th>{t("common.title")}</th>
              <th>{t("common.documents")}</th>
              <th>{t("common.official")}</th>
              <th>{t("common.status")}</th>
              <th>{t("common.sortOrder")}</th>
              <th>{t("common.actions")}</th>
            </tr>
          </thead>
          <tbody>
            {paged.map((set) => (
              <tr className={set.Archived ? "gift-row-disabled" : ""} key={set.ID}>
                <td>
                  {set.CoverDocumentID ? (
                    <StickerDocumentPreview documentID={set.CoverDocumentID} className="list-thumb" showError={false} />
                  ) : (
                    <div className="sticker-list-thumb-empty">
                      <ImageOff size={14} />
                    </div>
                  )}
                </td>
                <td className="mono">{set.ID}</td>
                <td className="mono">{set.ShortName || <span className="muted-cell">{t("common.none")}</span>}</td>
                <td>
                  <div className="sort-order-editor">
                    <input
                      className="small-input title-input"
                      value={titleDrafts[set.ID] ?? set.Title}
                      onChange={(event) => setTitleDrafts((prev) => ({ ...prev, [set.ID]: event.target.value }))}
                    />
                    <ActionButton
                      compact
                      tone="neutral"
                      label={t("common.save")}
                      path="/api/actions/rename-sticker-set"
                      payload={() => ({ set_id: set.ID, title: (titleDrafts[set.ID] ?? set.Title).trim() })}
                      onDone={() => void load()}
                    />
                  </div>
                </td>
                <td>{set.Count}</td>
                <td>{set.Official ? <Badge tone="good">{t("common.yes")}</Badge> : <Badge>{t("common.no")}</Badge>}</td>
                <td>{set.Archived ? <Badge tone="danger">{t("common.archived")}</Badge> : <Badge tone="good">{t("common.enabled")}</Badge>}</td>
                <td>
                  <div className="sort-order-editor">
                    <input
                      type="number"
                      className="small-input"
                      value={orderDrafts[set.ID] ?? String(set.SortOrder)}
                      onChange={(event) => setOrderDrafts((prev) => ({ ...prev, [set.ID]: event.target.value }))}
                    />
                    <ActionButton
                      compact
                      tone="neutral"
                      label={t("common.save")}
                      path="/api/actions/set-sticker-set-sort-order"
                      payload={() => ({ set_id: set.ID, sort_order: Number(orderDrafts[set.ID] ?? set.SortOrder) })}
                      onDone={() => void load()}
                    />
                  </div>
                </td>
                <td>
                  <div className="gift-table-actions">
                    <button className="btn compact-btn" type="button" onClick={() => setPreviewSet(set)}>
                      <Eye size={13} /> {t("common.view")}
                    </button>
                    <ActionButton
                      compact
                      tone="neutral"
                      label={set.Archived ? t("common.unarchive") : t("common.archive")}
                      path="/api/actions/set-sticker-set-archived"
                      payload={() => ({ set_id: set.ID, archived: !set.Archived })}
                      onDone={() => void load()}
                    />
                    <ActionButton
                      compact
                      tone="danger"
                      label={t("common.delete")}
                      path="/api/actions/delete-sticker-set"
                      payload={() => ({ set_id: set.ID })}
                      onDone={() => void load()}
                    />
                  </div>
                </td>
              </tr>
            ))}
            {paged.length === 0 && <EmptyRow colSpan={9} />}
          </tbody>
        </table>
      </div>
      {pageSize !== "all" && visible.length > 0 && (
        <div className="gift-pager">
          <span className="gift-pager-range">{t("common.showingRange", { start: rangeStart, end: rangeEnd, total: visible.length })}</span>
          <div className="gift-pager-controls">
            <button className="btn compact-btn" type="button" onClick={() => setPage((p) => Math.max(1, p - 1))} disabled={currentPage <= 1}>
              <ChevronLeft size={14} /> {t("common.previous")}
            </button>
            <span className="gift-pager-page">{t("common.pageOf", { current: currentPage, total: totalPages })}</span>
            <button className="btn compact-btn" type="button" onClick={() => setPage((p) => Math.min(totalPages, p + 1))} disabled={currentPage >= totalPages}>
              {t("common.next")} <ChevronRight size={14} />
            </button>
          </div>
        </div>
      )}
      {previewSet && <StickerSetPreviewModal set={previewSet} maxItems={maxItems} onClose={() => setPreviewSet(null)} />}
      {createOpen && <CreateStickerSetModal kind={kind} onClose={() => setCreateOpen(false)} onCreated={() => void load()} />}
    </PageFrame>
  );
}
