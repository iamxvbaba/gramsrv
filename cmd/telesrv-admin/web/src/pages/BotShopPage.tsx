import { RefreshCw, Save } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { SectionTabs } from "../components/SectionTabs";
import { Alert, PageFrame, SectionHead } from "../components/ui";
import { useI18n } from "../i18n";
import type { Navigate } from "../routing";
import type { BotShopConfig } from "../types";

const listToText = (values: number[] | null | undefined) => (values ?? []).join(", ");
const textToList = (text: string) =>
  text
    .split(/[\s,;]+/)
    .map((part) => Number(part))
    .filter((n) => Number.isFinite(n) && n > 0)
    .map((n) => Math.round(n));

export function BotShopPage({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const [config, setConfig] = useState<BotShopConfig | null>(null);
  const [packs, setPacks] = useState("");
  const [months, setMonths] = useState("");
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);

  async function load() {
    setLoading(true);
    setError("");
    try {
      const result = await api.botShop();
      setConfig(result.config);
      setPacks(listToText(result.config.star_packs));
      setMonths(listToText(result.config.premium_months));
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  async function save() {
    if (!config) return;
    setSaving(true);
    setError("");
    setSaved(false);
    try {
      const next = { ...config, star_packs: textToList(packs), premium_months: textToList(months) };
      const result = await api.saveBotShop(next);
      setConfig(result.config);
      setSaved(true);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setSaving(false);
    }
  }

  const set = <K extends keyof BotShopConfig>(key: K, value: BotShopConfig[K]) => {
    setSaved(false);
    setConfig((prev) => (prev ? { ...prev, [key]: value } : prev));
  };
  const num = (key: keyof BotShopConfig, step = "any") => (
    <input
      type="number"
      min="0"
      step={step}
      value={String(config?.[key] ?? 0)}
      onChange={(e) => set(key, Number(e.target.value) as never)}
    />
  );
  const text = (key: keyof BotShopConfig, max = 128) => (
    <input value={String(config?.[key] ?? "")} maxLength={max} onChange={(e) => set(key, e.target.value as never)} />
  );
  const check = (key: keyof BotShopConfig, label: string) => (
    <label className="check-row">
      <input type="checkbox" checked={Boolean(config?.[key])} onChange={(e) => set(key, e.target.checked as never)} /> {label}
    </label>
  );

  return (
    <PageFrame
      title={t("botShop.title")}
      eyebrow={t("botShop.eyebrow")}
      actions={
        <>
          <button className="btn icon-text" type="button" onClick={() => void load()} disabled={loading}>
            <RefreshCw size={15} className={loading ? "spin" : ""} /> {t("common.refresh")}
          </button>
          <button className="btn primary icon-text" type="button" onClick={() => void save()} disabled={!config || saving}>
            <Save size={15} /> {t("botShop.save")}
          </button>
        </>
      }
    >
      <SectionTabs sectionId="commerce" active="/bot-shop" navigate={navigate} />
      {error && <Alert>{error}</Alert>}
      {saved && <div className="notice success">{t("botShop.saved")}</div>}
      {config && (
        <>
          <section className="surface">
            <SectionHead title={t("botShop.stars")} text={t("botShop.starsHint")} />
            {check("enabled", t("botShop.enabled"))}
            <div className="gift-fields-grid">
              <label><span>{t("botShop.packs")}</span><input value={packs} onChange={(e) => { setSaved(false); setPacks(e.target.value); }} /></label>
              <label><span>{t("botShop.rateBase")}</span>{num("rate_base", "1")}</label>
              <label><span>{t("botShop.sgPerTgStar")}</span>{num("sg_stars_per_tg_star", "1")}</label>
              <label><span>{t("botShop.usdPerBase")}</span>{num("usd_per_base")}</label>
              <label><span>{t("botShop.uahPerBase")}</span>{num("uah_per_base")}</label>
            </div>
            {check("custom_enabled", t("botShop.customEnabled"))}
            <div className="gift-fields-grid">
              <label><span>{t("botShop.customMin")}</span>{num("custom_min", "1")}</label>
              <label><span>{t("botShop.customMax")}</span>{num("custom_max", "1")}</label>
            </div>
          </section>

          <section className="surface">
            <SectionHead title={t("botShop.premium")} text={t("botShop.premiumHint")} />
            {check("premium_enabled", t("botShop.premiumEnabled"))}
            <div className="gift-fields-grid">
              <label><span>{t("botShop.premiumMonths")}</span><input value={months} onChange={(e) => { setSaved(false); setMonths(e.target.value); }} /></label>
              <label><span>{t("botShop.premiumTg")}</span>{num("premium_tg_stars_per_month", "1")}</label>
              <label><span>{t("botShop.premiumUsd")}</span>{num("premium_usd_per_month")}</label>
              <label><span>{t("botShop.premiumUah")}</span>{num("premium_uah_per_month")}</label>
            </div>
          </section>

          <section className="surface">
            <SectionHead title={t("botShop.payments")} text={t("botShop.paymentsHint")} />
            {check("pay_tg_stars", t("botShop.payTg"))}
            {check("pay_usdt", t("botShop.payUsdt"))}
            {check("pay_ton", t("botShop.payTon"))}
            {check("pay_card", t("botShop.payCard"))}
            <div className="gift-fields-grid">
              <label><span>{t("botShop.tonAddress")}</span>{text("ton_address")}</label>
              <label><span>{t("botShop.tonRate")}</span>{num("ton_usd_rate")}</label>
              <label><span>{t("botShop.cardNumber")}</span>{text("card_number", 64)}</label>
              <label><span>{t("botShop.cardHolder")}</span>{text("card_holder")}</label>
              <label><span>{t("botShop.cardKop")}</span>{num("card_kop_max", "1")}</label>
            </div>
            <label className="wide-field"><span>{t("botShop.message")}</span>
              <textarea rows={3} maxLength={2000} value={config.shop_message ?? ""} onChange={(e) => set("shop_message", e.target.value)} />
            </label>
          </section>
        </>
      )}
    </PageFrame>
  );
}
