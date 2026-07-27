import { Check, Loader2, Search, X } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { useI18n } from "../i18n";
import { channelKind, displayName, displayPhone, displayUsername } from "../lib/format";
import type { AccountRow, ChannelRow } from "../types";
import { Badge } from "./ui";

export function UserPicker({
  label,
  value,
  onChange
}: {
  label: string;
  value: AccountRow | null;
  onChange: (row: AccountRow | null) => void;
}) {
  const { t } = useI18n();
  const [query, setQuery] = useState("");
  const [rows, setRows] = useState<AccountRow[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function search() {
    setBusy(true);
    setError("");
    const params = new URLSearchParams({ limit: "20" });
    if (query.trim()) {
      params.set("q", query.trim());
    }
    try {
      const result = await api.accounts(params);
      setRows(result.rows);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    void search();
  }, []);

  return (
    <div className="entity-picker">
      <div className="picker-head">
        <span>{label}</span>
        {value ? (
          <button className="link-button" type="button" onClick={() => onChange(null)}>
            <X size={13} /> {t("common.clear")}
          </button>
        ) : null}
      </div>
      {value ? (
        <div className="selected-entity">
          <Check size={15} />
          <div>
            <strong>{displayName(value)}</strong>
            <span className="mono">{value.ID}</span>
          </div>
          <span>{displayUsername(value.Username) || displayPhone(value.Phone) || "-"}</span>
        </div>
      ) : null}
      <div className="picker-search">
        <Search size={15} />
        <input
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter") {
              event.preventDefault();
              void search();
            }
          }}
          placeholder={t("picker.userPlaceholder")}
        />
        <button className="btn compact-btn" type="button" onClick={search} disabled={busy}>
          {busy ? <Loader2 size={14} className="spin" /> : t("common.search")}
        </button>
      </div>
      {error && <div className="picker-error">{error}</div>}
      <div className="picker-results">
        {rows.map((row) => (
          <button
            key={row.ID}
            className={`picker-row ${value?.ID === row.ID ? "selected" : ""}`}
            type="button"
            onClick={() => onChange(row)}
          >
            <span className="mono">{row.ID}</span>
            <strong>{displayName(row)}</strong>
            <span>{displayUsername(row.Username) || displayPhone(row.Phone) || "-"}</span>
            {row.Verified ? <Badge tone="good">{t("picker.verified")}</Badge> : <Badge>{t("picker.regular")}</Badge>}
          </button>
        ))}
        {rows.length === 0 && !busy ? <div className="picker-empty">{t("common.noResults")}</div> : null}
      </div>
    </div>
  );
}

export function ChannelPicker({
  label,
  value,
  onChange
}: {
  label: string;
  value: ChannelRow | null;
  onChange: (row: ChannelRow | null) => void;
}) {
  const { t } = useI18n();
  const [query, setQuery] = useState("");
  const [rows, setRows] = useState<ChannelRow[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function search() {
    setBusy(true);
    setError("");
    const params = new URLSearchParams({ limit: "20" });
    if (query.trim()) {
      params.set("q", query.trim());
    }
    try {
      const result = await api.channels(params);
      setRows(result.rows);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    void search();
  }, []);

  return (
    <div className="entity-picker">
      <div className="picker-head">
        <span>{label}</span>
        {value ? (
          <button className="link-button" type="button" onClick={() => onChange(null)}>
            <X size={13} /> {t("common.clear")}
          </button>
        ) : null}
      </div>
      {value ? (
        <div className="selected-entity">
          <Check size={15} />
          <div>
            <strong>{value.Title || "-"}</strong>
            <span className="mono">{value.ID}</span>
          </div>
          <span>{displayUsername(value.Username) || channelKind(value, t)}</span>
        </div>
      ) : null}
      <div className="picker-search">
        <Search size={15} />
        <input
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter") {
              event.preventDefault();
              void search();
            }
          }}
          placeholder={t("picker.channelPlaceholder")}
        />
        <button className="btn compact-btn" type="button" onClick={search} disabled={busy}>
          {busy ? <Loader2 size={14} className="spin" /> : t("common.search")}
        </button>
      </div>
      {error && <div className="picker-error">{error}</div>}
      <div className="picker-results">
        {rows.map((row) => (
          <button
            key={row.ID}
            className={`picker-row ${value?.ID === row.ID ? "selected" : ""}`}
            type="button"
            onClick={() => onChange(row)}
          >
            <span className="mono">{row.ID}</span>
            <strong>{row.Title || "-"}</strong>
            <span>{displayUsername(row.Username) || channelKind(row, t)}</span>
            {row.Verified ? <Badge tone="good">{t("picker.verified")}</Badge> : <Badge>{channelKind(row, t)}</Badge>}
          </button>
        ))}
        {rows.length === 0 && !busy ? <div className="picker-empty">{t("common.noResults")}</div> : null}
      </div>
    </div>
  );
}
