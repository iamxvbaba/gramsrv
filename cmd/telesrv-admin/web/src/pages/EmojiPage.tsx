import lottie from "lottie-web/build/player/lottie_light_canvas";
import { Check, ChevronRight, Copy, Loader2, RefreshCw, Search } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { api, errorMessage } from "../api";
import { Alert, Metric, PageFrame, QueryPanel } from "../components/ui";
import { useI18n } from "../i18n";
import type { EmojiListResponse, EmojiRow } from "../types";

function formatBytes(value: number): string {
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`;
  return `${(value / (1024 * 1024)).toFixed(1)} MB`;
}

function isAnimated(mime: string): boolean {
  const m = mime.toLowerCase();
  return m.includes("tgsticker") || m.includes("lottie") || m.includes("json");
}

function EmojiPreview({ row }: { row: EmojiRow }) {
  const host = useRef<HTMLDivElement>(null);
  const animation = useRef<ReturnType<typeof lottie.loadAnimation> | null>(null);
  const [failed, setFailed] = useState(!isAnimated(row.MimeType));

  useEffect(() => {
    if (!isAnimated(row.MimeType)) {
      setFailed(true);
      return;
    }
    let cancelled = false;
    api.emojiAnimation(row.DocumentID).then((data) => {
      if (cancelled || !host.current) return;
      animation.current?.destroy();
      animation.current = lottie.loadAnimation({
        container: host.current,
        renderer: "canvas",
        loop: true,
        autoplay: true,
        animationData: structuredClone(data)
      });
    }).catch(() => setFailed(true));
    return () => {
      cancelled = true;
      animation.current?.destroy();
      animation.current = null;
    };
  }, [row.DocumentID, row.MimeType]);

  if (failed) {
    return <div className="emoji-glyph">{row.Alt || "🙂"}</div>;
  }
  return <div className="emoji-anim" ref={host} />;
}

function EmojiCard({ row }: { row: EmojiRow }) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(row.DocumentID);
      setCopied(true);
      setTimeout(() => setCopied(false), 1200);
    } catch {
      // Clipboard is best-effort.
    }
  }

  return (
    <div className="emoji-card">
      <div className="emoji-preview"><EmojiPreview row={row} /></div>
      <div className="emoji-meta">
        <span className="emoji-alt">{row.Alt || "—"}</span>
        <button className="emoji-id" type="button" onClick={copy} title={t("emoji.copyID")}>
          <span className="mono">{row.DocumentID}</span>
          {copied ? <Check size={12} /> : <Copy size={12} />}
        </button>
        <span className="emoji-sub">{row.SetTitle || t("emoji.noSet")} · {formatBytes(row.Size)}</span>
      </div>
    </div>
  );
}

export function EmojiPage() {
  const { t } = useI18n();
  const [q, setQ] = useState("");
  const [data, setData] = useState<EmojiListResponse | null>(null);
  const [cursor, setCursor] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function load(next = false) {
    setBusy(true);
    setError("");
    const params = new URLSearchParams();
    if (q.trim()) {
      params.set("q", q.trim());
    } else if (next) {
      params.set("before_id", String(cursor));
    }
    try {
      const result = await api.emoji(params);
      setData(result);
      setCursor(result.next_before_id);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    void load(false);
  }, []);

  const rows = data?.rows ?? [];

  return (
    <PageFrame
      title={t("emoji.pageTitle")}
      eyebrow={data?.listing === false ? t("emoji.queryResults") : t("emoji.recent")}
      actions={
        <button className="btn" type="button" onClick={() => load(false)} disabled={busy}>
          <RefreshCw size={15} /> {t("common.refresh")}
        </button>
      }
    >
      {error && <Alert>{error}</Alert>}
      <div className="metric-row">
        <Metric label={t("emoji.currentPage")} value={String(rows.length)} />
      </div>
      <QueryPanel>
        <form className="toolbar" onSubmit={(event) => { event.preventDefault(); void load(false); }}>
          <label className="searchbox">
            <Search size={15} />
            <input value={q} onChange={(event) => setQ(event.target.value)} placeholder={t("emoji.searchPlaceholder")} />
          </label>
          <button className="btn primary icon-text" type="submit" disabled={busy}>
            {busy ? <Loader2 size={15} className="spin" /> : <Search size={15} />} {t("common.search")}
          </button>
          {data?.listing && data.has_more && (
            <button className="btn icon-text" type="button" onClick={() => load(true)} disabled={busy}>
              <ChevronRight size={15} /> {t("messages.nextPage")}
            </button>
          )}
        </form>
      </QueryPanel>
      <p className="about-text">{t("emoji.hint")}</p>
      {rows.length === 0 ? (
        <div className="empty-panel">{t("common.noResults")}</div>
      ) : (
        <div className="emoji-grid">
          {rows.map((row) => <EmojiCard key={row.DocumentID} row={row} />)}
        </div>
      )}
    </PageFrame>
  );
}
