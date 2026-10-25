import { RefreshCw } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { Alert, EmptyRow, Metric, PageFrame } from "../components/ui";
import { useI18n } from "../i18n";
import { formatBytes, formatQuantity } from "../lib/format";
import type { Navigate } from "../routing";
import type { StorageStatsResponse } from "../types";
import { SectionTabs } from "../components/SectionTabs";

export function StoragePage({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const [stats, setStats] = useState<StorageStatsResponse | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function refresh() {
    setBusy(true);
    setError("");
    try {
      setStats(await api.storageStats());
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    void refresh();
  }, []);

  const saved = stats
    ? (BigInt(stats.LogicalBytes || "0") - BigInt(stats.PhysicalBytes || "0")).toString()
    : "0";

  return (
    <PageFrame
      title={t("storage.title")}
      eyebrow={t("storage.eyebrow")}
      actions={
        <button className="btn icon-text" type="button" onClick={() => void refresh()} disabled={busy}>
          <RefreshCw size={15} className={busy ? "spin" : ""} /> {t("common.refresh")}
        </button>
      }
    >
      <SectionTabs sectionId="media" active="/storage" navigate={navigate} />
      {error && <Alert>{error}</Alert>}
      <div className="metric-row">
        <Metric label={t("storage.physical")} value={stats ? formatBytes(stats.PhysicalBytes) : "-"} />
        <Metric label={t("storage.logical")} value={stats ? formatBytes(stats.LogicalBytes) : "-"} />
        <Metric label={t("storage.dedupSaved")} value={formatBytes(saved)} tone={BigInt(saved) > 0n ? "good" : "neutral"} />
        <Metric label={t("storage.objects")} value={stats ? formatQuantity(stats.ObjectCount) : "-"} />
      </div>
      <div className="metric-row">
        <Metric label={t("storage.references")} value={stats ? formatQuantity(stats.ReferenceCount) : "-"} />
        <Metric label={t("storage.documents")} value={stats ? formatQuantity(stats.DocumentCount) : "-"} />
        <Metric label={t("storage.photos")} value={stats ? formatQuantity(stats.PhotoCount) : "-"} />
      </div>
      <Alert>{t("storage.retentionBlocked")}</Alert>
      <div className="table-wrap">
        <table className="data-table">
          <thead>
            <tr>
              <th>{t("storage.backend")}</th>
              <th>{t("storage.physical")}</th>
              <th>{t("storage.logical")}</th>
              <th>{t("storage.objects")}</th>
              <th>{t("storage.references")}</th>
            </tr>
          </thead>
          <tbody>
            {(stats?.Backends ?? []).map((row) => (
              <tr key={row.Backend}>
                <td className="mono">{row.Backend}</td>
                <td className="mono">{formatBytes(row.PhysicalBytes)}</td>
                <td className="mono">{formatBytes(row.LogicalBytes)}</td>
                <td className="mono">{formatQuantity(row.ObjectCount)}</td>
                <td className="mono">{formatQuantity(row.ReferenceCount)}</td>
              </tr>
            ))}
            {(stats?.Backends ?? []).length === 0 && <EmptyRow colSpan={5} />}
          </tbody>
        </table>
      </div>
    </PageFrame>
  );
}
