import test from "node:test";
import assert from "node:assert/strict";
import { createBot } from "../src/bot.js";
import { BotDatabase } from "../src/db.js";
import { buildPayload } from "../src/catalog.js";

const botInfo = { id: 999, is_bot: true, first_name: "Test", username: "test_bot", can_join_groups: true, can_read_all_group_messages: false, supports_inline_queries: false };

function mockDb() {
  const users = new Map();
  const numbers = new Map();
  const tickets = new Map();
  const settings = new Map([["stars_rate", "20"], ["number_discount_percent", "0"], ["free_number_daily_limit", "0"]]);
  const pendingState = new Map();
  let numberSeq = 1;
  const db = {
    _userCache: new Map(),
    _cachedUser: null,
    _settings: settings,
    upsertUser: async (from, chatID, language = "ru", referrerID = 0, referralBonus = 0) => {
      const existing = users.get(from.id);
      if (!users.has(from.id)) {
        users.set(from.id, { telegram_id: from.id, chat_id: chatID, username: from.username ?? "", first_name: from.first_name ?? "", server_user_id: 0, language, notifications: 1, bonus: 0, referred_by: null, referral_count: 0, daily_day: "", spin_day: "", spin_day_count: 0, spin_week: "", spin_week_count: 0, free_day: "", free_day_count: 0, created_at: 0, updated_at: 0 });
      }
      const user = users.get(from.id);
      user.chat_id = chatID;
      user.username = from.username ?? "";
      user.first_name = from.first_name ?? "";
      if (!existing && referrerID > 0 && referrerID !== from.id && users.has(referrerID)) {
        user.referred_by = referrerID;
        const referrer = users.get(referrerID);
        referrer.referral_count++;
        referrer.bonus += referralBonus;
      }
      return user;
    },
    user: async (id) => users.get(id) ?? null,
    userByChatID: async (chatID) => { for (const u of users.values()) if (u.chat_id === chatID) return u; return null; },
    users: async () => [...users.values()],
    notificationRecipients: async () => [...users.values()].filter((u) => u.notifications === 1),
    stats: async () => ({ users: users.size, numbers: numbers.size, sales: 0 }),
    setLanguage: async (id, lang) => { const u = users.get(id); if (u) u.language = lang; },
    toggleNotifications: async (id) => { const u = users.get(id); if (u) { u.notifications = u.notifications ? 0 : 1; return Boolean(u.notifications); } return false; },
    setServerUserID: async (id, sid) => { const u = users.get(id); if (u) u.server_user_id = sid; },
    addBonus: async (id, amount) => { const u = users.get(id); if (!u) throw new Error("invalid Telegram ID"); u.bonus = Math.max(0, u.bonus + amount); return u.bonus; },
    claimDaily: async (id, amount) => { const u = users.get(id); if (!u) throw new Error("user not found"); return { claimed: true, balance: u.bonus + amount }; },
    createNumber: async (ownerID, chatID, format = "free", country = "RU", replace = false) => {
      const existing = [...numbers.values()].find((n) => n.owner_id === ownerID && n.is_current);
      if (existing && !replace) return existing;
      if (existing) existing.is_current = false;
      const id = numberSeq++;
      const phone = `+7999${String(id).padStart(7, "0")}`;
      const num = { id, phone, display: phone, format, country, owner_id: ownerID, chat_id: chatID, is_current: true, login_code: "12345", code_expires_at: 9999999999, created_at: 0 };
      numbers.set(id, num);
      for (const n of [...numbers.values()]) {
        if (n.owner_id === ownerID && n.format === "free" && n.id !== id) numbers.delete(n.id);
      }
      return num;
    },
    currentNumber: async (ownerID) => [...numbers.values()].find((n) => n.owner_id === ownerID && n.is_current) ?? null,
    numbers: async (ownerID) => [...numbers.values()].filter((n) => n.owner_id === ownerID),
    findNumber: async (phone) => [...numbers.values()].find((n) => n.phone === phone) ?? null,
    updateLoginCode: async (phone, code) => { const n = [...numbers.values()].find((x) => x.phone === phone); if (n) { n.login_code = code; return { number: n, chatIDs: [n.chat_id] }; } return { number: null, chatIDs: [] }; },
    acceptLoginCodeDelivery: async () => ({ duplicate: false, number: null, chatIDs: [] }),
    grantCodeAccess: async () => {},
    revokePurchasedNumber: async () => false,
    getSetting: async (key, fallback = "") => settings.get(key) ?? fallback,
    setSetting: async (key, value) => { settings.set(key, String(value)); },
    starsRate: async () => Number(settings.get("stars_rate") ?? 20),
    numberDiscountPercent: async () => {
      const value = Number(settings.get("number_discount_percent") ?? 0);
      return Number.isSafeInteger(value) && value >= 0 ? Math.min(100, value) : 0;
    },
    freeNumberDailyLimit: async () => {
      const value = Number(settings.get("free_number_daily_limit") ?? 0);
      return Number.isSafeInteger(value) && value >= 0 ? value : 0;
    },
    freeNumberDailyCount: async (id) => {
      const u = users.get(id);
      if (!u || typeof u.free_day_count !== "number") return 0;
      return u.free_day_count;
    },
    productPrices: async () => ({}),
    adminBindNumber: async (ownerID, chatID, phone) => {
      for (const number of [...numbers.values()]) if (number.owner_id === ownerID) numbers.delete(number.id);
      const id = numberSeq++;
      const format = phone.startsWith("+8888") ? "short" : phone.startsWith("+8880") ? "long" : "free";
      const country = format === "short" || format === "long" ? "ANON" : phone.startsWith("+1") ? "US" : "RU";
      const num = { id, phone, display: phone, format, country, owner_id: ownerID, chat_id: chatID, is_current: true, login_code: "", code_expires_at: 0, created_at: 0 };
      numbers.set(id, num);
      return num;
    },
    fulfillNumberPurchase: async ({ recipientID }, _chatID, numberFormat) => {
      for (const number of [...numbers.values()]) if (number.owner_id === recipientID) numbers.delete(number.id);
      const id = numberSeq++;
      const phone = numberFormat === "short" ? "+88881234567" : numberFormat === "long" ? "+88809876543210" : "+79991234567";
      const num = { id, phone, display: phone, format: numberFormat, country: "ANON", owner_id: recipientID, chat_id: recipientID, is_current: true, login_code: "", code_expires_at: 0, created_at: 0 };
      numbers.set(id, num);
      return num;
    },
    setPending: async (id, kind, payload = {}) => { pendingState.set(id, { kind, payload }); },
    pending: async (id) => pendingState.get(id) ?? null,
    clearPending: async (id) => { pendingState.delete(id); },
    recentRecipients: async () => [],
    rememberRecipient: async () => {},
    reserveSpin: async () => ({ prize: 50, day: "2026-01-01", spin_key: "mock-spin" }),
    finishSpin: async () => {},
    createPromo: async (code) => code,
    claimPromo: async (code, id) => ({ stars_amount: 50 }),
    createGiveaway: async () => ({ id: "abc", text: "test", stars_amount: 10 }),
    claimGiveaway: async () => ({ stars_amount: 10 }),
    releaseCampaignClaim: async () => {},
    beginPayment: async () => true,
    finishPayment: async () => {},
    failPayment: async () => {},
    addSale: async () => {},
    saleByCharge: async () => null,
    recentSales: async () => [],
    refundByCharge: async () => null,
    isRefunded: async () => false,
    beginRefund: async () => ({ internal_reversed: false }),
    markRefundInternal: async () => {},
    failRefund: async () => {},
    markRefunded: async () => {},
    addSupportMessage: async (id, chatID, text) => { const ticket = tickets.size + 1; tickets.set(ticket, { id: ticket, telegram_id: id, chat_id: chatID, text, status: "open", created_at: 0, answered_at: 0, answered_by: 0, answer: "" }); return ticket; },
    supportMessage: async (ticketID) => tickets.get(ticketID) ?? null,
    closeSupportMessage: async (ticketID, answeredBy = 0, answer = "") => {
      const t = tickets.get(ticketID);
      if (!t || t.status !== "open") return false;
      t.status = "answered"; t.answered_by = answeredBy; t.answer = answer;
      return true;
    },
    verifiedPhone: async () => null,
    bindVerifiedPhone: async () => ({ phone: "+79990000000" }),
    unbindVerifiedPhone: async () => true,
    adminLookupByNumber: async () => null,
    adminLookupByTelegramID: async () => null,
    adminLookupByUsername: async () => null,
    userByUsername: async (username) => { for (const u of users.values()) if (String(u.username ?? "").toLowerCase() === String(username ?? "").replace(/^@/, "").toLowerCase()) return u; return null; },
    adminRecentActivity: async () => ({ sales: [], refunds: [] }),
    rateSupportTicket: async (ticketID, rating, telegramID) => {
      const t = tickets.get(ticketID);
      if (!t || t.telegram_id !== telegramID || t.status !== "answered" || t.rating) return false;
      t.rating = rating;
      return true;
    },
    recentSupportRatings: async () => [...tickets.values()].filter((t) => t.rating).map((t) => ({ id: t.id, answered_by: t.answered_by, rating: t.rating, rated_at: 0, created_at: 0 })),
    close: async () => {},
  };
  return db;
}

function fixture() {
  const db = mockDb();
  const calls = [];
  const config = {
    botToken: "999:TEST",
    defaultLanguage: "ru",
    defaultNumberCountry: "RU",
    botMode: "random",
    ownerIDs: new Set(),
    requiredChannel: "",
    requiredChannelURL: "",
    referralBonus: 100,
    dailyBonus: 10,
    notificationTTLDays: 30,
    productName: "ShuzaGram",
    publicUsername: "test_bot",
    gramsrvAPI: "http://localhost:9999",
    gramsrvToken: "test",
    gramsrvActor: "test",
    publicBaseURL: "https://example.com",
    codeHost: "127.0.0.1",
    codePort: 0,
    codeWebhookSecret: "test-secret-12345678901234",
  };
  const gramsrv = {};
  const bot = createBot({ config, db, gramsrv });
  bot.botInfo = botInfo;
  bot.api.config.use(async (_previous, method, payload) => {
    calls.push({ method, payload });
    if (method === "answerCallbackQuery") return { ok: true, result: true };
    return {
      ok: true,
      result: { message_id: 100, date: 1, chat: { id: payload.chat_id ?? 1, type: "private" }, text: payload.text ?? "" },
    };
  });
  return { bot, calls, config, db, gramsrv };
}

test("English language callback persists and redraws all settings controls in English", async () => {
  const { bot, calls, db } = fixture();
  await db.upsertUser({ id: 10, first_name: "User", language_code: "ru" }, 10, "ru");
  await bot.handleUpdate({
    update_id: 1,
    callback_query: {
      id: "callback-1",
      from: { id: 10, is_bot: false, first_name: "User", language_code: "ru" },
      chat_instance: "instance",
      data: "settings:lang:en",
      message: { message_id: 1, date: 1, chat: { id: 10, type: "private" }, text: "Настройки" },
    },
  });
  const user = await db.user(10);
  assert.equal(user.language, "en");
  const answer = calls.find((call) => call.method === "answerCallbackQuery");
  assert.equal(answer.payload.text, "Language switched to English.");
  const edit = calls.find((call) => call.method === "editMessageText");
  assert.match(edit.payload.text, /Settings/);
  const labels = edit.payload.reply_markup.inline_keyboard.flat().map((button) => button.text).join("\n");
  assert.match(labels, /Account/);
  assert.match(labels, /Notifications: on/);
  assert.doesNotMatch(labels, /[А-Яа-яЁё]/u);
});

test("first /start applies referral before generic user registration", async () => {
  const { bot, calls, db } = fixture();
  await db.upsertUser({ id: 1, first_name: "Referrer" }, 1, "ru");
  await bot.handleUpdate({
    update_id: 2,
    message: {
      message_id: 2,
      date: 1,
      chat: { id: 2, type: "private" },
      from: { id: 2, is_bot: false, first_name: "Guest", language_code: "en" },
      text: "/start ref_1",
      entities: [{ offset: 0, length: 6, type: "bot_command" }],
    },
  });
  const user2 = await db.user(2);
  assert.equal(user2.referred_by, 1);
  const user1 = await db.user(1);
  assert.equal(user1.referral_count, 1);
  assert.equal(user1.bonus, 100);
  const sent = calls.find((call) => call.method === "sendMessage");
  assert.match(sent.payload.text, /Welcome!/);
  assert.match(sent.payload.text, /Referral invitation applied/);
});

test("real mode rejects random number generation", async () => {
  const { bot, calls, db } = fixture();
  db._userCache = new Map();
  const config = {
    botToken: "999:TEST", defaultLanguage: "ru", defaultNumberCountry: "RU", botMode: "real",
    ownerIDs: new Set(), requiredChannel: "", requiredChannelURL: "", referralBonus: 100,
    dailyBonus: 10, notificationTTLDays: 30, productName: "ShuzaGram", publicUsername: "test_bot",
    gramsrvAPI: "http://localhost:9999", gramsrvToken: "test", gramsrvActor: "test",
    publicBaseURL: "https://example.com", codeHost: "127.0.0.1", codePort: 0,
    codeWebhookSecret: "test-secret-12345678901234",
  };
  const realBot = createBot({ config, db, gramsrv: {} });
  realBot.botInfo = botInfo;
  realBot.api.config.use(async (_prev, method, payload) => {
    calls.push({ method, payload });
    if (method === "answerCallbackQuery") return { ok: true, result: true };
    return { ok: true, result: { message_id: 100, date: 1, chat: { id: payload.chat_id ?? 1, type: "private" }, text: payload.text ?? "" } };
  });
  await db.upsertUser({ id: 10, first_name: "RealUser" }, 10, "ru");
  await realBot.handleUpdate({
    update_id: 1,
    callback_query: {
      id: "cb-1",
      from: { id: 10, is_bot: false, first_name: "RealUser" },
      chat_instance: "instance",
      data: "numbers:new",
      message: { message_id: 1, date: 1, chat: { id: 10, type: "private" }, text: "Numbers" },
    },
  });
  const rejectMsg = calls.find((call) => call.method === "sendMessage" && (call.payload.text?.includes("недоступна") || call.payload.text?.includes("not available")));
  assert.ok(rejectMsg, "Real mode should reject random number generation");
});

function accountCallbackUpdate({ fromID = 10, chatID = 10, data, messageText = "Settings" }) {
  return {
    update_id: Date.now(),
    callback_query: {
      id: `cb-${fromID}`,
      from: { id: fromID, is_bot: false, first_name: "User", language_code: "ru" },
      chat_instance: "instance",
      data,
      message: { message_id: 1, date: 1, chat: { id: chatID, type: "private" }, text: messageText },
    },
  };
}

function preCheckoutUpdate({ fromID = 10, payload, total }) {
  return {
    update_id: Date.now(),
    pre_checkout_query: {
      id: `pcq-${fromID}`,
      from: { id: fromID, is_bot: false, first_name: "User", language_code: "ru" },
      currency: "XTR",
      total_amount: total,
      invoice_payload: payload,
    },
  };
}

async function seedAccountUser(db, { serverUserID = 0, hasNumber = false, verifiedPhone = null } = {}) {
  await db.upsertUser({ id: 10, first_name: "User", language_code: "ru" }, 10, "ru");
  const user = await db.user(10);
  user.server_user_id = serverUserID;
  if (hasNumber) await db.createNumber(10, 10);
  if (verifiedPhone) db.verifiedPhone = async () => ({ phone: verifiedPhone });
  return user;
}

test("Account ID menu offers fetch and manual entry", async () => {
  const { bot, calls, db } = fixture();
  await seedAccountUser(db);
  await bot.handleUpdate(accountCallbackUpdate({ data: "settings:account" }));
  const edit = calls.find((call) => call.method === "editMessageText");
  const labels = edit.payload.reply_markup.inline_keyboard.flat().map((button) => button.text).join("\n");
  assert.match(labels, /Найти мой ID/);
  assert.match(labels, /Ввести вручную/);
});

test("Account fetch resolves the current number and saves the server id", async () => {
  const { bot, calls, db, gramsrv } = fixture();
  await seedAccountUser(db, { hasNumber: true });
  const resolveCalls = [];
  gramsrv.resolveUserByPhone = async (phone) => { resolveCalls.push(phone); return 1780243207; };
  await bot.handleUpdate(accountCallbackUpdate({ data: "settings:account:fetch" }));
  assert.deepEqual(resolveCalls, ["+79990000001"]);
  assert.equal((await db.user(10)).server_user_id, 1780243207);
  const answer = calls.find((call) => call.method === "answerCallbackQuery");
  assert.match(answer.payload.text, /1780243207/);
});

test("Account fetch uses the bound verified phone in real mode", async () => {
  const { bot, calls, db, config, gramsrv } = fixture();
  config.botMode = "real";
  await seedAccountUser(db, { hasNumber: true, verifiedPhone: "+79991234567" });
  const resolveCalls = [];
  gramsrv.resolveUserByPhone = async (phone) => { resolveCalls.push(phone); return 1780243207; };
  await bot.handleUpdate(accountCallbackUpdate({ data: "settings:account:fetch" }));
  assert.deepEqual(resolveCalls, ["+79991234567"]);
  assert.equal((await db.user(10)).server_user_id, 1780243207);
  assert.match(calls.find((call) => call.method === "answerCallbackQuery").payload.text, /1780243207/);
});

test("Account fetch reports when no lookup number exists", async () => {
  const { bot, calls, db, gramsrv } = fixture();
  await seedAccountUser(db);
  let resolveCalled = false;
  gramsrv.resolveUserByPhone = async () => { resolveCalled = true; return 0; };
  await bot.handleUpdate(accountCallbackUpdate({ data: "settings:account:fetch" }));
  assert.equal(resolveCalled, false);
  assert.match(calls.find((call) => call.method === "answerCallbackQuery").payload.text, /нет привязанного номера/);
});

test("Account fetch reports when the phone has no account", async () => {
  const { bot, calls, db, gramsrv } = fixture();
  await seedAccountUser(db, { hasNumber: true });
  gramsrv.resolveUserByPhone = async () => 0;
  await bot.handleUpdate(accountCallbackUpdate({ data: "settings:account:fetch" }));
  assert.equal((await db.user(10)).server_user_id, 0);
  assert.match(calls.find((call) => call.method === "answerCallbackQuery").payload.text, /не найден аккаунт/);
});

test("requesting a new free number replaces the previous number so exactly one remains", async () => {
  const { bot, calls, db } = fixture();
  await db.upsertUser({ id: 10, first_name: "User", language_code: "ru" }, 10, "ru");
  const first = await db.createNumber(10, 10, "free", "RU", false);
  await bot.handleUpdate(accountCallbackUpdate({ data: "numbers:new:RU" }));
  const owned = await db.numbers(10);
  assert.equal(owned.length, 1, "exactly one number is owned after a re-roll");
  const current = await db.currentNumber(10);
  assert.notEqual(current.id, first.id);
  assert.equal(current.is_current, true);
  assert.equal(await db.findNumber(first.phone), null, "the previous number was released to the pool");
  await bot.handleUpdate(accountCallbackUpdate({ data: "menu:numbers" }));
  const menu = calls.filter((call) => call.method === "editMessageText").at(-1);
  assert.ok(menu.payload.text.includes(current.display));
  assert.ok(!menu.payload.text.includes(first.display));
});

test("numbers menu hides the free number button after buying +888", async () => {
  const { bot, calls, db } = fixture();
  await db.upsertUser({ id: 10, first_name: "User", language_code: "ru" }, 10, "ru");
  await db.createNumber(10, 10, "short", "ANON", true);
  await bot.handleUpdate(accountCallbackUpdate({ data: "menu:numbers" }));
  const edit = calls.find((call) => call.method === "editMessageText");
  assert.ok(edit, "Random mode numbers menu should be editable");
  const labels = edit.payload.reply_markup.inline_keyboard.flat().map((button) => button.text).join("\n");
  assert.doesNotMatch(labels, /Новый бесплатный номер|New free number/);
  assert.match(edit.payload.text, /бесплатный номер недоступен|free number is unavailable/i);
});

test("numbers menu still offers a free number for a user with no purchased number", async () => {
  const { bot, calls, db } = fixture();
  await db.upsertUser({ id: 10, first_name: "User", language_code: "ru" }, 10, "ru");
  await db.createNumber(10, 10, "free", "RU", false);
  await bot.handleUpdate(accountCallbackUpdate({ data: "menu:numbers" }));
  const edit = calls.find((call) => call.method === "editMessageText");
  const labels = edit.payload.reply_markup.inline_keyboard.flat().map((button) => button.text).join("\n");
  assert.match(labels, /Новый бесплатный номер/);
});

function textUpdate({ fromID, chatID, text }) {
  return {
    update_id: Date.now(),
    message: {
      message_id: Date.now(),
      date: 1,
      chat: { id: chatID, type: "private" },
      from: { id: fromID, is_bot: false, first_name: "User", language_code: "ru" },
      text,
    },
  };
}

test("admin ticket reply is delivered to the user's private chat", async () => {
  const { bot, calls, db, config } = fixture();
  config.ownerIDs.add(777);
  await db.upsertUser({ id: 10, first_name: "User", language_code: "ru" }, 10, "ru");
  await db.upsertUser({ id: 777, first_name: "Admin", language_code: "ru" }, 777, "ru");
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 10, chatID: 10, data: "menu:support" }));
  await bot.handleUpdate(textUpdate({ fromID: 10, chatID: 10, text: "help me with my order" }));
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:reply" }));
  await bot.handleUpdate(textUpdate({ fromID: 777, chatID: 777, text: "1 restart your server" }));
  const delivered = calls.filter((call) => call.method === "sendMessage" && call.payload.chat_id === 10 && /restart your server/.test(call.payload.text)).at(-1);
  assert.ok(delivered, "The reply should be delivered to the user's private chat");
  assert.match(delivered.payload.text, /restart your server/);
  assert.match(delivered.payload.text, /обращени|тикет|ticket/i);
  const ticket = await db.supportMessage(1);
  assert.equal(ticket.status, "answered");
  assert.equal(ticket.answered_by, "777", "the ticket is assigned to the exact admin who answered");
  assert.equal(ticket.answer, "restart your server");
  assert.ok(calls.some((call) => call.method === "sendMessage" && call.payload.chat_id === 10 && /Оцените|Rate/i.test(call.payload.text)), "the user receives a rating prompt");
  const confirm = calls.find((call) => call.method === "sendMessage" && call.payload.chat_id === 777);
  assert.match(confirm.payload.text, /#1/);
});

test("admin replies to a ticket by replying to the notification message", async () => {
  const { bot, calls, db, config } = fixture();
  config.ownerIDs.add(777);
  await db.upsertUser({ id: 10, first_name: "User", language_code: "ru" }, 10, "ru");
  await db.upsertUser({ id: 777, first_name: "Admin", language_code: "ru" }, 777, "ru");
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 10, chatID: 10, data: "menu:support" }));
  await bot.handleUpdate(textUpdate({ fromID: 10, chatID: 10, text: "help me with my order" }));
  await bot.handleUpdate({
    update_id: Date.now(),
    message: {
      message_id: Date.now(), date: 1,
      chat: { id: 777, type: "private" },
      from: { id: 777, is_bot: false, first_name: "Admin", language_code: "ru" },
      text: "restart your server",
      reply_to_message: { message_id: 5, date: 1, chat: { id: 777, type: "private" }, from: { id: bot.botInfo.id, is_bot: true, first_name: "Test" }, text: "💬 Тикет #1\nОт: User (<code>10</code>)\n\nhelp me with my order" },
    },
  });
  const delivered = calls.filter((call) => call.method === "sendMessage" && call.payload.chat_id === 10 && /restart your server/.test(call.payload.text)).at(-1);
  assert.ok(delivered, "The reply should reach the user via Telegram native reply");
  assert.match(delivered.payload.text, /restart your server/);
  const ticket = await db.supportMessage(1);
  assert.equal(ticket.status, "answered");
  assert.equal(ticket.answered_by, "777", "the ticket is assigned to the exact admin who answered");
  assert.equal(ticket.answer, "restart your server");
  const confirmation = calls.filter((call) => call.method === "sendMessage" && call.payload.chat_id === 777).at(-1);
  assert.match(confirmation.payload.text, /#1/);
});

test("replying to a closed ticket does not leak into a leftover giveaway prompt", async () => {
  const { bot, calls, db, config } = fixture();
  config.ownerIDs.add(777);
  await db.upsertUser({ id: 10, first_name: "User", language_code: "ru" }, 10, "ru");
  await db.upsertUser({ id: 777, first_name: "Admin", language_code: "ru" }, 777, "ru");
  let giveawayCalls = 0;
  db.createGiveaway = async (text, stars, limit) => {
    giveawayCalls++;
    if (!text || text.length > 1000 || !Number.isSafeInteger(stars) || stars <= 0 || !Number.isSafeInteger(limit) || limit < 0) throw new Error("invalid giveaway parameters");
    return { id: "abc", text: "test", stars_amount: 10 };
  };
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 10, chatID: 10, data: "menu:support" }));
  await bot.handleUpdate(textUpdate({ fromID: 10, chatID: 10, text: "help me with my order" }));
  await db.closeSupportMessage(1, "111", "already answered");
  await db.setPending(777, "admin_giveaway");
  await bot.handleUpdate({
    update_id: Date.now(),
    message: {
      message_id: Date.now(), date: 1,
      chat: { id: 777, type: "private" },
      from: { id: 777, is_bot: false, first_name: "Admin", language_code: "ru" },
      text: "restart your server",
      reply_to_message: { message_id: 5, date: 1, chat: { id: 777, type: "private" }, from: { id: bot.botInfo.id, is_bot: true, first_name: "Test" }, text: "💬 Тикет #1\nОт: User (<code>10</code>)\n\nhelp me with my order" },
    },
  });
  assert.equal(giveawayCalls, 0, "a ticket reply must never reach the giveaway creator");
  const adminReply = calls.filter((call) => call.method === "sendMessage" && call.payload.chat_id === 777).at(-1);
  assert.match(adminReply.payload.text, /уже отвечено|already answered/i);
});

test("admin input errors are sent with HTML parse mode so <code> renders", async () => {
  const { bot, calls, db, config } = fixture();
  config.ownerIDs.add(777);
  db.createGiveaway = async (text, stars, limit) => {
    if (!text || text.length > 1000 || !Number.isSafeInteger(stars) || stars <= 0 || !Number.isSafeInteger(limit) || limit < 0) throw new Error("invalid giveaway parameters");
    return { id: "abc", text: "test", stars_amount: 10 };
  };
  await db.upsertUser({ id: 777, first_name: "Admin", language_code: "ru" }, 777, "ru");
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:giveaway" }));
  await bot.handleUpdate(textUpdate({ fromID: 777, chatID: 777, text: "not a giveaway" }));
  const reply = calls.filter((call) => call.method === "sendMessage" && call.payload.chat_id === 777).at(-1);
  assert.equal(reply.payload.parse_mode, "HTML", "admin errors must render <code> tags");
  assert.match(reply.payload.text, /invalid giveaway parameters/);
  assert.match(reply.payload.text, /<code>/);
});

test("a user can rate a closed ticket once", async () => {
  const { bot, calls, db, config } = fixture();
  config.ownerIDs.add(777);
  await db.upsertUser({ id: 10, first_name: "User", language_code: "ru" }, 10, "ru");
  await db.upsertUser({ id: 777, first_name: "Admin", language_code: "ru" }, 777, "ru");
  await db.addSupportMessage(10, 10, "help me with my order");
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 10, chatID: 10, data: "rate:1:5" }));
  const rejected = calls.filter((call) => call.method === "editMessageText" && call.payload.chat_id === 10).at(-1);
  assert.match(rejected.payload.text, /Не удалось оценить|Could not rate/i, "a forged rating on an open ticket is rejected");
  assert.equal(await db.rateSupportTicket(1, 5, 10), false, "the open ticket stores no rating");
  await db.closeSupportMessage(1, "777", "restart your server");
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 10, chatID: 10, data: "rate:1:5" }));
  const thanks = calls.filter((call) => call.method === "editMessageText" && call.payload.chat_id === 10).at(-1);
  assert.match(thanks.payload.text, /Спасибо|Thank you/i);
  assert.match(thanks.payload.text, /5\/5/);
  assert.equal(await db.rateSupportTicket(1, 3, 10), false, "a second rating from the same user is rejected");
});

test("a losing admin is told a ticket was already answered and delivers nothing", async () => {
  const { bot, calls, db, config } = fixture();
  config.ownerIDs.add(777);
  await db.upsertUser({ id: 10, first_name: "User", language_code: "ru" }, 10, "ru");
  await db.upsertUser({ id: 777, first_name: "Admin", language_code: "ru" }, 777, "ru");
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 10, chatID: 10, data: "menu:support" }));
  await bot.handleUpdate(textUpdate({ fromID: 10, chatID: 10, text: "help me with my order" }));
  await db.closeSupportMessage(1, "111", "first answer");
  const deliveredBefore = calls.filter((call) => call.method === "sendMessage" && call.payload.chat_id === 10).length;
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:reply" }));
  await bot.handleUpdate(textUpdate({ fromID: 777, chatID: 777, text: "1 second answer" }));
  const loserReply = calls.filter((call) => call.method === "sendMessage" && call.payload.chat_id === 777).at(-1);
  assert.match(loserReply.payload.text, /уже отвечено|already answered/i);
  const deliveredAfter = calls.filter((call) => call.method === "sendMessage" && call.payload.chat_id === 10).length;
  assert.equal(deliveredAfter, deliveredBefore, "a losing admin must not deliver a second answer to the user");
  const ticket = await db.supportMessage(1);
  assert.equal(ticket.answered_by, "111", "the first answer keeps the claim");
  assert.equal(ticket.answer, "first answer");
});

test("admin audit lists support ticket ratings", async () => {
  const { bot, calls, db, config, gramsrv } = fixture();
  config.ownerIDs.add(777);
  gramsrv.adminCommands = async () => [];
  await db.upsertUser({ id: 777, first_name: "Admin", language_code: "ru" }, 777, "ru");
  await db.addSupportMessage(10, 10, "help me with my order");
  await db.closeSupportMessage(1, "777", "restart your server");
  const rated = await db.rateSupportTicket(1, 5, 10);
  assert.equal(rated, true);
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:audit" }));
  const audit = calls.filter((call) => call.method === "editMessageText" || call.method === "sendMessage").at(-1);
  const text = audit.payload.text ?? audit.payload.text;
  assert.match(text, /Тикет #1|Ticket #1/);
  assert.match(text, /⭐⭐⭐⭐⭐/);
  assert.match(text, /777/);
});

test("admin lookup result shows the admin keyboard", async () => {
  const { bot, calls, db, config } = fixture();
  config.ownerIDs.add(777);
  await db.upsertUser({ id: 10, first_name: "User", language_code: "ru" }, 10, "ru");
  await db.upsertUser({ id: 777, first_name: "Admin", language_code: "ru" }, 777, "ru");
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:lookup" }));
  await bot.handleUpdate(textUpdate({ fromID: 777, chatID: 777, text: "10" }));
  const sent = calls.filter((call) => call.method === "sendMessage" && call.payload.chat_id === 777).at(-1);
  assert.ok(sent, "Lookup result should be sent");
  const labels = (sent.payload.reply_markup?.inline_keyboard ?? []).flat().map((b) => b.text).join("\n");
  assert.match(labels, /Статистика|Stats/);
});

test("admin binds a phone to any telegram account and updates the server account", async () => {
  const { bot, calls, db, config, gramsrv } = fixture();
  config.ownerIDs.add(777);
  await db.upsertUser({ id: 10, first_name: "User", language_code: "ru" }, 10, "ru");
  await db.upsertUser({ id: 777, first_name: "Admin", language_code: "ru" }, 777, "ru");
  const user = await db.user(10);
  user.server_user_id = 424242;
  const setPhoneCalls = [];
  const mintPhoneCalls = [];
  gramsrv.setPhone = async (serverID, phone, _reason, _key, _dryRun, _actor) => { setPhoneCalls.push({ serverID, phone }); };
  gramsrv.mintPhone = async (serverID, phone, _key, dryRun, _actor) => { mintPhoneCalls.push({ serverID, phone, dryRun }); return {}; };
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:bindphone" }));
  await bot.handleUpdate(textUpdate({ fromID: 777, chatID: 777, text: "10 +79991234567" }));
  assert.deepEqual(setPhoneCalls.at(-1), { serverID: 424242, phone: "+79991234567" }, "admin binding replaces the server account phone");
  assert.equal(mintPhoneCalls.length, 2, "a dry-run registry check precedes the real collectible mint");
  assert.deepEqual(mintPhoneCalls.at(-1), { serverID: 424242, phone: "+79991234567", dryRun: false }, "the bound number is minted as a collectible phone for the gramsrv account");
  const bound = await db.currentNumber(10);
  assert.equal(bound.phone, "+79991234567", "the bound phone becomes the user's only current number");
  const sent = calls.filter((call) => call.method === "sendMessage" && call.payload.chat_id === 777).at(-1);
  assert.ok(sent);
  assert.match(sent.payload.text, /привязан|bound/i);
  const labels = (sent.payload.reply_markup?.inline_keyboard ?? []).flat().map((b) => b.text).join("\n");
  assert.match(labels, /Выдачи|Grants/);
});

test("requesting a new free number after buying +888 is refused", async () => {
  const { bot, calls, db } = fixture();
  await db.upsertUser({ id: 10, first_name: "User", language_code: "ru" }, 10, "ru");
  await db.createNumber(10, 10, "short", "ANON", true);
  await bot.handleUpdate(accountCallbackUpdate({ data: "numbers:new:RU" }));
  const edit = calls.find((call) => call.method === "editMessageText");
  assert.ok(edit, "Refusal should be shown in the message");
  assert.match(edit.payload.text, /бесплатный номер недоступен|free number is unavailable/i);
  const owned = await db.numbers(10);
  assert.equal(owned.filter((n) => n.format === "free").length, 0);
});

test("rate limiting drops a flooded user and warns once", async () => {
  const { bot, calls, db, config } = fixture();
  config.rateLimitMaxRequests = 3;
  config.rateLimitWindowSeconds = 60;
  await db.upsertUser({ id: 10, first_name: "User", language_code: "ru" }, 10, "ru");
  for (let i = 0; i < 7; i++) {
    await bot.handleUpdate(accountCallbackUpdate({ data: "menu:home" }));
  }
  const alerts = calls.filter((call) => call.method === "answerCallbackQuery" && call.payload.show_alert === true);
  assert.equal(alerts.length, 1, "the warning is sent exactly once");
  const edits = calls.filter((call) => call.method === "editMessageText" && call.payload.text?.includes("Главное меню"));
  assert.equal(edits.length, 3, "only requests within the limit reach the handler");
});

test("rate limiting is not applied to bot owners", async () => {
  const { bot, calls, db, config } = fixture();
  config.rateLimitMaxRequests = 2;
  config.rateLimitWindowSeconds = 60;
  config.ownerIDs.add(10);
  await db.upsertUser({ id: 10, first_name: "Owner", language_code: "ru" }, 10, "ru");
  for (let i = 0; i < 5; i++) {
    await bot.handleUpdate(accountCallbackUpdate({ data: "menu:home" }));
  }
  const edits = calls.filter((call) => call.method === "editMessageText" && call.payload.text?.includes("Главное меню"));
  assert.equal(edits.length, 5, "owner requests are never rate limited");
});

test("manual account ID is rejected when the phone resolves to a different account", async () => {
  const { bot, calls, db, gramsrv } = fixture();
  await seedAccountUser(db, { hasNumber: true });
  gramsrv.resolveUserByPhone = async () => 42;
  await bot.handleUpdate(accountCallbackUpdate({ data: "settings:account:enter" }));
  assert.equal((await db.user(10)).server_user_id, 0);
  await bot.handleUpdate(textUpdate({ fromID: 10, chatID: 10, text: "17" }));
  assert.equal((await db.user(10)).server_user_id, 0, "a mismatched manual ID is rejected");
});

test("manual account ID is accepted when it matches the resolved phone", async () => {
  const { bot, calls, db, gramsrv } = fixture();
  await seedAccountUser(db, { hasNumber: true });
  gramsrv.resolveUserByPhone = async () => 17;
  await bot.handleUpdate(accountCallbackUpdate({ data: "settings:account:enter" }));
  await bot.handleUpdate(textUpdate({ fromID: 10, chatID: 10, text: "17" }));
  assert.equal((await db.user(10)).server_user_id, 17);
});

test("shop keeps a single +888 entry and no sell flow", async () => {
  const { bot, calls, db } = fixture();
  await db.upsertUser({ id: 10, first_name: "User", language_code: "ru" }, 10, "ru");
  await bot.handleUpdate(accountCallbackUpdate({ data: "menu:shop" }));
  const edit = calls.find((call) => call.method === "editMessageText");
  const labels = edit.payload.reply_markup.inline_keyboard.flat().map((button) => button.text).join("\n");
  assert.match(labels, /📱 \+888/);
  assert.doesNotMatch(labels, /Купить новый номер|Buy a new number/, "the +888 entry is not duplicated in the store");
  assert.doesNotMatch(labels, /Продать|Sell/);
  await bot.handleUpdate(accountCallbackUpdate({ data: "shop:number" }));
  const products = calls.filter((call) => call.method === "editMessageText").at(-1);
  assert.match(products.payload.text, /Выберите товар|Choose a product/);
});

test("the admin panel no longer exposes number sell offers", async () => {
  const { bot, calls, db, config } = fixture();
  config.ownerIDs.add(777);
  await db.upsertUser({ id: 777, first_name: "Admin", language_code: "ru" }, 777, "ru");
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:menu" }));
  const edit = calls.find((call) => call.method === "editMessageText");
  const labels = edit.payload.reply_markup.inline_keyboard.flat().map((button) => button.text).join("\n");
  assert.doesNotMatch(labels, /Предложения|Sell offers/);
});

test("repeat +888 purchases are allowed, discounted, and validated against the discounted price", async () => {
  const { bot, calls, db } = fixture();
  db._settings.set("number_discount_percent", "20");
  await db.upsertUser({ id: 10, first_name: "User", language_code: "ru" }, 10, "ru");
  await db.createNumber(10, 10, "short", "ANON", true);

  await bot.handleUpdate(accountCallbackUpdate({ data: "buy:num_long:0" }));
  const invoice = calls.find((call) => call.method === "sendInvoice");
  assert.ok(invoice, "a repeat +888 buyer gets an invoice instead of an 'already owned' refusal");
  assert.equal(invoice.payload.prices[0].amount, 20, "25 ⭐ with a 20% discount is 20 ⭐");

  await bot.handleUpdate(preCheckoutUpdate({
    fromID: 10, payload: buildPayload("num_long", 0), total: 25,
  }));
  assert.equal(calls.filter((call) => call.method === "answerPreCheckoutQuery" && call.payload.ok === true).length, 0, "the base price is rejected");

  await bot.handleUpdate(preCheckoutUpdate({
    fromID: 10, payload: buildPayload("num_long", 0), total: 20,
  }));
  const accepted = calls.filter((call) => call.method === "answerPreCheckoutQuery").at(-1);
  assert.equal(accepted.payload.ok, true, "the discounted price is accepted");
});

test("a first +888 purchase is not discounted", async () => {
  const { bot, calls, db } = fixture();
  db._settings.set("number_discount_percent", "20");
  await db.upsertUser({ id: 10, first_name: "User", language_code: "ru" }, 10, "ru");
  await db.createNumber(10, 10, "free", "RU", false);
  await bot.handleUpdate(accountCallbackUpdate({ data: "buy:num_long:0" }));
  const invoice = calls.find((call) => call.method === "sendInvoice");
  assert.ok(invoice);
  assert.equal(invoice.payload.prices[0].amount, 25, "a free-number owner pays the full catalog price");
});

test("a resolved account purchase mints the +888 collectible phone and swaps the server phone", async () => {
  const { bot, calls, db, gramsrv } = fixture();
  await seedAccountUser(db, { serverUserID: 1780243207, hasNumber: true });
  gramsrv.resolveUserByPhone = async (phone) => (phone === "+79990000001" ? 1780243207 : 0);
  const setPhoneCalls = [], mintPhoneCalls = [];
  gramsrv.setPhone = async (serverID, phone, reason, key) => { setPhoneCalls.push({ serverID, phone, reason, key }); };
  gramsrv.mintPhone = async (serverID, phone, key, dryRun) => { mintPhoneCalls.push({ serverID, phone, key, dryRun }); return {}; };
  await bot.handleUpdate({
    update_id: Date.now(),
    message: {
      message_id: 7,
      date: 1,
      chat: { id: 10, type: "private" },
      from: { id: 10, is_bot: false, first_name: "User", language_code: "ru" },
      successful_payment: {
        currency: "XTR",
        total_amount: 25,
        invoice_payload: buildPayload("num_long", 0),
        telegram_payment_charge_id: "charge-purchase-1",
        provider_payment_charge_id: "ppc-1",
      },
    },
  });
  assert.deepEqual(setPhoneCalls, [{ serverID: 1780243207, phone: "+88809876543210", reason: "Telegram bot number purchase", key: "payment:charge-purchase-1:num_long" }]);
  assert.deepEqual(mintPhoneCalls, [{ serverID: 1780243207, phone: "+88809876543210", key: "payment:charge-purchase-1:num_long:phone", dryRun: false }], "the purchased number is minted as a collectible for the server account");
  const recognized = calls.find((call) => call.method === "sendMessage" && /\+88809876543210/.test(call.payload.text));
  assert.ok(recognized, "the buyer is shown the reserved number");
});

test("admin can set and see the daily free number limit in the prices panel", async () => {
  const { bot, calls, db, config } = fixture();
  config.ownerIDs.add(777);
  await db.upsertUser({ id: 777, first_name: "Admin", language_code: "ru" }, 777, "ru");
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:prices" }));
  const prompt = calls.filter((call) => call.method === "editMessageText").at(-1);
  assert.match(prompt.payload.text, /\bfree N\b/, "the panel documents the free limit command");
  assert.match(prompt.payload.text, /\n<code>free 0<\/code>$/, "the current free limit renders as a copyable command");
  await bot.handleUpdate(textUpdate({ fromID: 777, chatID: 777, text: "free 3" }));
  assert.equal(db._settings.get("free_number_daily_limit"), "3");
  const saved = calls.filter((call) => call.method === "sendMessage").find((call) => /Обновлено позиций: 1|Updated 1 item/.test(call.payload.text));
  assert.ok(saved, "the admin is told the price entry was applied");
});

test("users at the daily free limit get a notice instead of the country menu", async () => {
  const { bot, calls, db } = fixture();
  db._settings.set("free_number_daily_limit", "1");
  await db.upsertUser({ id: 10, first_name: "User", language_code: "ru" }, 10, "ru");
  await db.createNumber(10, 10, "free", "RU", false);
  const user = await db.user(10);
  user.free_day_count = 1;
  await bot.handleUpdate(accountCallbackUpdate({ data: "numbers:new" }));
  const blocked = calls.filter((call) => call.method === "editMessageText").at(-1);
  assert.match(blocked.payload.text, /дневной лимит смены бесплатного номера|daily free number change limit/i);
  assert.doesNotMatch(blocked.payload.text, /Выберите страну|Choose a country/);
  user.free_day_count = 0;
  await bot.handleUpdate(accountCallbackUpdate({ data: "numbers:new" }));
  const menu = calls.filter((call) => call.method === "editMessageText").at(-1);
  assert.match(menu.payload.text, /Выберите страну|Choose a country/);
});

test("repeat +888 buyers see a warning that their old number will be replaced", async () => {
  const { bot, calls, db } = fixture();
  await db.upsertUser({ id: 10, first_name: "User", language_code: "ru" }, 10, "ru");
  await db.createNumber(10, 10, "short", "ANON", true);
  await bot.handleUpdate(accountCallbackUpdate({ data: "product:num_short" }));
  const edit = calls.filter((call) => call.method === "editMessageText").at(-1);
  assert.match(edit.payload.text, /Ваш текущий \+888 будет удалён|Your current \+888 will be deleted/i);
});

test("first-time +888 buyers are not warned about replacing a number", async () => {
  const { bot, calls, db } = fixture();
  await db.upsertUser({ id: 10, first_name: "User", language_code: "ru" }, 10, "ru");
  await db.createNumber(10, 10, "free", "RU", false);
  await bot.handleUpdate(accountCallbackUpdate({ data: "product:num_short" }));
  const edit = calls.filter((call) => call.method === "editMessageText").at(-1);
  assert.doesNotMatch(edit.payload.text, /Ваш текущий \+888 будет удалён|Your current \+888 will be deleted/i);
});

test("admin can grant an NFT username next to the bind phone button", async () => {
  const { bot, calls, config, db, gramsrv } = fixture();
  config.ownerIDs.add(777);
  await db.upsertUser({ id: 777, first_name: "Admin", language_code: "ru" }, 777, "ru");
  await db.upsertUser({ id: 10, first_name: "Target", language_code: "ru" }, 10, "ru");
  const target = await db.user(10);
  target.server_user_id = 1780243205;
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:menu" }));
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:grants" }));
  const grants = calls.find((call) => call.method === "editMessageText" && /grant|выдача/i.test(call.payload.text));
  const rows = grants.payload.reply_markup.inline_keyboard.map((row) => row.map((button) => button.text).join("\u0001"));
  assert.ok(rows.some((row) => row.includes("🎨 NFT username\u0001📞 Привязать номер")), "grant NFT username sits right next to bind phone on the same row");
  const mints = [];
  gramsrv.mintUsername = async (userID, username, bid, key, dryRun) => { mints.push({ userID, username, bid, dryRun }); return {}; };
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:grantusername" }));
  const prompt = calls.filter((call) => call.method === "editMessageText").at(-1);
  assert.match(prompt.payload.text, /TELEGRAM_ID USERNAME|Формат:/);
  await bot.handleUpdate(textUpdate({ fromID: 777, chatID: 777, text: "10 Aaaa" }));
  assert.equal(mints.length, 2, "a dry-run occupation check precedes the real zero-price mint");
  assert.equal(mints[0].dryRun, true);
  assert.ok(!mints[1].dryRun, "the real mint runs without the dry-run flag");
  assert.equal(mints[0].userID, 1780243205, "the mint targets the linked gramsrv account, not the Telegram id");
  assert.equal(mints[0].bid, 0);
  assert.equal(mints[0].username, "aaaa", "4-character usernames are grantable by admins");
  const done = calls.filter((call) => call.method === "sendMessage").find((call) => /aaaa/.test(call.payload.text) && /выдан|granted to/i.test(call.payload.text));
  assert.ok(done, "the admin sees the grant confirmation");
});

test("admin grant refuses an occupied NFT username without minting", async () => {
  const { bot, calls, config, db, gramsrv } = fixture();
  config.ownerIDs.add(777);
  await db.upsertUser({ id: 777, first_name: "Admin", language_code: "ru" }, 777, "ru");
  await db.upsertUser({ id: 10, first_name: "Target", language_code: "ru" }, 10, "ru");
  const target = await db.user(10);
  target.server_user_id = 1780243205;
  let called = 0;
  gramsrv.mintUsername = async (_userID, _username, _bid, _key, dryRun) => {
    called++;
    if (dryRun) {
      const error = new Error("gramsrv /v1/collectible-usernames/mint 400: {\"status\":\"failed\"}");
      error.code = "USERNAME_OCCUPIED";
      throw error;
    }
    return {};
  };
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:grantusername" }));
  await bot.handleUpdate(textUpdate({ fromID: 777, chatID: 777, text: "10 @Durov" }));
  assert.equal(called, 1, "the real mint is skipped when the dry run reports occupation");
  const reply = calls.filter((call) => call.method === "sendMessage").at(-1);
  assert.match(reply.payload.text, /уже занят другим пользователем|already occupied by another user/i);
});

test("admin stats panel embeds the recent sales feed", async () => {
  const { bot, calls, db, config } = fixture();
  config.ownerIDs.add(777);
  await db.upsertUser({ id: 777, first_name: "Admin", language_code: "ru" }, 777, "ru");
  db.recentSales = async () => [
    { id: 5, charge_id: "charge-5", product: "stars_1", title: "20 Stars", stars_price: 20, buyer_name: "User" },
    { id: 6, charge_id: "charge-6", product: "premium_1m", title: "Premium — 1 month", stars_price: 30, buyer_id: 11 },
  ];
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:stats" }));
  const sent = calls.filter((call) => call.method === "editMessageText").at(-1);
  assert.match(sent.payload.text, /#5 · <code>charge-5<\/code>/);
  assert.match(sent.payload.text, /20 ⭐/);
  assert.match(sent.payload.text, /#6 · <code>charge-6<\/code>/);
  assert.match(sent.payload.text, /· 11$/, "unknown buyer names fall back to the buyer id");
  assert.match(sent.payload.text, /📊 Пользователи|📊 Users/);
});

test("admin verified toggle dry-runs then applies and attributes the actor", async () => {
  const { bot, calls, config, db, gramsrv } = fixture();
  config.ownerIDs.add(777);
  await db.upsertUser({ id: 777, first_name: "Admin", language_code: "ru" }, 777, "ru");
  await db.upsertUser({ id: 424242, first_name: "Target", language_code: "ru" }, 424242, "ru");
  const target = await db.user(424242);
  target.server_user_id = 1780243205;
  const callsSeen = [];
  gramsrv.setVerified = async (...args) => callsSeen.push(args);
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:verified" }));
  await bot.handleUpdate(textUpdate({ fromID: 777, chatID: 777, text: "424242" }));
  assert.equal(callsSeen.length, 2, "a dry run precedes the real moderation command");
  assert.deepEqual(callsSeen[0], [1780243205, true, "Telegram bot administrator moderation", "", true, "777"], "the gramsrv account id is used, not the Telegram id");
  assert.equal(callsSeen[1][0], 1780243205);
  assert.equal(callsSeen[1][1], true);
  assert.equal(callsSeen[1][4], false, "the real call is not a dry run");
  assert.equal(callsSeen[1][3].startsWith("admin:verified:424242:"), true, "the real call gets a deterministic idempotency key");
  assert.equal(callsSeen[1][5], "777", "the command is attributed to the admin Telegram ID");
  const reply = calls.filter((call) => call.method === "sendMessage").at(-1);
  assert.match(reply.payload.text, /верифицирован|verified/i);
});

test("admin can un-verify and pass an explicit off state", async () => {
  const { bot, calls, config, db, gramsrv } = fixture();
  config.ownerIDs.add(777);
  await db.upsertUser({ id: 777, first_name: "Admin", language_code: "ru" }, 777, "ru");
  await db.upsertUser({ id: 424242, first_name: "Target", language_code: "ru" }, 424242, "ru");
  const target = await db.user(424242);
  target.server_user_id = 1780243205;
  const callsSeen = [];
  gramsrv.setVerified = async (...args) => callsSeen.push(args);
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:verified" }));
  await bot.handleUpdate(textUpdate({ fromID: 777, chatID: 777, text: "424242 off" }));
  assert.deepEqual(callsSeen[1].slice(0, 2), [1780243205, false]);
  const reply = calls.filter((call) => call.method === "sendMessage").at(-1);
  assert.match(reply.payload.text, /снят бейдж верификации|verified badge was removed/i);
});

test("moderation buttons freeze, flag scam and flag fake via the gramsrv API", async () => {
  const { bot, calls, config, db, gramsrv } = fixture();
  config.ownerIDs.add(777);
  await db.upsertUser({ id: 777, first_name: "Admin", language_code: "ru" }, 777, "ru");
  await db.upsertUser({ id: 424242, first_name: "Target", language_code: "ru" }, 424242, "ru");
  const target = await db.user(424242);
  target.server_user_id = 1780243205;
  const frozen = [], flagged = [];
  gramsrv.setFrozen = async (...args) => frozen.push(args);
  gramsrv.setFlags = async (...args) => flagged.push(args);
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:freeze" }));
  await bot.handleUpdate(textUpdate({ fromID: 777, chatID: 777, text: "424242" }));
  assert.deepEqual(frozen.map((c) => c.slice(0, 2)), [[1780243205, true], [1780243205, true]]);
  assert.equal(frozen[0][4], true, "freeze dry-run before applying");
  assert.equal(frozen[1][5], "777");
  const freezeReply = calls.filter((call) => call.method === "sendMessage").at(-1);
  assert.match(freezeReply.payload.text, /заморожен|frozen/i);

  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:scam" }));
  await bot.handleUpdate(textUpdate({ fromID: 777, chatID: 777, text: "424242 on" }));
  assert.deepEqual(flagged.at(-1).slice(0, 3), [1780243205, true, false], "scam sets the scam flag and leaves fake untouched");

  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:fake" }));
  await bot.handleUpdate(textUpdate({ fromID: 777, chatID: 777, text: "424242" }));
  assert.deepEqual(flagged.at(-1).slice(0, 3), [1780243205, false, true], "fake sets the fake flag and leaves scam untouched");
  const fakeReply = calls.filter((call) => call.method === "sendMessage").at(-1);
  assert.match(fakeReply.payload.text, /фейк|fake/i);
});

test("admin lookup resolves an @username and shows the gramsrv account id", async () => {
  const { bot, calls, db, config } = fixture();
  config.ownerIDs.add(777);
  await db.upsertUser({ id: 777, first_name: "Admin", language_code: "ru" }, 777, "ru");
  db.adminLookupByUsername = async () => ({
    user: { telegram_id: 10, username: "durov", language: "ru", bonus: 0, server_user_id: 424242 },
    numbers: [{ display: "+8880123456", is_current: true, format: "long" }],
  });
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:lookup" }));
  await bot.handleUpdate(textUpdate({ fromID: 777, chatID: 777, text: "@durov" }));
  const sent = calls.filter((call) => call.method === "sendMessage" && call.payload.chat_id === 777).at(-1);
  assert.match(sent.payload.text, /@durov/, "the username is shown in the result");
  assert.match(sent.payload.text, /gramsrv_id=<code>424242<\/code>/, "the gramsrv account id is displayed");
});

test("admin grant and moderation refuse a target without a linked gramsrv account", async () => {
  const { bot, calls, config, db, gramsrv } = fixture();
  config.ownerIDs.add(777);
  await db.upsertUser({ id: 777, first_name: "Admin", language_code: "ru" }, 777, "ru");
  await db.upsertUser({ id: 10, first_name: "Target", language_code: "ru" }, 10, "ru");
  let mints = 0, verifies = 0;
  gramsrv.mintUsername = async () => { mints++; return {}; };
  gramsrv.setVerified = async () => { verifies++; return {}; };

  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:grantusername" }));
  await bot.handleUpdate(textUpdate({ fromID: 777, chatID: 777, text: "10 abcd" }));
  assert.equal(mints, 0, "no mint happens without a linked gramsrv account");
  const grantReply = calls.filter((call) => call.method === "sendMessage").at(-1);
  assert.match(grantReply.payload.text, /нет привязанного аккаунта ShuzaGram|no linked ShuzaGram account/i);

  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:verified" }));
  await bot.handleUpdate(textUpdate({ fromID: 777, chatID: 777, text: "10" }));
  assert.equal(verifies, 0, "no moderation command runs without a linked gramsrv account");
  const modReply = calls.filter((call) => call.method === "sendMessage").at(-1);
  assert.match(modReply.payload.text, /нет привязанного аккаунта ShuzaGram|no linked ShuzaGram account/i);
});

test("admin TELEGRAM_ID operations accept an @telegram-username", async () => {
  const { bot, calls, config, db, gramsrv } = fixture();
  config.ownerIDs.add(777);
  await db.upsertUser({ id: 777, first_name: "Admin", language_code: "ru" }, 777, "ru");
  await db.upsertUser({ id: 10, first_name: "Target", username: "durov", language_code: "ru" }, 10, "ru");
  const target = await db.user(10);
  target.server_user_id = 424242;
  const mints = [];
  gramsrv.mintUsername = async (userID) => { mints.push(userID); return {}; };
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:grantusername" }));
  await bot.handleUpdate(textUpdate({ fromID: 777, chatID: 777, text: "@durov Aaaa" }));
  assert.deepEqual(mints, [424242, 424242], "the @username resolved to the linked gramsrv account");
  const verifies = [];
  gramsrv.setVerified = async (serverID) => { verifies.push(serverID); };
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:verified" }));
  await bot.handleUpdate(textUpdate({ fromID: 777, chatID: 777, text: "@durov on" }));
  assert.deepEqual(verifies, [424242, 424242], "moderation accepts @username as the Telegram target");
});

test("an occupied error on the real admin grant mint is surfaced to the moderator", async () => {
  const { bot, calls, config, db, gramsrv } = fixture();
  config.ownerIDs.add(777);
  await db.upsertUser({ id: 777, first_name: "Admin", language_code: "ru" }, 777, "ru");
  await db.upsertUser({ id: 10, first_name: "Target", language_code: "ru" }, 10, "ru");
  const target = await db.user(10);
  target.server_user_id = 424242;
  let realAttempts = 0;
  gramsrv.mintUsername = async (_userID, _username, _bid, _key, dryRun) => {
    if (!dryRun) {
      realAttempts++;
      const error = new Error("gramsrv /v1/collectible-usernames/mint 400: USERNAME_OCCUPIED: username occupied");
      error.code = "USERNAME_OCCUPIED";
      throw error;
    }
    return {};
  };
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:grantusername" }));
  await bot.handleUpdate(textUpdate({ fromID: 777, chatID: 777, text: "10 Ramble" }));
  assert.equal(realAttempts, 1, "a race gang is caught on the real mint attempt");
  const reply = calls.filter((call) => call.method === "sendMessage").at(-1);
  assert.match(reply.payload.text, /уже занят другим пользователем|already occupied by another user/i);
});

test("an unmapped gramsrv failure keeps the visible reason in the reply", async () => {
  const { bot, calls, config, db, gramsrv } = fixture();
  config.ownerIDs.add(777);
  await db.upsertUser({ id: 777, first_name: "Admin", language_code: "ru" }, 777, "ru");
  await db.upsertUser({ id: 10, first_name: "Target", language_code: "ru" }, 10, "ru");
  const target = await db.user(10);
  target.server_user_id = 424242;
  gramsrv.mintUsername = async () => {
    const error = new Error("gramsrv /v1/collectible-usernames/mint 400: COLLECTIBLE_PEER_LIMIT: peer limit reached");
    error.code = "COLLECTIBLE_PEER_LIMIT";
    throw error;
  };
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:grantusername" }));
  await bot.handleUpdate(textUpdate({ fromID: 777, chatID: 777, text: "10 Ramble" }));
  const reply = calls.filter((call) => call.method === "sendMessage").at(-1);
  assert.match(reply.payload.text, /peer limit/i, "the raw gramsrv reason stays visible instead of a generic crash");
});

test("admin audit button lists recent gramsrv actions", async () => {
  const { bot, calls, db, config, gramsrv } = fixture();
  config.ownerIDs.add(777);
  await db.upsertUser({ id: 777, first_name: "Admin", language_code: "ru" }, 777, "ru");
  gramsrv.adminCommands = async () => [
    { command_id: "bot-x", actor: "777", action: "set_phone", target_user_id: 10, status: "completed", reason: "Admin number bind", created_at: new Date().toISOString(), error: "" },
    { command_id: "bot-y", actor: "777", action: "mint_username", target_user_id: 10, status: "failed", reason: "", created_at: new Date().toISOString(), error: "USERNAME_OCCUPIED" },
  ];
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:audit" }));
  const sent = calls.filter((call) => call.method === "editMessageText").at(-1);
  assert.match(sent.payload.text, /set_phone/);
  assert.match(sent.payload.text, /mint_username/);
  assert.match(sent.payload.text, /USERNAME_OCCUPIED/);
});

test("admin lookup shows recent sales and refunds for the user", async () => {
  const { bot, calls, db, config } = fixture();
  config.ownerIDs.add(777);
  await db.upsertUser({ id: 777, first_name: "Admin", language_code: "ru" }, 777, "ru");
  db.adminLookupByTelegramID = async () => ({ user: { telegram_id: 10, username: "durov", language: "ru", bonus: 0, server_user_id: 424242 }, numbers: [] });
  db.adminRecentActivity = async () => ({
    sales: [{ title: "20 Stars", stars_price: 20, charge_id: "charge-a", created_at: 1710000000 }],
    refunds: [{ title: "Premium — 1 month", stars_price: 30, charge_id: "charge-b", refunded_at: 1710001000 }],
  });
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:lookup" }));
  await bot.handleUpdate(textUpdate({ fromID: 777, chatID: 777, text: "10" }));
  const sent = calls.filter((call) => call.method === "sendMessage" && call.payload.chat_id === 777).at(-1);
  assert.match(sent.payload.text, /🛒 <b>20 Stars<\/b> · 20 ⭐ · <code>charge-a<\/code>/);
  assert.match(sent.payload.text, /↩️ <b>Premium — 1 month<\/b> · 30 ⭐ · <code>charge-b<\/code>/);
});
