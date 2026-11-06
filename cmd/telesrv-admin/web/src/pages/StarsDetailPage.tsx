import { ArrowLeft, ChevronDown, Loader2, RefreshCw, User } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { Alert, EmptyRow, LoadingSurface, Metric, PageFrame, SectionHead, SplitLayout } from "../components/ui";
import { useI18n } from "../i18n";
import { displayName, displayUsername, formatQuantity, formatSigned, formatUnix } from "../lib/format";
import type { Navigate } from "../routing";
import type { StarsLedgerEntryRow, StarsLedgerResponse } from "../types";
import { GrantedBadge } from "./StarsPage";

export function StarsDetailPage({ userID, navigate }: { userID: string; navigate: Navigate }) {
  const { t } = useI18n();
  const [detail, setDetail] = useState<StarsLedgerResponse | null>(null);
  const [entries, setEntries] = useState<StarsLedgerEntryRow[]>([]);
  const [hasMore, setHasMore] = useState(false);
  const [cursor, setCursor] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function load(append = false) {
    setBusy(true);
    setError("");
    const params = new URLSearchParams({ limit: "100" });
    if (append && cursor) params.set("before_id", cursor);
    try {
      const result = await api.starsLedger(userID, params);
      const page = result.entries ?? [];
      setDetail(result);
      setEntries((current) => (append ? [...current, ...page] : page));
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
  }, [userID]);

  if (error && !detail) {
    return <Alert>{error}</Alert>;
  }
  if (!detail) {
    return <LoadingSurface label={busy ? t("stars.loadingDetail") : t("account.waitingData")} />;
  }

  const account = detail.account;
  // The account id is an int64 decimal string end to end, never a JS number.
  const payloadUserID = account.UserID || userID;

  return (
    <PageFrame
      title={t("stars.detailTitle", { user: displayUsername(account.Username) || displayName(account) || account.UserID })}
      eyebrow={t("stars.detailEyebrow")}
      actions={
        <>
          <button className="btn icon-text" type="button" onClick={() => navigate("/stars")}>
            <ArrowLeft size={15} /> {t("common.backToList")}
          </button>
          <button className="btn icon-text" type="button" onClick={() => load(false)} disabled={busy}>
            <RefreshCw size={15} className={busy ? "spin" : ""} /> {t("common.refresh")}
          </button>
        </>
      }
    >
      {error && <Alert>{error}</Alert>}
      <SplitLayout
        main={
          <div className="stacked-sections">
            <section className="entity-head">
              <div>
                <div className="entity-title">{displayUsername(account.Username) || displayName(account) || t("bots.unnamed")}</div>
                <div className="entity-subtitle">{t("stars.userID")}: {account.UserID}</div>
              </div>
              <div className="entity-badges">
                <GrantedBadge granted={account.Granted} />
              </div>
            </section>

            <div className="metric-row">
              <Metric label={t("stars.balance")} value={formatQuantity(account.Balance)} tone="good" mono />
              <Metric label={t("stars.ledgerEntries")} value={formatQuantity(account.TxnCount)} mono />
              <Metric label={t("stars.updatedAt")} value={formatUnixLocal(account.UpdatedAt)} />
            </div>

            <section className="section-block">
              <SectionHead title={t("stars.historyTitle")} text={t("stars.historyHint")} />
              <div className="table-wrap">
                <table className="data-table">
                  <thead>
                    <tr>
                      <th>{t("stars.id")}</th>
                      <th>{t("stars.date")}</th>
                      <th>{t("stars.amount")}</th>
                      <th>{t("stars.reason")}</th>
                      <th>{t("stars.title")}</th>
                      <th>{t("stars.description")}</th>
                      <th>{t("stars.peer")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {entries.map((row) => (
                      <tr key={row.ID}>
                        <td className="mono">{row.ID}</td>
                        <td>{formatUnix(Number(row.Date)) || "-"}</td>
                        <td className="mono">{formatSigned(row.Amount)}</td>
                        <td className="truncate">{row.Reason || "-"}</td>
                        <td className="truncate">{row.Title || "-"}</td>
                        <td className="truncate">{row.Description || "-"}</td>
                        <td className="mono">{row.PeerType ? `${row.PeerType}:${row.PeerID}` : "-"}</td>
                      </tr>
                    ))}
                    {entries.length === 0 && <EmptyRow colSpan={7} />}
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
            </section>
          </div>
        }
        side={
          <section className="action-dock">
            <div className="dock-title">{t("stars.actionDock")}</div>
            <button className="btn icon-text" type="button" onClick={() => navigate(`/accounts/${payloadUserID}`)}>
              <User size={15} /> {t("stars.openAccount")}
            </button>
            <p className="bot-create-note">{t("stars.dockHint")}</p>
          </section>
        }
      />
    </PageFrame>
  );
}

// formatUnixLocal interprets a "0001-01-01T00:00:00Z" zero as empty and renders
// RFC3339 through the same locale formatting every other timestamp uses.
function formatUnixLocal(value: string): string {
  if (!value || value.startsWith("0001-")) return "-";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "-" : date.toLocaleString();
}