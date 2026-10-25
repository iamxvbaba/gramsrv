import "dotenv/config";
import { parseProxyURL } from "./proxy.js";

const PLACEHOLDER_PATTERN = /^(CHANGE_ME|YOUR[_A-Z]*|<.+>|example|replace.with)/i;

function required(name) {
  const value = (process.env[name] ?? "").trim();
  if (!value) throw new Error(`${name} is required`);
  if (PLACEHOLDER_PATTERN.test(value)) throw new Error(`${name} contains a placeholder value: "${value}"`);
  return value;
}

function integer(name, fallback, { min = Number.MIN_SAFE_INTEGER } = {}) {
  const raw = (process.env[name] ?? "").trim();
  const value = raw ? Number(raw) : fallback;
  if (!Number.isSafeInteger(value) || value < min) throw new Error(`${name} must be an integer >= ${min}`);
  return value;
}

function ownerIDs() {
  const ids = new Set();
  for (const raw of required("OWNER_IDS").split(",")) {
    const id = Number(raw.trim());
    if (!Number.isSafeInteger(id) || id <= 0) throw new Error("OWNER_IDS contains an invalid Telegram user ID");
    ids.add(id);
  }
  return ids;
}

function botMode() {
  const raw = (process.env.BOT_MODE ?? "random").toLowerCase().trim();
  if (raw !== "random" && raw !== "real") throw new Error(`BOT_MODE must be "random" or "real", got "${raw}"`);
  return raw;
}

function databaseURL() {
  const configured = (process.env.DATABASE_URL ?? "").trim();
  if (configured) return configured;

  const host = (process.env.DATABASE_HOST ?? "localhost").trim();
  const user = encodeURIComponent(required("POSTGRES_USER"));
  const password = encodeURIComponent(required("POSTGRES_PASSWORD"));
  const database = encodeURIComponent(required("POSTGRES_DB"));
  return `postgresql://${user}:${password}@${host}:5432/${database}`;
}

export function loadConfig() {
  const webhookSecret = required("CODE_WEBHOOK_SECRET");
  if (webhookSecret.length < 24) throw new Error("CODE_WEBHOOK_SECRET must contain at least 24 characters");
  const mode = botMode();
  return Object.freeze({
    botToken: required("BOT_TOKEN"),
    telegramProxy: parseProxyURL(process.env.TELEGRAM_PROXY_URL, {
      username: process.env.TELEGRAM_PROXY_USERNAME,
      password: process.env.TELEGRAM_PROXY_PASSWORD,
    }),
    productName: (process.env.PRODUCT_NAME ?? "Telesrv").trim() || "Telesrv",
    ownerIDs: ownerIDs(),
    publicUsername: (process.env.BOT_PUBLIC_USERNAME ?? "").replace(/^@/, "").trim(),
    gramsrvAPI: (process.env.GRAMSRV_API ?? "http://127.0.0.1:2399").replace(/\/+$/, ""),
    gramsrvToken: required("GRAMSRV_TOKEN"),
    gramsrvActor: (process.env.GRAMSRV_ACTOR ?? "grammystore").trim(),
    publicBaseURL: (process.env.PUBLIC_BASE_URL ?? "https://example.com").replace(/\/+$/, ""),
    dbUrl: databaseURL(),
    botMode: mode,
    codeHost: (process.env.CODE_HTTP_HOST ?? "0.0.0.0").trim(),
    codePort: integer("CODE_HTTP_PORT", 2800, { min: 1 }),
    codeWebhookSecret: webhookSecret,
    requiredChannel: (process.env.REQUIRED_CHANNEL ?? "").trim(),
    requiredChannelURL: (process.env.REQUIRED_CHANNEL_URL ?? "").trim(),
    defaultLanguage: (process.env.DEFAULT_LANGUAGE ?? "ru").toLowerCase() === "en" ? "en" : "ru",
    defaultNumberCountry: (process.env.DEFAULT_NUMBER_COUNTRY ?? "RU").toUpperCase() === "US" ? "US" : "RU",
    referralBonus: integer("REFERRAL_BONUS", 100, { min: 0 }),
    dailyBonus: integer("DAILY_BONUS", 15, { min: 0 }),
    notificationTTLDays: integer("NOTIFICATION_TTL_DAYS", 30, { min: 1 }),
  });
}
