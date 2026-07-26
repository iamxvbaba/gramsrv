import { ChevronRight, RefreshCw, ShieldAlert } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { Alert, Badge, EmptyRow, Metric, PageFrame, QueryPanel } from "../components/ui";
import { useI18n, type TFunction } from "../i18n";
import { formatDate } from "../lib/format";
import type { Navigate } from "../routing";
import type { ModerationCaseRow } from "../types";

const defaultStatuses = "open,in_review,action_pending,action_failed,appeal_review";

export function ModerationCasesPage({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const [statuses, setStatuses] = useState(defaultStatuses);
  const [assignedTo, setAssignedTo] = useState("");
  const [rows, setRows] = useState<ModerationCaseRow[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function load() {
    setBusy(true);
    setError("");
    try {
      const params = new URLSearchParams({ statuses, limit: "100" });
      if (assignedTo.trim()) params.set("assigned_to", assignedTo.trim());
      setRows((await api.moderationCases(params)).cases);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  const pendingActions = rows.filter((row) => row.Status === "action_pending" || row.Status === "action_failed").length;
  const critical = rows.filter((row) => row.Severity === 4).length;

  return (
    <PageFrame
      title={t("route.moderation")}
      eyebrow={t("moderation.casesEyebrow")}
      actions={
        <button className="btn icon-text" type="button" onClick={load} disabled={busy}>
          <RefreshCw size={15} className={busy ? "spin" : ""} /> {t("common.refresh")}
        </button>
      }
    >
      {error && <Alert>{error}</Alert>}
      <div className="metric-row">
        <Metric label={t("moderation.currentQueue")} value={String(rows.length)} />
        <Metric label={t("moderation.criticalCases")} value={String(critical)} tone={critical ? "danger" : "neutral"} />
        <Metric label={t("moderation.pendingOrFailed")} value={String(pendingActions)} tone={pendingActions ? "warn" : "good"} />
      </div>
      <QueryPanel>
        <form className="toolbar" onSubmit={(event) => { event.preventDefault(); void load(); }}>
          <label className="field-inline">
            <span>{t("common.status")}</span>
            <input
              aria-label={t("moderation.statusFilter")}
              value={statuses}
              onChange={(event) => setStatuses(event.target.value)}
            />
          </label>
          <label className="field-inline">
            <span>{t("moderation.assignee")}</span>
            <input
              value={assignedTo}
              onChange={(event) => setAssignedTo(event.target.value)}
              placeholder={t("moderation.allAssignees")}
            />
          </label>
          <button className="btn primary icon-text" type="submit" disabled={busy}>
            <ShieldAlert size={15} /> {t("common.search")}
          </button>
        </form>
      </QueryPanel>
      <div className="table-wrap">
        <table className="data-table">
          <thead>
            <tr>
              <th>{t("moderation.case")}</th>
              <th>{t("moderation.target")}</th>
              <th>{t("common.status")}</th>
              <th>{t("moderation.severity")}</th>
              <th>{t("moderation.reportsAndReporters")}</th>
              <th>{t("moderation.assignee")}</th>
              <th>{t("moderation.latestReport")}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.ID}>
                <td className="mono">#{row.ID}</td>
                <td className="mono">{moderationTargetLabel(t, row.Target.Type, row.Target.ID)}</td>
                <td><CaseStatus status={row.Status} /></td>
                <td><CaseSeverity value={row.Severity} /></td>
                <td>{row.ReportCount} / {row.DistinctReporterCount}</td>
                <td>{row.AssignedTo || "-"}</td>
                <td>{formatDate(row.LastReportAt)}</td>
                <td>
                  <button className="row-link" onClick={() => navigate(`/moderation/${row.ID}`)}>
                    {t("moderation.review")} <ChevronRight size={14} />
                  </button>
                </td>
              </tr>
            ))}
            {rows.length === 0 && <EmptyRow colSpan={8} />}
          </tbody>
        </table>
      </div>
    </PageFrame>
  );
}

export function CaseStatus({ status }: { status: string }) {
  const { t } = useI18n();
  const tone = status === "resolved" || status === "dismissed"
    ? "good"
    : status === "action_failed"
      ? "danger"
      : status === "action_pending"
        ? "warn"
        : "neutral";
  return <Badge tone={tone}>{moderationEnumLabel(t, "status", status)}</Badge>;
}

export function CaseSeverity({ value }: { value: number }) {
  const { t } = useI18n();
  const keys = ["", "low", "medium", "high", "critical"];
  const key = keys[value];
  return (
    <Badge tone={value >= 4 ? "danger" : value >= 3 ? "warn" : "neutral"}>
      {key ? t(`moderation.severity.${key}`) : value}
    </Badge>
  );
}

export function moderationEnumLabel(t: TFunction, group: string, value: string): string {
  const key = `moderation.${group}.${value}`;
  const translated = t(key);
  return translated === key ? value : translated;
}

export function moderationTargetLabel(t: TFunction, type: string, id: number): string {
  return `${moderationEnumLabel(t, "targetType", type)} #${id}`;
}
