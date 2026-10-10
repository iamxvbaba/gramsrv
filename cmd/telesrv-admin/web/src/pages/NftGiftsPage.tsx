import { Gem, RefreshCw, Search, Wallet } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { SectionTabs, nftTabs } from "../components/SectionTabs";
import { Alert, Badge, EmptyRow, Metric, PageFrame, QueryPanel } from "../components/ui";
import { useI18n } from "../i18n";
import { displayUsername, formatDate } from "../lib/format";
import type { Navigate } from "../routing";
import type { UniqueStarGiftRow } from "../types";
import { NftGiftWalletModal } from "./NftGiftWalletModal";

const pageSize = "50";

// NFT Gifts is the third tab of the NFT Items section: it lists minted,
// numbered collectible gift instances that accounts actually hold -- the
// gift-side counterpart to the NFT usernames and +888 numbers tabs. The catalog
// (what can be bought) lives under Star Gifts; this is what was minted.
export function NftGiftsPage({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const [rows, setRows] = useState<UniqueStarGiftRow[]>([]);
  const [hasMore, setHasMore] = useState(false);
  const [cursor, setCursor] = useState("");
  const [q, setQ] = useState("");
  const [giftID, setGiftID] = useState("");
  const [ownerUserID, setOwnerUserID] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [walletRef, setWalletRef] = useState<string | null>(null);
  const [walletGift, setWalletGift] = useState<UniqueStarGiftRow | null>(null);

  async function load(reset: boolean) {
    setBusy(true);
    setError("");
    try {
      const params = new URLSearchParams();
      if (q.trim()) params.set("q", q.trim());
      if (giftID.trim()) params.set("gift_id", giftID.trim());
      if (ownerUserID.trim()) params.set("owner_user_id", ownerUserID.trim());
      params.set("limit", pageSize);
      if (!reset && cursor) params.set("before_id", cursor);
      const response = await api.nftGifts(params);
      const next = response.rows ?? [];
      setRows((current) => (reset ? next : [...current, ...next]));
      setHasMore(response.has_more);
      setCursor(response.next_before_id);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => { void load(true); }, []);

  const burnedCount = rows.filter((row) => row.Burned).length;
  const craftedCount = rows.filter((row) => row.Crafted).length;

  function ownerLabel(row: UniqueStarGiftRow) {
    if (row.OwnerAddress) return row.WalletName || row.OwnerAddress;
    const handle = displayUsername(row.OwnerUsername);
    if (row.OwnerName && handle) return `${row.OwnerName} (${handle})`;
    if (row.OwnerName) return row.OwnerName;
    if (handle) return handle;
    return `${row.OwnerPeerType} ${row.OwnerPeerID}`;
  }

  function hostLabel(row: UniqueStarGiftRow) {
    return row.HostPeerID !== "0" ? `${t("nft.hostedBy")} ${row.HostPeerType}/${row.HostPeerID}` : "";
  }

  return (
    <PageFrame
      title={t("nft.gifts")}
      eyebrow={t("nft.eyebrow")}
      actions={
        <>
          <button className="btn icon-text" type="button" onClick={() => { setWalletRef(q.trim()); setWalletGift(null); }}>
            <Wallet size={15} /> {t("nft.walletOpen")}
          </button>
          <button className="btn icon-text" type="button" onClick={() => void load(true)} disabled={busy}>
            <RefreshCw size={15} className={busy ? "spin" : ""} /> {t("common.refresh")}
          </button>
        </>
      }
    >
      <SectionTabs tabs={nftTabs} active="/nft-gifts" navigate={navigate} />
      {error && <Alert>{error}</Alert>}
      <div className="metric-row">
        <Metric label={t("nft.metricLoaded")} value={String(rows.length)} />
        <Metric label={t("nft.metricBurned")} value={String(burnedCount)} tone={burnedCount ? "danger" : "neutral"} />
        <Metric label={t("nft.metricCrafted")} value={String(craftedCount)} tone="good" />
      </div>

      <QueryPanel>
        <form
          className="bot-create-fields"
          onSubmit={(event) => { event.preventDefault(); void load(true); }}
        >
          <label className="duration-field"><span>{t("common.search")}</span><input value={q} onChange={(event) => setQ(event.target.value)} placeholder={t("nft.searchPlaceholder")} /></label>
          <label className="duration-field"><span>{t("nft.giftID")}</span><input value={giftID} onChange={(event) => setGiftID(event.target.value)} placeholder="9000000000000001" /></label>
          <label className="duration-field"><span>{t("nft.ownerUserID")}</span><input value={ownerUserID} onChange={(event) => setOwnerUserID(event.target.value)} placeholder="0" /></label>
          <button className="btn primary" type="submit" disabled={busy}>{t("common.search")}</button>
        </form>
      </QueryPanel>

      <div className="table-wrap">
        <table className="data-table">
          <thead><tr>
            <th>{t("common.id")}</th>
            <th>{t("nft.gift")}</th>
            <th>{t("nft.number")}</th>
            <th>{t("nft.owner")}</th>
            <th>{t("nft.wallet")}</th>
            <th>{t("nft.status")}</th>
            <th>{t("nft.created")}</th>
            <th />
          </tr></thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.ID}>
                <td className="mono">{row.ID}</td>
                <td>
                  <div className="gift-name"><Gem size={14} /> {row.Title || row.Slug}</div>
                  {row.Title && <div className="mono">{row.Slug}</div>}
                  {row.GiftAddress && <div className="mono">{row.GiftAddress}</div>}
                </td>
                <td>#{row.Num}</td>
                <td>
                  <div>{ownerLabel(row)}</div>
                  {row.OwnerAddress && <span className="muted">{t("nft.ownerTon")}</span>}
                  {hostLabel(row) && <div className="muted">{hostLabel(row)}</div>}
                </td>
                <td>
                  {row.OwnerAddress
                    ? <div className="mono" title={row.OwnerAddress}>{row.OwnerAddress}</div>
                    : <span className="muted">{t("nft.walletNone")}</span>}
                </td>
                <td>
                  {row.Burned
                    ? <Badge tone="danger">{t("nft.burned")}</Badge>
                    : row.Crafted
                      ? <Badge tone="warn">{t("nft.crafted")}</Badge>
                      : <Badge tone="good">{t("nft.live")}</Badge>}
                </td>
                <td>{formatDate(row.CreatedAt)}</td>
                <td>
                  <button
                    className="icon-btn"
                    type="button"
                    aria-label={t("nft.walletOpen")}
                    title={t("nft.walletOpen")}
                    onClick={() => { setWalletRef(row.Slug); setWalletGift(row); }}
                  >
                    <Wallet size={14} />
                  </button>
                </td>
              </tr>
            ))}
            {rows.length === 0 && <EmptyRow colSpan={8} />}
          </tbody>
        </table>
      </div>
      {hasMore && (
        <div className="gift-table-actions">
          <button className="btn" type="button" onClick={() => void load(false)} disabled={busy}>{t("nft.loadMore")}</button>
        </div>
      )}
      {walletRef !== null && (
        <NftGiftWalletModal
          giftRef={walletRef}
          gift={walletGift}
          onClose={() => { setWalletRef(null); setWalletGift(null); }}
          onApplied={() => void load(true)}
        />
      )}
    </PageFrame>
  );
}
