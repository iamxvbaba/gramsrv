import {
  Ban,
  BadgeCheck,
  Building2,
  ChevronDown,
  ChevronRight,
  ExternalLink,
  Loader2,
  Plus,
  Power,
  PowerOff,
  RefreshCw,
  Search,
  Stamp,
  Sticker,
  Trash2
} from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { api, APIError, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { BotPicker } from "../components/EntityPicker";
import { Alert, Badge, EmptyRow, Metric, PageFrame, QueryPanel, SectionHead } from "../components/ui";
import { useI18n } from "../i18n";
import { displayUsername, formatDate } from "../lib/format";
import {
  permissionBotVerificationManage,
  permissionVerificationReview,
  usePermissions
} from "../permissions";
import type { Navigate } from "../routing";
import type {
  BotRow,
  BotVerificationPeerType,
  BotVerifierRow,
  CustomVerificationRequestRow,
  CustomVerificationRequestStatus,
  CustomVerificationRow,
  VerificationIconRow
} from "../types";

type Tab = "requests" | "verifiers" | "icons" | "marks";
type StatusFilter = "all" | CustomVerificationRequestStatus;
type PeerTypeFilter = "all" | BotVerificationPeerType;

const statuses: CustomVerificationRequestStatus[] = ["pending", "approved", "rejected", "revoked"];
const peerTypes: BotVerificationPeerType[] = ["user", "channel"];

// The section owns four different objects — applications, verifiers, the icon
// catalogue and the granted marks — and mixing them into one table would hide which
// row an action addresses. They are separate tabs over one shared verifier/icon
// load: the roster feeds three of the four filters, so it is fetched once here
// rather than per tab.
export function BotVerificationPage({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const { can } = usePermissions();
  const canManage = can(permissionBotVerificationManage);
  const canSeeOfficial = can(permissionVerificationReview);
  const [tab, setTab] = useState<Tab>("requests");
  const [verifiers, setVerifiers] = useState<BotVerifierRow[]>([]);
  const [icons, setIcons] = useState<VerificationIconRow[]>([]);
  const [error, setError] = useState("");
  const [rosterDenied, setRosterDenied] = useState(false);

  async function loadRoster() {
    setError("");
    setRosterDenied(false);
    try {
      const [verifierResult, iconResult] = await Promise.all([
        api.botVerifiers(new URLSearchParams({ limit: "200" })),
        api.verificationIcons(new URLSearchParams({ limit: "200" }))
      ]);
      setVerifiers(verifierResult.rows ?? []);
      setIcons(iconResult.rows ?? []);
    } catch (err) {
      // A 403 here is not a fault to alarm about: it means the session may review
      // applications but not see the roster. Saying so beats an empty table that
      // reads as "no verifiers configured".
      if (err instanceof APIError && err.status === 403) {
        setVerifiers([]);
        setIcons([]);
        setRosterDenied(true);
        return;
      }
      setError(errorMessage(err));
    }
  }

  useEffect(() => {
    void loadRoster();
  }, []);

  const tabs: Array<{ key: Tab; label: string; icon: ReactNode }> = [
    { key: "requests", label: t("botverification.tabRequests"), icon: <Stamp size={15} /> },
    { key: "verifiers", label: t("botverification.tabVerifiers"), icon: <Building2 size={15} /> },
    { key: "icons", label: t("botverification.tabIcons"), icon: <Sticker size={15} /> },
    { key: "marks", label: t("botverification.tabMarks"), icon: <BadgeCheck size={15} /> }
  ];

  return (
    <PageFrame
      title={t("botverification.pageTitle")}
      eyebrow={t("botverification.eyebrow")}
      actions={
        canSeeOfficial ? (
          <button className="btn icon-text" type="button" onClick={() => navigate("/verification")}>
            <ExternalLink size={15} /> {t("botverification.openOfficial")}
          </button>
        ) : undefined
      }
    >
      {error && <Alert>{error}</Alert>}
      {rosterDenied && <Alert>{t("botverification.rosterDenied")}</Alert>}
      {/* The one thing an operator has to understand before touching anything here:
          this is a verifier company's own icon, not the platform checkmark. */}
      <section className="section-block">
        <SectionHead title={t("botverification.explainTitle")} text={t("botverification.explainText")} />
        <p className="bot-create-note">{t("botverification.explainIcon")}</p>
        <p className="bot-create-note">{t("botverification.explainOfficial")}</p>
        {!canManage && <p className="bot-create-note">{t("botverification.manageMissing")}</p>}
      </section>

      <div className="toolbar" role="group" aria-label={t("botverification.pageTitle")}>
        {tabs.map((item) => (
          <button
            key={item.key}
            className={`btn icon-text ${tab === item.key ? "primary" : ""}`}
            type="button"
            aria-pressed={tab === item.key}
            onClick={() => setTab(item.key)}
          >
            {item.icon} {item.label}
          </button>
        ))}
      </div>

      {tab === "requests" && <RequestsBlock navigate={navigate} verifiers={verifiers} />}
      {tab === "verifiers" && (
        <VerifiersBlock
          verifiers={verifiers}
          icons={icons}
          canManage={canManage}
          onChanged={loadRoster}
          navigate={navigate}
        />
      )}
      {tab === "icons" && (
        <IconsBlock icons={icons} verifiers={verifiers} canManage={canManage} onChanged={loadRoster} />
      )}
      {tab === "marks" && <MarksBlock verifiers={verifiers} canManage={canManage} navigate={navigate} />}
    </PageFrame>
  );
}

// ---------------------------------------------------------------------------
// Applications
// ---------------------------------------------------------------------------

function RequestsBlock({ navigate, verifiers }: { navigate: Navigate; verifiers: BotVerifierRow[] }) {
  const { t } = useI18n();
  const [status, setStatus] = useState<StatusFilter>("pending");
  const [verifierBotID, setVerifierBotID] = useState("");
  const [peerType, setPeerType] = useState<PeerTypeFilter>("all");
  const [q, setQ] = useState("");
  const [limit, setLimit] = useState("50");
  const [rows, setRows] = useState<CustomVerificationRequestRow[]>([]);
  const [counts, setCounts] = useState<Record<string, string>>({});
  const [hasMore, setHasMore] = useState(false);
  const [cursor, setCursor] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  // One free-text field: the backend matches the application id, the peer id and a
  // username (applicant or peer), so "@durov", "42" and a peer id all work without a
  // mode switch.
  async function load(next = false) {
    setBusy(true);
    setError("");
    const params = new URLSearchParams({ limit });
    if (status !== "all") params.set("status", status);
    if (verifierBotID) params.set("verifier_bot_id", verifierBotID);
    if (peerType !== "all") params.set("peer_type", peerType);
    if (q.trim()) params.set("q", q.trim().replace(/^@/, ""));
    if (next && cursor) params.set("before_id", cursor);
    try {
      const result = await api.customVerificationRequests(params);
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

  // The counts describe the whole queue, not the current page, so they are fetched
  // separately from the keyset listing.
  async function loadCounts() {
    try {
      const result = await api.botVerificationCounts();
      setCounts(result.counts ?? {});
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  useEffect(() => {
    void load(false);
    void loadCounts();
  }, []);

  function refresh() {
    void load(false);
    void loadCounts();
  }

  return (
    <>
      <section className="section-block">
        <SectionHead
          title={t("botverification.queueTitle")}
          text={t("botverification.queueHint")}
          action={
            <button className="btn icon-text" type="button" onClick={refresh} disabled={busy}>
              <RefreshCw size={15} className={busy ? "spin" : ""} /> {t("common.refresh")}
            </button>
          }
        />
        {error && <Alert>{error}</Alert>}
        <div className="metric-row">
          {statuses.map((item) => (
            <Metric
              key={item}
              label={t(`botverification.status.${item}`)}
              value={counts[item] ?? "0"}
              mono
              tone={countTone(item, counts[item] ?? "0")}
            />
          ))}
        </div>
      </section>

      <QueryPanel>
        <form className="toolbar" onSubmit={(event) => { event.preventDefault(); void load(false); }}>
          <label className="searchbox">
            <Search size={15} />
            <input value={q} onChange={(event) => setQ(event.target.value)} placeholder={t("botverification.searchPlaceholder")} />
          </label>
          <label className="field-inline">
            <span>{t("common.status")}</span>
            <select value={status} onChange={(event) => setStatus(event.target.value as StatusFilter)}>
              <option value="all">{t("botverification.statusAll")}</option>
              {statuses.map((item) => (
                <option key={item} value={item}>{t(`botverification.status.${item}`)}</option>
              ))}
            </select>
          </label>
          <label className="field-inline">
            <span>{t("botverification.verifier")}</span>
            <VerifierOptions value={verifierBotID} verifiers={verifiers} onChange={setVerifierBotID} />
          </label>
          <label className="field-inline">
            <span>{t("botverification.peerType")}</span>
            <select value={peerType} onChange={(event) => setPeerType(event.target.value as PeerTypeFilter)}>
              <option value="all">{t("botverification.peerTypeAll")}</option>
              {peerTypes.map((item) => (
                <option key={item} value={item}>{t(`botverification.peer.${item}`)}</option>
              ))}
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
              <th>{t("common.id")}</th>
              <th>{t("botverification.verifier")}</th>
              <th>{t("botverification.target")}</th>
              <th>{t("botverification.applicant")}</th>
              <th>{t("botverification.reason")}</th>
              <th>{t("common.status")}</th>
              <th>{t("botverification.createdAt")}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.ID}>
                <td className="mono">
                  <button className="row-link" type="button" onClick={() => navigate(`/bot-verification/${row.ID}`)}>
                    #{row.ID}
                  </button>
                </td>
                <td>
                  <strong>{displayUsername(row.VerifierBotUsername) || row.VerifierBotID}</strong>
                  <div className="entity-subtitle mono">{row.VerifierBotID}</div>
                </td>
                <td>
                  <strong>{peerLabel(row)}</strong>
                  <div className="entity-subtitle mono">
                    {t(`botverification.peer.${row.PeerType}`)} · {row.PeerID}
                  </div>
                </td>
                <td>
                  {displayUsername(row.ApplicantUsername) || "-"}
                  <div className="entity-subtitle mono">{row.ApplicantUserID}</div>
                </td>
                <td className="truncate">{row.Reason || "-"}</td>
                <td><RequestStatusBadge status={row.Status} /></td>
                <td>{formatDate(row.CreatedAt) || "-"}</td>
                <td>
                  <button className="row-link" type="button" onClick={() => navigate(`/bot-verification/${row.ID}`)}>
                    <Stamp size={14} /> {t("common.detail")} <ChevronRight size={14} />
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
    </>
  );
}

// ---------------------------------------------------------------------------
// Verifiers
// ---------------------------------------------------------------------------

function VerifiersBlock({
  verifiers,
  icons,
  canManage,
  onChanged,
  navigate
}: {
  verifiers: BotVerifierRow[];
  icons: VerificationIconRow[];
  canManage: boolean;
  onChanged: () => void;
  navigate: Navigate;
}) {
  const { t } = useI18n();
  const [bot, setBot] = useState<BotRow | null>(null);
  // editing carries the bot id of the row being updated: the grant endpoint is an
  // upsert, and version is the optimistic lock of the row it overwrites. A fresh
  // grant sends "0", which is what "there is no row yet" means.
  const [editing, setEditing] = useState<BotVerifierRow | null>(null);
  const [iconDocumentID, setIconDocumentID] = useState("");
  const [company, setCompany] = useState("");
  const [defaultDescription, setDefaultDescription] = useState("");
  const [canModify, setCanModify] = useState(false);
  const activeIcons = icons.filter((icon) => icon.Active);
  // A verifier can hold an icon the operator has since retired. Editing that row must
  // not silently swap the icon just because the select has no matching option, so the
  // current document is kept in the list and labelled instead.
  const iconOptions: Array<{ value: string; label: string }> = activeIcons.map((icon) => ({
    value: icon.DocumentID,
    label: `${icon.Name} · ${icon.DocumentID}`
  }));
  if (iconDocumentID && !iconOptions.some((option) => option.value === iconDocumentID)) {
    const retired = icons.find((icon) => icon.DocumentID === iconDocumentID);
    iconOptions.unshift({
      value: iconDocumentID,
      label: `${retired?.Name ?? iconDocumentID} · ${iconDocumentID} (${t("botverification.iconInactive")})`
    });
  }

  function startEdit(row: BotVerifierRow) {
    setEditing(row);
    setBot(null);
    setIconDocumentID(row.IconDocumentID);
    setCompany(row.CompanyName);
    setDefaultDescription(row.DefaultDescription);
    setCanModify(row.CanModifyCustomDescription);
  }

  function resetForm() {
    setEditing(null);
    setBot(null);
    setIconDocumentID("");
    setCompany("");
    setDefaultDescription("");
    setCanModify(false);
  }

  // int64 fields go out as decimal strings (the backend tags them `,string`), which
  // is also the shape they arrived in, so nothing is re-parsed on the way back.
  function grantPayload(): Record<string, unknown> {
    const botID = editing ? editing.BotID : bot ? String(bot.ID) : "0";
    return {
      bot_id: botID,
      icon_document_id: iconDocumentID || "0",
      company_name: company.trim(),
      default_description: defaultDescription.trim(),
      can_modify_custom_description: canModify,
      version: editing ? editing.Version : "0"
    };
  }

  return (
    <>
      {canManage && (
        <section className="section-block">
          <SectionHead
            title={editing ? t("botverification.updateTitle") : t("botverification.grantTitle")}
            text={t("botverification.grantHint")}
            action={
              editing ? (
                <button className="btn icon-text" type="button" onClick={resetForm}>
                  {t("botverification.cancelEdit")}
                </button>
              ) : undefined
            }
          />
          {editing ? (
            <p className="bot-create-note">
              {t("botverification.editing", {
                bot: displayUsername(editing.BotUsername) || editing.BotID,
                version: editing.Version
              })}
            </p>
          ) : (
            <BotPicker label={t("botverification.grantBot")} value={bot} onChange={setBot} />
          )}
          <div className="bot-create-fields">
            <label className="duration-field">
              <span>{t("botverification.grantIcon")}</span>
              <select value={iconDocumentID} onChange={(event) => setIconDocumentID(event.target.value)}>
                <option value="">{t("botverification.grantIconPick")}</option>
                {iconOptions.map((option) => (
                  <option key={option.value} value={option.value}>{option.label}</option>
                ))}
              </select>
            </label>
            <label className="duration-field">
              <span>{t("botverification.company")}</span>
              <input
                value={company}
                onChange={(event) => setCompany(event.target.value)}
                placeholder={t("botverification.companyPlaceholder")}
              />
            </label>
            <label className="duration-field">
              <span>{t("botverification.defaultDescription")}</span>
              <input
                value={defaultDescription}
                onChange={(event) => setDefaultDescription(event.target.value)}
                placeholder={t("botverification.defaultDescriptionPlaceholder")}
              />
            </label>
          </div>
          <label className="checkline">
            <input type="checkbox" checked={canModify} onChange={(event) => setCanModify(event.target.checked)} />
            {t("botverification.canModify")}
          </label>
          <p className="bot-create-note">{t("botverification.canModifyHint")}</p>
          {activeIcons.length === 0 && <Alert>{t("botverification.noActiveIcons")}</Alert>}
          <div className="bot-create-actions">
            <span className="bot-create-note">{t("botverification.grantNote")}</span>
            <ActionButton
              label={editing ? t("botverification.update") : t("botverification.grant")}
              icon={<Plus size={15} />}
              tone="neutral"
              path="/api/actions/grant-bot-verifier"
              payload={grantPayload}
              onDone={() => {
                resetForm();
                onChanged();
              }}
            />
          </div>
        </section>
      )}

      <section className="section-block">
        <SectionHead
          title={t("botverification.verifiersTitle")}
          text={t("botverification.verifiersHint")}
          action={
            <button className="btn icon-text" type="button" onClick={onChanged}>
              <RefreshCw size={15} /> {t("common.refresh")}
            </button>
          }
        />
        <div className="table-wrap">
          <table className="data-table">
            <thead>
              <tr>
                <th>{t("botverification.bot")}</th>
                <th>{t("botverification.company")}</th>
                <th>{t("botverification.icon")}</th>
                <th>{t("botverification.canModifyShort")}</th>
                <th>{t("common.status")}</th>
                <th>{t("botverification.markCount")}</th>
                <th>{t("botverification.grantedBy")}</th>
                <th>{t("common.updatedAt")}</th>
                {canManage && <th></th>}
              </tr>
            </thead>
            <tbody>
              {verifiers.map((row) => (
                <tr key={row.BotID}>
                  <td>
                    <button className="row-link" type="button" onClick={() => navigate(`/bots/${row.BotID}`)}>
                      <strong>{displayUsername(row.BotUsername) || row.BotName || row.BotID}</strong>
                    </button>
                    <div className="entity-subtitle mono">{row.BotID}</div>
                  </td>
                  <td>
                    <strong>{row.CompanyName || "-"}</strong>
                    <div className="entity-subtitle truncate">{row.DefaultDescription || t("botverification.notProvided")}</div>
                  </td>
                  <td>
                    {row.IconName || "-"}
                    <div className="entity-subtitle mono">{row.IconDocumentID}</div>
                  </td>
                  <td>{row.CanModifyCustomDescription ? t("common.yes") : t("common.no")}</td>
                  <td>
                    {row.Enabled
                      ? <Badge tone="good">{t("botverification.enabled")}</Badge>
                      : <Badge tone="warn">{t("botverification.disabled")}</Badge>}
                  </td>
                  <td className="mono">{String(row.MarkCount ?? "0")}</td>
                  <td>
                    {row.GrantedBy || "-"}
                    <div className="entity-subtitle truncate">{row.GrantReason || "-"}</div>
                  </td>
                  <td>{formatDate(row.UpdatedAt) || "-"}</td>
                  {canManage && (
                    <td>
                      <div className="row-actions">
                        <button className="btn compact-btn" type="button" onClick={() => startEdit(row)}>
                          {t("botverification.edit")}
                        </button>
                        <ActionButton
                          label={row.Enabled ? t("botverification.disable") : t("botverification.enable")}
                          icon={row.Enabled ? <PowerOff size={14} /> : <Power size={14} />}
                          tone={row.Enabled ? "warn" : "neutral"}
                          compact
                          path="/api/actions/set-bot-verifier-enabled"
                          payload={() => ({ bot_id: row.BotID, enabled: !row.Enabled })}
                          onDone={onChanged}
                        />
                        <ActionButton
                          label={t("botverification.revokeVerifier")}
                          icon={<Trash2 size={14} />}
                          tone="danger"
                          compact
                          path="/api/actions/revoke-bot-verifier"
                          payload={() => ({ bot_id: row.BotID })}
                          onDone={onChanged}
                        />
                      </div>
                    </td>
                  )}
                </tr>
              ))}
              {verifiers.length === 0 && <EmptyRow colSpan={canManage ? 9 : 8} />}
            </tbody>
          </table>
        </div>
        <p className="bot-create-note">{t("botverification.disableHint")}</p>
        <p className="bot-create-note">{t("botverification.revokeVerifierHint")}</p>
      </section>
    </>
  );
}

// ---------------------------------------------------------------------------
// Icon catalogue
// ---------------------------------------------------------------------------

function IconsBlock({
  icons,
  verifiers,
  canManage,
  onChanged
}: {
  icons: VerificationIconRow[];
  verifiers: BotVerifierRow[];
  canManage: boolean;
  onChanged: () => void;
}) {
  const { t } = useI18n();
  const [documentID, setDocumentID] = useState("");
  const [name, setName] = useState("");
  const [ownerBotID, setOwnerBotID] = useState("");

  // owner_bot_id is omitted entirely for a shared entry rather than sent as "" —
  // `,string,omitempty` cannot decode an empty string.
  function iconPayload(): Record<string, unknown> {
    const payload: Record<string, unknown> = {
      document_id: documentID.trim() || "0",
      name: name.trim()
    };
    if (ownerBotID) payload.owner_bot_id = ownerBotID;
    return payload;
  }

  return (
    <>
      {canManage && (
        <section className="section-block">
          <SectionHead title={t("botverification.addIconTitle")} text={t("botverification.addIconHint")} />
          <div className="bot-create-fields">
            <label className="duration-field">
              <span>{t("botverification.iconDocument")}</span>
              <input
                value={documentID}
                onChange={(event) => setDocumentID(event.target.value)}
                inputMode="numeric"
                placeholder="5361371319611781774"
              />
            </label>
            <label className="duration-field">
              <span>{t("botverification.iconName")}</span>
              <input
                value={name}
                onChange={(event) => setName(event.target.value)}
                placeholder={t("botverification.iconNamePlaceholder")}
              />
            </label>
            <label className="duration-field">
              <span>{t("botverification.iconOwner")}</span>
              <select value={ownerBotID} onChange={(event) => setOwnerBotID(event.target.value)}>
                <option value="">{t("botverification.iconOwnerShared")}</option>
                {verifiers.map((row) => (
                  <option key={row.BotID} value={row.BotID}>
                    {`${row.CompanyName || row.BotID} · ${displayUsername(row.BotUsername) || row.BotID}`}
                  </option>
                ))}
              </select>
            </label>
          </div>
          <p className="bot-create-note">{t("botverification.iconDocumentHint")}</p>
          <p className="bot-create-note">{t("botverification.iconOwnerHint")}</p>
          <div className="bot-create-actions">
            <span className="bot-create-note">{t("botverification.addIconNote")}</span>
            <ActionButton
              label={t("botverification.addIcon")}
              icon={<Plus size={15} />}
              tone="neutral"
              path="/api/actions/upsert-verification-icon"
              payload={iconPayload}
              onDone={() => {
                setDocumentID("");
                setName("");
                setOwnerBotID("");
                onChanged();
              }}
            />
          </div>
        </section>
      )}

      <section className="section-block">
        <SectionHead
          title={t("botverification.iconsTitle")}
          text={t("botverification.iconsHint")}
          action={
            <button className="btn icon-text" type="button" onClick={onChanged}>
              <RefreshCw size={15} /> {t("common.refresh")}
            </button>
          }
        />
        <div className="table-wrap">
          <table className="data-table">
            <thead>
              <tr>
                <th>{t("botverification.iconDocument")}</th>
                <th>{t("botverification.iconName")}</th>
                <th>{t("botverification.iconOwner")}</th>
                <th>{t("common.status")}</th>
                <th>{t("botverification.usedBy")}</th>
                <th>{t("botverification.createdAt")}</th>
                {canManage && <th></th>}
              </tr>
            </thead>
            <tbody>
              {icons.map((row) => (
                <tr key={row.ID}>
                  <td className="mono">{row.DocumentID}</td>
                  <td><strong>{row.Name || "-"}</strong></td>
                  <td>
                    {row.OwnerBotID && row.OwnerBotID !== "0"
                      ? <>
                          {displayUsername(row.OwnerBotUsername) || row.OwnerBotID}
                          <div className="entity-subtitle mono">{row.OwnerBotID}</div>
                        </>
                      : <Badge>{t("botverification.iconOwnerShared")}</Badge>}
                  </td>
                  <td>
                    {row.Active
                      ? <Badge tone="good">{t("botverification.iconActive")}</Badge>
                      : <Badge tone="warn">{t("botverification.iconInactive")}</Badge>}
                  </td>
                  <td className="mono">{String(row.UsedByVerifiers ?? "0")}</td>
                  <td>{formatDate(row.CreatedAt) || "-"}</td>
                  {canManage && (
                    <td>
                      <div className="row-actions">
                        <ActionButton
                          label={row.Active ? t("botverification.deactivateIcon") : t("botverification.activateIcon")}
                          icon={row.Active ? <PowerOff size={14} /> : <Power size={14} />}
                          tone={row.Active ? "warn" : "neutral"}
                          compact
                          path="/api/actions/set-verification-icon-active"
                          payload={() => ({ icon_id: row.ID, active: !row.Active })}
                          onDone={onChanged}
                        />
                      </div>
                    </td>
                  )}
                </tr>
              ))}
              {icons.length === 0 && <EmptyRow colSpan={canManage ? 7 : 6} />}
            </tbody>
          </table>
        </div>
        <p className="bot-create-note">{t("botverification.deactivateIconHint")}</p>
      </section>
    </>
  );
}

// ---------------------------------------------------------------------------
// Granted marks
// ---------------------------------------------------------------------------

function MarksBlock({
  verifiers,
  canManage,
  navigate
}: {
  verifiers: BotVerifierRow[];
  canManage: boolean;
  navigate: Navigate;
}) {
  const { t } = useI18n();
  const [verifierBotID, setVerifierBotID] = useState("");
  const [peerType, setPeerType] = useState<PeerTypeFilter>("all");
  const [q, setQ] = useState("");
  const [limit, setLimit] = useState("50");
  const [rows, setRows] = useState<CustomVerificationRow[]>([]);
  const [hasMore, setHasMore] = useState(false);
  const [cursor, setCursor] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function load(next = false) {
    setBusy(true);
    setError("");
    const params = new URLSearchParams({ limit });
    if (verifierBotID) params.set("verifier_bot_id", verifierBotID);
    if (peerType !== "all") params.set("peer_type", peerType);
    if (q.trim()) params.set("q", q.trim().replace(/^@/, ""));
    if (next && cursor) params.set("before_id", cursor);
    try {
      const result = await api.customVerifications(params);
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

  return (
    <>
      <section className="section-block">
        <SectionHead
          title={t("botverification.marksTitle")}
          text={t("botverification.marksHint")}
          action={
            <button className="btn icon-text" type="button" onClick={() => load(false)} disabled={busy}>
              <RefreshCw size={15} className={busy ? "spin" : ""} /> {t("common.refresh")}
            </button>
          }
        />
        {error && <Alert>{error}</Alert>}
      </section>

      <QueryPanel>
        <form className="toolbar" onSubmit={(event) => { event.preventDefault(); void load(false); }}>
          <label className="searchbox">
            <Search size={15} />
            <input value={q} onChange={(event) => setQ(event.target.value)} placeholder={t("botverification.markSearchPlaceholder")} />
          </label>
          <label className="field-inline">
            <span>{t("botverification.verifier")}</span>
            <VerifierOptions value={verifierBotID} verifiers={verifiers} onChange={setVerifierBotID} />
          </label>
          <label className="field-inline">
            <span>{t("botverification.peerType")}</span>
            <select value={peerType} onChange={(event) => setPeerType(event.target.value as PeerTypeFilter)}>
              <option value="all">{t("botverification.peerTypeAll")}</option>
              {peerTypes.map((item) => (
                <option key={item} value={item}>{t(`botverification.peer.${item}`)}</option>
              ))}
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
              <th>{t("common.id")}</th>
              <th>{t("botverification.verifier")}</th>
              <th>{t("botverification.target")}</th>
              <th>{t("botverification.description")}</th>
              <th>{t("botverification.icon")}</th>
              <th>{t("botverification.createdAt")}</th>
              {canManage && <th></th>}
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.ID}>
                <td className="mono">#{row.ID}</td>
                <td>
                  <strong>{row.CompanyName || displayUsername(row.VerifierBotUsername) || row.VerifierBotID}</strong>
                  <div className="entity-subtitle mono">
                    {displayUsername(row.VerifierBotUsername) || row.VerifierBotID}
                  </div>
                </td>
                <td>
                  <button className="row-link" type="button" onClick={() => navigate(peerHref(row.PeerType, row.PeerID))}>
                    <strong>{peerLabel(row)}</strong>
                  </button>
                  <div className="entity-subtitle mono">
                    {t(`botverification.peer.${row.PeerType}`)} · {row.PeerID}
                  </div>
                </td>
                <td className="truncate">{row.Description || t("botverification.notProvided")}</td>
                <td className="mono">{row.IconDocumentID}</td>
                <td>{formatDate(row.CreatedAt) || "-"}</td>
                {canManage && (
                  <td>
                    <div className="row-actions">
                      <ActionButton
                        label={t("botverification.revokeMark")}
                        icon={<Ban size={14} />}
                        tone="danger"
                        compact
                        path="/api/actions/revoke-custom-verification"
                        payload={() => ({
                          verifier_bot_id: row.VerifierBotID,
                          peer_type: row.PeerType,
                          peer_id: row.PeerID
                        })}
                        onDone={() => load(false)}
                      />
                    </div>
                  </td>
                )}
              </tr>
            ))}
            {rows.length === 0 && <EmptyRow colSpan={canManage ? 7 : 6} />}
          </tbody>
        </table>
      </div>
      <p className="bot-create-note">{t("botverification.revokeMarkHint")}</p>
      {hasMore && (
        <div className="toolbar">
          <button className="btn icon-text" type="button" onClick={() => load(true)} disabled={busy}>
            {busy ? <Loader2 size={15} className="spin" /> : <ChevronDown size={15} />} {t("common.loadMore")}
          </button>
        </div>
      )}
    </>
  );
}

// ---------------------------------------------------------------------------
// Shared bits
// ---------------------------------------------------------------------------

// The verifier filter lists the roster rather than asking for a bot id: a company
// name is what an operator reads in the queue, and a disabled verifier still owns
// rows worth filtering by, so it stays in the list and is labelled instead.
function VerifierOptions({
  value,
  verifiers,
  onChange
}: {
  value: string;
  verifiers: BotVerifierRow[];
  onChange: (value: string) => void;
}) {
  const { t } = useI18n();
  return (
    <select value={value} onChange={(event) => onChange(event.target.value)}>
      <option value="">{t("botverification.verifierAll")}</option>
      {verifiers.map((row) => (
        <option key={row.BotID} value={row.BotID}>
          {`${row.CompanyName || row.BotID} · ${displayUsername(row.BotUsername) || row.BotID}`
            + (row.Enabled ? "" : ` (${t("botverification.disabled")})`)}
        </option>
      ))}
    </select>
  );
}

export function RequestStatusBadge({ status }: { status: CustomVerificationRequestStatus }) {
  const { t } = useI18n();
  return <Badge tone={statusTone(status)}>{t(`botverification.status.${status}`)}</Badge>;
}

export function statusTone(status: CustomVerificationRequestStatus): "neutral" | "good" | "warn" | "danger" {
  if (status === "approved") return "good";
  if (status === "pending") return "warn";
  if (status === "rejected") return "danger";
  return "neutral";
}

// pending is the only status that waits for somebody, so it is the only one
// highlighted — and only while something actually sits in it.
function countTone(status: CustomVerificationRequestStatus, count: string): "neutral" | "good" | "warn" {
  if (status === "pending") return count !== "0" && count !== "" ? "warn" : "neutral";
  return status === "approved" ? "good" : "neutral";
}

export function peerLabel(row: { PeerUsername: string; PeerTitle: string; PeerID: string }): string {
  return displayUsername(row.PeerUsername) || row.PeerTitle || `#${row.PeerID}`;
}

// The panel page that owns the peer type. A third-party mark can sit on an ordinary
// account or on a bot — both are user rows, so both open the account page.
export function peerHref(peerType: BotVerificationPeerType, peerID: string): string {
  return peerType === "channel" ? `/channels/${peerID}` : `/accounts/${peerID}`;
}
