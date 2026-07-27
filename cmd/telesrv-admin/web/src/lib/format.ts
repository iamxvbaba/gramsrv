import type { AccountRow, ChannelRow } from "../types";
import type { TFunction } from "../i18n";

export function displayPhone(value: string): string {
  const phone = value.trim();
  if (!phone || phone.startsWith("+")) return phone;
  return /^\d+$/.test(phone) ? `+${phone}` : phone;
}

export function displayUsername(value: string): string {
  const username = value.trim();
  if (!username) return "";
  return username.startsWith("@") ? username : `@${username}`;
}

export function displayName(row: Pick<AccountRow, "FirstName" | "LastName">): string {
  return `${row.FirstName || ""} ${row.LastName || ""}`.trim() || "-";
}

export function channelKind(ch: ChannelRow, t?: TFunction): string {
  const translate = t ?? ((key: string) => ({
    "channel.kind.broadcast": "Channel",
    "channel.kind.forum": "Supergroup / Forum",
    "channel.kind.megagroup": "Supergroup",
    "channel.kind.generic": "Channel / Group"
  })[key] ?? key);
  if (ch.Broadcast && !ch.Megagroup) return translate("channel.kind.broadcast");
  if (ch.Megagroup && ch.Forum) return translate("channel.kind.forum");
  if (ch.Megagroup) return translate("channel.kind.megagroup");
  return translate("channel.kind.generic");
}

export function formatDate(value: string): string {
  if (!value || value.startsWith("0001-")) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleString();
}

export function formatUnix(value: number): string {
  if (!value || value <= 0) return "";
  const date = new Date(value * 1000);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleString();
}

// safeHttpURL vets a link an applicant typed. Only http(s) is turned into an
// anchor: a submitted string may just as well be javascript:, data: or a bare
// word, and must stay inert text in that case. The parsed href is returned so a
// malformed authority cannot slip through the prefix test.
export function safeHttpURL(value: string): string {
  const raw = (value ?? "").trim();
  if (!/^https?:\/\//i.test(raw)) return "";
  try {
    const parsed = new URL(raw);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return "";
    return parsed.href;
  } catch {
    return "";
  }
}

export function toInt(value: string): number {
  if (!value.trim()) return 0;
  const parsed = Number.parseInt(value, 10);
  return Number.isFinite(parsed) ? parsed : 0;
}

// int64 values arrive as JSON strings; keep parsing tolerant so an unexpected
// empty string or "null" never renders as NaN.
export function toNumeric(value: string): number {
  const raw = (value ?? "").trim();
  if (!raw) return 0;
  const parsed = Number(raw);
  return Number.isFinite(parsed) ? parsed : 0;
}

export function formatQuantity(value: string): string {
  const raw = (value ?? "").trim();
  if (!raw) return "0";
  const parsed = Number(raw);
  return Number.isFinite(parsed) ? parsed.toLocaleString() : raw;
}

// Currency scaling for fragment.collectibleInfo.
//
// The wire format is integer smallest units: core.telegram.org says amount is
// "Total price in the smallest units of the currency (integer, not
// float/double)" -- $1.45 is 145 -- and crypto_amount likewise, so TON is
// nanotons (1 TON = 1e9). Clients divide by that exponent before drawing the
// price, which is why a panel that both stores and shows the raw integer makes an
// operator type 900 for "900 TON" and Telegram Desktop then renders 0.0000009.
//
// Everything the operator reads or types in the panel is therefore in whole
// currency units, and these helpers are the only conversion boundary.
const currencyExponents: Record<string, number> = {
  // Stars have no subunit: an XTR amount is a count of stars.
  XTR: 0,
  // Nanotons.
  TON: 9,
  // Fiat minor units.
  USD: 2,
  EUR: 2,
  RUB: 2
};

export function currencyExponent(currency: string): number {
  const key = (currency ?? "").trim().toUpperCase();
  // Two decimals is the ISO 4217 default, and it is what an unknown fiat code
  // most likely is; guessing 0 would silently multiply a price by 100.
  return key in currencyExponents ? currencyExponents[key] : 2;
}

// formatCurrencyAmount renders smallest units as whole currency units. It works
// on the decimal string rather than a JS number so a nanoton amount beyond
// Number.MAX_SAFE_INTEGER is not rounded on the way to the screen.
export function formatCurrencyAmount(value: string, currency: string): string {
  const raw = (value ?? "").trim();
  if (!raw) return "0";
  if (!/^-?\d+$/.test(raw)) return raw;
  const exponent = currencyExponent(currency);
  const negative = raw.startsWith("-");
  const digits = (negative ? raw.slice(1) : raw).replace(/^0+(?=\d)/, "");
  const padded = digits.padStart(exponent + 1, "0");
  const whole = padded.slice(0, padded.length - exponent) || "0";
  let fraction = exponent > 0 ? padded.slice(padded.length - exponent) : "";
  // Fiat keeps its two decimals the way a client draws them ($10.00); a
  // nine-decimal crypto amount would just be a wall of zeros, so trim those.
  if (exponent > 2) fraction = fraction.replace(/0+$/, "");
  const sign = negative ? "-" : "";
  return fraction ? `${sign}${groupDigits(whole)}.${fraction}` : `${sign}${groupDigits(whole)}`;
}

// groupDigits inserts thousands separators without going through a JS number, so
// a value past Number.MAX_SAFE_INTEGER keeps every digit.
function groupDigits(digits: string): string {
  return digits.replace(/\B(?=(\d{3})+(?!\d))/g, " ");
}

// formatCurrency is formatCurrencyAmount with the code appended, which is the
// shape every price cell in the panel wants.
export function formatCurrency(value: string, currency: string): string {
  const code = (currency ?? "").trim().toUpperCase();
  const amount = formatCurrencyAmount(value, code);
  return code ? `${amount} ${code}` : amount;
}

// toSmallestUnits turns what the operator typed -- whole currency units, with an
// optional fraction -- into the integer decimal string the API expects. It
// returns null for anything that is not a plain non-negative amount, or that
// carries more decimals than the currency has, so the form can refuse instead of
// silently truncating a price.
export function toSmallestUnits(value: string, currency: string): string | null {
  const raw = (value ?? "").trim().replace(/\s+/g, "").replace(",", ".");
  if (!raw) return "0";
  if (!/^\d*(\.\d*)?$/.test(raw) || raw === "." ) return null;
  const exponent = currencyExponent(currency);
  const [wholePart, fractionPart = ""] = raw.split(".");
  if (fractionPart.length > exponent) return null;
  const digits = `${wholePart || "0"}${fractionPart.padEnd(exponent, "0")}`.replace(/^0+(?=\d)/, "");
  return digits === "" ? "0" : digits;
}

export function formatSigned(value: string): string {
  const raw = (value ?? "").trim();
  if (!raw) return "0";
  const parsed = Number(raw);
  if (!Number.isFinite(parsed)) return raw;
  return parsed > 0 ? `+${parsed.toLocaleString()}` : parsed.toLocaleString();
}

export function parseIDs(value: string, invalidMessage = "msg ids invalid"): number[] {
  const ids = value
    .split(/[\s,]+/)
    .map((item) => item.trim())
    .filter(Boolean)
    .map((item) => Number.parseInt(item, 10));
  if (ids.length === 0 || ids.some((id) => !Number.isFinite(id) || id <= 0)) {
    throw new Error(invalidMessage);
  }
  return ids;
}
