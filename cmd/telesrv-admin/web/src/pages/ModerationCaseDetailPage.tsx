import { ArrowLeft, CheckCircle2, RefreshCw, ShieldCheck } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { api, errorMessage } from "../api";
import { Alert, Badge, JsonBlock, LoadingSurface, PageFrame, SectionHead, SplitLayout, Summary } from "../components/ui";
import { formatDate } from "../lib/format";
import type { Navigate } from "../routing";
import type { ModerationCaseDetail, ModerationReport } from "../types";
import { CaseStatus } from "./ModerationCasesPage";

type DecisionPreset = "no_violation" | "scam" | "fake" | "freeze" | "scam_freeze" | "fake_freeze" | "delete_messages" | "delete_account";

export function ModerationCaseDetailPage({ id, navigate }: { id: number; navigate: Navigate }) {
  const [detail, setDetail] = useState<ModerationCaseDetail | null>(null);
  const [report, setReport] = useState<ModerationReport | null>(null);
  const [reason, setReason] = useState("");
  const [preset, setPreset] = useState<DecisionPreset>("no_violation");
  const [messageIDs, setMessageIDs] = useState("");
  const [ownerUserID, setOwnerUserID] = useState("");
  const [revokeMessages, setRevokeMessages] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  function selectReport(next: ModerationReport | null) {
    setReport(next);
    if (!next) return;
    const ids = next.Items
      .filter((item) => item.Kind === "message")
      .map((item) => Number(item.ItemID))
      .filter((value) => Number.isSafeInteger(value) && value > 0);
    setMessageIDs(ids.join(", "));
    setOwnerUserID(String(next.ReporterUserID));
  }

  async function load() {
    setError("");
    try {
      const next = await api.moderationCase(id);
      setDetail(next);
      const reportID = next.ReportIDs[0];
      selectReport(reportID ? await api.moderationReport(reportID) : null);
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  useEffect(() => {
    void load();
  }, [id]);

  const selectedActions = useMemo(
    () => actionsForPreset(
      preset,
      detail?.Case.Target.Type,
      parseMessageIDs(messageIDs),
      Number(ownerUserID),
      revokeMessages
    ),
    [preset, detail?.Case.Target.Type, messageIDs, ownerUserID, revokeMessages]
  );
  const appealRemedy = useMemo(
    () => detail ? requiredAppealRemedy(detail) : { actions: [], label: "无", blocked: false },
    [detail]
  );

  async function claim() {
    if (!detail) return;
    setBusy(true);
    setError("");
    try {
      await api.claimModerationCase(id, detail.Case.Version);
      await load();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function decide() {
    if (!detail || !reason.trim()) {
      setError("必须填写审核理由。");
      return;
    }
    if (preset === "delete_messages" && selectedActions.length === 0) {
      setError(detail.Case.Target.Type === "user"
        ? "私聊删除需要合法的证据消息 ID 和举报人 owner_user_id。"
        : "频道删除需要至少一个合法的证据消息 ID。");
      return;
    }
    if (!window.confirm(`确认提交 ${preset} 决定？处置会通过 durable action 队列执行。`)) return;
    setBusy(true);
    setError("");
    try {
      const result = await api.decideModerationCase(id, {
        expected_version: detail.Case.Version,
        reason: reason.trim(),
        kind: preset === "no_violation" ? "no_violation" : "violation",
        actions: selectedActions
      });
      setDetail(result.case);
      setReason("");
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function reviewAppeal(appealID: number, granted: boolean) {
    if (!detail || !reason.trim()) {
      setError("必须填写申诉复核理由。");
      return;
    }
    if (!window.confirm(granted ? "确认通过申诉？" : "确认驳回申诉？")) return;
    setBusy(true);
    try {
      const result = await api.reviewModerationAppeal(id, appealID, {
        expected_version: detail.Case.Version,
        reason: reason.trim(),
        granted,
        actions: granted ? appealRemedy.actions : []
      });
      setDetail(result.case);
      setReason("");
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  if (error && !detail) return <Alert>{error}</Alert>;
  if (!detail) return <LoadingSurface label="正在加载审核案件…" />;
  const item = detail.Case;
  const canClaim = item.Status === "open" || item.Status === "in_review" || item.Status === "appeal_review";
  const canDecide = (item.Status === "in_review" || item.Status === "action_failed") && Boolean(item.AssignedTo);
  const canSubmitDecision = canDecide && (item.Status !== "action_failed" || preset !== "no_violation");
  const pendingAppeal = detail.Appeals.find((appeal) => appeal.Status === "pending");

  return (
    <PageFrame
      title={`审核案件 #${item.ID}`}
      eyebrow="Moderation / Case detail"
      actions={
        <>
          <button className="btn icon-text" onClick={() => navigate("/moderation")}><ArrowLeft size={15} /> 返回队列</button>
          <button className="btn icon-text" onClick={load}><RefreshCw size={15} /> 刷新</button>
        </>
      }
    >
      {error && <Alert>{error}</Alert>}
      <SplitLayout
        main={
          <div className="stacked-sections">
            <section className="entity-head">
              <div>
                <div className="entity-title">{item.Target.Type}:{item.Target.ID}</div>
                <div className="entity-subtitle">版本 {item.Version} · 最近更新 {formatDate(item.UpdatedAt)}</div>
              </div>
              <div className="entity-badges">
                <CaseStatus status={item.Status} />
                <Badge tone={item.Severity >= 4 ? "danger" : item.Severity >= 3 ? "warn" : "neutral"}>severity {item.Severity}</Badge>
              </div>
            </section>
            <div className="summary-grid">
              <Summary label="目标" value={`${item.Target.Type}:${item.Target.ID}`} mono />
              <Summary label="举报数" value={`${item.ReportCount}（${item.DistinctReporterCount} 位举报人）`} />
              <Summary label="审核人" value={item.AssignedTo || "-"} />
              <Summary label="首个 / 最近举报" value={`${formatDate(item.FirstReportAt)} / ${formatDate(item.LastReportAt)}`} />
            </div>
            <section className="section-block">
              <SectionHead title="举报证据" text="最多显示最近 100 条；快照在举报受理时冻结。" />
              <div className="toolbar">
                {detail.ReportIDs.map((reportID) => (
                  <button className="btn" key={reportID} onClick={async () => selectReport(await api.moderationReport(reportID))}>
                    #{reportID}
                  </button>
                ))}
              </div>
              {report && (
                <>
                  <div className="summary-grid">
                    <Summary label="来源 / 原因" value={`${report.Source} / ${report.Reason}`} />
                    <Summary label="举报人" value={String(report.ReporterUserID)} mono />
                    <Summary label="选项" value={report.Option} mono />
                    <Summary label="时间" value={formatDate(report.CreatedAt)} />
                  </div>
                  {report.Comment && <p className="about-text">{report.Comment}</p>}
                  <JsonBlock value={JSON.stringify(report, null, 2)} />
                </>
              )}
            </section>
            <section className="section-block">
              <SectionHead title="决定与处置审计" text="动作由租约 worker 幂等执行；失败保留错误与尝试次数。" />
              <JsonBlock value={JSON.stringify({ decisions: detail.Decisions, actions: detail.Actions }, null, 2)} />
            </section>
            {detail.Appeals.length > 0 && (
              <section className="section-block">
                <SectionHead title="申诉" />
                <JsonBlock value={JSON.stringify(detail.Appeals, null, 2)} />
              </section>
            )}
          </div>
        }
        side={
          <section className="action-dock">
            <div className="dock-title">案件操作</div>
            {canClaim && (
              <button className="btn primary icon-text" disabled={busy} onClick={claim}>
                <ShieldCheck size={15} /> {item.AssignedTo ? "续领案件" : "领取案件"}
              </button>
            )}
            <label className="field">
              <span>审核理由</span>
              <textarea value={reason} onChange={(event) => setReason(event.target.value)} rows={5} />
            </label>
            <label className="field">
              <span>决定模板</span>
              <select value={preset} onChange={(event) => setPreset(event.target.value as DecisionPreset)}>
                <option value="no_violation">无违规（驳回举报）</option>
                <option value="scam">标记 SCAM</option>
                <option value="fake">标记 FAKE</option>
                <option value="freeze">冻结账号</option>
                <option value="scam_freeze">SCAM + 冻结</option>
                <option value="fake_freeze">FAKE + 冻结</option>
                <option value="delete_messages">删除证据覆盖的消息</option>
                <option value="delete_account">删除账号</option>
              </select>
            </label>
            {preset === "delete_messages" && (
              <>
                <label className="field">
                  <span>证据消息 ID（逗号分隔）</span>
                  <input value={messageIDs} onChange={(event) => setMessageIDs(event.target.value)} placeholder="101, 102" />
                </label>
                {item.Target.Type === "user" && (
                  <>
                    <label className="field">
                      <span>私聊 owner_user_id</span>
                      <input value={ownerUserID} onChange={(event) => setOwnerUserID(event.target.value)} inputMode="numeric" />
                    </label>
                    <label className="field checkbox-field">
                      <input type="checkbox" checked={revokeMessages} onChange={(event) => setRevokeMessages(event.target.checked)} />
                      <span>双方撤回</span>
                    </label>
                  </>
                )}
                <Alert>服务端会再次校验每个消息 ID 必须存在于该案件的不可变举报证据中。</Alert>
              </>
            )}
            {item.Status === "action_failed" && preset === "no_violation" && (
              <Alert>处置已部分执行，不能直接改为无违规；请选择新的处置动作重新执行并保留旧失败审计。</Alert>
            )}
            {canDecide && (
              <button className="btn danger icon-text" disabled={busy || !canSubmitDecision} onClick={decide}>
                <CheckCircle2 size={15} /> {item.Status === "action_failed" ? "重新执行处置" : "提交决定"}
              </button>
            )}
            {pendingAppeal && item.AssignedTo && (
              <>
                <div className="dock-title">申诉复核 #{pendingAppeal.ID}</div>
                <Summary label="通过后自动恢复" value={appealRemedy.label} />
                {appealRemedy.blocked && (
                  <Alert>案件包含已成功的不可逆删除动作，不能标记为“申诉通过并已恢复”；请驳回或升级人工处理。</Alert>
                )}
                <button className="btn" disabled={busy} onClick={() => reviewAppeal(pendingAppeal.ID, false)}>驳回申诉</button>
                <button className="btn primary" disabled={busy || appealRemedy.blocked} onClick={() => reviewAppeal(pendingAppeal.ID, true)}>通过申诉</button>
              </>
            )}
          </section>
        }
      />
    </PageFrame>
  );
}

function actionsForPreset(
  preset: DecisionPreset,
  targetType: string | undefined,
  messageIDs: number[],
  ownerUserID: number,
  revoke: boolean
): Array<{ kind: string; payload: Record<string, unknown> }> {
  switch (preset) {
    case "scam":
      return [{ kind: "mark_scam", payload: {} }];
    case "fake":
      return [{ kind: "mark_fake", payload: {} }];
    case "freeze":
      return [{ kind: "freeze_account", payload: {} }];
    case "scam_freeze":
      return [{ kind: "mark_scam", payload: {} }, { kind: "freeze_account", payload: {} }];
    case "fake_freeze":
      return [{ kind: "mark_fake", payload: {} }, { kind: "freeze_account", payload: {} }];
    case "delete_messages":
      if (messageIDs.length === 0) return [];
      if (targetType === "channel") {
        return [{ kind: "delete_channel_message", payload: { ids: messageIDs } }];
      }
      if (targetType === "user" && Number.isSafeInteger(ownerUserID) && ownerUserID > 0) {
        return [{ kind: "delete_private_message", payload: { owner_user_id: ownerUserID, ids: messageIDs, revoke } }];
      }
      return [];
    case "delete_account":
      return [{ kind: "delete_account", payload: {} }];
    default:
      return [];
  }
}

function parseMessageIDs(raw: string): number[] {
  const values = raw
    .split(/[,\s]+/)
    .filter(Boolean)
    .map(Number);
  if (values.length === 0 || values.some((value) => !Number.isSafeInteger(value) || value <= 0)) return [];
  return [...new Set(values)];
}

function requiredAppealRemedy(detail: ModerationCaseDetail): {
  actions: Array<{ kind: string; payload: Record<string, unknown> }>;
  label: string;
  blocked: boolean;
} {
  let flagsActive = false;
  let freezeActive = false;
  let irreversible = false;
  for (const action of [...detail.Actions].sort((left, right) => left.ID - right.ID)) {
    if (action.Status !== "succeeded") continue;
    switch (action.Kind) {
      case "mark_scam":
      case "mark_fake":
        flagsActive = true;
        break;
      case "clear_peer_flags":
        flagsActive = false;
        break;
      case "freeze_account":
        freezeActive = true;
        break;
      case "unfreeze_account":
        freezeActive = false;
        break;
      case "delete_private_message":
      case "delete_channel_message":
      case "delete_account":
        irreversible = true;
        break;
    }
  }
  const actions: Array<{ kind: string; payload: Record<string, unknown> }> = [];
  const labels: string[] = [];
  if (flagsActive) {
    actions.push({ kind: "clear_peer_flags", payload: {} });
    labels.push("清除 SCAM / FAKE");
  }
  if (freezeActive) {
    actions.push({ kind: "unfreeze_account", payload: {} });
    labels.push("解除冻结");
  }
  return { actions, label: labels.join(" + ") || "无需恢复动作", blocked: irreversible };
}
