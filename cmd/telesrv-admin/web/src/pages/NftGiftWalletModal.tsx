import { Loader2, ShieldCheck, Wallet, X } from "lucide-react";
import { useState } from "react";
import { createPortal } from "react-dom";
import { api, errorMessage } from "../api";
import { Alert } from "../components/ui";
import { useI18n } from "../i18n";
import type { CommandResult, NftGiftWalletPayload, UniqueStarGiftRow } from "../types";

type Mode = "bind" | "release";

// NftGiftWalletModal is the operator form behind "any gift -> any TON wallet":
// it addresses a collectible by slug, NFT address or id and fills the owner
// wallet record by hand, edits an existing wallet binding, or releases the gift
// back to its Telegram owner. TON owner, Telegram holder and host stay separate
// fields here, mirroring the ledger.
export function NftGiftWalletModal({
	giftRef,
	gift,
	onClose,
	onApplied,
}: {
	giftRef: string;
	gift?: UniqueStarGiftRow | null;
	onClose: () => void;
	onApplied: () => void;
}) {
	const { t } = useI18n();
	const bound = Boolean(gift?.OwnerAddress);
	const [ref, setRef] = useState(giftRef);
	const [mode, setMode] = useState<Mode>("bind");
	const [ownerName, setOwnerName] = useState(gift?.WalletName ?? "");
	const [walletAddress, setWalletAddress] = useState(gift?.OwnerAddress ?? "");
	const [hostUserID, setHostUserID] = useState(
		gift?.HostPeerType === "user" && gift.HostPeerID !== "0" ? gift.HostPeerID : "",
	);
	const [reason, setReason] = useState("");
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState("");
	const [preview, setPreview] = useState<CommandResult | null>(null);

	const invalidate = () => setPreview(null);

	function buildPayload(confirm: boolean, commandID = ""): NftGiftWalletPayload {
		if (!reason.trim()) throw new Error(t("action.reasonRequired"));
		const payload: NftGiftWalletPayload = {
			command_id: commandID,
			reason: reason.trim(),
			confirm,
			ref: ref.trim(),
			clear: mode === "release",
		};
		if (mode === "bind") {
			payload.wallet_name = ownerName.trim();
			payload.wallet_address = walletAddress.trim();
			if (hostUserID.trim()) payload.host_user_id = hostUserID.trim();
		}
		return payload;
	}

	async function validate() {
		setBusy(true);
		setError("");
		setPreview(null);
		try {
			setPreview(await api.setNftGiftWallet(buildPayload(false)));
		} catch (err) {
			setError(errorMessage(err));
		} finally {
			setBusy(false);
		}
	}

	async function execute() {
		if (!preview) return;
		setBusy(true);
		setError("");
		try {
			await api.setNftGiftWallet(buildPayload(true, preview.command_id));
			onApplied();
			onClose();
		} catch (err) {
			setError(errorMessage(err));
		} finally {
			setBusy(false);
		}
	}

	const modeButton = (value: Mode, label: string) => (
		<button
			className={`btn ${mode === value ? "primary" : ""}`}
			type="button"
			onClick={() => { setMode(value); invalidate(); }}
			disabled={busy}
		>
			{label}
		</button>
	);

	return createPortal(
		<div className="modal-backdrop" role="presentation">
			<section className="modal command-modal" role="dialog" aria-modal="true" aria-label={t("nft.walletTitle")}>
				<div className="modal-head">
					<div>
						<div className="eyebrow">{t("nft.eyebrow")}</div>
						<h2>{t("nft.walletTitle")}</h2>
						<p>{bound ? t("nft.walletEditHint") : t("nft.walletHint")}</p>
					</div>
					<button className="icon-btn" type="button" onClick={onClose} disabled={busy} aria-label={t("action.close")}>
						<X size={15} />
					</button>
				</div>
				<div className="command-body">
					{gift && (
						<div className="gift-validation">
							<div className="gift-validation-head">
								<Wallet size={17} />
								<div>
									<strong>{t("nft.walletCurrent")}</strong>
									<span>
										{gift.Title || gift.Slug} #{gift.Num} —{" "}
										{bound
											? `${gift.WalletName || t("nft.ownerTon")}: ${gift.OwnerAddress}`
											: t("nft.walletNone")}
									</span>
									{gift.HostPeerID !== "0" && (
										<span>{`${t("nft.hostedBy")} ${gift.HostPeerType}/${gift.HostPeerID}`}</span>
									)}
								</div>
							</div>
						</div>
					)}
					<div className="bot-create-fields">
						{modeButton("bind", bound ? t("nft.walletEdit") : t("nft.walletBind"))}
						{modeButton("release", t("nft.walletRelease"))}
					</div>
					<div className="gift-fields-grid">
						<label>
							<span>{t("nft.walletRef")}</span>
							<input value={ref} onChange={(event) => { setRef(event.target.value); invalidate(); }} placeholder="slug / EQ… / 9000000000000001" />
						</label>
						<label>
							<span>{t("gifts.reason")}</span>
							<input value={reason} maxLength={1000} placeholder={t("gifts.reasonPlaceholder")} onChange={(event) => { setReason(event.target.value); invalidate(); }} />
						</label>
					</div>
					{mode === "bind" ? (
						<div className="gift-fields-grid">
							<label>
								<span>{t("nft.walletOwnerName")}</span>
								<input value={ownerName} maxLength={64} placeholder={t("nft.walletNamePlaceholder")} onChange={(event) => { setOwnerName(event.target.value); invalidate(); }} />
							</label>
							<label>
								<span>{t("nft.walletAddress")}</span>
								<input value={walletAddress} onChange={(event) => { setWalletAddress(event.target.value); invalidate(); }} placeholder={t("nft.walletAddressPlaceholder")} />
							</label>
							<label>
								<span>{t("nft.walletHost")}</span>
								<input value={hostUserID} inputMode="numeric" placeholder="0" onChange={(event) => { setHostUserID(event.target.value); invalidate(); }} />
							</label>
						</div>
					) : (
						<Alert>{t("nft.walletReleaseNote")}</Alert>
					)}
					{error && <Alert>{error}</Alert>}
					{preview && (
						<div className="gift-validation">
							<div className="gift-validation-head">
								<ShieldCheck size={17} />
								<div>
									<strong>{preview.dry_run ? t("gifts.validate") : t("nft.walletTitle")}</strong>
									<span>{preview.message}</span>
								</div>
							</div>
							<pre>{JSON.stringify(preview.details, null, 2)}</pre>
						</div>
					)}
				</div>
				<div className="modal-actions">
					<button className="btn" type="button" onClick={onClose} disabled={busy}>{t("common.close")}</button>
					<button className="btn" type="button" onClick={validate} disabled={busy}>
						{busy ? <Loader2 className="spin" size={15} /> : <ShieldCheck size={15} />}{t("gifts.validate")}
					</button>
					<button className="btn primary" type="button" onClick={execute} disabled={busy || !preview}>
						<Wallet size={15} />{mode === "bind" ? t("nft.walletBindExecute") : t("nft.walletReleaseExecute")}
					</button>
				</div>
			</section>
		</div>,
		document.body,
	);
}
