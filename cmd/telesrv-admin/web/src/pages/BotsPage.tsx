import { BadgeCheck, Bot, ChevronRight, Loader2, Plus, RefreshCw, Search } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { Alert, Badge, EmptyRow, Metric, PageFrame, QueryPanel } from "../components/ui";
import { ScamFakeBadges } from "../components/flags";
import { useI18n } from "../i18n";
import { displayUsername, formatDate, toInt } from "../lib/format";
import type { Navigate } from "../routing";
import type { BotListResponse } from "../types";

export function BotsPage({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const [q, setQ] = useState("");
  const [limit, setLimit] = useState("50");
  const [data, setData] = useState<BotListResponse | null>(null);
  const [cursor, setCursor] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const [ownerID, setOwnerID] = useState("");
  const [botName, setBotName] = useState("");
  const [botUsername, setBotUsername] = useState("");

  async function load(next = false) {
    setBusy(true);
    setError("");
    const params = new URLSearchParams({ limit });
    if (q.trim()) {
      params.set("q", q.trim());
    } else if (next) {
      params.set("before_id", String(cursor));
    }
    try {
      const result = await api.bots(params);
      setData(result);
      setCursor(result.next_before_id);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    void load(false);
  }, []);

  const rows = data?.rows ?? [];
  const verified = rows.filter((row) => row.Verified).length;
  const systemCount = rows.filter((row) => row.System).length;

  return (
    <PageFrame
      title={t("bots.pageTitle")}
      eyebrow={data?.listing === false ? t("bots.queryResults") : t("bots.recent")}
      actions={
        <button className="btn" type="button" onClick={() => load(false)} disabled={busy}>
          <RefreshCw size={15} /> {t("common.refresh")}
        </button>
      }
    >
      {error && <Alert>{error}</Alert>}
      <div className="metric-row">
        <Metric label={t("bots.currentPage")} value={String(rows.length)} />
        <Metric label={t("common.verified")} value={String(verified)} tone="good" />
        <Metric label={t("bots.system")} value={String(systemCount)} />
      </div>

      <section className="section-block">
        <div className="section-head">
          <div>
            <h2>{t("bots.createTitle")}</h2>
            <p>{t("bots.createHint")}</p>
          </div>
        </div>
        <div className="bot-create-fields">
          <label className="duration-field">
            <span>{t("bots.ownerUserID")}</span>
            <input
              value={ownerID}
              onChange={(event) => setOwnerID(event.target.value)}
              type="number"
              min="1"
              placeholder="123456789"
            />
          </label>
          <label className="duration-field">
            <span>{t("bots.name")}</span>
            <input value={botName} onChange={(event) => setBotName(event.target.value)} placeholder={t("bots.namePlaceholder")} maxLength={64} />
          </label>
          <label className="duration-field">
            <span>{t("bots.username")}</span>
            <input value={botUsername} onChange={(event) => setBotUsername(event.target.value)} placeholder="my_service_bot" />
          </label>
        </div>
        <div className="bot-create-actions">
          <span className="bot-create-note">{t("bots.usernameHint")}</span>
          <ActionButton
            label={t("bots.create")}
            icon={<Plus size={15} />}
            tone="neutral"
            path="/api/actions/create-bot"
            payload={() => ({
              owner_user_id: toInt(ownerID),
              name: botName.trim(),
              username: botUsername.trim().replace(/^@/, "")
            })}
            onDone={() => load(false)}
          />
        </div>
      </section>

      <QueryPanel>
        <form className="toolbar" onSubmit={(event) => { event.preventDefault(); void load(false); }}>
          <label className="searchbox">
            <Search size={15} />
            <input value={q} onChange={(event) => setQ(event.target.value)} placeholder={t("bots.searchPlaceholder")} />
          </label>
          <label className="field-inline">
            <span>{t("common.limit")}</span>
            <input className="small-input" value={limit} onChange={(event) => setLimit(event.target.value)} type="number" min="1" max="100" />
          </label>
          <button className="btn primary icon-text" type="submit" disabled={busy}>
            {busy ? <Loader2 size={15} className="spin" /> : <Search size={15} />} {t("common.search")}
          </button>
          {data?.listing && data.has_more && (
            <button className="btn icon-text" type="button" onClick={() => load(true)} disabled={busy}>
              <ChevronRight size={15} /> {t("messages.nextPage")}
            </button>
          )}
        </form>
      </QueryPanel>

      <div className="table-wrap">
        <table className="data-table">
          <thead>
            <tr>
              <th>{t("bots.botID")}</th>
              <th>{t("common.username")}</th>
              <th>{t("common.name")}</th>
              <th>{t("bots.owner")}</th>
              <th>{t("common.verified")}</th>
              <th>{t("bots.type")}</th>
              <th>{t("account.createdAt")}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.ID}>
                <td className="mono">{row.ID}</td>
                <td>{displayUsername(row.Username) || "-"}</td>
                <td>{row.FirstName || "-"}</td>
                <td className="mono">{row.OwnerUserID > 0 ? row.OwnerUserID : "-"}</td>
                <td>{row.Verified ? <Badge tone="good"><BadgeCheck size={12} /> {t("common.verified")}</Badge> : <Badge>{t("account.notVerified")}</Badge>} <ScamFakeBadges scam={row.Scam} fake={row.Fake} /></td>
                <td>{row.System ? <Badge tone="warn">{t("bots.system")}</Badge> : <Badge>{t("bots.user")}</Badge>}</td>
                <td>{formatDate(row.CreatedAt)}</td>
                <td><button className="row-link" onClick={() => navigate(`/bots/${row.ID}`)}><Bot size={14} /> {t("common.detail")} <ChevronRight size={14} /></button></td>
              </tr>
            ))}
            {rows.length === 0 && <EmptyRow colSpan={8} />}
          </tbody>
        </table>
      </div>
    </PageFrame>
  );
}
