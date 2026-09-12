import { Plus, RefreshCw, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { Avatar } from "../components/Avatar";
import { ChannelPicker } from "../components/EntityPicker";
import { Alert, EmptyRow, PageFrame, SectionHead } from "../components/ui";
import { useI18n } from "../i18n";
import { formatDate } from "../lib/format";
import type { AutoSubscribeChannelRow, ChannelRow } from "../types";

export function AutoSubscribeChannelsPage() {
  const { t } = useI18n();
  const [rows, setRows] = useState<AutoSubscribeChannelRow[]>([]);
  const [picked, setPicked] = useState<ChannelRow | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  async function load() {
    setLoading(true);
    setError("");
    try {
      const result = await api.autoSubscribeChannels();
      setRows(result.channels ?? []);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  return (
    <PageFrame
      title={t("autoSubscribe.pageTitle")}
      eyebrow={t("autoSubscribe.eyebrow")}
      actions={
        <button className="btn icon-text" type="button" onClick={() => void load()} disabled={loading}>
          <RefreshCw size={15} className={loading ? "spin" : ""} /> {t("common.refresh")}
        </button>
      }
    >
      {error && <Alert>{error}</Alert>}

      <section className="surface">
        <SectionHead title={t("autoSubscribe.addChannel")} text={t("autoSubscribe.hint")} />
        <div className="toolbar">
          <ChannelPicker label={t("autoSubscribe.channelPicker")} value={picked} onChange={setPicked} />
          <ActionButton
            compact
            tone="neutral"
            icon={<Plus size={14} />}
            label={t("autoSubscribe.addAction")}
            path="/api/actions/add-auto-subscribe-channel"
            disabled={!picked}
            payload={() => ({ channel_id: picked?.ID ?? 0 })}
            onDone={() => {
              setPicked(null);
              void load();
            }}
          />
        </div>
      </section>

      <div className="table-wrap">
        <table className="data-table">
          <thead>
            <tr>
              <th>{t("autoSubscribe.columnChannel")}</th>
              <th>{t("autoSubscribe.columnAddedBy")}</th>
              <th>{t("autoSubscribe.columnAddedAt")}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.channel_id}>
                <td>
                  <span className="table-identity">
                    <Avatar id={Number(row.channel_id)} kind="channel" label={row.title || row.channel_id} />
                    {row.title || row.channel_id}
                  </span>
                </td>
                <td>{row.added_by || "-"}</td>
                <td>{formatDate(row.added_at) || "-"}</td>
                <td>
                  <ActionButton
                    compact
                    tone="danger"
                    icon={<Trash2 size={13} />}
                    label={t("autoSubscribe.removeAction")}
                    path="/api/actions/remove-auto-subscribe-channel"
                    payload={() => ({ channel_id: row.channel_id })}
                    onDone={() => void load()}
                  />
                </td>
              </tr>
            ))}
            {!loading && rows.length === 0 && <EmptyRow colSpan={4} />}
          </tbody>
        </table>
      </div>
    </PageFrame>
  );
}
