import { Loader2, RefreshCw, Tag } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { SectionTabs } from "../components/SectionTabs";
import { Alert, Badge, EmptyRow, PageFrame } from "../components/ui";
import { useI18n } from "../i18n";
import { formatQuantity } from "../lib/format";
import type { Navigate } from "../routing";
import type { ItemPrice } from "../types";

type PriceDraft = {
  starsPrice: string;
  bid: string;
};

function draftFromPrice(price: ItemPrice): PriceDraft {
  return { starsPrice: String(price.stars_price), bid: String(price.bid) };
}

function draftValid(draft: PriceDraft): boolean {
  const stars = Number(draft.starsPrice);
  const bid = Number(draft.bid);
  return Number.isInteger(stars) && stars >= 0 && stars <= 1_000_000
    && Number.isInteger(bid) && bid >= 0;
}

function draftDirty(draft: PriceDraft, row: ItemPrice): boolean {
  return draft.starsPrice !== String(row.stars_price) || draft.bid !== String(row.bid);
}

// Shop pricing: the effective price of every catalog product (stored overrides
// merged over the bot's catalog defaults), the per-product kill switch, and the
// Stars rate a purchase is billed at. Every save runs the console's standard
// reason -> dry-run -> confirm flow through admin.Service.
export function PricesPage({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const [rows, setRows] = useState<ItemPrice[]>([]);
  const [packages, setPackages] = useState<number[]>([]);
  const [rate, setRate] = useState("");
  const [drafts, setDrafts] = useState<Record<string, PriceDraft>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function load() {
    setBusy(true);
    setError("");
    try {
      const resp = await api.itemPrices();
      setRows(resp.rows);
      setPackages(resp.star_packages ?? []);
      setRate(resp.stars_rate > 0 ? String(resp.stars_rate) : "");
      setDrafts(Object.fromEntries(resp.rows.map((row) => [row.product_code, draftFromPrice(row)])));
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => { void load(); }, []);

  function updateDraft(code: string, patch: Partial<PriceDraft>) {
    setDrafts((prev) => ({
      ...prev,
      [code]: { ...(prev[code] ?? draftFromPrice(rows.find((r) => r.product_code === code)!)), ...patch }
    }));
  }

  const rateValue = Number(rate);
  const rateValid = rate !== "" && Number.isInteger(rateValue) && rateValue > 0 && rateValue <= 1_000_000;
  const liveRate = Number.isInteger(rateValue) && rateValue > 0 ? rateValue : 0;

  return (
    <PageFrame
      title={t("prices.title")}
      eyebrow={t("prices.eyebrow")}
      actions={
        <button className="btn icon-text" type="button" onClick={() => void load()} disabled={busy}>
          <RefreshCw size={15} className={busy ? "spin" : undefined} /> {t("common.refresh")}
        </button>
      }
    >
      <SectionTabs sectionId="commerce" active="/prices" navigate={navigate} />
      {error && <Alert>{error}</Alert>}

      <section className="section-block">
        <h2>{t("prices.starsRate")}</h2>
        <div className="card-body">
          <div className="attr-block">
            <div className="result-line">
              <span>{t("prices.starsPerStar")}</span>
              <strong>
                <input
                  aria-label={t("prices.starsPerStar")}
                  type="number"
                  min="1"
                  max="1000000"
                  className="small-input"
                  value={rate}
                  placeholder="100"
                  onChange={(event) => setRate(event.target.value)}
                />
              </strong>
            </div>
          </div>
          <div className="gift-table-actions">
            <ActionButton
              compact
              tone="neutral"
              label={t("prices.saveRate")}
              path="/api/actions/item-price-set-rate"
              payload={() => ({ stars_rate: rateValue })}
              disabled={!rateValid}
              onDone={() => void load()}
            />
          </div>
          {packages.length > 0 && (
            <>
              <div className="prices-packages">
                <div className="prices-packages-head">{t("prices.packagesHint")}</div>
                <div className="prices-packages-list">
                  {packages.map((size) => (
                    <div key={size} className="prices-package">
                      <strong>{formatQuantity(String(size))}</strong>
                      <span>{t("prices.telegramStars")}</span>
                      <span className="muted">
                        {liveRate > 0
                          ? t("prices.packageCredits", { amount: formatQuantity(String(size * liveRate)) })
                          : t("prices.packageUnset")}
                      </span>
                    </div>
                  ))}
                </div>
              </div>
              <p className="muted prices-note">
                <Tag size={13} /> {t("prices.packagesNote")}
              </p>
            </>
          )}
        </div>
      </section>

      <section className="section-block">
        <h2>{t("prices.products")}</h2>
        <div className="table-wrap">
          <table className="data-table">
            <thead>
              <tr>
                <th>{t("prices.product")}</th>
                <th>{t("prices.priceStars")}</th>
                <th>{t("prices.bidTon")}</th>
                <th>{t("common.status")}</th>
                <th>{t("prices.changed")}</th>
                <th>{t("common.actions")}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => {
                const draft = drafts[row.product_code] ?? draftFromPrice(row);
                const valid = draftValid(draft);
                const dirty = draftDirty(draft, row);
                return (
                  <tr key={row.product_code}>
                    <td>{row.title || row.product_code}<div className="muted mono">{row.product_code}</div></td>
                    <td>
                      <input
                        aria-label={t("prices.priceStars")}
                        type="number"
                        min="0"
                        max="1000000"
                        className="small-input"
                        value={draft.starsPrice}
                        onChange={(event) => updateDraft(row.product_code, { starsPrice: event.target.value })}
                      />
                    </td>
                    <td>
                      <input
                        aria-label={t("prices.bidTon")}
                        type="number"
                        min="0"
                        className="small-input"
                        value={draft.bid}
                        onChange={(event) => updateDraft(row.product_code, { bid: event.target.value })}
                      />
                    </td>
                    <td>{row.enabled ? <Badge tone="good">{t("common.enabled")}</Badge> : <Badge tone="danger">{t("prices.hidden")}</Badge>}</td>
                    <td className="muted">
                      {row.updated_by
                        ? <>{row.updated_by}{row.updated_at ? ` · ${new Date(row.updated_at * 1000).toLocaleString()}` : ""}</>
                        : <span>{t("prices.catalogDefault")}</span>}
                    </td>
                    <td>
                      <div className="gift-table-actions">
                        <ActionButton
                          compact
                          tone="neutral"
                          label={t("common.save")}
                          path="/api/actions/item-price-update"
                          payload={() => ({
                            product_code: row.product_code,
                            stars_price: Number(draft.starsPrice),
                            bid: Number(draft.bid),
                            enabled: row.enabled
                          })}
                          disabled={!valid || !dirty}
                          onDone={() => void load()}
                        />
                        {/* The kill switch carries the stored price, not the
                            draft. It used to send Number(draft.starsPrice) and
                            Number(draft.bid), so an operator who typed a new
                            price and then hit Hide published the new price as
                            a side effect of hiding the product -- and a
                            half-typed value ("12" of "129") went out with it. */}
                        <ActionButton
                          compact
                          tone={row.enabled ? "danger" : "neutral"}
                          label={row.enabled ? t("prices.hide") : t("prices.show")}
                          path="/api/actions/item-price-update"
                          payload={() => ({
                            product_code: row.product_code,
                            stars_price: row.stars_price,
                            bid: row.bid,
                            enabled: !row.enabled
                          })}
                          onDone={() => void load()}
                        />
                      </div>
                    </td>
                  </tr>
                );
              })}
              {rows.length === 0 && !busy && <EmptyRow colSpan={6} />}
            </tbody>
          </table>
        </div>
        <p className="muted">{t("prices.hiddenNote")}</p>
      </section>
    </PageFrame>
  );
}