import test from "node:test";
import assert from "node:assert/strict";
import { createBot } from "../src/bot.js";
import { BotDatabase } from "../src/db.js";

const botInfo = { id: 999, is_bot: true, first_name: "Test", username: "test_bot", can_join_groups: true, can_read_all_group_messages: false, supports_inline_queries: false };

function mockDb() {
  const users = new Map();
  const numbers = new Map();
  const tickets = new Map();
  const pendingState = new Map();
  let numberSeq = 1;
  const db = {
    _userCache: new Map(),
    _cachedUser: null,
    upsertUser: async (from, chatID, language = "ru", referrerID = 0, referralBonus = 0) => {
      const existing = users.get(from.id);
      if (!users.has(from.id)) {
        users.set(from.id, { telegram_id: from.id, chat_id: chatID, username: from.username ?? "", first_name: from.first_name ?? "", server_user_id: 0, language, notifications: 1, bonus: 0, referred_by: null, referral_count: 0, daily_day: "", spin_day: "", spin_day_count: 0, spin_week: "", spin_week_count: 0, created_at: 0, updated_at: 0 });
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
      return num;
    },
    currentNumber: async (ownerID) => [...numbers.values()].find((n) => n.owner_id === ownerID && n.is_current) ?? null,
    numbers: async (ownerID) => [...numbers.values()].filter((n) => n.owner_id === ownerID),
    findNumber: async (phone) => [...numbers.values()].find((n) => n.phone === phone) ?? null,
    updateLoginCode: async (phone, code) => { const n = [...numbers.values()].find((x) => x.phone === phone); if (n) { n.login_code = code; return { number: n, chatIDs: [n.chat_id] }; } return { number: null, chatIDs: [] }; },
    acceptLoginCodeDelivery: async () => ({ duplicate: false, number: null, chatIDs: [] }),
    grantCodeAccess: async () => {},
    revokePurchasedNumber: async () => false,
    getSetting: async () => "20",
    setSetting: async () => {},
    starsRate: async () => 20,
    setPending: async (id, kind, payload = {}) => { pendingState.set(id, { kind, payload }); },
    pending: async (id) => pendingState.get(id) ?? null,
    clearPending: async (id) => { pendingState.delete(id); },
    recentRecipients: async () => [],
    rememberRecipient: async () => {},
    reserveSpin: async () => ({ prize: 50, day: "2026-01-01" }),
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
    addSupportMessage: async (id, chatID, text) => { const ticket = tickets.size + 1; tickets.set(ticket, { id: ticket, telegram_id: id, chat_id: chatID, text, status: "open", created_at: 0, answered_at: 0 }); return ticket; },
    supportMessage: async (ticketID) => tickets.get(ticketID) ?? null,
    closeSupportMessage: async (ticketID) => { const t = tickets.get(ticketID); if (t) t.status = "answered"; },
    verifiedPhone: async () => null,
    bindVerifiedPhone: async () => ({ phone: "+79990000000" }),
    unbindVerifiedPhone: async () => true,
    adminLookupByNumber: async () => null,
    adminLookupByTelegramID: async () => null,
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
    productName: "Telesrv",
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
    dailyBonus: 10, notificationTTLDays: 30, productName: "Telesrv", publicUsername: "test_bot",
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

test("requesting a new free number keeps the previous number and its OTP route", async () => {
  const { bot, calls, db } = fixture();
  await db.upsertUser({ id: 10, first_name: "User", language_code: "ru" }, 10, "ru");
  const first = await db.createNumber(10, 10, "free", "RU", false);
  await bot.handleUpdate(accountCallbackUpdate({ data: "numbers:new:RU" }));
  const owned = await db.numbers(10);
  assert.equal(owned.length, 2);
  const current = await db.currentNumber(10);
  assert.notEqual(current.id, first.id);
  assert.equal(current.is_current, true);
  assert.equal((await db.findNumber(first.phone)).id, first.id);
  const delivery = await db.updateLoginCode(first.phone, "00000");
  assert.equal(delivery.number.id, first.id);
  assert.deepEqual(delivery.chatIDs, [10]);
  await bot.handleUpdate(accountCallbackUpdate({ data: "menu:numbers" }));
  const menu = calls.filter((call) => call.method === "editMessageText").at(-1);
  assert.ok(menu.payload.text.includes(current.display));
  assert.ok(menu.payload.text.includes(first.display));
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
  const delivered = calls.filter((call) => call.method === "sendMessage" && call.payload.chat_id === 10).at(-1);
  assert.ok(delivered, "The reply should be delivered to the user's private chat");
  assert.match(delivered.payload.text, /restart your server/);
  assert.match(delivered.payload.text, /обращени|тикет|ticket/i);
  const ticket = await db.supportMessage(1);
  assert.equal(ticket.status, "answered");
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
  const delivered = calls.filter((call) => call.method === "sendMessage" && call.payload.chat_id === 10).at(-1);
  assert.ok(delivered, "The reply should reach the user via Telegram native reply");
  assert.match(delivered.payload.text, /restart your server/);
  const ticket = await db.supportMessage(1);
  assert.equal(ticket.status, "answered");
  const confirmation = calls.filter((call) => call.method === "sendMessage" && call.payload.chat_id === 777).at(-1);
  assert.match(confirmation.payload.text, /#1/);
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

test("admin binds a phone to any telegram account", async () => {
  const { bot, calls, db, config, gramsrv } = fixture();
  config.ownerIDs.add(777);
  await db.upsertUser({ id: 10, first_name: "User", language_code: "ru" }, 10, "ru");
  await db.upsertUser({ id: 777, first_name: "Admin", language_code: "ru" }, 777, "ru");
  const user = await db.user(10);
  user.server_user_id = 424242;
  const setPhoneCalls = [];
  gramsrv.setPhone = async (serverID, phone) => { setPhoneCalls.push({ serverID, phone }); };
  await bot.handleUpdate(accountCallbackUpdate({ fromID: 777, chatID: 777, data: "admin:bindphone" }));
  await bot.handleUpdate(textUpdate({ fromID: 777, chatID: 777, text: "10 +79991234567" }));
  assert.deepEqual(setPhoneCalls, [], "admin binding changes delivery routing, not the server account phone");
  const sent = calls.filter((call) => call.method === "sendMessage" && call.payload.chat_id === 777).at(-1);
  assert.ok(sent);
  assert.match(sent.payload.text, /привязан|bound/i);
  const labels = (sent.payload.reply_markup?.inline_keyboard ?? []).flat().map((b) => b.text).join("\n");
  assert.match(labels, /Привязать|Bind/);
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
