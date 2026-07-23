import { CheckCircle2, CircleAlert, Gift, Loader2, Play, User, Users } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { api, errorMessage } from "../api";
import { ChannelPicker, UserPicker } from "../components/EntityPicker";
import { Alert, JsonBlock } from "../components/ui";
import { useI18n } from "../i18n";
import type { AccountRow, ChannelRow, CommandResult, StarGiftCollectibleAttributeRow, StarGiftCollectiblePreview, StarGiftRow } from "../types";

const SYSTEM_SENDER = "777000";

type RecipientKind = "user" | "channel";

function attrLabel(attr: StarGiftCollectibleAttributeRow): string {
  const rarity = attr.rarity_permille > 0 ? ` · ${(attr.rarity_permille / 10).toFixed(1)}%` : "";
  return `${attr.name || `#${attr.id}`}${rarity}`;
}

export function GiveGiftForm({ gift, onDone }: { gift: StarGiftRow; onDone?: () => void }) {
  const { t } = useI18n();
  const [kind, setKind] = useState<RecipientKind>("user");
  const [user, setUser] = useState<AccountRow | null>(null);
  const [channel, setChannel] = useState<ChannelRow | null>(null);
  const [sender, setSender] = useState(SYSTEM_SENDER);
  const [message, setMessage] = useState("");
  const [hideName, setHideName] = useState(false);
  const [upgrade, setUpgrade] = useState(false);
  const [preview, setPreview] = useState<StarGiftCollectiblePreview | null>(null);
  const [previewError, setPreviewError] = useState("");
  const [modelID, setModelID] = useState("0");
  const [patternID, setPatternID] = useState("0");
  const [backdropID, setBackdropID] = useState("0");
  const [num, setNum] = useState("");
  const [reason, setReason] = useState("");
  const [result, setResult] = useState<CommandResult | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const recipientID = kind === "user" ? user?.ID ?? 0 : channel?.ID ?? 0;
  const upgradable = kind === "user" && upgrade;

  // Reset the collectible selection whenever the chosen gift changes; the
  // recipient/sender/message are intentionally preserved for fast re-issuing.
  useEffect(() => {
    setUpgrade(false);
    setPreview(null);
    setPreviewError("");
    setModelID("0");
    setPatternID("0");
    setBackdropID("0");
    setNum("");
    setResult(null);
    setError("");
  }, [gift.GiftID]);

  useEffect(() => {
    if (!upgradable || preview) return;
    let cancelled = false;
    setPreviewError("");
    api.giftCollectibles(gift.GiftID)
      .then((data) => { if (!cancelled) setPreview(data); })
      .catch((err) => { if (!cancelled) setPreviewError(errorMessage(err)); });
    return () => { cancelled = true; };
  }, [upgradable, preview, gift.GiftID]);

  function buildPayload(confirm: boolean): Record<string, unknown> {
    const senderID = Number.parseInt(sender.trim() || SYSTEM_SENDER, 10);
    const parsedNum = Number.parseInt(num.trim(), 10);
    return {
      gift_id: gift.GiftID,
      sender_user_id: Number.isFinite(senderID) ? senderID : 0,
      user_id: kind === "user" ? recipientID : 0,
      channel_id: kind === "channel" ? recipientID : 0,
      hide_name: hideName,
      message: message.trim(),
      upgrade: upgradable,
      model_attribute_id: upgradable ? modelID : "0",
      pattern_attribute_id: upgradable ? patternID : "0",
      backdrop_attribute_id: upgradable ? backdropID : "0",
      num: upgradable && Number.isFinite(parsedNum) && parsedNum > 0 ? parsedNum : 0,
      reason: reason.trim(),
      confirm
    };
  }

  const previewPayload = useMemo(() => buildPayload(false), [gift.GiftID, kind, recipientID, sender, message, hideName, upgrade, modelID, patternID, backdropID, num, reason]);
  const canConfirm = result?.dry_run && !result.error;

  async function run(confirm: boolean) {
    if (recipientID <= 0) {
      setError(t("giveGift.recipientRequired"));
      return;
    }
    if (!reason.trim()) {
      setError(t("action.reasonRequired"));
      return;
    }
    setBusy(true);
    setError("");
    try {
      const commandResult = await api.action("/api/actions/give-gift", buildPayload(confirm));
      setResult(commandResult);
      if (confirm && !commandResult.error) {
        onDone?.();
      }
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="give-gift-form">
      <div className="give-gift-summary">
        <Gift size={16} />
        <div>
          <strong>{gift.Title || `Gift #${gift.GiftID}`}</strong>
          <span className="mono">#{gift.GiftID} · ⭐ {gift.Stars}</span>
        </div>
      </div>

      <div className="give-gift-tabs" role="group" aria-label={t("giveGift.recipientKind")}>
        <button type="button" className={`btn ${kind === "user" ? "primary" : ""}`} onClick={() => { setKind("user"); setResult(null); }}>
          <User size={15} /> {t("giveGift.recipientUser")}
        </button>
        <button type="button" className={`btn ${kind === "channel" ? "primary" : ""}`} onClick={() => { setKind("channel"); setUpgrade(false); setResult(null); }}>
          <Users size={15} /> {t("giveGift.recipientChannel")}
        </button>
      </div>

      {kind === "user"
        ? <UserPicker label={t("giveGift.pickUser")} value={user} onChange={(row) => { setUser(row); setResult(null); }} />
        : <ChannelPicker label={t("giveGift.pickChannel")} value={channel} onChange={(row) => { setChannel(row); setResult(null); }} />}

      <label className="form-field">
        <span>{t("giveGift.sender")}</span>
        <input value={sender} inputMode="numeric" onChange={(event) => { setSender(event.target.value.replace(/[^0-9]/g, "")); setResult(null); }} placeholder={SYSTEM_SENDER} />
        <small className="field-hint">{t("giveGift.senderHint")}</small>
      </label>

      <label className="form-field">
        <span>{t("giveGift.message")}</span>
        <textarea value={message} rows={2} maxLength={255} onChange={(event) => { setMessage(event.target.value); setResult(null); }} placeholder={t("giveGift.messagePlaceholder")} />
      </label>

      <label className="gift-switch">
        <input type="checkbox" checked={hideName} onChange={(event) => { setHideName(event.target.checked); setResult(null); }} />
        <span className="gift-switch-track" aria-hidden="true"><span /></span>
        <span>{t("giveGift.hideName")}</span>
      </label>

      {kind === "user" && (
        <>
          <label className="gift-switch">
            <input type="checkbox" checked={upgrade} onChange={(event) => { setUpgrade(event.target.checked); if (!event.target.checked) { setModelID("0"); setPatternID("0"); setBackdropID("0"); setNum(""); } setResult(null); }} />
            <span className="gift-switch-track" aria-hidden="true"><span /></span>
            <span>{t("giveGift.upgrade")}</span>
          </label>
          {upgrade && <p className="give-gift-upgrade-note">{t("giveGift.upgradeNote")}</p>}
          {upgrade && previewError && <Alert>{previewError}</Alert>}
          {upgrade && preview && (
            <div className="gift-fields-grid give-gift-attrs">
              <label>
                <span>{t("giveGift.model")}</span>
                <select value={modelID} onChange={(event) => { setModelID(event.target.value); setResult(null); }}>
                  <option value="0">{t("giveGift.random")}</option>
                  {(preview.models ?? []).map((attr) => <option key={attr.id} value={attr.id}>{attrLabel(attr)}</option>)}
                </select>
              </label>
              <label>
                <span>{t("giveGift.pattern")}</span>
                <select value={patternID} onChange={(event) => { setPatternID(event.target.value); setResult(null); }}>
                  <option value="0">{t("giveGift.random")}</option>
                  {(preview.patterns ?? []).map((attr) => <option key={attr.id} value={attr.id}>{attrLabel(attr)}</option>)}
                </select>
              </label>
              <label>
                <span>{t("giveGift.backdrop")}</span>
                <select value={backdropID} onChange={(event) => { setBackdropID(event.target.value); setResult(null); }}>
                  <option value="0">{t("giveGift.random")}</option>
                  {(preview.backdrops ?? []).map((attr) => <option key={attr.id} value={attr.id}>{attrLabel(attr)}</option>)}
                </select>
              </label>
              <label>
                <span>{t("giveGift.number")}</span>
                <input type="number" min="1" max={preview.supply_total ?? undefined} value={num} placeholder={t("giveGift.numberAuto")} onChange={(event) => { setNum(event.target.value.replace(/[^0-9]/g, "")); setResult(null); }} />
              </label>
            </div>
          )}
        </>
      )}

      <label className="form-field">
        <span>{t("action.reason")}</span>
        <textarea value={reason} rows={2} onChange={(event) => setReason(event.target.value)} placeholder={t("action.reasonPlaceholder")} />
      </label>

      <div className="command-preview">
        <div className="preview-head">{t("action.requestPreview")}</div>
        <JsonBlock value={JSON.stringify(previewPayload, null, 2)} />
      </div>

      {error && <Alert>{error}</Alert>}
      {result && (
        <div className="result-box">
          <div className="result-title">
            {result.error ? <CircleAlert size={16} /> : <CheckCircle2 size={16} />}
            <strong>{result.message || result.error || t("action.result")}</strong>
          </div>
          <div className="result-line"><span>{t("action.commandID")}</span><strong>{result.command_id}</strong></div>
          <div className="result-line"><span>{t("action.status")}</span><strong>{result.status}</strong></div>
          <div className="result-line"><span>{t("action.dryRun")}</span><strong>{result.dry_run ? t("common.yes") : t("common.no")}</strong></div>
          {result.details && <JsonBlock value={JSON.stringify(result.details, null, 2)} />}
        </div>
      )}

      <div className="give-gift-form-actions">
        <button className="btn icon-text" type="button" onClick={() => run(false)} disabled={busy}>
          {busy ? <Loader2 size={15} className="spin" /> : <Play size={15} />}
          {result ? t("action.runAgain") : t("action.runDry")}
        </button>
        <button className="btn primary icon-text" type="button" onClick={() => run(true)} disabled={busy || !canConfirm}>
          <Gift size={15} />
          {t("giveGift.confirm")}
        </button>
      </div>
    </div>
  );
}
