import { ChevronRight, RefreshCw, ShieldAlert } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { Alert, Badge, EmptyRow, Metric, PageFrame, QueryPanel } from "../components/ui";
import { formatDate } from "../lib/format";
import type { Navigate } from "../routing";
import type { ModerationCaseRow } from "../types";

const defaultStatuses = "open,in_review,action_pending,action_failed,appeal_review";

export function ModerationCasesPage({ navigate }: { navigate: Navigate }) {
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
      title="举报与审核"
      eyebrow="Moderation / Cases"
      actions={
        <button className="btn icon-text" type="button" onClick={load} disabled={busy}>
          <RefreshCw size={15} className={busy ? "spin" : ""} /> 刷新
        </button>
      }
    >
      {error && <Alert>{error}</Alert>}
      <div className="metric-row">
        <Metric label="当前队列" value={String(rows.length)} />
        <Metric label="关键案件" value={String(critical)} tone={critical ? "danger" : "neutral"} />
        <Metric label="处置待完成/失败" value={String(pendingActions)} tone={pendingActions ? "warn" : "good"} />
      </div>
      <QueryPanel>
        <form className="toolbar" onSubmit={(event) => { event.preventDefault(); void load(); }}>
          <label className="field-inline">
            <span>状态</span>
            <input value={statuses} onChange={(event) => setStatuses(event.target.value)} />
          </label>
          <label className="field-inline">
            <span>审核人</span>
            <input value={assignedTo} onChange={(event) => setAssignedTo(event.target.value)} placeholder="留空为全部" />
          </label>
          <button className="btn primary icon-text" type="submit" disabled={busy}>
            <ShieldAlert size={15} /> 查询
          </button>
        </form>
      </QueryPanel>
      <div className="table-wrap">
        <table className="data-table">
          <thead>
            <tr>
              <th>案件</th><th>目标</th><th>状态</th><th>等级</th>
              <th>举报 / 举报人</th><th>审核人</th><th>最近举报</th><th></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.ID}>
                <td className="mono">#{row.ID}</td>
                <td className="mono">{row.Target.Type}:{row.Target.ID}</td>
                <td><CaseStatus status={row.Status} /></td>
                <td><Severity value={row.Severity} /></td>
                <td>{row.ReportCount} / {row.DistinctReporterCount}</td>
                <td>{row.AssignedTo || "-"}</td>
                <td>{formatDate(row.LastReportAt)}</td>
                <td>
                  <button className="row-link" onClick={() => navigate(`/moderation/${row.ID}`)}>
                    审核 <ChevronRight size={14} />
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
  const tone = status === "resolved" || status === "dismissed"
    ? "good"
    : status === "action_failed"
      ? "danger"
      : status === "action_pending"
        ? "warn"
        : "neutral";
  return <Badge tone={tone}>{status}</Badge>;
}

function Severity({ value }: { value: number }) {
  const labels = ["", "低", "中", "高", "关键"];
  return <Badge tone={value >= 4 ? "danger" : value >= 3 ? "warn" : "neutral"}>{labels[value] || value}</Badge>;
}
