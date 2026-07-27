import { ArrowLeft, ArrowLeftRight, ExternalLink, Flame, Trash2, RefreshCw, Undo2 } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { ChannelPicker, UserPicker } from "../components/EntityPicker";
import { Alert, Badge, EmptyRow, LoadingSurface, PageFrame, SectionHead, SplitLayout, Summary } from "../components/ui";
import { useI18n } from "../i18n";
import { displayUsername, formatCurrency, formatDate } from "../lib/format";
import type { Navigate } from "../routing";
import type {
  AccountRow,
  ChannelRow,
  CollectibleUsernameDetail,
  CollectibleUsernameTransferKind
} from "../types";
import { UsernameStatus, ownerLabel, priceLabel } from "./CollectibleUsernamesPage";

type RecipientKind = "user" | "channel";

export function CollectibleUsernameDetailPage({ id, navigate }: { id: string; navigate: Navigate }) {
  const { t } = useI18n();
  const [detail, setDetail] = useState<CollectibleUsernameDetail | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [recipientKind, setRecipientKind] = useState<RecipientKind>("user");
  const [recipientUser, setRecipientUser] = useState<AccountRow | null>(null);
  const [recipientChannel, setRecipientChannel] = useState<ChannelRow | null>(null);

  async function load() {
    setBusy(true);
    setError("");
    try {
      setDetail(await api.collectibleUsername(id));
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    void load();
  }, [id]);

  if (error && !detail) {
    return <Alert>{error}</Alert>;
  }
  if (!detail) {
    return <LoadingSurface label={busy ? t("usernames.loadingDetail") : t("account.waitingData")} />;
  }

  const asset = detail.asset;
  const transfers = detail.transfers ?? [];
  const vaultLabel = t("usernames.statusVault");
  const hasOwner = Boolean(asset.OwnerPeerType) && asset.OwnerPeerID !== "" && asset.OwnerPeerID !== "0";
  const burned = asset.Status === "burned";

  function openOwner() {
    if (!hasOwner) return;
    navigate(asset.OwnerPeerType === "channel" ? `/channels/${asset.OwnerPeerID}` : `/accounts/${asset.OwnerPeerID}`);
  }

  // Peer ids travel as decimal strings to match the backend `,string` tags.
  function transferPayload(): Record<string, unknown> {
    const payload: Record<string, unknown> = { username: asset.Username };
    if (recipientKind === "user" && recipientUser) payload.to_user_id = String(recipientUser.ID);
    if (recipientKind === "channel" && recipientChannel) payload.to_channel_id = String(recipientChannel.ID);
    return payload;
  }

  return (
    <PageFrame
      title={t("usernames.detailTitle", { username: displayUsername(asset.Username) })}
      eyebrow={t("usernames.detailEyebrow")}
      actions={
        <>
          <button className="btn icon-text" type="button" onClick={() => navigate("/collectible-usernames")}>
            <ArrowLeft size={15} /> {t("common.backToList")}
          </button>
          <button className="btn icon-text" type="button" onClick={load} disabled={busy}>
            <RefreshCw size={15} className={busy ? "spin" : ""} /> {t("common.refresh")}
          </button>
        </>
      }
    >
      {error && <Alert>{error}</Alert>}
      <SplitLayout
        main={
          <div className="stacked-sections">
            <section className="entity-head">
              <div>
                <div className="entity-title">{displayUsername(asset.Username)}</div>
                <div className="entity-subtitle">{t("usernames.assetID", { id: asset.ID })}</div>
              </div>
              <div className="entity-badges">
                <UsernameStatus status={asset.Status} />
                <Badge tone={asset.TransferCount > 0 ? "warn" : "neutral"}>
                  {t("usernames.transferCount", { count: asset.TransferCount })}
                </Badge>
                {asset.Status === "owned" && (
                  <Badge tone={asset.RegistryActive ? "good" : "warn"}>
                    {asset.RegistryActive ? t("usernames.registryActive") : t("usernames.registryHidden")}
                  </Badge>
                )}
              </div>
            </section>
            <div className="summary-grid">
              <Summary label={t("common.owner")} value={ownerLabel(asset, vaultLabel)} />
              <Summary label={t("usernames.price")} value={priceLabel(asset)} mono />
              <Summary label={t("usernames.purchaseDate")} value={formatDate(asset.PurchaseDate) || "-"} />
              <Summary
                label={t("usernames.originalOwner")}
                value={peerLabel(asset.OriginalOwnerPeerType, asset.OriginalOwnerPeerID, vaultLabel, asset.OriginalOwnerUsername)}
              />
              <Summary label={t("usernames.transfers")} value={String(asset.TransferCount)} mono />
              <Summary label={t("account.createdAt")} value={formatDate(asset.CreatedAt) || "-"} />
              <Summary label={t("common.updatedAt")} value={formatDate(asset.UpdatedAt) || "-"} />
            </div>
            <div className="toolbar">
              {hasOwner && (
                <button className="row-link" type="button" onClick={openOwner}>
                  {asset.OwnerPeerType === "channel" ? t("usernames.openOwnerChannel") : t("usernames.openOwnerAccount")}
                </button>
              )}
              {asset.URL && (
                <a className="row-link" href={asset.URL} target="_blank" rel="noreferrer noopener">
                  <ExternalLink size={14} /> {t("usernames.openMarketplace")}
                </a>
              )}
            </div>

            {!burned && (
              <section className="section-block">
                <SectionHead title={t("usernames.transferTitle")} text={t("usernames.transferHint")} />
                <div className="toolbar" role="group" aria-label={t("usernames.recipientKind")}>
                  <button type="button" className={`btn ${recipientKind === "user" ? "primary" : ""}`} onClick={() => setRecipientKind("user")}>
                    {t("usernames.recipientUser")}
                  </button>
                  <button type="button" className={`btn ${recipientKind === "channel" ? "primary" : ""}`} onClick={() => setRecipientKind("channel")}>
                    {t("usernames.recipientChannel")}
                  </button>
                </div>
                {recipientKind === "user"
                  ? <UserPicker label={t("usernames.recipientUser")} value={recipientUser} onChange={setRecipientUser} />
                  : <ChannelPicker label={t("usernames.recipientChannel")} value={recipientChannel} onChange={setRecipientChannel} />}
                <div className="bot-create-actions">
                  <span className="bot-create-note">{t("usernames.transferNote")}</span>
                  <ActionButton
                    label={t("usernames.transfer")}
                    icon={<ArrowLeftRight size={15} />}
                    tone="warn"
                    path="/api/actions/transfer-collectible-username"
                    payload={transferPayload}
                    onDone={load}
                  />
                </div>
              </section>
            )}

            <section className="section-block">
              <SectionHead title={t("usernames.historyTitle")} text={t("usernames.historyHint")} />
              <div className="table-wrap">
                <table className="data-table">
                  <thead>
                    <tr>
                      <th>{t("common.id")}</th>
                      <th>{t("usernames.eventKind")}</th>
                      <th>{t("usernames.fromPeer")}</th>
                      <th>{t("usernames.toPeer")}</th>
                      <th>{t("usernames.price")}</th>
                      <th>{t("audit.actor")}</th>
                      <th>{t("audit.reason")}</th>
                      <th>{t("common.time")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {transfers.map((row) => (
                      <tr key={row.ID}>
                        <td className="mono">{row.ID}</td>
                        <td><TransferKind kind={row.Kind} /></td>
                        <td className="mono">{peerLabel(row.FromPeerType, row.FromPeerID, vaultLabel, row.FromUsername)}</td>
                        <td className="mono">{peerLabel(row.ToPeerType, row.ToPeerID, vaultLabel, row.ToUsername)}</td>
                        <td className="mono">{row.Amount && row.Amount !== "0" ? formatCurrency(row.Amount, row.Currency) : "-"}</td>
                        <td>{row.Actor || "-"}</td>
                        <td className="truncate">{row.Reason || "-"}</td>
                        <td>{formatDate(row.CreatedAt) || "-"}</td>
                      </tr>
                    ))}
                    {transfers.length === 0 && <EmptyRow colSpan={8} />}
                  </tbody>
                </table>
              </div>
            </section>
          </div>
        }
        side={
          <section className="action-dock">
            <div className="dock-title">{t("usernames.actionDock")}</div>
            {burned ? (
              <p className="bot-create-note">{t("usernames.burnedHint")}</p>
            ) : (
              <>
                <div className="action-stack">
                  <ActionButton
                    label={t("usernames.revoke")}
                    icon={<Undo2 size={15} />}
                    tone="warn"
                    path="/api/actions/revoke-collectible-username"
                    payload={() => ({ username: asset.Username, burn: false })}
                    onDone={load}
                  />
                </div>
                <p className="bot-create-note">{t("usernames.revokeHint")}</p>
                <div className="danger-zone">
                  <ActionButton
                    label={t("usernames.burn")}
                    icon={<Flame size={15} />}
                    tone="danger"
                    path="/api/actions/revoke-collectible-username"
                    payload={() => ({ username: asset.Username, burn: true })}
                    onDone={load}
                  />
                  <p className="bot-create-note">{t("usernames.burnHint")}</p>
                  <ActionButton
                    label={t("usernames.delete")}
                    icon={<Trash2 size={15} />}
                    tone="danger"
                    path="/api/actions/delete-collectible-username"
                    payload={() => ({ username: asset.Username })}
                    onDone={() => navigate("/collectible-usernames")}
                  />
                  <p className="bot-create-note">{t("usernames.deleteHint")}</p>
                </div>
              </>
            )}
          </section>
        }
      />
    </PageFrame>
  );
}

function TransferKind({ kind }: { kind: CollectibleUsernameTransferKind }) {
  const { t } = useI18n();
  const tone = kind === "burn" ? "danger" : kind === "revoke" ? "warn" : kind === "mint" ? "good" : "neutral";
  return <Badge tone={tone}>{t(`usernames.kind.${kind}`)}</Badge>;
}

function peerLabel(type: string, peerID: string, vaultLabel: string, username = ""): string {
  if (!type || peerID === "" || peerID === "0") return vaultLabel;
  const handle = displayUsername(username);
  return handle ? `${handle} · ${type}:${peerID}` : `${type}:${peerID}`;
}
