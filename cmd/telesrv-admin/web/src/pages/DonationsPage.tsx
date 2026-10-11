import { Loader2, RefreshCw, Wallet } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { Alert, Badge, EmptyRow, PageFrame } from "../components/ui";
import type { DonationChain, DonationDepositRow, DonationToken, DonationWalletStatus } from "../types";

type ChainDraft = {
  rpcURL: string;
  wsURL: string;
  enabled: boolean;
  confirmationsRequired: string;
  priceFeedAddress: string;
  manualUSDRateMicros: string;
};

function draftFromChain(chain: DonationChain): ChainDraft {
  return {
    rpcURL: chain.RPCURL,
    wsURL: chain.WSURL,
    enabled: chain.Enabled,
    confirmationsRequired: String(chain.ConfirmationsRequired),
    priceFeedAddress: chain.PriceFeedAddress,
    manualUSDRateMicros: String(chain.ManualUSDRateMicros)
  };
}

function statusTone(status: DonationDepositRow["Status"]): "good" | "warn" | "danger" | "neutral" {
  if (status === "credited") return "good";
  if (status === "confirmed") return "warn";
  if (status === "orphaned") return "danger";
  return "neutral";
}

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

// Crypto donations: custodial-wallet status, per-chain watcher config (RPC/WS
// endpoint, enabled flag, confirmation depth, pricing), and the deposit
// ledger across every user. The wallet itself is auto-provisioned at server
// startup (see docs/donations.md) -- there is deliberately no button here to
// create or regenerate it, and the mnemonic/private keys are never exposed
// through this or any other admin route.
export function DonationsPage() {
  const [wallet, setWallet] = useState<DonationWalletStatus | null>(null);
  const [chains, setChains] = useState<DonationChain[]>([]);
  const [tokens, setTokens] = useState<DonationToken[]>([]);
  const [drafts, setDrafts] = useState<Record<string, ChainDraft>>({});
  const [deposits, setDeposits] = useState<DonationDepositRow[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function load() {
    setBusy(true);
    setError("");
    try {
      const [walletStatus, chainsResp, depositsResp] = await Promise.all([
        api.donationWalletStatus(),
        api.donationChains(),
        api.donationDeposits(new URLSearchParams({ limit: "100" }))
      ]);
      setWallet(walletStatus);
      setChains(chainsResp.chains);
      setTokens(chainsResp.tokens);
      setDrafts(Object.fromEntries(chainsResp.chains.map((c) => [c.Key, draftFromChain(c)])));
      setDeposits(depositsResp.rows);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => { void load(); }, []);

  function updateDraft(key: string, patch: Partial<ChainDraft>) {
    setDrafts((prev) => ({ ...prev, [key]: { ...(prev[key] ?? draftFromChain(chains.find((c) => c.Key === key)!)), ...patch } }));
  }

  return (
    <PageFrame
      title={"Crypto donations"}
      eyebrow={"Custodial wallet status, per-chain watcher config, and the deposit ledger for /deposit donations"}
      actions={
        <button className="btn" type="button" onClick={() => void load()} disabled={busy}>
          {busy ? <Loader2 size={15} className="spin" /> : <RefreshCw size={15} />} {"Refresh"}
        </button>
      }
    >
      {error && <Alert>{error}</Alert>}

      <section className="section-block">
        <h2>{"Wallet"}</h2>
        <div className="card-body">
          <div className="attr-block">
            <div className="result-line">
              <span>{"Status"}</span>
              <strong>
                {wallet?.HasWallet ? (
                  <Badge tone="good"><Wallet size={13} /> {"Provisioned"}</Badge>
                ) : (
                  <Badge tone="warn">{"Not yet provisioned"}</Badge>
                )}
              </strong>
            </div>
            <div className="result-line"><span>{"Deposit addresses assigned"}</span><strong>{wallet?.AddressCount ?? 0}</strong></div>
          </div>
          {!wallet?.HasWallet && (
            <p className="muted">
              {"A wallet is generated automatically the next time the server starts -- there is no manual step, and no admin action creates one. The recovery phrase is logged once at that startup and never stored anywhere retrievable through this panel."}
            </p>
          )}
        </div>
      </section>

      <section className="section-block">
        <h2>{"Chains"}</h2>
        <div className="table-wrap">
          <table className="data-table">
            <thead>
              <tr>
                <th>{"Chain"}</th>
                <th>{"Chain ID"}</th>
                <th>{"Native"}</th>
                <th>{"RPC URL"}</th>
                <th>{"WS URL"}</th>
                <th>{"Confirmations"}</th>
                <th>{"Manual USD rate (µ)"}</th>
                <th>{"Price feed address"}</th>
                <th>{"Enabled"}</th>
                <th>{"Tokens"}</th>
                <th>{"Actions"}</th>
              </tr>
            </thead>
            <tbody>
              {chains.map((chain) => {
                const draft = drafts[chain.Key] ?? draftFromChain(chain);
                const chainTokens = tokens.filter((t) => t.ChainKey === chain.Key);
                return (
                  <tr key={chain.Key}>
                    <td>{chain.Name}<div className="muted mono">{chain.Key}</div></td>
                    <td className="mono">{chain.ChainID}</td>
                    <td className="mono">{chain.NativeSymbol} ({chain.NativeDecimals})</td>
                    <td>
                      <input
                        className="small-input"
                        value={draft.rpcURL}
                        placeholder={"https://…"}
                        onChange={(event) => updateDraft(chain.Key, { rpcURL: event.target.value })}
                      />
                    </td>
                    <td>
                      <input
                        className="small-input"
                        value={draft.wsURL}
                        placeholder={"wss://… (optional)"}
                        onChange={(event) => updateDraft(chain.Key, { wsURL: event.target.value })}
                      />
                    </td>
                    <td>
                      <input
                        type="number"
                        min="1"
                        className="small-input"
                        value={draft.confirmationsRequired}
                        onChange={(event) => updateDraft(chain.Key, { confirmationsRequired: event.target.value })}
                      />
                    </td>
                    <td>
                      <input
                        type="number"
                        className="small-input"
                        value={draft.manualUSDRateMicros}
                        onChange={(event) => updateDraft(chain.Key, { manualUSDRateMicros: event.target.value })}
                      />
                    </td>
                    <td>
                      <input
                        className="small-input"
                        value={draft.priceFeedAddress}
                        placeholder={"0x… (optional)"}
                        onChange={(event) => updateDraft(chain.Key, { priceFeedAddress: event.target.value })}
                      />
                    </td>
                    <td>{chain.Enabled ? <Badge tone="good">{"Enabled"}</Badge> : <Badge tone="danger">{"Disabled"}</Badge>}</td>
                    <td>
                      {chainTokens.length === 0
                        ? <span className="muted">{"none"}</span>
                        : chainTokens.map((t) => (
                          <div key={t.Symbol} className="mono">
                            {t.Symbol}: {t.ContractAddress || <span className="muted">{"not set"}</span>}
                          </div>
                        ))}
                    </td>
                    <td>
                      <div className="gift-table-actions">
                        <ActionButton
                          compact
                          tone="neutral"
                          label={"Save"}
                          path="/api/actions/donation-chain-update"
                          payload={() => ({
                            chain_key: chain.Key,
                            rpc_url: draft.rpcURL,
                            ws_url: draft.wsURL,
                            enabled: draft.enabled,
                            confirmations_required: Number(draft.confirmationsRequired),
                            price_feed_address: draft.priceFeedAddress,
                            manual_usd_rate_micros: Number(draft.manualUSDRateMicros)
                          })}
                          onDone={() => void load()}
                        />
                        <ActionButton
                          compact
                          tone="neutral"
                          label={chain.Enabled ? "Disable" : "Enable"}
                          path="/api/actions/donation-chain-update"
                          payload={() => ({
                            chain_key: chain.Key,
                            rpc_url: draft.rpcURL,
                            ws_url: draft.wsURL,
                            enabled: !chain.Enabled,
                            confirmations_required: Number(draft.confirmationsRequired),
                            price_feed_address: draft.priceFeedAddress,
                            manual_usd_rate_micros: Number(draft.manualUSDRateMicros)
                          })}
                          onDone={() => void load()}
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
      </section>

      <section className="section-block">
        <h2>{"Deposits"}</h2>
        <div className="table-wrap">
          <table className="data-table">
            <thead>
              <tr>
                <th>{"ID"}</th>
                <th>{"User"}</th>
                <th>{"Chain"}</th>
                <th>{"Asset"}</th>
                <th>{"Amount"}</th>
                <th>{"USD (est.)"}</th>
                <th>{"Stars credited"}</th>
                <th>{"Status"}</th>
                <th>{"Confirmations"}</th>
                <th>{"Detected"}</th>
                <th>{"Tx"}</th>
              </tr>
            </thead>
            <tbody>
              {deposits.map((d) => {
                const decimals = chains.find((c) => c.Key === d.ChainKey)?.NativeDecimals ?? 0;
                return (
                  <tr key={d.ID}>
                    <td className="mono">{d.ID}</td>
                    <td>{d.UserFirstName || d.UserPhone || d.UserID}</td>
                    <td>{d.ChainKey}</td>
                    <td className="mono">{d.TokenSymbol || chains.find((c) => c.Key === d.ChainKey)?.NativeSymbol}</td>
                    <td className="mono">{formatAmount(d.AmountRaw, decimals)}</td>
                    <td className="mono">{(d.USDValueMicros / 1_000_000).toFixed(2)}</td>
                    <td className="mono">{d.StarsCredited}</td>
                    <td><Badge tone={statusTone(d.Status)}>{d.Status}</Badge></td>
                    <td className="mono">{d.Confirmations}</td>
                    <td>{new Date(d.DetectedAt).toLocaleString()}</td>
                    <td className="mono">{d.TxHash.slice(0, 10)}…</td>
                  </tr>
                );
              })}
              {deposits.length === 0 && !busy && <EmptyRow colSpan={11} />}
            </tbody>
          </table>
        </div>
      </section>
    </PageFrame>
  );
}
