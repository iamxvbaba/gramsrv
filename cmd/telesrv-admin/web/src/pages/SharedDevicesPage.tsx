import { ArrowLeft, ChevronDown, ChevronRight, Loader2, RefreshCw, Smartphone } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { Avatar } from "../components/Avatar";
import { Alert, Badge, EmptyRow, Metric, PageFrame, SectionHead } from "../components/ui";
import { useI18n } from "../i18n";
import { displayName, displayPhone, displayUsername, formatDate } from "../lib/format";
import type { Navigate } from "../routing";
import type { SharedDeviceGroup } from "../types";

// SharedDevicesPage surfaces authorizations that look like they came from the
// same physical device but belong to different accounts — a lead worth
// investigating for multi-accounting, not a verdict: device_model and
// system_version are self-reported by the client and easy to spoof, and a
// matching IP alone is common and innocent behind NAT, shared wifi, or
// carrier CGNAT. Treat every group here as "worth a look", not "guilty".
export function SharedDevicesPage({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const [groups, setGroups] = useState<SharedDeviceGroup[]>([]);
  const [hasMore, setHasMore] = useState(false);
  const [offset, setOffset] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function load(next = false) {
    setBusy(true);
    setError("");
    const at = next ? offset : 0;
    const params = new URLSearchParams({ limit: "20", offset: String(at) });
    try {
      const result = await api.sharedDeviceGroups(params);
      const page = result.rows ?? [];
      setGroups((current) => (next ? [...current, ...page] : page));
      setOffset(result.next_offset);
      setHasMore(Boolean(result.has_more));
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    void load(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const totalFlaggedAccounts = groups.reduce((sum, group) => sum + group.AccountCount, 0);

  return (
    <PageFrame
      title={t("sharedDevices.title")}
      eyebrow={t("sharedDevices.eyebrow")}
      actions={
        <>
          <button className="btn icon-text" type="button" onClick={() => navigate("/accounts")}>
            <ArrowLeft size={15} /> {t("sharedDevices.back")}
          </button>
          <button className="btn" type="button" onClick={() => load(false)} disabled={busy}>
            <RefreshCw size={15} className={busy ? "spin" : ""} /> {t("sharedDevices.refresh")}
          </button>
        </>
      }
    >
      {error && <Alert>{error}</Alert>}
      <div className="metric-row">
        <Metric label={t("sharedDevices.groupsOnPage")} value={String(groups.length)} />
        <Metric label={t("sharedDevices.accountsFlagged")} value={String(totalFlaggedAccounts)} tone="warn" />
      </div>
      <p className="about-text">{t("sharedDevices.intro")}</p>

      <div className="stacked-sections">
        {groups.map((group) => (
          <section className="section-block" key={`${group.DeviceModel}|${group.SystemVersion}|${group.Platform}|${group.IP}`}>
            <SectionHead
              title={group.DeviceModel || t("sharedDevices.unknownDevice")}
              text={`${group.Platform} ${group.SystemVersion} · ${group.IP} · ${t("sharedDevices.lastActive", { date: formatDate(group.LastActiveAt) })}`}
              action={<Badge tone="warn"><Smartphone size={12} /> {t("sharedDevices.accounts", { count: group.AccountCount })}</Badge>}
            />
            <div className="table-wrap">
              <table className="data-table">
                <thead>
                  <tr>
                    <th className="avatar-col"></th>
                    <th>{t("sharedDevices.userID")}</th>
                    <th>{t("sharedDevices.phone")}</th>
                    <th>{t("sharedDevices.username")}</th>
                    <th>{t("sharedDevices.name")}</th>
                    <th>{t("sharedDevices.activeFromDevice")}</th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  {group.Accounts.map((account) => (
                    <tr key={account.UserID}>
                      <td className="avatar-col">
                        <button className="avatar-link" type="button" onClick={() => navigate(`/accounts/${account.UserID}`)} aria-label={t("sharedDevices.openAccount", { id: account.UserID })}>
                          <Avatar id={account.UserID} kind="user" label={displayName(account)} />
                        </button>
                      </td>
                      <td className="mono">{account.UserID}</td>
                      <td>{displayPhone(account.Phone)}</td>
                      <td>{displayUsername(account.Username) || "-"}</td>
                      <td>{displayName(account) || "-"}</td>
                      <td>{formatDate(account.ActiveAt)}</td>
                      <td>
                        <button className="row-link" onClick={() => navigate(`/accounts/${account.UserID}`)}>
                          {t("sharedDevices.detail")} <ChevronRight size={14} />
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
        ))}
        {groups.length === 0 && (
          <div className="table-wrap">
            <table className="data-table">
              <tbody>
                <EmptyRow colSpan={7} />
              </tbody>
            </table>
          </div>
        )}
      </div>

      {hasMore && (
        <div className="toolbar">
          <button className="btn icon-text" type="button" onClick={() => load(true)} disabled={busy}>
            {busy ? <Loader2 size={15} className="spin" /> : <ChevronDown size={15} />} {t("sharedDevices.loadMore")}
          </button>
        </div>
      )}
    </PageFrame>
  );
}