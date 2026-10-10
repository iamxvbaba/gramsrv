import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { api, errorMessage } from "../api";
import { useI18n } from "../i18n";
import type { GiftAttributesResponse } from "../types";
import { Alert, Badge, EmptyRow } from "../components/ui";

// Upgrade pools viewer: published models/patterns/backdrops of a catalog
// gift with their rarity. Read-only; publishing stays in the import flow.
export function GiftAttributesModal({ giftID, title, onClose }: {
  giftID: string;
  title: string;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const [data, setData] = useState<GiftAttributesResponse | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    api.giftAttributes(giftID)
      .then((res) => { if (!cancelled) { setData(res); setError(""); } })
      .catch((err) => { if (!cancelled) setError(errorMessage(err)); })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [giftID]);

  function poolTable(head: string, rows: { id: number; name: string; rarity: number }[]) {
    return (
      <div>
        <h4>{head} · {rows.length}</h4>
        <div className="table-wrap">
          <table className="data-table">
            <thead><tr><th>{t("gifts.poolsName")}</th><th>{t("gifts.poolsRarity")}</th><th>ID</th></tr></thead>
            <tbody>
              {rows.map((row) => (
                <tr key={row.id}><td>{row.name}</td><td>⭐ {row.rarity}‰</td><td className="mono">{row.id}</td></tr>
              ))}
              {rows.length === 0 && <EmptyRow colSpan={3} />}
            </tbody>
          </table>
        </div>
      </div>
    );
  }

  return createPortal(
    <div className="modal-backdrop" role="presentation" onClick={onClose}>
      <section className="modal command-modal" role="dialog" aria-modal="true" aria-label={t("gifts.poolsTitle")} onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <div><div className="eyebrow">{t("gifts.poolsEyebrow")}</div><h2>{title}</h2></div>
          <button className="icon-btn" type="button" onClick={onClose} aria-label={t("common.close")}>✕</button>
        </div>
        <div className="command-body">
          {error && <Alert>{error}</Alert>}
          {loading && <div>{t("common.loading")}</div>}
          {data && !loading && <>
            <div>
              <Badge tone={data.has_upgrade ? "good" : "neutral"}>
                {data.has_upgrade ? t("gifts.hasUpgrade") : t("gifts.noUpgrade")}
              </Badge>
            </div>
            {poolTable(t("collectibles.models"), data.models)}
            {poolTable(t("collectibles.patterns"), data.patterns)}
            {poolTable(t("collectibles.backdrops"), data.backdrops)}
          </>}
        </div>
      </section>
    </div>,
    document.body
  );
}
