import { BadgeCheck, ChevronDown, ChevronRight, Loader2, RefreshCw, Search, ShieldCheck } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { Alert, Badge, EmptyRow, Metric, PageFrame, QueryPanel } from "../components/ui";
import { useI18n } from "../i18n";
import { displayUsername, formatDate } from "../lib/format";
import type { Navigate } from "../routing";
import type {
  VerificationApplicationRow,
  VerificationStatus,
  VerificationTargetType
} from "../types";

type StatusFilter = "all" | VerificationStatus;
type TargetFilter = "all" | VerificationTargetType;

const statuses: VerificationStatus[] = ["draft", "submitted", "in_review", "approved", "rejected", "cancelled"];
const targetTypes: VerificationTargetType[] = ["bot", "channel", "supergroup", "user"];

export function VerificationPage({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const [status, setStatus] = useState<StatusFilter>("all");
  const [targetType, setTargetType] = useState<TargetFilter>("all");
  const [reviewer, setReviewer] = useState("");
  const [q, setQ] = useState("");
  const [limit, setLimit] = useState("50");
  const [rows, setRows] = useState<VerificationApplicationRow[]>([]);
  const [counts, setCounts] = useState<Record<string, string>>({});
  const [hasMore, setHasMore] = useState(false);
  const [cursor, setCursor] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  // One free-text field: the backend matches the application id, the target peer
  // id and a username (applicant or target), so "@durov", "42" and a peer id all
  // work without a mode switch.
  async function load(next = false) {
    setBusy(true);
    setError("");
    const params = new URLSearchParams({ limit });
    if (status !== "all") params.set("status", status);
    if (targetType !== "all") params.set("target_type", targetType);
    if (reviewer.trim()) params.set("reviewer", reviewer.trim());
    if (q.trim()) params.set("q", q.trim().replace(/^@/, ""));
    if (next && cursor) params.set("before_id", cursor);
    try {
      const result = await api.verificationApplications(params);
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

  // The counts are the whole queue, not the current page, so they are fetched
  // separately from the keyset listing.
  async function loadCounts() {
    try {
      const result = await api.verificationCounts();
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
    <PageFrame
      title={t("verification.pageTitle")}
      eyebrow={t("verification.eyebrow")}
      actions={
        <button className="btn icon-text" type="button" onClick={refresh} disabled={busy}>
          <RefreshCw size={15} className={busy ? "spin" : ""} /> {t("common.refresh")}
        </button>
      }
    >
      {error && <Alert>{error}</Alert>}
      <div className="metric-row">
        {statuses.map((item) => (
          <Metric
            key={item}
            label={t(`verification.status.${item}`)}
            value={counts[item] ?? "0"}
            mono
            tone={statusMetricTone(item, counts[item] ?? "0")}
          />
        ))}
      </div>

      <QueryPanel>
        <form className="toolbar" onSubmit={(event) => { event.preventDefault(); void load(false); }}>
          <label className="searchbox">
            <Search size={15} />
            <input value={q} onChange={(event) => setQ(event.target.value)} placeholder={t("verification.searchPlaceholder")} />
          </label>
          <label className="field-inline">
            <span>{t("common.status")}</span>
            <select value={status} onChange={(event) => setStatus(event.target.value as StatusFilter)}>
              <option value="all">{t("verification.statusAll")}</option>
              {statuses.map((item) => (
                <option key={item} value={item}>{t(`verification.status.${item}`)}</option>
              ))}
            </select>
          </label>
          <label className="field-inline">
            <span>{t("verification.targetType")}</span>
            <select value={targetType} onChange={(event) => setTargetType(event.target.value as TargetFilter)}>
              <option value="all">{t("verification.targetTypeAll")}</option>
              {targetTypes.map((item) => (
                <option key={item} value={item}>{t(`verification.type.${item}`)}</option>
              ))}
            </select>
          </label>
          <label className="field-inline">
            <span>{t("verification.reviewer")}</span>
            <input value={reviewer} onChange={(event) => setReviewer(event.target.value)} placeholder={t("verification.reviewerPlaceholder")} />
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
              <th>{t("verification.target")}</th>
              <th>{t("verification.applicant")}</th>
              <th>{t("verification.category")}</th>
              <th>{t("common.status")}</th>
              <th>{t("verification.submittedAt")}</th>
              <th>{t("verification.reviewer")}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.ID}>
                <td className="mono">
                  <button className="row-link" type="button" onClick={() => navigate(`/verification/${row.ID}`)}>
                    #{row.ID}
                  </button>
                </td>
                <td>
                  <strong>{targetLabel(row)}</strong>
                  <div className="entity-subtitle mono">
                    {t(`verification.type.${row.TargetType}`)} · {row.TargetID}
                  </div>
                  {row.TargetVerified && (
                    <Badge tone="good"><BadgeCheck size={12} /> {t("verification.alreadyVerified")}</Badge>
                  )}
                </td>
                <td>
                  {displayUsername(row.ApplicantUsername) || row.ApplicantName || "-"}
                  <div className="entity-subtitle mono">{row.ApplicantUserID}</div>
                </td>
                <td>{row.Category || "-"}</td>
                <td><VerificationStatusBadge status={row.Status} /></td>
                <td>{formatDate(row.SubmittedAt) || "-"}</td>
                <td>{row.ReviewerAdminID || "-"}</td>
                <td>
                  <button className="row-link" type="button" onClick={() => navigate(`/verification/${row.ID}`)}>
                    <ShieldCheck size={14} /> {t("common.detail")} <ChevronRight size={14} />
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
    </PageFrame>
  );
}

export function VerificationStatusBadge({ status }: { status: VerificationStatus }) {
  const { t } = useI18n();
  return <Badge tone={statusTone(status)}>{t(`verification.status.${status}`)}</Badge>;
}

export function statusTone(status: VerificationStatus): "neutral" | "good" | "warn" | "danger" {
  if (status === "approved") return "good";
  if (status === "submitted" || status === "in_review") return "warn";
  if (status === "rejected") return "danger";
  return "neutral";
}

// submitted and in_review are the two statuses that need a reviewer; they are
// highlighted only while something actually sits in them.
function statusMetricTone(status: VerificationStatus, count: string): "neutral" | "good" | "warn" {
  const waiting = status === "submitted" || status === "in_review";
  if (!waiting) return status === "approved" ? "good" : "neutral";
  return count !== "0" && count !== "" ? "warn" : "neutral";
}

export function targetLabel(row: VerificationApplicationRow): string {
  return displayUsername(row.TargetUsername) || row.TargetTitle || `#${row.TargetID}`;
}

// The panel page that owns the target peer type, so a reviewer can inspect the
// live record rather than only the submission snapshot.
export function targetHref(row: VerificationApplicationRow): string {
  if (row.TargetType === "bot") return `/bots/${row.TargetID}`;
  if (row.TargetType === "user") return `/accounts/${row.TargetID}`;
  return `/channels/${row.TargetID}`;
}
