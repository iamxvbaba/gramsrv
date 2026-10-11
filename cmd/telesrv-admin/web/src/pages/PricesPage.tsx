import { Loader2, RefreshCw, Tag } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { Alert, Badge, EmptyRow, PageFrame } from "../components/ui";
import type { ItemPrice } from "../types";

type PriceDraft = {
  starsPrice: string;
  bid: string;
  enabled: boolean;
};

function draftFromPrice(price: ItemPrice): PriceDraft {
  return {
    starsPrice: String(price.stars_price),
    bid: String(price.bid),
    enabled: price.enabled
  };
}

function draftValid(draft: PriceDraft): boolean {
  const stars = Number(draft.starsPrice);
  const bid = Number(draft.bid);
  return Number.isInteger(stars) && stars >= 0 && stars <= 1_000_000
    && Number.isInteger(bid) && bid >= 0;
}

// Shop pricing: the effective price of every catalog product (stored overrides
// merged over the defaults), the per-product kill switch, and the stars rate
// the bot multiplies Telegram Star purchases by. Every save runs the console's
// standard reason -> dry-run -> confirm flow through admin.Service.
export function PricesPage() {
  const [rows, setRows] = useState<ItemPrice[]>([]);
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

  return (
    <PageFrame
      title={"Shop prices"}
      eyebrow={"Product prices, availability and the Stars rate the bot bills purchases at"}
      actions={
        <button className="btn" type="button" onClick={() => void load()} disabled={busy}>
          {busy ? <Loader2 size={15} className="spin" /> : <RefreshCw size={15} />} {"Refresh"}
        </button>
      }
    >
      {error && <Alert>{error}</Alert>}

      <section className="section-block">
        <h2>{"Stars rate"}</h2>
        <div className="card-body">
          <div className="attr-block">
            <div className="result-line">
              <span>{"FG Stars per 1 Telegram Star"}</span>
              <strong>
                <input
                  type="number"
                  min="1"
                  max="1000000"
                  className="small-input"
                  value={rate}
                  placeholder={"e.g. 100"}
                  onChange={(event) => setRate(event.target.value)}
                />
              </strong>
            </div>
          </div>
          <div className="gift-table-actions">
            <ActionButton
              compact
              tone="neutral"
              label={"Save rate"}
              path="/api/actions/item-price-set-rate"
              payload={() => ({ stars_rate: rateValue })}
              disabled={!rateValid}
              onDone={() => void load()}
            />
          </div>
        </div>
      </section>

      <section className="section-block">
        <h2>{"Products"}</h2>
        <div className="table-wrap">
          <table className="data-table">
            <thead>
              <tr>
                <th>{"Product"}</th>
                <th>{"Price (stars)"}</th>
                <th>{"Bid (TON)"}</th>
                <th>{"State"}</th>
                <th>{"Changed"}</th>
                <th>{"Actions"}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => {
                const draft = drafts[row.product_code] ?? draftFromPrice(row);
                const valid = draftValid(draft);
                const dirty = draft.starsPrice !== String(row.stars_price)
                  || draft.bid !== String(row.bid)
                  || draft.enabled !== row.enabled;
                return (
                  <tr key={row.product_code}>
                    <td>{row.title || row.product_code}<div className="muted mono">{row.product_code}</div></td>
                    <td>
                      <input
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
                        type="number"
                        min="0"
                        className="small-input"
                        value={draft.bid}
                        onChange={(event) => updateDraft(row.product_code, { bid: event.target.value })}
                      />
                    </td>
                    <td>{row.enabled ? <Badge tone="good">{"Enabled"}</Badge> : <Badge tone="danger">{"Hidden"}</Badge>}</td>
                    <td className="muted">
                      {row.updated_by
                        ? <>{row.updated_by}{row.updated_at ? ` · ${new Date(row.updated_at * 1000).toLocaleString()}` : ""}</>
                        : <span>{"catalog default"}</span>}
                    </td>
                    <td>
                      <div className="gift-table-actions">
                        <ActionButton
                          compact
                          tone="neutral"
                          label={"Save"}
                          path="/api/actions/item-price-update"
                          payload={() => ({
                            product_code: row.product_code,
                            stars_price: Number(draft.starsPrice),
                            bid: Number(draft.bid),
                            enabled: draft.enabled
                          })}
                          disabled={!valid || !dirty}
                          onDone={() => void load()}
                        />
                        <ActionButton
                          compact
                          tone={row.enabled ? "danger" : "neutral"}
                          label={row.enabled ? "Hide" : "Show"}
                          path="/api/actions/item-price-update"
                          payload={() => ({
                            product_code: row.product_code,
                            stars_price: Number(draft.starsPrice),
                            bid: Number(draft.bid),
                            enabled: !row.enabled
                          })}
                          disabled={!valid}
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
        <p className="muted">
          <Tag size={13} /> {"Hidden products disappear from the bot's shop within a minute. The rate applies to FlashGram Stars purchases."}
        </p>
      </section>
    </PageFrame>
  );
}
