import { ChevronDown, Loader2, RefreshCw, Wallet } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { SectionTabs } from "../components/SectionTabs";
import { Alert, Badge, EmptyRow, Metric, PageFrame } from "../components/ui";
import { useI18n } from "../i18n";
import { formatDate } from "../lib/format";
import type { Navigate } from "../routing";
import type { DonationChain, DonationDepositRow, DonationToken, DonationWalletStatus } from "../types";

type ChainDraft = {
  rpcURL: string;
  wsURL: string;
  confirmationsRequired: string;
  priceFeedAddress: string;
  manualUSDRateMicros: string;
};

function draftFromChain(chain: DonationChain): ChainDraft {
  return {
    rpcURL: chain.RPCURL,
    wsURL: chain.WSURL,
    confirmationsRequired: String(chain.ConfirmationsRequired),
    priceFeedAddress: chain.PriceFeedAddress,
    manualUSDRateMicros: String(chain.ManualUSDRateMicros)
  };
}

function draftValid(draft: ChainDraft): boolean {
  const confirmations = Number(draft.confirmationsRequired);
  const rate = Number(draft.manualUSDRateMicros);
  return Number.isInteger(confirmations) && confirmations > 0
    && Number.isInteger(rate) && rate >= 0;
}

function draftDirty(draft: ChainDraft, chain: DonationChain): boolean {
  return draft.rpcURL !== chain.RPCURL
    || draft.wsURL !== chain.WSURL
    || draft.confirmationsRequired !== String(chain.ConfirmationsRequired)
    || draft.priceFeedAddress !== chain.PriceFeedAddress
    || draft.manualUSDRateMicros !== String(chain.ManualUSDRateMicros);
}

function statusTone(status: DonationDepositRow["Status"]): "good" | "warn" | "danger" | "neutral" {
  if (status === "credited") return "good";
  if (status === "confirmed") return "warn";
  if (status === "orphaned") return "danger";
  return "neutral";
}

// formatAmount renders a raw smallest-unit figure (wei, or a token's base units)
// at the asset's own scale.
function formatAmount(amountRaw: string, decimals: number): string {
  if (decimals <= 0 || !amountRaw) return amountRaw;
  const neg = amountRaw.startsWith("-");
  let digits = neg ? amountRaw.slice(1) : amountRaw;
  while (digits.length <= decimals) digits = "0" + digits;
  const intPart = digits.slice(0, digits.length - decimals);
  const fracPart = digits.slice(digits.length - decimals).replace(/0+$/, "");
  const out = fracPart ? `${intPart}.${fracPart}` : intPart;
  return neg ? `-${out}` : out;
}

const PAGE_SIZE = "50";

// Crypto donations: custodial-wallet status, per-chain watcher config (RPC/WS
// endpoint, enabled flag, confirmation depth, pricing), and the deposit
// ledger across every user.
//
// The wallet is only provisioned when the server runs with
// the shared donations.wallet_key_file (v2, D7), so "not provisioned" is the normal state of a
// deployment that has not turned the feature on -- there is deliberately no
// button here to create one, and no admin route returns the mnemonic or a
// private key. Turning it on is a Core YAML change and a restart.
export function DonationsPage({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const [wallet, setWallet] = useState<DonationWalletStatus | null>(null);
  const [chains, setChains] = useState<DonationChain[]>([]);
  const [tokens, setTokens] = useState<DonationToken[]>([]);
  const [drafts, setDrafts] = useState<Record<string, ChainDraft>>({});
  const [deposits, setDeposits] = useState<DonationDepositRow[]>([]);
  const [hasMore, setHasMore] = useState(false);
  const [beforeID, setBeforeID] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function load(reset: boolean) {
    setBusy(true);
    setError("");
    try {
      const params = new URLSearchParams({ limit: PAGE_SIZE });
      if (!reset && beforeID) params.set("before_id", beforeID);
      const [walletStatus, chainsResp, depositsResp] = await Promise.all([
        api.donationWalletStatus(),
        api.donationChains(),
        api.donationDeposits(params)
      ]);
      setWallet(walletStatus);
      setChains(chainsResp.chains);
      setTokens(chainsResp.tokens);
      setDrafts(Object.fromEntries(chainsResp.chains.map((c) => [c.Key, draftFromChain(c)])));
      setDeposits((current) => (reset ? depositsResp.rows : [...current, ...depositsResp.rows]));
      setHasMore(depositsResp.has_more);
      setBeforeID(depositsResp.next_before_id ?? "");
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => { void load(true); }, []);

  function updateDraft(key: string, patch: Partial<ChainDraft>) {
    setDrafts((prev) => ({
      ...prev,
      [key]: { ...(prev[key] ?? draftFromChain(chains.find((c) => c.Key === key)!)), ...patch }
    }));
  }

  // A deposit is denominated either in the chain's native currency or in one of
  // its stablecoins, and the two have different scales -- USDC is 6 decimals,
  // everything native here is 18. Reading the scale off the chain row instead
  // of the token row showed every USDC deposit 10^12 times too large.
  function decimalsFor(deposit: DonationDepositRow): number {
    const chain = chains.find((c) => c.Key === deposit.ChainKey);
    if (!deposit.TokenSymbol) return chain?.NativeDecimals ?? 0;
    const token = tokens.find((x) => x.ChainKey === deposit.ChainKey && x.Symbol === deposit.TokenSymbol);
    return token?.Decimals ?? chain?.NativeDecimals ?? 0;
  }

  function chainPayload(key: string, enabled: boolean) {
    const draft = drafts[key] ?? draftFromChain(chains.find((c) => c.Key === key)!);
    return {
      chain_key: key,
      rpc_url: draft.rpcURL,
      ws_url: draft.wsURL,
      enabled,
      confirmations_required: Number(draft.confirmationsRequired),
      price_feed_address: draft.priceFeedAddress,
      manual_usd_rate_micros: Number(draft.manualUSDRateMicros)
    };
  }

  const enabledCount = chains.filter((c) => c.Enabled).length;
  const watchableCount = chains.filter((c) => c.Enabled && c.RPCURL !== "").length;
  const creditedTotal = deposits.reduce((sum, d) => sum + d.StarsCredited, 0);

  return (
    <PageFrame
      title={t("donations.title")}
      eyebrow={t("donations.eyebrow")}
      actions={
        <button className="btn icon-text" type="button" onClick={() => void load(true)} disabled={busy}>
          <RefreshCw size={15} className={busy ? "spin" : undefined} /> {t("common.refresh")}
        </button>
      }
    >
      <SectionTabs sectionId="donations" active="/donations" navigate={navigate} />
      {error && <Alert>{error}</Alert>}

      <div className="metric-row">
        <Metric
          label={t("donations.metricAddresses")}
          value={String(wallet?.AddressCount ?? 0)}
        />
        <Metric
          label={t("donations.metricChains")}
          value={`${watchableCount} / ${enabledCount}`}
          tone={watchableCount > 0 ? "good" : "neutral"}
        />
        <Metric label={t("donations.metricDeposits")} value={String(deposits.length)} />
        <Metric label={t("donations.metricCredited")} value={String(creditedTotal)} tone="good" />
      </div>

      <section className="section-block">
        <h2>{t("donations.wallet")}</h2>
        <div className="card-body">
          <div className="attr-block">
            <div className="result-line">
              <span>{t("donations.walletStatus")}</span>
              <strong>
                {wallet?.HasWallet ? (
                  <Badge tone="good"><Wallet size={13} /> {t("donations.provisioned")}</Badge>
                ) : (
                  <Badge tone="warn">{t("donations.notProvisioned")}</Badge>
                )}
              </strong>
            </div>
            <div className="result-line">
              <span>{t("donations.addressCount")}</span>
              <strong>{wallet?.AddressCount ?? 0}</strong>
            </div>
          </div>
          {!wallet?.HasWallet && <p className="muted">{t("donations.notProvisionedHint")}</p>}
        </div>
      </section>

      <section className="section-block">
        <h2>{t("donations.chains")}</h2>
        <div className="table-wrap">
          <table className="data-table">
            <thead>
              <tr>
                <th>{t("common.name")}</th>
                <th>{t("donations.chainID")}</th>
                <th>{t("donations.native")}</th>
                <th>{t("donations.rpcUrl")}</th>
                <th>{t("donations.wsUrl")}</th>
                <th>{t("donations.confirmations")}</th>
                <th>{t("donations.manualRate")}</th>
                <th>{t("donations.priceFeed")}</th>
                <th>{t("common.status")}</th>
                <th>{t("donations.tokens")}</th>
                <th>{t("common.actions")}</th>
              </tr>
            </thead>
            <tbody>
              {chains.map((chain) => {
                const draft = drafts[chain.Key] ?? draftFromChain(chain);
                const valid = draftValid(draft);
                const dirty = draftDirty(draft, chain);
                const chainTokens = tokens.filter((t2) => t2.ChainKey === chain.Key);
                const watchable = chain.Enabled && chain.RPCURL !== "";
                return (
                  <tr key={chain.Key}>
                    <td>
                      {chain.Name}
                      <div className="muted mono">{chain.Key}</div>
                    </td>
                    <td className="mono">{chain.ChainID}</td>
                    <td className="mono">{chain.NativeSymbol} ({chain.NativeDecimals})</td>
                    <td>
                      <input
                        aria-label={t("donations.rpcUrl")}
                        className="small-input"
                        value={draft.rpcURL}
                        placeholder="https://…"
                        onChange={(event) => updateDraft(chain.Key, { rpcURL: event.target.value })}
                      />
                    </td>
                    <td>
                      <input
                        aria-label={t("donations.wsUrl")}
                        className="small-input"
                        value={draft.wsURL}
                        placeholder="wss://… (optional)"
                        onChange={(event) => updateDraft(chain.Key, { wsURL: event.target.value })}
                      />
                    </td>
                    <td>
                      <input
                        aria-label={t("donations.confirmations")}
                        type="number"
                        min="1"
                        className="small-input"
                        value={draft.confirmationsRequired}
                        onChange={(event) => updateDraft(chain.Key, { confirmationsRequired: event.target.value })}
                      />
                    </td>
                    <td>
                      <input
                        aria-label={t("donations.manualRate")}
                        type="number"
                        min="0"
                        className="small-input"
                        value={draft.manualUSDRateMicros}
                        onChange={(event) => updateDraft(chain.Key, { manualUSDRateMicros: event.target.value })}
                      />
                    </td>
                    <td>
                      <input
                        aria-label={t("donations.priceFeed")}
                        className="small-input"
                        value={draft.priceFeedAddress}
                        placeholder="0x… (optional)"
                        onChange={(event) => updateDraft(chain.Key, { priceFeedAddress: event.target.value })}
                      />
                    </td>
                    <td>
                      {/* An enabled chain with no endpoint is the state the
                          seed migration left behind, and it is why this is two
                          states rather than one boolean: it is enabled, it will
                          still not watch anything. */}
                      {watchable ? (
                        <Badge tone="good">{t("donations.watching")}</Badge>
                      ) : chain.Enabled ? (
                        <Badge tone="warn">{t("donations.enabledNoRpc")}</Badge>
                      ) : (
                        <Badge tone="neutral">{t("common.disabled")}</Badge>
                      )}
                    </td>
                    <td>
                      {chainTokens.length === 0 ? (
                        <span className="muted">{t("donations.noTokens")}</span>
                      ) : (
                        chainTokens.map((token) => (
                          <div key={token.Symbol} className="mono">
                            {token.Symbol}:{" "}
                            {token.ContractAddress || <span className="muted">{t("donations.notSet")}</span>}
                          </div>
                        ))
                      )}
                    </td>
                    <td>
                      <div className="gift-table-actions">
                        <ActionButton
                          compact
                          tone="neutral"
                          label={t("common.save")}
                          path="/api/actions/donation-chain-update"
                          payload={() => chainPayload(chain.Key, chain.Enabled)}
                          disabled={!valid || !dirty}
                          onDone={() => void load(true)}
                        />
                        {/* The toggle carries the stored config, not the draft.
                            It used to send draft.rpcURL / draft.wsURL and the
                            rest, so an operator who typed a new endpoint and
                            then hit Enable published a half-finished endpoint
                            along with the flag -- and a cleared field went out
                            as an empty one. */}
                        <ActionButton
                          compact
                          tone={chain.Enabled ? "danger" : "neutral"}
                          label={chain.Enabled ? t("donations.disable") : t("donations.enable")}
                          path="/api/actions/donation-chain-update"
                          payload={() => ({
                            chain_key: chain.Key,
                            rpc_url: chain.RPCURL,
                            ws_url: chain.WSURL,
                            enabled: !chain.Enabled,
                            confirmations_required: chain.ConfirmationsRequired,
                            price_feed_address: chain.PriceFeedAddress,
                            manual_usd_rate_micros: chain.ManualUSDRateMicros
                          })}
                          onDone={() => void load(true)}
                        />
                      </div>
                    </td>
                  </tr>
                );
              })}
              {chains.length === 0 && !busy && <EmptyRow colSpan={11} />}
            </tbody>
          </table>
        </div>
        <p className="muted">{t("donations.chainsNote")}</p>
      </section>

      <section className="section-block">
        <h2>{t("donations.deposits")}</h2>
        <div className="table-wrap">
          <table className="data-table">
            <thead>
              <tr>
                <th>{t("common.id")}</th>
                <th>{t("donations.user")}</th>
                <th>{t("donations.chain")}</th>
                <th>{t("donations.asset")}</th>
                <th>{t("donations.amount")}</th>
                <th>{t("donations.usd")}</th>
                <th>{t("donations.starsCredited")}</th>
                <th>{t("common.status")}</th>
                <th>{t("donations.confirmations")}</th>
                <th>{t("donations.detected")}</th>
                <th>{t("donations.tx")}</th>
              </tr>
            </thead>
            <tbody>
              {deposits.map((d) => (
                <tr key={d.ID}>
                  <td className="mono">{d.ID}</td>
                  <td>{d.UserFirstName || d.UserPhone || d.UserID}</td>
                  <td>{d.ChainKey}</td>
                  <td className="mono">
                    {d.TokenSymbol || chains.find((c) => c.Key === d.ChainKey)?.NativeSymbol}
                  </td>
                  <td className="mono">{formatAmount(d.AmountRaw, decimalsFor(d))}</td>
                  <td className="mono">{(d.USDValueMicros / 1_000_000).toFixed(2)}</td>
                  <td className="mono">{d.StarsCredited}</td>
                  <td><Badge tone={statusTone(d.Status)}>{d.Status}</Badge></td>
                  <td className="mono">{d.Confirmations}</td>
                  <td>{formatDate(d.DetectedAt)}</td>
                  <td className="mono">{d.TxHash.slice(0, 10)}…</td>
                </tr>
              ))}
              {deposits.length === 0 && !busy && <EmptyRow colSpan={11} />}
            </tbody>
          </table>
        </div>
        {/* The ledger used to stop at the first 100 rows with no way to ask for
            more, so on a busy deployment older deposits were unreachable. */}
        {hasMore && (
          <div className="gift-table-actions">
            <button className="btn icon-text" type="button" onClick={() => void load(false)} disabled={busy}>
              {busy ? <Loader2 size={15} className="spin" /> : <ChevronDown size={15} />} {t("common.loadMore")}
            </button>
          </div>
        )}
      </section>
    </PageFrame>
  );
}