import { AtSign, ChevronDown, ChevronRight, Flame, Loader2, Plus, RefreshCw, Search, Vault } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { ChannelPicker, UserPicker } from "../components/EntityPicker";
import { Alert, Badge, EmptyRow, Metric, PageFrame, QueryPanel, SectionHead } from "../components/ui";
import { useI18n } from "../i18n";
import { displayUsername, formatDate, formatQuantity } from "../lib/format";
import type { Navigate } from "../routing";
import type {
  AccountRow,
  ChannelRow,
  CollectibleCurrency,
  CollectibleUsernameRow,
  CollectibleUsernameStatus
} from "../types";

type StatusFilter = "all" | CollectibleUsernameStatus;
type OwnerKind = "vault" | "user" | "channel";

export function CollectibleUsernamesPage({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const [status, setStatus] = useState<StatusFilter>("all");
  const [q, setQ] = useState("");
  const [limit, setLimit] = useState("50");
  const [rows, setRows] = useState<CollectibleUsernameRow[]>([]);
  const [hasMore, setHasMore] = useState(false);
  const [cursor, setCursor] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  // Mint form state.
  const [ownerKind, setOwnerKind] = useState<OwnerKind>("vault");
  const [owner, setOwner] = useState<AccountRow | null>(null);
  const [ownerChannel, setOwnerChannel] = useState<ChannelRow | null>(null);
  const [mintUsername, setMintUsername] = useState("");
  const [currency, setCurrency] = useState<CollectibleCurrency>("XTR");
  const [amount, setAmount] = useState("");
  const [cryptoCurrency, setCryptoCurrency] = useState("");
  const [cryptoAmount, setCryptoAmount] = useState("");
  const [url, setUrl] = useState("");
  const [purchaseDate, setPurchaseDate] = useState("");

  async function load(next = false) {
    setBusy(true);
    setError("");
    const params = new URLSearchParams({ limit });
    if (status !== "all") params.set("status", status);
    if (q.trim()) params.set("q", q.trim().replace(/^@/, ""));
    if (next && cursor) params.set("before_id", cursor);
    try {
      const result = await api.collectibleUsernames(params);
      const page = result.rows ?? [];
      setRows((current) => (next ? [...current, ...page] : page));
      setCursor(result.next_before_id ?? "");
      setHasMore(Boolean(result.has_more));
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    void load(false);
  }, []);

  const vaultCount = rows.filter((row) => row.Status === "vault").length;
  const ownedCount = rows.filter((row) => row.Status === "owned").length;
  const burnedCount = rows.filter((row) => row.Status === "burned").length;

  // int64 request fields are sent as decimal strings (the backend tags them
  // `,string`); purchase_date is Unix seconds. Optional owner keys are omitted
  // entirely rather than sent empty, because `,string,omitempty` cannot decode "".
  function mintPayload(): Record<string, unknown> {
    const payload: Record<string, unknown> = {
      username: mintUsername.trim().replace(/^@/, ""),
      currency,
      amount: amount.trim() || "0"
    };
    if (ownerKind === "user" && owner) payload.owner_user_id = String(owner.ID);
    if (ownerKind === "channel" && ownerChannel) payload.owner_channel_id = String(ownerChannel.ID);
    // The backend accepts either no crypto leg at all, or TON with a positive
    // nanoton amount — never a currency without an amount.
    if (cryptoCurrency) {
      payload.crypto_currency = cryptoCurrency;
      payload.crypto_amount = cryptoAmount.trim() || "0";
    }
    if (url.trim()) payload.url = url.trim();
    if (purchaseDate) {
      const parsed = Date.parse(`${purchaseDate}T00:00:00Z`);
      if (Number.isFinite(parsed)) payload.purchase_date = Math.floor(parsed / 1000);
    }
    return payload;
  }

  return (
    <PageFrame
      title={t("usernames.pageTitle")}
      eyebrow={t("usernames.eyebrow")}
      actions={
        <button className="btn icon-text" type="button" onClick={() => load(false)} disabled={busy}>
          <RefreshCw size={15} className={busy ? "spin" : ""} /> {t("common.refresh")}
        </button>
      }
    >
      {error && <Alert>{error}</Alert>}
      <div className="metric-row">
        <Metric label={t("usernames.metricLoaded")} value={String(rows.length)} />
        <Metric label={t("usernames.metricVault")} value={String(vaultCount)} />
        <Metric label={t("usernames.metricOwned")} value={String(ownedCount)} tone="good" />
        <Metric label={t("usernames.metricBurned")} value={String(burnedCount)} tone={burnedCount ? "danger" : "neutral"} />
      </div>

      <section className="section-block">
        <SectionHead title={t("usernames.mintTitle")} text={t("usernames.mintHint")} />
        <div className="toolbar" role="group" aria-label={t("usernames.ownerKind")}>
          <button type="button" className={`btn ${ownerKind === "vault" ? "primary" : ""}`} onClick={() => setOwnerKind("vault")}>
            <Vault size={15} /> {t("usernames.ownerVault")}
          </button>
          <button type="button" className={`btn ${ownerKind === "user" ? "primary" : ""}`} onClick={() => setOwnerKind("user")}>
            {t("usernames.ownerUser")}
          </button>
          <button type="button" className={`btn ${ownerKind === "channel" ? "primary" : ""}`} onClick={() => setOwnerKind("channel")}>
            {t("usernames.ownerChannel")}
          </button>
        </div>
        {ownerKind === "user" && <UserPicker label={t("usernames.ownerUser")} value={owner} onChange={setOwner} />}
        {ownerKind === "channel" && <ChannelPicker label={t("usernames.ownerChannel")} value={ownerChannel} onChange={setOwnerChannel} />}
        <div className="bot-create-fields">
          <label className="duration-field">
            <span>{t("common.username")}</span>
            <input value={mintUsername} onChange={(event) => setMintUsername(event.target.value)} placeholder="durov" />
          </label>
          <label className="duration-field">
            <span>{t("usernames.currency")}</span>
            <select value={currency} onChange={(event) => setCurrency(event.target.value as CollectibleCurrency)}>
              <option value="XTR">XTR</option>
              <option value="TON">TON</option>
              <option value="USD">USD</option>
            </select>
          </label>
          <label className="duration-field">
            <span>{t("usernames.amount")}</span>
            <input value={amount} onChange={(event) => setAmount(event.target.value)} inputMode="numeric" placeholder="1000" />
          </label>
          <label className="duration-field">
            <span>{t("usernames.cryptoCurrency")}</span>
            <select value={cryptoCurrency} onChange={(event) => setCryptoCurrency(event.target.value)}>
              <option value="">{t("usernames.cryptoNone")}</option>
              <option value="TON">TON</option>
            </select>
          </label>
          {cryptoCurrency !== "" && (
            <label className="duration-field">
              <span>{t("usernames.cryptoAmount")}</span>
              <input value={cryptoAmount} onChange={(event) => setCryptoAmount(event.target.value)} inputMode="numeric" placeholder="0" />
            </label>
          )}
          <label className="duration-field">
            <span>{t("usernames.url")}</span>
            <input value={url} onChange={(event) => setUrl(event.target.value)} placeholder="https://fragment.com/username/durov" />
          </label>
          <label className="duration-field">
            <span>{t("usernames.purchaseDate")}</span>
            <input value={purchaseDate} onChange={(event) => setPurchaseDate(event.target.value)} type="date" />
          </label>
        </div>
        <div className="bot-create-actions">
          <span className="bot-create-note">{t("usernames.mintNote")}</span>
          <ActionButton
            label={t("usernames.mint")}
            icon={<Plus size={15} />}
            tone="neutral"
            path="/api/actions/mint-collectible-username"
            payload={mintPayload}
            onDone={() => load(false)}
          />
        </div>
      </section>

      <QueryPanel>
        <form className="toolbar" onSubmit={(event) => { event.preventDefault(); void load(false); }}>
          <label className="searchbox">
            <Search size={15} />
            <input value={q} onChange={(event) => setQ(event.target.value)} placeholder={t("usernames.searchPlaceholder")} />
          </label>
          <label className="field-inline">
            <span>{t("common.status")}</span>
            <select value={status} onChange={(event) => setStatus(event.target.value as StatusFilter)}>
              <option value="all">{t("usernames.statusAll")}</option>
              <option value="vault">{t("usernames.statusVault")}</option>
              <option value="owned">{t("usernames.statusOwned")}</option>
              <option value="burned">{t("usernames.statusBurned")}</option>
            </select>
          </label>
          <label className="field-inline">
            <span>{t("common.limit")}</span>
            <input className="small-input" value={limit} onChange={(event) => setLimit(event.target.value)} type="number" min="1" max="200" />
          </label>
          <button className="btn primary icon-text" type="submit" disabled={busy}>
            {busy ? <Loader2 size={15} className="spin" /> : <Search size={15} />} {t("common.search")}
          </button>
        </form>
      </QueryPanel>

      <div className="table-wrap">
        <table className="data-table">
          <thead>
            <tr>
              <th>{t("common.username")}</th>
              <th>{t("common.status")}</th>
              <th>{t("common.owner")}</th>
              <th>{t("usernames.price")}</th>
              <th>{t("usernames.purchaseDate")}</th>
              <th>{t("usernames.transfers")}</th>
              <th>{t("common.updatedAt")}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.ID}>
                <td><strong>{displayUsername(row.Username)}</strong></td>
                <td><UsernameStatus status={row.Status} /></td>
                <td>{ownerLabel(row, t("usernames.statusVault"))}</td>
                <td className="mono">{priceLabel(row)}</td>
                <td>{formatDate(row.PurchaseDate) || "-"}</td>
                <td className="mono">{row.TransferCount}</td>
                <td>{formatDate(row.UpdatedAt) || "-"}</td>
                <td>
                  <button className="row-link" type="button" onClick={() => navigate(`/collectible-usernames/${row.ID}`)}>
                    <AtSign size={14} /> {t("common.detail")} <ChevronRight size={14} />
                  </button>
                </td>
              </tr>
            ))}
            {rows.length === 0 && <EmptyRow colSpan={8} />}
          </tbody>
        </table>
      </div>
      {hasMore && (
        <div className="toolbar">
          <button className="btn icon-text" type="button" onClick={() => load(true)} disabled={busy}>
            {busy ? <Loader2 size={15} className="spin" /> : <ChevronDown size={15} />} {t("common.loadMore")}
          </button>
        </div>
      )}
    </PageFrame>
  );
}

export function UsernameStatus({ status }: { status: CollectibleUsernameStatus }) {
  const { t } = useI18n();
  if (status === "owned") return <Badge tone="good">{t("usernames.statusOwned")}</Badge>;
  if (status === "burned") return <Badge tone="danger"><Flame size={12} /> {t("usernames.statusBurned")}</Badge>;
  return <Badge><Vault size={12} /> {t("usernames.statusVault")}</Badge>;
}

export function ownerLabel(row: CollectibleUsernameRow, vaultLabel: string): string {
  if (!row.OwnerPeerType || row.OwnerPeerID === "" || row.OwnerPeerID === "0") return vaultLabel;
  const name = displayUsername(row.OwnerUsername) || row.OwnerName || row.OwnerPeerID;
  return `${name} · ${row.OwnerPeerType}:${row.OwnerPeerID}`;
}

export function priceLabel(row: CollectibleUsernameRow): string {
  const base = `${formatQuantity(row.Amount)} ${row.Currency}`.trim();
  if (row.CryptoCurrency && row.CryptoAmount && row.CryptoAmount !== "0") {
    return `${base} (${formatQuantity(row.CryptoAmount)} ${row.CryptoCurrency})`;
  }
  return base;
}
