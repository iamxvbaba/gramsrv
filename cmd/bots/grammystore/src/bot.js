import { Bot, GrammyError, HttpError, InlineKeyboard } from "grammy";
import { randomInt } from "node:crypto";
import { buildPayload, findProduct, KINDS, localizeProduct, normalizeUsername, parsePayload, productsOfKind } from "./catalog.js";
import { normalizeLanguage, translate, translateError } from "./i18n.js";
import { isRandomMode, isRealMode, rejectRandomInRealMode } from "./real-number.js";
import { createProxyAgent, describeProxy } from "./proxy.js";

const spinPrizes = Object.freeze([
  { amount: 50, weight: 250 }, { amount: 100, weight: 130 }, { amount: 500, weight: 50 },
  { amount: 1000, weight: 30 }, { amount: 10000, weight: 10 }, { amount: 15, weight: 530 },
]);

function escapeHTML(value) {
  return String(value ?? "").replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;").replaceAll('"', "&quot;");
}

function initialLanguage(from, fallback) { return normalizeLanguage(from?.language_code, fallback); }
function isOwner(config, id) { return config.ownerIDs.has(id); }
function userName(from) { return from.username ? `@${from.username}` : [from.first_name, from.last_name].filter(Boolean).join(" "); }
function positiveInteger(value, max = Number.MAX_SAFE_INTEGER) {
  const number = Number(value);
  return Number.isSafeInteger(number) && number > 0 && number <= max ? number : 0;
}
export function isStartCommand(text) { return /^\/start(?:@\w+)?(?:\s|$)/i.test(text ?? ""); }

export function fulfillmentForSale(sale, starsRate = 20) {
  if (sale?.fulfillment?.kind) return sale.fulfillment;
  if (!sale) throw new Error("sale not found");
  if (sale.product === "custom") return { kind: "custom" };
  let parsed = null;
  try { if (sale.invoice_payload) parsed = parsePayload(sale.invoice_payload); } catch {}
  const product = findProduct(sale.product, starsRate);
  if (!product) throw new Error("could not identify the fulfilled product");
  if (product.kind === KINDS.stars) {
    const titleAmount = Number(String(sale.title).match(/^(\d+)(?:\s+\S+)?\s+Stars$/)?.[1] ?? 0);
    const amount = parsed?.starsAmount || titleAmount || product.starsAmount;
    return { kind: "stars", recipientID: sale.recipient_id, amount };
  }
  if (product.kind === KINDS.premium) return { kind: "premium", recipientID: sale.recipient_id, months: product.months, entitlementID: 0 };
  if (product.kind === KINDS.username) return { kind: "username", recipientID: sale.recipient_id, username: normalizeUsername(parsed?.extra), bid: product.bid };
  throw new Error("legacy number sale has no exact fulfillment data and requires manual review");
}

export async function reverseSaleFulfillment(sale, db, gramsrv) {
  const item = fulfillmentForSale(sale, await db.starsRate());
  const key = `refund:${sale.charge_id}:${item.kind}`;
  if (item.kind === "custom") return item;
  if (item.kind === "stars") await gramsrv.debitStars(item.recipientID, item.amount, "Telegram bot refund", key);
  else if (item.kind === "premium") {
    if (!positiveInteger(item.entitlementID)) throw new Error("legacy Premium sale has no entitlement ID; automatic reversal is unsafe");
    await gramsrv.revokePremium(item.recipientID, item.entitlementID, "Telegram bot refund", key);
  } else if (item.kind === "username") {
    if (!item.username) throw new Error("sale has no stored @username");
    await gramsrv.revokeUsername(item.username, item.recipientID, key);
  } else if (item.kind === "number") {
    if (!positiveInteger(item.numberID) || !item.phone) throw new Error("sale has no stored number data");
    await db.revokePurchasedNumber(item.ownerID, item.numberID, item.phone, (phone) => gramsrv.resolveUserByPhone(phone));
  } else throw new Error("unknown fulfillment kind");
  return item;
}

export async function executeCompensatedRefund({ sale, telegramID, db, gramsrv, refundStarPayment }) {
  const chargeID = sale.charge_id;
  const refund = await db.beginRefund(chargeID, telegramID);
  try {
    if (!refund.internal_reversed) {
      await reverseSaleFulfillment(sale, db, gramsrv);
      await db.markRefundInternal(chargeID);
    }
    try { await refundStarPayment(telegramID, chargeID); }
    catch (error) {
      if (!/REFUND.*ALREADY|ALREADY.*REFUND/i.test(String(error?.description ?? error?.message ?? error))) throw error;
    }
    await db.markRefunded(chargeID, telegramID);
  } catch (error) {
    await db.failRefund(chargeID, error.message);
    throw error;
  }
}

export function mainKeyboard(language, admin = false) {
  const kb = new InlineKeyboard()
    .text(translate(language, "buttonNumbers"), "menu:numbers").text(translate(language, "buttonShop"), "menu:shop").row()
    .text(translate(language, "buttonBonuses"), "menu:bonuses").text(translate(language, "buttonReferrals"), "menu:referrals").row()
    .text(translate(language, "buttonSupport"), "menu:support").text(translate(language, "buttonSettings"), "menu:settings");
  if (admin) kb.row().text(translate(language, "buttonAdmin"), "admin:menu");
  return kb;
}

export function backKeyboard(language, target = "menu:home") {
  return new InlineKeyboard().text(translate(language, "back"), target);
}

export function shopKeyboard(language) {
  return new InlineKeyboard()
    .text(translate(language, "buttonPremium"), "shop:premium").text(translate(language, "buttonStars"), "shop:stars").row()
    .text(translate(language, "buttonNumber"), "shop:number").text(translate(language, "buttonUsername"), "shop:username").row()
    .text(translate(language, "back"), "menu:home");
}

export function settingsKeyboard(language, user, config) {
  const current = normalizeLanguage(user?.language, language);
  const russian = `${current === "ru" ? "✅ " : ""}${translate(language, "languageRussian")}`;
  const english = `${current === "en" ? "✅ " : ""}${translate(language, "languageEnglish")}`;
  const kb = new InlineKeyboard()
    .text(translate(language, "accountButton"), "settings:account").row()
    .text(russian, "settings:lang:ru").text(english, "settings:lang:en").row()
    .text(translate(language, user?.notifications ? "notificationsOn" : "notificationsOff"), "settings:notifications").row();
  if (config && isRealMode(config)) {
    kb.text(translate(language, "phoneTitle"), "settings:phone").row();
  }
  kb.text(translate(language, "back"), "menu:home");
  return kb;
}

export function accountKeyboard(language) {
  return new InlineKeyboard()
    .text(translate(language, "accountFetchButton"), "settings:account:fetch").row()
    .text(translate(language, "accountEnterButton"), "settings:account:enter").row()
    .text(translate(language, "back"), "menu:settings");
}

export function adminKeyboard(language) {
  return new InlineKeyboard()
    .text(translate(language, "adminStatsButton"), "admin:stats").text(translate(language, "adminLookupButton"), "admin:lookup").row()
    .text(translate(language, "adminStarsButton"), "admin:stars").text(translate(language, "adminPremiumButton"), "admin:premium").row()
    .text(translate(language, "adminPromoButton"), "admin:promo").text(translate(language, "adminGiveawayButton"), "admin:giveaway").row()
    .text(translate(language, "adminBonusButton"), "admin:bonus").text(translate(language, "adminInvoiceButton"), "admin:invoice").row()
    .text(translate(language, "adminAccessButton"), "admin:access").text(translate(language, "adminRefundButton"), "admin:refund").row()
    .text(translate(language, "adminReplyButton"), "admin:reply").text(translate(language, "adminRateButton"), "admin:rate").row()
    .text(translate(language, "adminBindPhoneButton"), "admin:bindphone").row()
    .text(translate(language, "back"), "menu:home");
}

export function commandList(language) {
  return [
    { command: "start", description: translate(language, "commandStart") },
    { command: "promo_code", description: translate(language, "commandPromo") },
  ];
}

async function editOrReply(ctx, message, keyboard = undefined) {
  const options = { parse_mode: "HTML", link_preview_options: { is_disabled: true }, reply_markup: keyboard };
  if (ctx.callbackQuery?.message) {
    try { return await ctx.editMessageText(message, options); }
    catch (error) { if (!String(error.description ?? error).includes("message is not modified")) throw error; }
    return;
  }
  return ctx.reply(message, options);
}

async function subscribed(ctx, config) {
  if (!config.requiredChannel || isOwner(config, ctx.from.id)) return true;
  try {
    const member = await ctx.api.getChatMember(config.requiredChannel, ctx.from.id);
    return ["creator", "administrator", "member"].includes(member.status) || (member.status === "restricted" && member.is_member === true);
  } catch {
    return false;
  }
}

async function subscriptionGate(ctx, config, language) {
  const kb = new InlineKeyboard();
  if (config.requiredChannelURL) kb.url(translate(language, "openChannel"), config.requiredChannelURL).row();
  kb.text(translate(language, "checkSubscription"), "subscription:check");
  await editOrReply(ctx, translate(language, "subscriptionPrompt"), kb);
}

function parseStartRef(ctx) {
  const match = String(ctx.match ?? "").match(/^ref_(\d+)$/);
  return match ? Number(match[1]) : 0;
}

function productText(product, language) {
  const extra = product.kind === KINDS.username
    ? `\n${translate(language, "productBid", { bid: product.bid })}`
    : product.kind === KINDS.stars ? `\n${translate(language, "productCredit", { amount: product.starsAmount })}` : "";
  return `<b>${escapeHTML(product.title)}</b>\n\n${escapeHTML(product.description)}\n\n${translate(language, "productPrice", { price: product.starsPrice })}${extra}`;
}

function productKeyboard(product, db, buyerID, language) {
  const kb = new InlineKeyboard();
  if (product.kind === KINDS.number) {
    return kb.text(translate(language, "buyFor", { price: product.starsPrice }), `buy:${product.code}:0`).row().text(translate(language, "back"), `shop:${product.kind}`);
  }
  const selfID = db._cachedUser?.server_user_id ?? 0;
  if (selfID > 0) kb.text(translate(language, "buySelf"), `buy:${product.code}:${selfID}`).row();
  kb.text(translate(language, "giftOther"), `target:${product.code}`).row();
  return kb.row().text(translate(language, "back"), `shop:${product.kind}`);
}

async function hasActiveAnonymousNumber(db, ownerID) {
  const current = await db.currentNumber(ownerID);
  return Boolean(current && current.format !== "free");
}

async function sendInvoice(ctx, product, targetUserID, language, extra = "") {
  const localized = localizeProduct(product, language);
  const starsAmount = product.kind === KINDS.stars ? product.starsAmount : 0;
  await ctx.api.sendInvoice(ctx.chat.id, localized.title, localized.description, buildPayload(product.code, targetUserID, extra, starsAmount), "XTR", [{ label: localized.title, amount: product.starsPrice }]);
}

function rollPrize() {
  const total = spinPrizes.reduce((sum, value) => sum + value.weight, 0);
  let value = randomInt(total);
  for (const prize of spinPrizes) { value -= prize.weight; if (value < 0) return prize.amount; }
  return 15;
}

export function createBot({ config, db, gramsrv }) {
  const bot = config.telegramProxy
    ? new Bot(config.botToken, { client: { baseFetchConfig: { agent: createProxyAgent(config.telegramProxy) } } })
    : new Bot(config.botToken);
  const languageOf = (id) => normalizeLanguage(db._userCache?.get(id)?.language, config.defaultLanguage);
  const tr = (id, key, variables = {}) => translate(languageOf(id), key, { product: escapeHTML(config.productName), ...variables });
  const localized = (id, product) => localizeProduct(product, languageOf(id));
  const phoneShareMessages = new Map();

  function deleteAfter(chatID, messageID, delayMs = 30_000) {
    if (chatID > 0 && messageID) setTimeout(() => bot.api.deleteMessage(chatID, messageID).catch(() => {}), delayMs).unref?.();
  }

  async function numbersMenu(ctx) {
    const currentNumber = await db.currentNumber(ctx.from.id);
    if (isRealMode(config)) {
      const bound = await db.verifiedPhone(ctx.from.id);
      const language = languageOf(ctx.from.id);
      if (!bound && !currentNumber) {
        const { phoneShareKeyboard } = await import("./real-number.js");
        const sent = await ctx.reply(`${tr(ctx.from.id, "phoneTitle")}\n\n${tr(ctx.from.id, "phoneIntro")}`, { parse_mode: "HTML", reply_markup: phoneShareKeyboard(language) });
        phoneShareMessages.set(ctx.chat.id, sent.message_id);
        return sent;
      }
      const lines = [tr(ctx.from.id, "numbersTitle")];
      if (bound) lines.push(tr(ctx.from.id, "phoneStatus", { phone: escapeHTML(bound.phone) }));
      if (currentNumber) lines.push(tr(ctx.from.id, "numberReserved", { phone: escapeHTML(currentNumber.display) }));
      const kb = new InlineKeyboard();
      if (bound) kb.text(tr(ctx.from.id, "phoneUnbindButton"), "phone:unbind").row();
      kb.text(tr(ctx.from.id, "back"), "menu:home");
      return editOrReply(ctx, lines.join("\n\n"), kb);
    }
    const numbers = await db.numbers(ctx.from.id);
    const list = numbers.slice(0, 10).map((number) => `${number.is_current ? "▶️" : "▫️"} <code>${escapeHTML(number.display)}</code>`).join("\n");
    const purchasedOwned = currentNumber && currentNumber.format !== "free";
    const kb = new InlineKeyboard();
    if (!purchasedOwned) kb.text(tr(ctx.from.id, "newFreeNumber"), "numbers:new").row();
    kb.text(tr(ctx.from.id, "back"), "menu:home");
    const note = purchasedOwned ? `\n\n${tr(ctx.from.id, "freeNumberUnavailable")}` : "";
    return editOrReply(ctx, `${tr(ctx.from.id, "numbersTitle")}\n\n${list || "—"}${note}`, kb);
  }

  db._userCache = new Map();

  bot.use(async (ctx, next) => {
    // Account/number flows and OTP routing are private-chat only.
    if (ctx.chat && ctx.chat.type !== "private") return;
    const isStart = isStartCommand(ctx.message?.text);
    if (ctx.from && ctx.chat && !isStart) {
      const user = await db.upsertUser(ctx.from, ctx.chat.id, initialLanguage(ctx.from, config.defaultLanguage));
      db._userCache.set(ctx.from.id, user);
    }
    await next();
  });

  bot.command("start", async (ctx) => {
    const referrer = parseStartRef(ctx);
    const existed = await db.user(ctx.from.id);
    const user = await db.upsertUser(ctx.from, ctx.chat.id, initialLanguage(ctx.from, config.defaultLanguage), referrer, config.referralBonus);
    db._userCache.set(ctx.from.id, user);
    const language = languageOf(ctx.from.id);
    if (!(await subscribed(ctx, config))) return subscriptionGate(ctx, config, language);

    if (isRandomMode(config)) {
      const number = await db.createNumber(ctx.from.id, ctx.chat.id, "free", config.defaultNumberCountry, false);
      const referralApplied = !existed && referrer > 0 && (await db.user(ctx.from.id))?.referred_by === referrer;
      const lines = [tr(ctx.from.id, "startHello"), tr(ctx.from.id, "startPhone", { phone: escapeHTML(number.display) })];
      if (referralApplied) lines.push(tr(ctx.from.id, "referralAccepted"));
      await ctx.reply(lines.join("\n\n"), { parse_mode: "HTML", reply_markup: mainKeyboard(language, isOwner(config, ctx.from.id)) });
    } else {
      const bound = await db.verifiedPhone(ctx.from.id);
      const lines = [tr(ctx.from.id, "startHello")];
      if (bound) {
        lines.push(tr(ctx.from.id, "phoneStatus", { phone: escapeHTML(bound.phone) }));
      }
      const referralApplied = !existed && referrer > 0 && (await db.user(ctx.from.id))?.referred_by === referrer;
      if (referralApplied) lines.push(tr(ctx.from.id, "referralAccepted"));
      await ctx.reply(lines.join("\n\n"), { parse_mode: "HTML", reply_markup: mainKeyboard(language, isOwner(config, ctx.from.id)) });
    }
  });

  bot.command("admin", async (ctx) => {
    if (!isOwner(config, ctx.from.id)) return;
    const language = languageOf(ctx.from.id);
    await editOrReply(ctx, tr(ctx.from.id, "adminTitle"), adminKeyboard(language));
  });

  bot.command("promo_code", async (ctx) => {
    const language = languageOf(ctx.from.id);
    const [code, rawID] = String(ctx.match ?? "").trim().split(/\s+/);
    const serverID = Number(rawID || (await db.user(ctx.from.id))?.server_user_id);
    if (!code || !Number.isSafeInteger(serverID) || serverID <= 0) return ctx.reply(tr(ctx.from.id, "promoUsage", { product: config.productName.toUpperCase() }));
    try {
      const promo = await db.claimPromo(code, ctx.from.id);
      try { await gramsrv.grantStars(serverID, promo.stars_amount, `Promo ${code}`, `promo:${code.toLowerCase()}:${ctx.from.id}`); }
      catch (error) { await db.releaseCampaignClaim("promo", code.toLowerCase(), ctx.from.id); throw error; }
      await ctx.reply(tr(ctx.from.id, "promoGranted", { amount: promo.stars_amount }));
    } catch (error) {
      console.error("Promo redemption failed", error);
      await ctx.reply(translateError(language, error));
    }
  });

  bot.on("pre_checkout_query", async (ctx) => {
    const language = languageOf(ctx.from.id);
    try {
      if (ctx.preCheckoutQuery.currency !== "XTR" || ctx.preCheckoutQuery.total_amount <= 0) throw new Error("unsupported currency or amount");
      if (ctx.preCheckoutQuery.invoice_payload.startsWith("custom|")) {
        const title = Buffer.from(ctx.preCheckoutQuery.invoice_payload.slice(7), "base64url").toString("utf8");
        if (!title || title.length > 32) throw new Error("invalid invoice");
      } else {
        const parsed = parsePayload(ctx.preCheckoutQuery.invoice_payload);
        const product = findProduct(parsed.code, await db.starsRate());
        if (!product || product.starsPrice !== ctx.preCheckoutQuery.total_amount ||
          (product.kind === KINDS.stars && parsed.starsAmount <= 0) ||
          (product.kind === KINDS.number && await hasActiveAnonymousNumber(db, ctx.from.id))) throw new Error("number already owned");
      }
      await ctx.answerPreCheckoutQuery(true);
    } catch {
      await ctx.answerPreCheckoutQuery(false, { error_message: translate(language, "precheckoutInvalid") });
    }
  });

  async function fulfill(product, recipientID, buyer, chatID, chargeID, extra = "") {
    if (product.kind !== KINDS.number && (!Number.isSafeInteger(recipientID) || recipientID <= 0)) throw new Error(`recipient ${config.productName} ID is invalid`);
    if (await db.saleByCharge(chargeID)) return;
    const key = `payment:${chargeID}:${product.code}`;
    let number = null;
    let fulfillment = null;
    if (product.kind === KINDS.premium) {
      const result = await gramsrv.grantPremium(recipientID, product.months, "Telegram bot purchase", key);
      fulfillment = { kind: "premium", recipientID, months: product.months, entitlementID: Number(result?.details?.entitlement_id ?? 0) };
    } else if (product.kind === KINDS.stars) {
      await gramsrv.grantStars(recipientID, product.starsAmount, "Telegram bot purchase", key);
      fulfillment = { kind: "stars", recipientID, amount: product.starsAmount };
    }
    else if (product.kind === KINDS.username) {
      const username = normalizeUsername(extra);
      if (!username) throw new Error("collectible username is invalid");
      await gramsrv.mintUsername(recipientID, username, product.bid, key);
      fulfillment = { kind: "username", recipientID, username, bid: product.bid };
    } else if (product.kind === KINDS.number) {
      recipientID = buyer.id;
      const view = localized(buyer.id, product);
      number = await db.fulfillNumberPurchase({ product: product.code, title: view.title, starsPrice: product.starsPrice, recipientID, buyerID: buyer.id, buyerName: userName(buyer), chargeID }, chatID, product.numberFormat);
    } else throw new Error("unknown product kind");
    const productView = localized(buyer.id, product);
    if (!number) await db.addSale({ product: product.code, title: productView.title, starsPrice: product.starsPrice, recipientID, buyerID: buyer.id, buyerName: userName(buyer), chargeID, fulfillment });
    const message = number
      ? tr(buyer.id, "numberReserved", { phone: escapeHTML(number.display) })
      : tr(buyer.id, "productGranted", { title: escapeHTML(productView.title), id: recipientID });
    const sent = await bot.api.sendMessage(chatID, message, { parse_mode: "HTML" }).catch(() => null);
    if (chatID > 0 && sent) deleteAfter(chatID, sent.message_id);
  }

  bot.on("message:successful_payment", async (ctx) => {
    const payment = ctx.message.successful_payment;
    if (!await db.beginPayment(payment.telegram_payment_charge_id, ctx.from.id, payment.invoice_payload, payment.total_amount)) return;
    try {
      if (payment.invoice_payload.startsWith("custom|")) {
        const title = Buffer.from(payment.invoice_payload.slice(7), "base64url").toString("utf8");
        await db.addSale({ product: "custom", title, starsPrice: payment.total_amount, recipientID: ctx.from.id, buyerID: ctx.from.id, buyerName: userName(ctx.from), chargeID: payment.telegram_payment_charge_id, fulfillment: { kind: "custom" } });
        await ctx.reply(tr(ctx.from.id, "paymentReceived", { title: escapeHTML(title) }), { parse_mode: "HTML" });
      } else {
        const parsed = parsePayload(payment.invoice_payload);
        let product = findProduct(parsed.code, await db.starsRate());
        if (!product) throw new Error("product no longer exists");
        if (payment.currency !== "XTR" || payment.total_amount !== product.starsPrice) throw new Error("paid amount does not match the product");
        if (product.kind === KINDS.stars) {
          if (parsed.starsAmount <= 0) throw new Error("invoice has no snapshotted server Stars amount");
          product = { ...product, starsAmount: parsed.starsAmount, title: `${parsed.starsAmount} Stars`, titleRu: `${parsed.starsAmount} Stars` };
        }
        const recipient = parsed.targetUserID || (await db.user(ctx.from.id))?.server_user_id || 0;
        await fulfill(product, recipient, ctx.from, ctx.chat.id, payment.telegram_payment_charge_id, parsed.extra);
      }
      await db.finishPayment(payment.telegram_payment_charge_id);
    } catch (error) {
      await db.failPayment(payment.telegram_payment_charge_id, error);
      console.error("Payment fulfillment failed", payment.telegram_payment_charge_id, error);
      await ctx.reply(tr(ctx.from.id, "paymentFailed", { charge: escapeHTML(payment.telegram_payment_charge_id) }), { parse_mode: "HTML" });
      for (const owner of config.ownerIDs) {
        await bot.api.sendMessage(owner, tr(owner, "fulfillmentOwnerError", { charge: escapeHTML(payment.telegram_payment_charge_id), error: escapeHTML(error.message) }), { parse_mode: "HTML" }).catch(() => {});
      }
    }
  });

  // Explicit recovery for a stored number payment after a database/process
  // failure. Never manufacture a payment or ask the customer to pay again.
  bot.command("retry_payment", async (ctx) => {
    if (!isOwner(config, ctx.from.id)) return;
    try {
      const payment = await db.paymentByCharge(String(ctx.match ?? "").trim());
      if (!payment) throw new Error("payment not found");
      const parsed = parsePayload(payment.invoice_payload);
      const product = findProduct(parsed.code, await db.starsRate());
      if (product?.kind !== KINDS.number || product.starsPrice !== payment.amount) throw new Error("only stored number payments can be retried");
      const buyer = await db.user(payment.telegram_id);
      if (!buyer) throw new Error("user not found");
      await fulfill(product, buyer.telegram_id, { id: buyer.telegram_id, first_name: buyer.first_name, username: buyer.username }, buyer.telegram_id, payment.charge_id);
      await ctx.reply(tr(ctx.from.id, "numberPaymentRecovered"));
    } catch (error) {
      await ctx.reply(translateError(languageOf(ctx.from.id), error));
    }
  });

  bot.callbackQuery(/^subscription:check$/, async (ctx) => {
    await ctx.answerCallbackQuery();
    const language = languageOf(ctx.from.id);
    if (await subscribed(ctx, config)) await editOrReply(ctx, tr(ctx.from.id, "menuTitle"), mainKeyboard(language, isOwner(config, ctx.from.id)));
    else await subscriptionGate(ctx, config, language);
  });

  bot.callbackQuery(/^menu:(.+)$/, async (ctx) => {
    await ctx.answerCallbackQuery();
    const page = ctx.match[1];
    const user = await db.user(ctx.from.id);
    db._userCache.set(ctx.from.id, user);
    const language = languageOf(ctx.from.id);
    if (page === "home") return editOrReply(ctx, tr(ctx.from.id, "menuTitle"), mainKeyboard(language, isOwner(config, ctx.from.id)));
    if (page === "numbers") return numbersMenu(ctx);
    if (page === "shop") return editOrReply(ctx, tr(ctx.from.id, "shopTitle"), shopKeyboard(language));
    if (page === "bonuses") {
      const kb = new InlineKeyboard().text(tr(ctx.from.id, "dailyBonus"), "bonus:daily").text(tr(ctx.from.id, "wheel"), "bonus:spin").row().text(tr(ctx.from.id, "back"), "menu:home");
      return editOrReply(ctx, `${tr(ctx.from.id, "bonusesTitle")}\n\n${tr(ctx.from.id, "balance", { balance: user.bonus })}\n${tr(ctx.from.id, "referralsCount", { count: user.referral_count })}`, kb);
    }
    if (page === "referrals") {
      const username = config.publicUsername || bot.botInfo?.username || "bot";
      const link = `https://t.me/${username}?start=ref_${ctx.from.id}`;
      return editOrReply(ctx, `${tr(ctx.from.id, "referralsTitle")}\n\n${tr(ctx.from.id, "invited", { count: user.referral_count })}\n${tr(ctx.from.id, "referralBonus", { amount: config.referralBonus })}\n\n<code>${link}</code>`, backKeyboard(language));
    }
    if (page === "support") {
      await db.setPending(ctx.from.id, "support");
      return editOrReply(ctx, tr(ctx.from.id, "supportPrompt"), backKeyboard(language));
    }
    if (page === "settings") return editOrReply(ctx, tr(ctx.from.id, "settingsTitle"), settingsKeyboard(language, user, config));
  });

  bot.callbackQuery(/^numbers:new$/, async (ctx) => {
    await ctx.answerCallbackQuery();
    if (rejectRandomInRealMode(ctx, config, languageOf(ctx.from.id))) return;
    const language = languageOf(ctx.from.id);
    if (await hasActiveAnonymousNumber(db, ctx.from.id)) {
      return editOrReply(ctx, tr(ctx.from.id, "freeNumberUnavailable"), backKeyboard(language, "menu:numbers"));
    }
    const kb = new InlineKeyboard().text(tr(ctx.from.id, "countryRU"), "numbers:new:RU").text(tr(ctx.from.id, "countryUS"), "numbers:new:US").row().text(tr(ctx.from.id, "back"), "menu:numbers");
    await editOrReply(ctx, tr(ctx.from.id, "chooseCountry"), kb);
  });

  bot.callbackQuery(/^numbers:new:(RU|US)$/, async (ctx) => {
    await ctx.answerCallbackQuery();
    if (rejectRandomInRealMode(ctx, config, languageOf(ctx.from.id))) return;
    const language = languageOf(ctx.from.id);
    if (await hasActiveAnonymousNumber(db, ctx.from.id)) {
      return editOrReply(ctx, tr(ctx.from.id, "freeNumberUnavailable"), backKeyboard(language, "menu:numbers"));
    }
    try {
      const number = await db.createNumber(ctx.from.id, ctx.chat.id, "free", ctx.match[1], true);
      await editOrReply(ctx, tr(ctx.from.id, "newNumber", { phone: escapeHTML(number.display) }), backKeyboard(language, "menu:numbers"));
    } catch (error) {
      await editOrReply(ctx, translateError(language, error), backKeyboard(language, "menu:numbers"));
    }
  });

  bot.callbackQuery(/^shop:(premium|stars|number|username)$/, async (ctx) => {
    await ctx.answerCallbackQuery();
    const kind = ctx.match[1];
    const language = languageOf(ctx.from.id);
    const kb = new InlineKeyboard();
    const starsRate = await db.starsRate();
    for (const product of productsOfKind(kind, starsRate)) {
      const view = localizeProduct(product, language);
      kb.text(`${view.title} · ${product.starsPrice} ⭐`, `product:${product.code}`).row();
    }
    if (kind === KINDS.stars) kb.text(tr(ctx.from.id, "customAmountButton"), "stars:custom").row();
    kb.text(tr(ctx.from.id, "back"), "menu:shop");
    await editOrReply(ctx, tr(ctx.from.id, "selectProduct"), kb);
  });

  bot.callbackQuery(/^stars:custom$/, async (ctx) => {
    await ctx.answerCallbackQuery();
    await db.setPending(ctx.from.id, "stars_amount");
    await editOrReply(ctx, tr(ctx.from.id, "customAmountPrompt"), backKeyboard(languageOf(ctx.from.id), "shop:stars"));
  });

  bot.callbackQuery(/^product:(.+)$/, async (ctx) => {
    await ctx.answerCallbackQuery();
    const starsRate = await db.starsRate();
    const product = findProduct(ctx.match[1], starsRate);
    if (!product) return;
    const language = languageOf(ctx.from.id);
    const view = localizeProduct(product, language);
    await editOrReply(ctx, productText(view, language), productKeyboard(product, { _cachedUser: db._userCache.get(ctx.from.id) }, ctx.from.id, language));
  });

  bot.callbackQuery(/^target:(.+)$/, async (ctx) => {
    await ctx.answerCallbackQuery();
    await db.setPending(ctx.from.id, "target", { productCode: ctx.match[1] });
    await editOrReply(ctx, tr(ctx.from.id, "targetPrompt"), backKeyboard(languageOf(ctx.from.id), `product:${ctx.match[1]}`));
  });

  bot.callbackQuery(/^buy:([^:]+):(\d+)$/, async (ctx) => {
    await ctx.answerCallbackQuery();
    const starsRate = await db.starsRate();
    const product = findProduct(ctx.match[1], starsRate);
    const targetID = Number(ctx.match[2]);
    if (!product) return;
    const language = languageOf(ctx.from.id);
    if (product.kind === KINDS.number && await hasActiveAnonymousNumber(db, ctx.from.id)) {
      return ctx.reply(tr(ctx.from.id, "errorNumberAlreadyOwned"));
    }
    if (product.kind === KINDS.username) {
      await db.setPending(ctx.from.id, "username", { productCode: product.code, targetID });
      return editOrReply(ctx, tr(ctx.from.id, "enterUsername"), backKeyboard(language, `product:${product.code}`));
    }
    if (targetID > 0 && targetID !== (await db.user(ctx.from.id))?.server_user_id) await db.rememberRecipient(ctx.from.id, targetID);
    if (isOwner(config, ctx.from.id)) return fulfill(product, targetID, ctx.from, ctx.chat.id, `owner-${ctx.from.id}-${Date.now()}-${randomInt(1_000_000)}`);
    await sendInvoice(ctx, product, targetID, language);
  });

  bot.callbackQuery(/^settings:lang:(ru|en)$/, async (ctx) => {
    await db.setLanguage(ctx.from.id, ctx.match[1]);
    const user = await db.user(ctx.from.id);
    db._userCache.set(ctx.from.id, user);
    const language = languageOf(ctx.from.id);
    await ctx.answerCallbackQuery({ text: translate(language, "languageChanged") });
    await editOrReply(ctx, tr(ctx.from.id, "settingsTitle"), settingsKeyboard(language, user, config));
  });

  bot.callbackQuery(/^settings:notifications$/, async (ctx) => {
    const enabled = await db.toggleNotifications(ctx.from.id);
    const user = await db.user(ctx.from.id);
    db._userCache.set(ctx.from.id, user);
    const language = languageOf(ctx.from.id);
    await ctx.answerCallbackQuery({ text: tr(ctx.from.id, enabled ? "notificationsEnabled" : "notificationsDisabled") });
    await editOrReply(ctx, tr(ctx.from.id, "settingsTitle"), settingsKeyboard(language, user, config));
  });

  bot.callbackQuery(/^settings:account$/, async (ctx) => {
    await ctx.answerCallbackQuery();
    const user = await db.user(ctx.from.id);
    const language = languageOf(ctx.from.id);
    const title = user?.server_user_id
      ? tr(ctx.from.id, "accountMenuID", { id: user.server_user_id })
      : tr(ctx.from.id, "accountMenuTitle");
    await editOrReply(ctx, title, accountKeyboard(language));
  });

  bot.callbackQuery(/^settings:account:enter$/, async (ctx) => {
    await ctx.answerCallbackQuery();
    await db.setPending(ctx.from.id, "account");
    await editOrReply(ctx, tr(ctx.from.id, "accountPrompt"), backKeyboard(languageOf(ctx.from.id), "settings:account"));
  });

  bot.callbackQuery(/^settings:account:fetch$/, async (ctx) => {
    const language = languageOf(ctx.from.id);
    try {
      let phone = null;
      if (isRealMode(config)) {
        const verified = await db.verifiedPhone(ctx.from.id);
        phone = verified?.phone ?? null;
      }
      if (!phone) {
        const number = await db.currentNumber(ctx.from.id);
        phone = number?.phone ?? null;
      }
      if (!phone) {
        return ctx.answerCallbackQuery({ text: tr(ctx.from.id, "accountFetchNoPhone"), show_alert: true });
      }
      const serverUserID = await gramsrv.resolveUserByPhone(phone);
      if (!serverUserID) {
        return ctx.answerCallbackQuery({ text: tr(ctx.from.id, "accountFetchNotFound"), show_alert: true });
      }
      await db.setServerUserID(ctx.from.id, serverUserID);
      await db.clearPending(ctx.from.id);
      db._userCache.set(ctx.from.id, await db.user(ctx.from.id));
      const message = tr(ctx.from.id, "accountSaved", { id: serverUserID });
      await ctx.answerCallbackQuery({ text: message });
      return editOrReply(ctx, message, mainKeyboard(language, isOwner(config, ctx.from.id)));
    } catch (error) {
      console.error("Account fetch failed", error);
      const text = String(error?.message ?? "").startsWith("gramsrv ")
        ? tr(ctx.from.id, "accountFetchFailed")
        : translateError(language, error);
      return ctx.answerCallbackQuery({ text, show_alert: true });
    }
  });

  bot.callbackQuery(/^settings:phone$/, async (ctx) => {
    await ctx.answerCallbackQuery();
    if (!isRealMode(config)) return;
    const bound = await db.verifiedPhone(ctx.from.id);
    const language = languageOf(ctx.from.id);
    if (!bound) {
      const { phoneShareKeyboard } = await import("./real-number.js");
      const sent = await ctx.reply(`${tr(ctx.from.id, "phoneTitle")}\n\n${tr(ctx.from.id, "phoneIntro")}`, { parse_mode: "HTML", reply_markup: phoneShareKeyboard(language) });
      phoneShareMessages.set(ctx.chat.id, sent.message_id);
      return sent;
    }
    const kb = new InlineKeyboard().text(tr(ctx.from.id, "phoneUnbindButton"), "phone:unbind").row().text(tr(ctx.from.id, "back"), "menu:home");
    await editOrReply(ctx, `${tr(ctx.from.id, "phoneTitle")}\n\n${tr(ctx.from.id, "phoneStatus", { phone: escapeHTML(bound.phone) })}`, kb);
  });

  bot.callbackQuery(/^phone:unbind$/, async (ctx) => {
    if (!isRealMode(config)) return;
    await ctx.answerCallbackQuery({ text: translate(languageOf(ctx.from.id), "phoneUnbound") });
    await db.unbindVerifiedPhone(ctx.from.id);
    return numbersMenu(ctx);
  });

  bot.on("message:contact", async (ctx) => {
    if (!isRealMode(config)) return;
    const language = languageOf(ctx.from.id);
    const shareMsgID = phoneShareMessages.get(ctx.chat.id);
    if (shareMsgID) { phoneShareMessages.delete(ctx.chat.id); ctx.api.deleteMessage(ctx.chat.id, shareMsgID).catch(() => {}); }
    ctx.api.deleteMessage(ctx.chat.id, ctx.message.message_id).catch(() => {});
    try {
      if (ctx.message.contact.user_id !== ctx.from.id) throw new Error("errorContactNotOwn");
      const bound = await db.bindVerifiedPhone(ctx.from.id, ctx.chat.id, ctx.message.contact.phone_number);
      const sent = await ctx.reply(tr(ctx.from.id, "phoneBound", { phone: escapeHTML(bound.phone) }), { parse_mode: "HTML" });
      deleteAfter(ctx.chat.id, sent.message_id);
      return ctx.reply(tr(ctx.from.id, "menuTitle"), { reply_markup: mainKeyboard(language, isOwner(config, ctx.from.id)) });
    } catch (error) {
      await ctx.reply(translateError(language, error), { reply_markup: mainKeyboard(language, isOwner(config, ctx.from.id)) });
    }
  });

  bot.callbackQuery(/^bonus:daily$/, async (ctx) => {
    const result = await db.claimDaily(ctx.from.id, config.dailyBonus);
    const language = languageOf(ctx.from.id);
    await ctx.answerCallbackQuery({ text: result.claimed ? `+${config.dailyBonus}` : tr(ctx.from.id, "dailyAlready") });
    await editOrReply(ctx, `${tr(ctx.from.id, "bonusesTitle")}\n\n${tr(ctx.from.id, "balance", { balance: result.balance })}`, backKeyboard(language, "menu:bonuses"));
  });

  bot.callbackQuery(/^bonus:spin$/, async (ctx) => {
    await ctx.answerCallbackQuery();
    const user = await db.user(ctx.from.id);
    const language = languageOf(ctx.from.id);
    if (!user.server_user_id) {
      await db.setPending(ctx.from.id, "account");
      return editOrReply(ctx, tr(ctx.from.id, "accountPrompt"), backKeyboard(language, "menu:bonuses"));
    }
    try {
      const award = await db.reserveSpin(ctx.from.id, user.server_user_id, rollPrize());
      await gramsrv.grantStars(user.server_user_id, award.prize, "Daily bot wheel", `spin:${ctx.from.id}:${award.day}`);
      await db.finishSpin(ctx.from.id, award.day);
      await editOrReply(ctx, tr(ctx.from.id, "wheelWon", { amount: award.prize }), backKeyboard(language, "menu:bonuses"));
    } catch (error) {
      console.error("Wheel grant failed", error);
      await editOrReply(ctx, translateError(language, error), backKeyboard(language, "menu:bonuses"));
    }
  });

  bot.callbackQuery(/^giveaway:([a-f0-9]+)$/, async (ctx) => {
    await ctx.answerCallbackQuery();
    const user = await db.user(ctx.from.id);
    const language = languageOf(ctx.from.id);
    if (!user.server_user_id) return ctx.reply(tr(ctx.from.id, "giveawayNeedAccount"));
    const id = ctx.match[1];
    try {
      const item = await db.claimGiveaway(id, ctx.from.id);
      try { await gramsrv.grantStars(user.server_user_id, item.stars_amount, `Giveaway ${id}`, `giveaway:${id}:${ctx.from.id}`); }
      catch (error) { await db.releaseCampaignClaim("giveaway", id, ctx.from.id); throw error; }
      await ctx.reply(tr(ctx.from.id, "giveawayGranted", { amount: item.stars_amount }));
    } catch (error) {
      console.error("Giveaway claim failed", error);
      await ctx.reply(translateError(language, error));
    }
  });

  bot.callbackQuery(/^admin:(.+)$/, async (ctx) => {
    await ctx.answerCallbackQuery();
    if (!isOwner(config, ctx.from.id)) return;
    const action = ctx.match[1];
    const language = languageOf(ctx.from.id);
    if (action === "menu") return editOrReply(ctx, tr(ctx.from.id, "adminTitle"), adminKeyboard(language));
    if (action === "stats") {
      const stats = await db.stats();
      return editOrReply(ctx, tr(ctx.from.id, "adminStats", stats), backKeyboard(language, "admin:menu"));
    }
    if (action === "lookup") {
      await db.setPending(ctx.from.id, "admin_lookup");
      return editOrReply(ctx, tr(ctx.from.id, "adminPromptLookup"), backKeyboard(language, "admin:menu"));
    }
    const promptKeys = {
      broadcast: "adminPromptBroadcast", stars: "adminPromptStars", premium: "adminPromptPremium", promo: "adminPromptPromo",
      giveaway: "adminPromptGiveaway", bonus: "adminPromptBonus", invoice: "adminPromptInvoice", access: "adminPromptAccess",
      refund: "adminPromptRefund", reply: "adminPromptReply", rate: "adminPromptRate", bindphone: "adminPromptBindPhone",
    };
    if (promptKeys[action]) {
      await db.setPending(ctx.from.id, `admin_${action}`, { operationID: `admin:${ctx.from.id}:${Date.now()}:${randomInt(1_000_000)}` });
      return editOrReply(ctx, tr(ctx.from.id, promptKeys[action], { product: config.productName.toUpperCase() }), backKeyboard(language, "admin:menu"));
    }
  });

  bot.on("message:text", async (ctx) => {
    if (ctx.message.text.startsWith("/")) return;
    const language = languageOf(ctx.from.id);
    const input = ctx.message.text.trim();

    if (input === translate(language, "phoneCancelButton")) {
      const shareMsgID = phoneShareMessages.get(ctx.chat.id);
      if (shareMsgID) { phoneShareMessages.delete(ctx.chat.id); ctx.api.deleteMessage(ctx.chat.id, shareMsgID).catch(() => {}); }
      return ctx.reply(tr(ctx.from.id, "menuTitle"), { reply_markup: mainKeyboard(language, isOwner(config, ctx.from.id)) });
    }

    if (isOwner(config, ctx.from.id) && ctx.message.reply_to_message?.from?.id === (bot.botInfo?.id ?? 0)) {
      const ticketMatch = ctx.message.reply_to_message.text?.match(/#(\d+)/);
      if (ticketMatch) {
        const ticketID = Number(ticketMatch[1]);
        const ticket = await db.supportMessage(ticketID);
        if (ticket?.status === "open") {
          if (!input) return ctx.reply(tr(ctx.from.id, "errorEmptyReply"));
          await bot.api.sendMessage(ticket.telegram_id, tr(ticket.telegram_id, "supportReply", { ticket: ticketID, answer: escapeHTML(input) }), { parse_mode: "HTML" });
          await db.closeSupportMessage(ticketID);
          return ctx.reply(tr(ctx.from.id, "supportReplySent", { ticket: ticketID }), { reply_markup: adminKeyboard(language) });
        }
      }
    }

    const pending = await db.pending(ctx.from.id);
    if (!pending) return;
    const adminResult = async (text, extra = {}) => {
      const sent = await ctx.reply(text, { parse_mode: "HTML", reply_markup: adminKeyboard(language), ...extra });
      if (sent?.message_id) deleteAfter(ctx.chat.id, sent.message_id);
      return sent;
    };
    const toast = async (text, extra = {}) => {
      const sent = await ctx.reply(text, { parse_mode: "HTML", ...extra });
      if (sent?.message_id) deleteAfter(ctx.chat.id, sent.message_id);
      return sent;
    };
    try {
      if (pending.kind === "account") {
        const id = Number(input);
        if (!Number.isSafeInteger(id) || id <= 0) throw new Error("invalid ID");
        await db.setServerUserID(ctx.from.id, id); await db.clearPending(ctx.from.id);
        return ctx.reply(tr(ctx.from.id, "accountSaved", { id }), { parse_mode: "HTML", reply_markup: mainKeyboard(language, isOwner(config, ctx.from.id)) });
      }
      if (pending.kind === "stars_amount") {
        const stars = Number(input);
        if (!Number.isSafeInteger(stars) || stars <= 0 || stars > 99999) throw new Error("amount must be from 1 to 99999");
        const starsRate = await db.starsRate();
        const product = findProduct(`stars_${stars}`, starsRate); await db.clearPending(ctx.from.id);
        const view = localizeProduct(product, language);
        return ctx.reply(productText(view, language), { parse_mode: "HTML", reply_markup: productKeyboard(product, { _cachedUser: db._userCache.get(ctx.from.id) }, ctx.from.id, language) });
      }
      if (pending.kind === "target") {
        const id = Number(input);
        if (!Number.isSafeInteger(id) || id <= 0) throw new Error("invalid ID");
        const starsRate = await db.starsRate();
        const product = findProduct(pending.payload.productCode, starsRate);
        if (!product) throw new Error("product not found");
        await db.rememberRecipient(ctx.from.id, id); await db.clearPending(ctx.from.id);
        if (product.kind === KINDS.username) {
          await db.setPending(ctx.from.id, "username", { productCode: product.code, targetID: id });
          return ctx.reply(tr(ctx.from.id, "enterUsername"));
        }
        if (isOwner(config, ctx.from.id)) return fulfill(product, id, ctx.from, ctx.chat.id, `owner-${ctx.from.id}-${Date.now()}-${randomInt(1_000_000)}`);
        await sendInvoice(ctx, product, id, language); return;
      }
      if (pending.kind === "username") {
        const username = normalizeUsername(input);
        if (!username) throw new Error("username must be 5-32 latin characters and start with a letter");
        const starsRate = await db.starsRate();
        const product = findProduct(pending.payload.productCode, starsRate);
        if (!product) throw new Error("product not found");
        await db.clearPending(ctx.from.id);
        if (isOwner(config, ctx.from.id)) return fulfill(product, pending.payload.targetID, ctx.from, ctx.chat.id, `owner-${ctx.from.id}-${Date.now()}-${randomInt(1_000_000)}`, username);
        await sendInvoice(ctx, product, pending.payload.targetID, language, username); return;
      }
      if (pending.kind === "support") {
        const ticket = await db.addSupportMessage(ctx.from.id, ctx.chat.id, input); await db.clearPending(ctx.from.id);
        for (const owner of config.ownerIDs) {
          const message = `${tr(owner, "supportOwnerTicket", { ticket })}\n${tr(owner, "supportOwnerFrom", { name: escapeHTML(userName(ctx.from)), id: ctx.from.id })}\n\n${escapeHTML(input)}`;
          await bot.api.sendMessage(owner, message, { parse_mode: "HTML" }).catch(() => {});
        }
        return toast(tr(ctx.from.id, "supportTicketSent", { ticket }));
      }
      if (!isOwner(config, ctx.from.id)) return;
      if (pending.kind === "admin_lookup") {
        await db.clearPending(ctx.from.id);
        const isPhone = /^\+/.test(input);
        const result = isPhone ? await db.adminLookupByNumber(input) : await db.adminLookupByTelegramID(Number(input));
        if (!result) return adminResult(tr(ctx.from.id, "adminLookupNotFound"));
        const lines = [];
        if (result.user) {
          const u = result.user;
          lines.push(`👤 <b>User</b>: <code>${u.telegram_id}</code> · @${escapeHTML(u.username || "—")} · lang=${u.language} · bonus=${u.bonus}`);
        }
        if (result.number) {
          const n = result.number;
          lines.push(`📱 <b>Number</b>: <code>${escapeHTML(n.display)}</code> · owner=<code>${n.owner_id}</code> · current=${n.is_current} · format=${n.format}`);
        }
        if (result.numbers) {
          for (const n of result.numbers) lines.push(`📱 <b>Number</b>: <code>${escapeHTML(n.display)}</code> · current=${n.is_current} · format=${n.format}`);
        }
        if (result.verifiedPhone) {
          lines.push(`📞 <b>Verified phone</b>: <code>${escapeHTML(result.verifiedPhone.phone)}</code>`);
        }
        return adminResult(tr(ctx.from.id, "adminLookupResult", { result: lines.join("\n") }));
      }
      if (pending.kind === "admin_broadcast") {
        await db.clearPending(ctx.from.id);
        let ok = 0, failed = 0;
        const recipients = await db.notificationRecipients(config.notificationTTLDays);
        const allUsers = await db.users();
        const skipped = allUsers.length - recipients.length;
        for (const user of recipients) {
          try { await bot.api.sendMessage(user.chat_id, input, { parse_mode: "HTML" }); ok++; } catch { failed++; }
        }
        return adminResult(tr(ctx.from.id, "broadcastDone", { ok, skipped, failed }));
      }
      if (pending.kind === "admin_stars") {
        const [id, amount] = input.split(/\s+/).map(Number);
        if (!Number.isSafeInteger(id) || id <= 0 || !Number.isSafeInteger(amount) || amount <= 0) throw new Error("invalid ID or amount");
        await gramsrv.grantStars(id, amount, "Telegram bot administrator grant", pending.payload.operationID); await db.clearPending(ctx.from.id);
        return adminResult(tr(ctx.from.id, "starsGranted", { id, amount }));
      }
      if (pending.kind === "admin_premium") {
        const [id, months] = input.split(/\s+/).map(Number);
        if (!Number.isSafeInteger(id) || id <= 0 || !Number.isSafeInteger(months) || months <= 0) throw new Error("invalid ID or months");
        await gramsrv.grantPremium(id, months, "Telegram bot administrator grant", pending.payload.operationID); await db.clearPending(ctx.from.id);
        return adminResult(tr(ctx.from.id, "premiumGranted", { id, months }));
      }
      if (pending.kind === "admin_promo") {
        const [code, starsRaw, limitRaw] = input.split(/\s+/);
        const stars = Number(starsRaw), limit = Number(limitRaw);
        const normalized = await db.createPromo(code, stars, limit); await db.clearPending(ctx.from.id);
        return adminResult(tr(ctx.from.id, "promoCreated", { code: normalized }));
      }
      if (pending.kind === "admin_giveaway") {
        const [starsRaw, limitRaw, ...words] = input.split(/\s+/);
        const item = await db.createGiveaway(words.join(" "), Number(starsRaw), Number(limitRaw)); await db.clearPending(ctx.from.id);
        return ctx.reply(`🎁 ${escapeHTML(item.text)}`, { parse_mode: "HTML", reply_markup: new InlineKeyboard().text(tr(ctx.from.id, "claimReward"), `giveaway:${item.id}`).row().text(tr(ctx.from.id, "back"), "admin:menu") });
      }
      if (pending.kind === "admin_bonus") {
        const [id, amount] = input.split(/\s+/).map(Number);
        if (!Number.isSafeInteger(id) || id <= 0 || !Number.isSafeInteger(amount) || amount === 0) throw new Error("invalid ID or amount");
        const balance = await db.addBonus(id, amount); await db.clearPending(ctx.from.id);
        return adminResult(tr(ctx.from.id, "bonusBalance", { id, balance }));
      }
      if (pending.kind === "admin_invoice") {
        const [idRaw, starsRaw, ...words] = input.split(/\s+/);
        const id = Number(idRaw), stars = Number(starsRaw), title = words.join(" ").trim();
        if (!Number.isSafeInteger(id) || id <= 0 || !Number.isSafeInteger(stars) || stars <= 0 || !title || title.length > 32) throw new Error("invalid invoice");
        const targetLanguage = languageOf(id);
        await bot.api.sendInvoice(id, title, translate(targetLanguage, "invoiceDescription", { title }), `custom|${Buffer.from(title).toString("base64url")}`, "XTR", [{ label: title, amount: stars }]);
        await db.clearPending(ctx.from.id); return adminResult(tr(ctx.from.id, "invoiceSent"));
      }
      if (pending.kind === "admin_access") {
        const [phone, telegramRaw] = input.split(/\s+/); const telegramID = Number(telegramRaw);
        if (!phone || !Number.isSafeInteger(telegramID) || telegramID <= 0) throw new Error("invalid phone or Telegram ID");
        await db.grantCodeAccess(phone, telegramID); await db.clearPending(ctx.from.id);
        return adminResult(tr(ctx.from.id, "accessGranted", { phone: escapeHTML(phone), id: telegramID }));
      }
      if (pending.kind === "admin_bindphone") {
        const [idRaw, ...phoneParts] = input.split(/\s+/);
        const targetID = Number(idRaw); const phone = phoneParts.join("");
        if (!Number.isSafeInteger(targetID) || targetID <= 0 || !phone) throw new Error("invalid Telegram ID or phone");
        const user = await db.user(targetID);
        if (!user) return adminResult(tr(ctx.from.id, "userNotFound"));
        await db.bindVerifiedPhone(targetID, user.chat_id, phone);
        await db.clearPending(ctx.from.id);
        return adminResult(tr(ctx.from.id, "bindPhoneDone", { phone: escapeHTML(phone), id: targetID }));
      }
      if (pending.kind === "admin_refund") {
        const parts = input.split(/\s+/); let telegramID, chargeID, sale;
        if (parts.length === 1) { chargeID = parts[0]; sale = await db.saleByCharge(chargeID); telegramID = sale?.buyer_id; }
        else if (parts.length === 2) { telegramID = positiveInteger(parts[0]); chargeID = parts[1]; }
        if (!telegramID || !chargeID) throw new Error("sale not found for this transaction ID");
        if (await db.isRefunded(chargeID)) throw new Error("payment was already refunded");
        sale ??= await db.saleByCharge(chargeID);
        if (!sale || sale.buyer_id !== telegramID || sale.payment_status !== "done") throw new Error("completed sale or its owner was not found");
        await executeCompensatedRefund({ sale, telegramID, db, gramsrv, refundStarPayment: bot.api.refundStarPayment.bind(bot.api) });
        await db.clearPending(ctx.from.id);
        await bot.api.sendMessage(telegramID, tr(telegramID, "paymentRefunded", { charge: escapeHTML(chargeID) }), { parse_mode: "HTML" }).catch(() => {});
        return adminResult(tr(ctx.from.id, "refundDone"));
      }
      if (pending.kind === "admin_reply") {
        const [ticketRaw, ...words] = input.split(/\s+/); const ticketID = Number(ticketRaw), answer = words.join(" ").trim();
        const ticket = await db.supportMessage(ticketID);
        if (!ticket || !answer) throw new Error("ticket not found or reply is empty");
        await bot.api.sendMessage(ticket.telegram_id, tr(ticket.telegram_id, "supportReply", { ticket: ticketID, answer: escapeHTML(answer) }), { parse_mode: "HTML" });
        await db.closeSupportMessage(ticketID); await db.clearPending(ctx.from.id);
        return adminResult(tr(ctx.from.id, "supportReplySent", { ticket: ticketID }));
      }
      if (pending.kind === "admin_rate") {
        const rate = Number(input);
        if (!Number.isSafeInteger(rate) || rate <= 0) throw new Error("invalid rate");
        await db.setSetting("stars_rate", rate); await db.clearPending(ctx.from.id);
        return adminResult(tr(ctx.from.id, "rateSaved", { rate }));
      }
    } catch (error) {
      console.error("Bot input action failed", pending.kind, error);
      await ctx.reply(translateError(language, error));
    }
  });

  bot.catch(async ({ error, ctx }) => {
    if (error instanceof GrammyError) console.error("Telegram API error", error.description);
    else if (error instanceof HttpError) console.error("Telegram network error", error);
    else console.error(`Bot update ${ctx.update.update_id} failed`, error);
    if (ctx.from && ctx.chat) await ctx.reply(tr(ctx.from.id, "genericError")).catch(() => {});
  });
  return bot;
}
