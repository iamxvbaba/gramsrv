import http from "node:http";
import { loadConfig } from "./config.js";
import { BotDatabase } from "./db.js";
import { GramsrvClient } from "./gramsrv.js";
import { commandList, createBot } from "./bot.js";
import { normalizeLanguage, translate } from "./i18n.js";
import { parseTelesrvDelivery, verifyTelesrvSignature } from "./otp.js";
import { isRealMode } from "./real-number.js";
import { describeProxy } from "./proxy.js";

const config = loadConfig();
const db = new BotDatabase(config.dbUrl);
// Fail before opening the webhook or polling Telegram if the required schema
// migration has not been applied. Runtime startup never repairs persisted data.
await db.pool.query("SELECT retired FROM numbers LIMIT 0");
const gramsrv = new GramsrvClient(config);
const bot = createBot({ config, db, gramsrv });

function escapeHTML(value) { return String(value ?? "").replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;"); }
function json(response, status, body) { response.writeHead(status, { "content-type": "application/json; charset=utf-8" }); response.end(JSON.stringify(body)); }

function loginCodeMessage(language, recipient, code) {
  const variables = {
    product: escapeHTML(config.productName),
    phone: escapeHTML(recipient),
    code: escapeHTML(code),
  };
  const lines = [
    translate(language, "otpTitle", variables),
    "",
    translate(language, "otpPhone", variables),
    translate(language, "otpCode", variables),
    "",
    translate(language, "otpWarning", variables),
  ];
  return lines.join("\n");
}

async function deliverLoginCode(recipient, code, chatIDs) {
  let delivered = 0;
  for (const chatID of chatIDs) {
    try {
      const language = normalizeLanguage((await db.userByChatID(chatID))?.language, config.defaultLanguage);
      const sent = await bot.api.sendMessage(chatID, loginCodeMessage(language, recipient, code), { parse_mode: "HTML" });
      if (sent?.message_id) setTimeout(() => bot.api.deleteMessage(chatID, sent.message_id).catch(() => {}), 120_000).unref?.();
      delivered++;
    }
    catch (error) { console.error("OTP delivery failed", chatID, error); }
  }
  if (!delivered) console.log(`OTP code for ${recipient} did not match any bound chat, not sent`);
}

const server = http.createServer((request, response) => {
  if (request.method === "GET" && request.url === "/healthz") return json(response, 200, { ok: true });
  if (request.method !== "POST" || !["/code", "/v1/otp/deliveries"].includes(request.url)) return json(response, 404, { error: "not found" });
  const chunks = []; let size = 0, tooLarge = false;
  request.on("data", (chunk) => { size += chunk.length; if (size > 16_384) tooLarge = true; else chunks.push(chunk); });
  request.on("end", () => {
    try {
      if (tooLarge) return json(response, 413, { accepted: false, error_code: "REQUEST_TOO_LARGE", retryable: false });
      const raw = Buffer.concat(chunks);
      if (!verifyTelesrvSignature(config.codeWebhookSecret, request.headers, raw)) return json(response, 401, { accepted: false, error_code: "SIGNATURE_INVALID", retryable: false });
      const { recipient, code, deliveryID, expiresAt, fingerprint } = parseTelesrvDelivery(raw, request.headers);
      db.acceptLoginCodeDelivery(deliveryID, fingerprint, recipient, code, expiresAt).then((delivery) => {
        json(response, 202, { accepted: true, message_id: `grammy:${deliveryID}` });
        if (!delivery.duplicate) void deliverLoginCode(recipient, code, delivery.chatIDs).catch((error) => console.error("OTP dispatch failed", error));
      }).catch((error) => {
        console.error("OTP webhook failed", error);
        const conflict = error.message === "IDEMPOTENCY_CONFLICT";
        const retired = error.message === "NUMBER_RETIRED";
        json(response, conflict ? 409 : 400, { accepted: false, error_code: conflict ? "IDEMPOTENCY_CONFLICT" : retired ? "RECIPIENT_INVALID" : "JSON_INVALID", retryable: false });
      });
    } catch (error) {
      console.error("OTP webhook failed", error);
      json(response, 400, { accepted: false, error_code: "JSON_INVALID", retryable: false });
    }
  });
});

server.listen(config.codePort, config.codeHost, () => console.log(`OTP webhook listening on http://${config.codeHost}:${config.codePort}`));

const me = await bot.api.getMe();
bot.botInfo = me;
console.log(`Starting @${me.username}`);
console.log(`Bot mode: ${config.botMode}`);
if (config.telegramProxy) console.log(`Proxy: ${describeProxy(config.telegramProxy)}`);
await bot.api.setMyCommands(commandList(config.defaultLanguage));
await bot.api.setMyCommands(commandList("ru"), { language_code: "ru" });
await bot.api.setMyCommands(commandList("en"), { language_code: "en" });

let stopping = false;
async function shutdown(signal) {
  if (stopping) return; stopping = true; console.log(`Stopping on ${signal}`);
  bot.stop(); server.close(); await db.close();
}
process.once("SIGINT", () => shutdown("SIGINT"));
process.once("SIGTERM", () => shutdown("SIGTERM"));

await bot.start({ allowed_updates: ["message", "callback_query", "pre_checkout_query"], onStart: () => console.log("Bot polling started") });
