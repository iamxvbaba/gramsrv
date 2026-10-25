import { Crown, Flame, Phone, RefreshCw, Save, Search, ShieldCheck, Undo2 } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { UserPicker } from "../components/EntityPicker";
import { SectionTabs } from "../components/SectionTabs";
import { Alert, Badge, EmptyRow, PageFrame, SectionHead } from "../components/ui";
import { useI18n } from "../i18n";
import { formatCurrency, formatCurrencyAmount, toSmallestUnits } from "../lib/format";
import type { Navigate } from "../routing";
import type { AccountRow, CollectiblePhoneRow, CollectiblePhoneTier } from "../types";

type Status = "" | "vault" | "owned" | "burned";

function shownPhone(value: string) { return value.startsWith("+") ? value : `+${value}`; }

export function CollectiblePhonesPage({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const [rows, setRows] = useState<CollectiblePhoneRow[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [q, setQ] = useState("");
  const [status, setStatus] = useState<Status>("");
  const [tierFilter, setTierFilter] = useState<"" | CollectiblePhoneTier>("");
  const [phone, setPhone] = useState("+888");
  const [tier, setTier] = useState<CollectiblePhoneTier>("exclusive");
  const [owner, setOwner] = useState<AccountRow | null>(null);
  const [amount, setAmount] = useState("");
  const [cryptoAmount, setCryptoAmount] = useState("");
  const [url, setURL] = useState("");
  const [selected, setSelected] = useState<CollectiblePhoneRow | null>(null);
  const [recipient, setRecipient] = useState<AccountRow | null>(null);
  const [editAmount, setEditAmount] = useState("");
  const [editCryptoAmount, setEditCryptoAmount] = useState("");

  async function load() {
    setBusy(true); setError("");
    try {
      const params = new URLSearchParams({ limit: "200" });
      if (q.trim()) params.set("q", q.trim());
      if (status) params.set("status", status);
      if (tierFilter) params.set("tier", tierFilter);
      const result = await api.collectiblePhones(params);
      setRows(result.assets ?? []);
    } catch (err) { setError(errorMessage(err)); } finally { setBusy(false); }
  }

  useEffect(() => { void load(); }, []);
  const counts = useMemo(() => ({
    standard: rows.filter(x => x.tier === "standard").length,
    exclusive: rows.filter(x => x.tier === "exclusive").length,
    owned: rows.filter(x => x.status === "owned").length
  }), [rows]);

  const minorAmount = toSmallestUnits(amount, "USD");
  const minorCryptoAmount = toSmallestUnits(cryptoAmount, "TON");
  const priceInvalid = minorAmount === null || minorAmount === "0" || minorCryptoAmount === null || minorCryptoAmount === "0";
  const editMinorAmount = toSmallestUnits(editAmount, "USD");
  const editMinorCryptoAmount = toSmallestUnits(editCryptoAmount, "TON");
  const editPriceInvalid = editMinorAmount === null || editMinorAmount === "0" || editMinorCryptoAmount === null || editMinorCryptoAmount === "0";

  const mintPayload = () => ({
    phone, tier, owner_user_id: owner ? String(owner.ID) : undefined,
    currency: "USD", amount: minorAmount ?? "0", crypto_currency: "TON", crypto_amount: minorCryptoAmount ?? "0",
    url: url.trim() || undefined
  });

  function manage(row: CollectiblePhoneRow) {
    setSelected(row);
    setRecipient(null);
    setEditAmount(row.currency === "USD" ? formatCurrencyAmount(row.amount, "USD").replace(/ /g, "") : "");
    setEditCryptoAmount(row.crypto_currency === "TON" ? formatCurrencyAmount(row.crypto_amount, "TON").replace(/ /g, "") : "");
  }

  return <PageFrame title={t("phones.pageTitle")} eyebrow={t("phones.eyebrow")} actions={
    <button className="btn icon-text" onClick={load} disabled={busy}><RefreshCw size={15} className={busy ? "spin" : ""}/>{t("common.refresh")}</button>
  }>
    <SectionTabs sectionId="nft" active="/collectible-phones" navigate={navigate} />
    {error && <Alert>{error}</Alert>}
    <div className="metric-row compact-metrics">
      <div className="metric"><span>{t("phones.standard")}</span><strong>{counts.standard}</strong></div>
      <div className="metric"><span>{t("phones.exclusive")}</span><strong>{counts.exclusive}</strong></div>
      <div className="metric"><span>{t("usernames.statusOwned")}</span><strong>{counts.owned}</strong></div>
    </div>

    <section className="section-block collectible-phone-compose">
      <SectionHead title={t("phones.mintTitle")} text={t("phones.mintHint")} />
      <div className="compact-form-grid">
        <label className="duration-field"><span>{t("phones.number")}</span><input value={phone} onChange={e=>setPhone(e.target.value)} placeholder="+8881111"/></label>
        <label className="duration-field"><span>{t("phones.class")}</span><select value={tier} onChange={e=>setTier(e.target.value as CollectiblePhoneTier)}><option value="standard">{t("phones.standard")}</option><option value="exclusive">{t("phones.exclusive")}</option></select></label>
        <label className="duration-field"><span>{t("phones.fiatAmount")}</span><input value={amount} onChange={e=>setAmount(e.target.value)} inputMode="decimal" placeholder="5593.00"/></label>
        <label className="duration-field"><span>{t("phones.tonAmount")}</span><input value={cryptoAmount} onChange={e=>setCryptoAmount(e.target.value)} inputMode="decimal" placeholder="3753"/></label>
        <label className="duration-field compact-span-2"><span>{t("usernames.url")}</span><input value={url} onChange={e=>setURL(e.target.value)} placeholder="https://fragment.com/number/8881111"/></label>
      </div>
      <p className="bot-create-note">{t("phones.pricePreview", { crypto: formatCurrency(minorCryptoAmount ?? "0", "TON"), fiat: formatCurrency(minorAmount ?? "0", "USD") })}</p>
      {priceInvalid && (amount !== "" || cryptoAmount !== "") && <Alert>{t("phones.priceInvalid")}</Alert>}
      <div className="compact-owner-row"><UserPicker label={t("phones.ownerOptional")} value={owner} onChange={setOwner}/><ActionButton disabled={priceInvalid} label={t("phones.mint")} icon={<Phone size={15}/>} tone="neutral" path="/api/actions/mint-collectible-phone" payload={mintPayload} onDone={load}/></div>
    </section>

    <div className="toolbar collectible-phone-filter">
      <label className="searchbox"><Search size={15}/><input value={q} onChange={e=>setQ(e.target.value)} placeholder={t("phones.search")}/></label>
      <select value={tierFilter} onChange={e=>setTierFilter(e.target.value as ""|CollectiblePhoneTier)}><option value="">{t("phones.allClasses")}</option><option value="standard">{t("phones.standard")}</option><option value="exclusive">{t("phones.exclusive")}</option></select>
      <select value={status} onChange={e=>setStatus(e.target.value as Status)}><option value="">{t("usernames.statusAll")}</option><option value="vault">{t("usernames.statusVault")}</option><option value="owned">{t("usernames.statusOwned")}</option><option value="burned">{t("usernames.statusBurned")}</option></select>
      <button className="btn primary" onClick={load}>{t("common.search")}</button>
    </div>

    <div className="table-wrap"><table className="data-table"><thead><tr><th>{t("phones.number")}</th><th>{t("phones.class")}</th><th>{t("common.status")}</th><th>{t("common.owner")}</th><th>{t("usernames.price")}</th><th>{t("usernames.transfers")}</th><th/></tr></thead><tbody>
      {rows.map(row => <tr key={row.id} className={selected?.id===row.id?"selected-row":""}><td className="mono"><strong>{shownPhone(row.phone)}</strong></td><td><Badge tone={row.tier==="exclusive"?"warn":"neutral"}>{row.tier==="exclusive"?<Crown size={13}/>:<ShieldCheck size={13}/>} {t(`phones.${row.tier}`)}</Badge></td><td><Badge tone={row.status==="owned"?"good":row.status==="burned"?"danger":"neutral"}>{t(`usernames.status${row.status[0].toUpperCase()}${row.status.slice(1)}`)}</Badge></td><td className="mono">{row.owner_user_id!=="0"?row.owner_user_id:"—"}</td><td className="mono">{phonePriceLabel(row)}</td><td>{row.transfer_count}</td><td><button className="row-link" onClick={()=>manage(row)}>{t("phones.manage")}</button></td></tr>)}
      {!rows.length && <EmptyRow colSpan={7}/>}</tbody></table></div>

    {selected && selected.status!=="burned" && <section className="section-block phone-action-strip">
      <div className="phone-action-summary"><strong>{shownPhone(selected.phone)}</strong><span>{t("phones.manageHint")}</span></div>
      <div className="phone-price-editor">
        <label className="duration-field"><span>{t("phones.fiatAmount")}</span><input value={editAmount} onChange={e=>setEditAmount(e.target.value)} inputMode="decimal" placeholder="5593.00"/></label>
        <label className="duration-field"><span>{t("phones.tonAmount")}</span><input value={editCryptoAmount} onChange={e=>setEditCryptoAmount(e.target.value)} inputMode="decimal" placeholder="3753"/></label>
        <ActionButton disabled={editPriceInvalid} label={t("phones.updatePrice")} icon={<Save size={14}/>} tone="neutral" path="/api/actions/update-collectible-phone-price" payload={()=>({phone:selected.phone,currency:"USD",amount:editMinorAmount??"0",crypto_currency:"TON",crypto_amount:editMinorCryptoAmount??"0"})} onDone={()=>{setSelected(null);void load()}}/>
      </div>
      <div className="phone-transfer-editor"><UserPicker label={t("usernames.recipientUser")} value={recipient} onChange={setRecipient}/><div className="toolbar">
        <ActionButton label={t("usernames.transfer")} icon={<Phone size={14}/>} tone="neutral" path="/api/actions/transfer-collectible-phone" payload={()=>({phone:selected.phone,to_user_id:recipient?String(recipient.ID):undefined})} onDone={load}/>
        {selected.status==="owned" && <ActionButton label={t("usernames.revoke")} icon={<Undo2 size={14}/>} tone="warn" path="/api/actions/revoke-collectible-phone" payload={()=>({phone:selected.phone,burn:false})} onDone={load}/>}
        <ActionButton label={t("usernames.burn")} icon={<Flame size={14}/>} tone="danger" path="/api/actions/revoke-collectible-phone" payload={()=>({phone:selected.phone,burn:true})} onDone={load}/>
      </div></div>
    </section>}
  </PageFrame>;
}

// Telegram Desktop renders crypto first, then the fiat amount in parentheses.
export function phonePriceLabel(row: CollectiblePhoneRow): string {
  const fiat = formatCurrency(row.amount, row.currency);
  if (row.crypto_currency === "TON" && row.crypto_amount && row.crypto_amount !== "0") {
    return `${formatCurrency(row.crypto_amount, row.crypto_currency)} (${fiat})`;
  }
  return fiat;
}
