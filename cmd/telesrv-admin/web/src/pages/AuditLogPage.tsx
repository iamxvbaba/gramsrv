import { ChevronDown, ChevronRight, Search } from "lucide-react";
import { useState } from "react";
import { api, errorMessage } from "../api";
import { Alert, EmptyRow, JsonBlock, LoadingSurface, PageFrame, QueryPanel } from "../components/ui";
import { useI18n } from "../i18n";
import { formatDate } from "../lib/format";
import type { AuditLogEntry } from "../types";

// The global action trail. Every command run through the panel lands here --
// who ran it, what it was aimed at, whether it was a dry run, why, and what it
// actually did. The actions themselves are read-only; this page never writes.
// Viewing it is gated on audit.read server-side, so an operator with the right
// to do something still cannot read what their colleagues did without its own
// permission.
export function AuditLogPage() {
  const { t } = useI18n();
  const [actor, setActor] = useState("");
  const [action, setAction] = useState("");
  const [status, setStatus] = useState("");
  const [limit, setLimit] = useState("100");
  const [rows, setRows] = useState<AuditLogEntry[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [expanded, setExpanded] = useState<Record<number, boolean>>({});

  async function load() {
    setBusy(true);
    setError("");
    try {
      const params = new URLSearchParams();
      if (actor.trim()) {
        params.set("actor", actor.trim());
      }
      if (action.trim()) {
        params.set("action", action.trim());
      }
      if (status) {
        params.set("status", status);
      }
      const n = parseInt(limit, 10);
      if (!Number.isNaN(n) && n > 0) {
        params.set("limit", String(Math.min(n, 200)));
      }
      const result = await api.auditLogs(params);
      setRows(result.rows ?? []);
      setExpanded({});
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  function toggleRow(id: number) {
    setExpanded((current) => ({ ...current, [id]: !current[id] }));
  }

  return (
    <PageFrame eyebrow={t("auditPage.eyebrow")} title={t("auditPage.title")}>
      {error && <Alert>{error}</Alert>}

      <QueryPanel>
        <div className="toolbar filter-toolbar">
          <label className="form-field">
            <span>{t("auditPage.actor")}</span>
            <input
              type="text"
              value={actor}
              spellCheck={false}
              onChange={(event) => setActor(event.target.value)}
            />
          </label>
          <label className="form-field">
            <span>{t("auditPage.action")}</span>
            <input
              type="text"
              value={action}
              spellCheck={false}
              onChange={(event) => setAction(event.target.value)}
            />
          </label>
          <label className="form-field">
            <span>{t("auditPage.status")}</span>
            <select value={status} onChange={(event) => setStatus(event.target.value)}>
              <option value="">{t("auditPage.statusAny")}</option>
              <option value="completed">{t("auditPage.statusCompleted")}</option>
              <option value="failed">{t("auditPage.statusFailed")}</option>
            </select>
          </label>
          <label className="form-field">
            <span>{t("auditPage.limit")}</span>
            <input
              type="number"
              min={1}
              max={200}
              value={limit}
              onChange={(event) => setLimit(event.target.value)}
            />
          </label>
          <button className="btn primary icon-text" type="button" onClick={() => void load()} disabled={busy}>
            <Search size={15} /> {t("common.search")}
          </button>
        </div>
      </QueryPanel>

      {busy && <LoadingSurface label={t("common.loading")} />}

      {!busy && (
        <div className="table-wrap">
          <table className="data-table audit-table">
            <thead>
              <tr>
                <th>{t("auditPage.id")}</th>
                <th>{t("auditPage.commandID")}</th>
                <th>{t("auditPage.actor")}</th>
                <th>{t("auditPage.action")}</th>
                <th>{t("auditPage.status")}</th>
                <th>{t("auditPage.dryRun")}</th>
                <th>{t("auditPage.reason")}</th>
                <th>{t("auditPage.time")}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <Row key={row.id} row={row} open={Boolean(expanded[row.id])} onToggle={() => toggleRow(row.id)} />
              ))}
              {rows.length === 0 && <EmptyRow colSpan={8} />}
            </tbody>
          </table>
        </div>
      )}
    </PageFrame>
  );
}

function Row({ row, open, onToggle }: { row: AuditLogEntry; open: boolean; onToggle: () => void }) {
  const { t } = useI18n();
  const hasDetail = Boolean(row.error) || Boolean(row.result);
  return (
    <>
      <tr className="audit-row">
        <td>
          {hasDetail && (
            <button className="icon-btn expand-btn" type="button" onClick={onToggle} aria-label={t("auditPage.expand")}>
              {open ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
            </button>
          )}
          {row.id}
        </td>
        <td className="mono">{row.command_id}</td>
        <td className="mono">{row.actor}</td>
        <td className="mono">{row.action}</td>
        <td>
          {row.status === "failed"
            ? <span className="status-chip bad">{t("auditPage.statusFailed")}</span>
            : <span className="status-chip good">{row.dry_run ? t("auditPage.statusDry") : t("auditPage.statusCompleted")}</span>}
        </td>
        <td>{row.dry_run ? t("common.yes") : t("common.no")}</td>
        <td className="truncate">{row.reason}</td>
        <td>{formatDate(row.created_at)}</td>
      </tr>
      {open && hasDetail && (
        <tr className="audit-detail">
          <td colSpan={8}>
            {row.error && <Alert>{row.error}</Alert>}
            {row.result && <JsonBlock value={row.result} />}
          </td>
        </tr>
      )}
    </>
  );
}