import {
  ChevronDown,
  Coins,
  Crown,
  Minus,
  Package,
  Plus,
  RefreshCw,
  Save,
  Settings2,
  X
} from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { UserPicker } from "../components/EntityPicker";
import { SectionTabs } from "../components/SectionTabs";
import { Alert, Badge, EmptyRow, PageFrame, SectionHead } from "../components/ui";
import { useI18n } from "../i18n";
import type { Navigate } from "../routing";
import type { AccountRow, PremiumPlan } from "../types";

function newPlan(label: string): PremiumPlan {
  return {
    Months: 1,
    DurationDays: 30,
    AmountStars: 100,
    FiatCurrency: "USD",
    FiatAmount: 100,
    StoreProduct: "",
    StoreQuantity: 0,
    Enabled: true,
    SortOrder: 40,
    Label: label,
    ManagedBy: "admin",
    Version: 0,
    UpdatedAt: 0
  };
}

function validPlan(plan: PremiumPlan): boolean {
  const hasProduct = plan.StoreProduct.trim().length > 0;
  return Number.isInteger(plan.Months) && plan.Months > 0 && plan.Months <= 120 &&
    Number.isInteger(plan.DurationDays) && plan.DurationDays > 0 && plan.DurationDays <= 36500 &&
    Number.isSafeInteger(plan.AmountStars) && plan.AmountStars > 0 &&
    Number.isSafeInteger(plan.FiatAmount) && plan.FiatAmount > 0 &&
    /^[A-Z]{3}$/.test(plan.FiatCurrency.trim().toUpperCase()) &&
    plan.StoreProduct.trim().length <= 256 &&
    ((!hasProduct && plan.StoreQuantity === 0) ||
      (hasProduct && Number.isInteger(plan.StoreQuantity) && plan.StoreQuantity > 0)) &&
    Number.isInteger(plan.SortOrder) && plan.Label.trim().length > 0;
}

export function PremiumPlansPage({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const [plans, setPlans] = useState<PremiumPlan[]>([]);
  const [draft, setDraft] = useState<PremiumPlan | null>(null);
  const [selectedUser, setSelectedUser] = useState<AccountRow | null>(null);
  const [starsAmount, setStarsAmount] = useState("1000");
  const [premiumMonths, setPremiumMonths] = useState("1");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  const parsedStars = Number(starsAmount);
  const parsedMonths = Number(premiumMonths);
  const selectedUserID = selectedUser?.ID ?? 0;
  const enabledPlans = plans.filter((plan) => plan.Enabled).length;

  async function load() {
    setLoading(true);
    setError("");
    try {
      const catalog = await api.premiumPlans();
      setPlans(catalog.plans ?? []);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => { void load(); }, []);

  function patch(months: number, update: Partial<PremiumPlan>) {
    setPlans((current) => current.map((plan) => plan.Months === months ? { ...plan, ...update } : plan));
  }

  function payload(plan: PremiumPlan) {
    return {
      months: plan.Months,
      duration_days: plan.DurationDays,
      amount_stars: plan.AmountStars,
      fiat_currency: plan.FiatCurrency.trim().toUpperCase(),
      fiat_amount: plan.FiatAmount,
      store_product: plan.StoreProduct.trim(),
      store_quantity: plan.StoreProduct.trim() ? plan.StoreQuantity : 0,
      enabled: plan.Enabled,
      sort_order: plan.SortOrder,
      label: plan.Label.trim(),
      expected_version: plan.Version
    };
  }

  function Field({ label, children, wide = false }: {
    label: string;
    children: ReactNode;
    wide?: boolean;
  }) {
    return <label className={`premium-plan-field ${wide ? "wide" : ""}`}><span>{label}</span>{children}</label>;
  }

  function PlanFields({ plan, onChange, create = false }: {
    plan: PremiumPlan;
    onChange: (update: Partial<PremiumPlan>) => void;
    create?: boolean;
  }) {
    return (
      <div className="premium-plan-groups">
        <section className="premium-plan-group">
          <div className="premium-plan-group-title"><Crown size={15} /><strong>{t("premium.planDetails")}</strong></div>
          <div className="premium-plan-field-grid">
            <Field label={t("premium.label")} wide>
              <input value={plan.Label} onChange={(event) => onChange({ Label: event.target.value })} />
            </Field>
            <Field label={t("premium.months")}>
              <input type="number" min={1} max={120} value={plan.Months} disabled={!create}
                onChange={(event) => onChange({ Months: Number(event.target.value) })} />
            </Field>
            <Field label={t("premium.durationDays")}>
              <input type="number" min={1} max={36500} value={plan.DurationDays}
                onChange={(event) => onChange({ DurationDays: Number(event.target.value) })} />
            </Field>
            <Field label={t("premium.sortOrder")}>
              <input type="number" value={plan.SortOrder}
                onChange={(event) => onChange({ SortOrder: Number(event.target.value) })} />
            </Field>
          </div>
        </section>

        <section className="premium-plan-group">
          <div className="premium-plan-group-title"><Coins size={15} /><strong>{t("premium.pricing")}</strong></div>
          <div className="premium-plan-field-grid">
            <Field label={t("premium.amountStars")} wide>
              <div className="premium-input-affix"><span>★</span><input type="number" min={1} value={plan.AmountStars}
                onChange={(event) => onChange({ AmountStars: Number(event.target.value) })} /></div>
            </Field>
            <Field label={t("premium.fiatCurrency")}>
              <input maxLength={3} value={plan.FiatCurrency}
                onChange={(event) => onChange({ FiatCurrency: event.target.value.toUpperCase() })} />
            </Field>
            <Field label={t("premium.fiatAmount")}>
              <input type="number" min={1} value={plan.FiatAmount}
                onChange={(event) => onChange({ FiatAmount: Number(event.target.value) })} />
            </Field>
          </div>
        </section>

        <section className="premium-plan-group">
          <div className="premium-plan-group-title"><Package size={15} /><strong>{t("premium.storeSettings")}</strong></div>
          <div className="premium-plan-field-grid">
            <Field label={t("premium.storeProduct")} wide>
              <input value={plan.StoreProduct} placeholder={t("premium.storeProductPlaceholder")}
                onChange={(event) => {
                  const storeProduct = event.target.value;
                  onChange({ StoreProduct: storeProduct, ...(!storeProduct.trim() ? { StoreQuantity: 0 } : {}) });
                }} />
            </Field>
            <Field label={t("premium.storeQuantity")}>
              <input type="number" min={0} value={plan.StoreQuantity} disabled={!plan.StoreProduct.trim()}
                onChange={(event) => onChange({ StoreQuantity: Number(event.target.value) })} />
            </Field>
            <p className="premium-store-hint">{t("premium.storeSettingsHint")}</p>
          </div>
        </section>
      </div>
    );
  }

  return (
    <PageFrame title={t("premium.title")} eyebrow={t("premium.eyebrow")} actions={
      <button className="btn icon-text" type="button" onClick={() => void load()} disabled={loading}>
        <RefreshCw size={15} /> {t("common.refresh")}
      </button>
    }>
      <SectionTabs sectionId="commerce" active="/monetization" navigate={navigate} />
      <section className="surface premium-operations-compact">
        <SectionHead title={t("premium.operations")} text={t("premium.operationsHint")} />
        <div className="premium-operation-bar">
          <div className="premium-account-picker">
            <UserPicker variant="dropdown" label={t("premium.userID")} value={selectedUser} onChange={setSelectedUser} />
          </div>

          <div className="premium-quick-action stars">
            <div className="premium-quick-action-head">
              <span><Coins size={14} /></span><strong>{t("premium.starsAmount")}</strong>
            </div>
            <div className="premium-quick-action-control">
              <input aria-label={t("premium.starsAmount")} type="number" min={1} value={starsAmount}
                onChange={(event) => setStarsAmount(event.target.value)} />
              <div className="premium-action-submit">
                <ActionButton compact tone="neutral" icon={<Coins size={14} />} label={t("premium.grantStars")}
                  path="/api/actions/grant-stars"
                  disabled={!selectedUserID || !Number.isSafeInteger(parsedStars) || parsedStars <= 0}
                  payload={() => ({ user_id: selectedUserID, amount: parsedStars })} />
                <ActionButton compact tone="danger" icon={<Minus size={14} />} label={t("premium.debitStars")}
                  path="/api/actions/debit-stars"
                  disabled={!selectedUserID || !Number.isSafeInteger(parsedStars) || parsedStars <= 0}
                  payload={() => ({ user_id: selectedUserID, amount: parsedStars })} />
              </div>
            </div>
          </div>

          <div className="premium-quick-action premium">
            <div className="premium-quick-action-head">
              <span><Crown size={14} /></span><strong>{t("premium.months")}</strong>
            </div>
            <div className="premium-quick-action-control">
              <input aria-label={t("premium.months")} type="number" min={1} max={120} value={premiumMonths}
                onChange={(event) => setPremiumMonths(event.target.value)} />
              <div className="premium-action-submit">
                <ActionButton compact tone="neutral" icon={<Crown size={14} />} label={t("premium.grantPremium")}
                  path="/api/actions/grant-premium"
                  disabled={!selectedUserID || !Number.isInteger(parsedMonths) || parsedMonths <= 0 || parsedMonths > 120}
                  payload={() => ({ user_id: selectedUserID, months: parsedMonths })} />
              </div>
            </div>
          </div>
        </div>
      </section>

      <section className="surface premium-catalog-compact">
        <SectionHead title={t("premium.catalog")} text={t("premium.catalogHint")} action={
          <div className="premium-catalog-actions">
            <span className="premium-catalog-count">
              {t("premium.catalogSummary", { total: plans.length, enabled: enabledPlans })}
            </span>
            <button className="btn icon-text" type="button" onClick={() => setDraft(newPlan(t("premium.defaultPlanLabel")))}>
              <Plus size={15} /> {t("premium.addPlan")}
            </button>
          </div>
        } />
        {error && <Alert>{error}</Alert>}
        <div className="premium-plan-list">
          {plans.map((plan) => (
            <details className="premium-plan-card" key={plan.Months}>
              <summary>
                <span className="premium-plan-chevron"><ChevronDown size={16} /></span>
                <span className="premium-plan-identity">
                  <strong>{plan.Label}</strong>
                  <span>{t("premium.planTerm", { months: plan.Months, days: plan.DurationDays })}</span>
                </span>
                <span className="premium-plan-summary-value">
                  <small>{t("premium.amountStars")}</small><strong>★ {plan.AmountStars}</strong>
                </span>
                <span className="premium-plan-summary-value">
                  <small>{t("premium.fiatPrice")}</small><strong>{plan.FiatCurrency} {plan.FiatAmount}</strong>
                </span>
                <Badge tone={plan.ManagedBy === "admin" ? "good" : "neutral"}>
                  {plan.ManagedBy === "admin" ? t("premium.sourceAdmin") : t("premium.sourceConfig")}
                </Badge>
                <span className={`premium-plan-state ${plan.Enabled ? "enabled" : ""}`}>
                  <i />{plan.Enabled ? t("common.enabled") : t("common.disabled")}
                </span>
                <span className="premium-plan-edit"><Settings2 size={14} />{t("premium.editPlan")}</span>
              </summary>
              <div className="premium-plan-editor">
                <PlanFields plan={plan} onChange={(update) => patch(plan.Months, update)} />
                <footer className="premium-plan-footer">
                  <div className="premium-plan-meta">
                    <span>{t("premium.source")}: <strong>{plan.ManagedBy === "admin" ? t("premium.sourceAdmin") : t("premium.sourceConfig")}</strong></span>
                    <span>{t("premium.version")}: <strong>{plan.Version}</strong></span>
                  </div>
                  <label className="gift-switch">
                    <input type="checkbox" checked={plan.Enabled}
                      onChange={(event) => patch(plan.Months, { Enabled: event.target.checked })} />
                    <span className="gift-switch-track"><span /></span>
                    {plan.Enabled ? t("common.enabled") : t("common.disabled")}
                  </label>
                  <ActionButton compact tone="neutral" icon={<Save size={13} />} label={t("premium.save")}
                    path="/api/actions/upsert-premium-plan" disabled={!validPlan(plan)}
                    payload={() => payload(plan)} onDone={() => void load()} />
                </footer>
              </div>
            </details>
          ))}
          {!loading && plans.length === 0 && <table><tbody><EmptyRow colSpan={1} /></tbody></table>}
          {loading && <div className="loading-line">{t("common.loading")}</div>}
        </div>
      </section>

      {draft && createPortal(
        <div className="modal-backdrop" role="presentation">
          <section className="modal command-modal premium-plan-modal" role="dialog" aria-modal="true" aria-label={t("premium.newPlan")}>
            <div className="modal-head">
              <div>
                <div className="eyebrow">{t("premium.eyebrow")}</div>
                <h2>{t("premium.newPlan")}</h2>
                <p>{t("premium.newPlanHint")}</p>
              </div>
              <button className="icon-btn" type="button" onClick={() => setDraft(null)} aria-label={t("common.close")}>
                <X size={15} />
              </button>
            </div>
            <div className="command-body premium-plan-modal-body">
              <PlanFields plan={draft} create onChange={(update) => setDraft({ ...draft, ...update })} />
            </div>
            <div className="modal-actions">
              <button className="btn" type="button" onClick={() => setDraft(null)}>{t("common.close")}</button>
              <ActionButton compact tone="neutral" icon={<Plus size={14} />} label={t("premium.create")}
                path="/api/actions/upsert-premium-plan" disabled={!validPlan(draft)} payload={() => payload(draft)}
                onDone={() => { setDraft(null); void load(); }} />
            </div>
          </section>
        </div>,
        document.body
      )}
    </PageFrame>
  );
}
