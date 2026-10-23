import { useEffect, useState } from "react";
import { withBase } from "../lib/base";

const GRADIENTS: [string, string][] = [
  ["#FF885E", "#FF516A"], ["#FFCD6A", "#FFA85C"], ["#82B1FF", "#665FFF"],
  ["#A0DE7E", "#54CB68"], ["#53EDD6", "#28C9B7"], ["#72D5FD", "#2A9EF1"],
  ["#E0A2F3", "#D669ED"]
];

function initials(value: string): string {
  const words = value.trim().split(/\s+/).filter(Boolean);
  if (words.length === 0) return "T";
  const first = Array.from(words[0])[0] ?? "T";
  const last = words.length > 1 ? Array.from(words[words.length - 1])[0] ?? "" : "";
  return (first + last).toUpperCase();
}

export function Avatar({ id, kind = "user", label, size = 34, refreshKey }: {
  id: number;
  kind?: "user" | "channel";
  label: string;
  size?: number;
  refreshKey?: number | string;
}) {
  const [failed, setFailed] = useState(false);
  useEffect(() => setFailed(false), [id, kind, refreshKey]);
  if (failed) {
    const [from, to] = GRADIENTS[Math.abs(id) % GRADIENTS.length];
    return <div className="avatar-fallback" style={{ width: size, height: size, background: `linear-gradient(135deg, ${from}, ${to})`, fontSize: Math.round(size * 0.42) }}>{initials(label)}</div>;
  }
  const path = kind === "channel" ? `/api/channels/${id}/avatar` : `/api/accounts/${id}/avatar`;
  const suffix = refreshKey === undefined ? "" : `?v=${encodeURIComponent(String(refreshKey))}`;
  return <img className="avatar-photo-img" src={withBase(`${path}${suffix}`)} alt="" loading="lazy" style={{ width: size, height: size }} onError={() => setFailed(true)} />;
}
