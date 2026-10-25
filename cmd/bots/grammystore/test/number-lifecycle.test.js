import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { openTestDatabase } from "../test-support/database.js";
import { createBot, executeCompensatedRefund } from "../src/bot.js";
import { GramsrvClient } from "../src/gramsrv.js";
import { buildPayload } from "../src/catalog.js";

function databaseTest(name, run) {
  test(name, { skip: !process.env.DATABASE_URL }, async (t) => {
    const db = await openTestDatabase();
    t.after(() => db.close());
    await run(db);
  });
}
async function user(db, id = 1) {
  return db.upsertUser({ id, first_name: "Test", language_code: "en" }, id, "en");
}
function sale(id = 1, chargeID = "number-charge") {
  return { product: "num_long", title: "Number", starsPrice: 25, buyerID: id, recipientID: id, buyerName: "Test", chargeID };
}
async function purchase(db, id = 1, chargeID = "number-charge") {
  await db.beginPayment(chargeID, id, buildPayload("num_long", 0), 25);
  return db.fulfillNumberPurchase(sale(id, chargeID), id, "long");
}
function fixture(db, mode = "random") {
  let seq = 1;
  const calls = [], requests = [];
  const config = { botToken: "999:TEST", botMode: mode, defaultLanguage: "en", defaultNumberCountry: "US", ownerIDs: new Set([900]), requiredChannel: "", productName: "Test", referralBonus: 100, dailyBonus: 10 };
  const gramsrv = new GramsrvClient(config);
  gramsrv.post = async (route, payload) => { requests.push({ route, payload }); return { found: false, user_id: 0 }; };
  const bot = createBot({ config, db, gramsrv });
  bot.botInfo = { id: 999, is_bot: true, first_name: "Test", username: "test_bot" };
  bot.api.config.use(async (_, method, payload) => {
    calls.push({ method, payload });
    return { ok: true, result: method === "answerCallbackQuery" ? true : { message_id: seq++, date: 1, chat: { id: payload.chat_id, type: "private" }, text: payload.text ?? "" } };
  });
  const from = (id) => ({ id, is_bot: false, first_name: "Test", language_code: "en" });
  const message = (id, text, extra = {}, type = "private") => bot.handleUpdate({ update_id: seq++, message: { message_id: seq++, date: 1, from: from(id), chat: { id, type }, text, ...extra } });
  const command = (id, text) => message(id, text, { entities: [{ offset: 0, length: text.split(" ")[0].length, type: "bot_command" }] });
  const callback = (id, data) => bot.handleUpdate({ update_id: seq++, callback_query: { id: `cb-${seq}`, from: from(id), chat_instance: "test", data, message: { message_id: seq++, date: 1, chat: { id, type: "private" }, text: "menu" } } });
  const pay = (id, chargeID) => message(id, undefined, { successful_payment: { currency: "XTR", total_amount: 25, invoice_payload: buildPayload("num_long", 0), telegram_payment_charge_id: chargeID, provider_payment_charge_id: "test" } });
  return { bot, calls, requests, gramsrv, message, command, callback, pay };
}

databaseTest("manual account IDs never authorize server phone mutations on start, replacement or purchase", async (db) => {
  const f = fixture(db);
  await f.callback(1, "settings:account:enter");
  await f.message(1, "424242");
  assert.equal((await db.user(1)).server_user_id, 424242);
  await f.command(1, "/start");
  const old = await db.currentNumber(1);
  await f.callback(1, "numbers:new:US");
  await f.pay(1, "unverified-id");
  assert.equal((await db.paymentByCharge("unverified-id")).status, "done");
  assert.equal(typeof f.gramsrv.setPhone, "undefined");
  assert.equal(f.requests.length, 0);
  assert.deepEqual((await db.updateLoginCode(old.phone, "12345")).chatIDs, [1]);
  assert.ok(f.calls.some((c) => c.payload.text?.includes("signed-in client")));
});

databaseTest("real mode purchases retain the verified phone and never change the server account", async (db) => {
  await user(db);
  await db.setServerUserID(1, 424242);
  await db.bindVerifiedPhone(1, 1, "+15550000001");
  const f = fixture(db, "real");
  await f.pay(1, "real-number");
  assert.ok(await db.saleByCharge("real-number"));
  assert.equal((await db.verifiedPhone(1)).phone, "+15550000001");
  assert.deepEqual((await db.updateLoginCode("+15550000001", "12345")).chatIDs, [1]);
  assert.equal(f.requests.length, 0);
});

databaseTest("group messages do not create an account or redirect private OTP routing", async (db) => {
  const f = fixture(db, "real");
  await f.message(1, undefined, { contact: { user_id: 1, phone_number: "+15550000001", first_name: "Test" } }, "group");
  assert.equal(await db.user(1), null);
  assert.equal(f.calls.length, 0);
});

databaseTest("first upsert returns the committed account without reading through another pool connection", async (db) => {
  assert.equal((await user(db)).telegram_id, 1);
});

databaseTest("PostgreSQL retries a deterministic phone collision and surfaces unrelated SQL errors", async (db) => {
  await user(db, 1); await user(db, 2);
  let attempts = 0;
  const generated = (phone) => ({ phone, display: phone, country: "ANON" });
  db.generateNumber = () => generated("+8888001");
  await db.createNumber(1, 1, "short", "ANON");
  db.generateNumber = () => generated(++attempts === 1 ? "+8888001" : "+8888002");
  const number = await db.createNumber(2, 2, "short", "ANON");
  assert.equal(number.phone, "+8888002");
  assert.equal(attempts, 2);
  await user(db, 3);
  attempts = 0;
  db.generateNumber = () => { attempts++; return { phone: "+8888003", display: null, country: "ANON" }; };
  await assert.rejects(() => db.createNumber(3, 3, "short", "ANON"), { code: "23502" });
  assert.equal(attempts, 1);
});

databaseTest("collision exhaustion rolls back replacement and preserves the previous route", async (db) => {
  await user(db);
  const old = await db.createNumber(1, 1);
  let attempts = 0;
  db.generateNumber = () => { attempts++; return old; };
  await assert.rejects(() => db.createNumber(1, 1, "free", "RU", true), /unique number/);
  assert.equal(attempts, 400);
  assert.equal((await db.currentNumber(1)).id, old.id);
  assert.deepEqual((await db.updateLoginCode(old.phone, "12345")).chatIDs, [1]);
});

databaseTest("concurrent first allocations serialize on the owner", async (db) => {
  await user(db);
  const numbers = await Promise.all(Array.from({ length: 8 }, () => db.createNumber(1, 1)));
  assert.equal(new Set(numbers.map((n) => n.id)).size, 1);
});

databaseTest("free reservation cap preserves all existing routes and still allows a paid upgrade", async (db) => {
  await user(db);
  const numbers = [];
  for (let i = 0; i < 10; i++) numbers.push(await db.createNumber(1, 1, "free", "US", true));
  await assert.rejects(() => db.createNumber(1, 1, "free", "US", true), /reservation limit/);
  assert.equal((await db.currentNumber(1)).id, numbers.at(-1).id);
  assert.equal((await db.numbers(1)).length, 10);
  await purchase(db);
  assert.equal((await db.numbers(1)).length, 11);
  assert.deepEqual((await db.updateLoginCode(numbers[0].phone, "12345")).chatIDs, [1]);
});

databaseTest("a SQL failure recording the sale rolls back allocation; retry completes the same payment", async (db) => {
  await user(db);
  const old = await db.createNumber(1, 1);
  await db.pool.query("ALTER TABLE sales ADD CONSTRAINT test_sale_failure CHECK (product <> 'num_long')");
  await assert.rejects(() => purchase(db), { code: "23514" });
  assert.equal((await db.currentNumber(1)).id, old.id);
  assert.equal(await db.saleByCharge("number-charge"), null);
  await db.pool.query("ALTER TABLE sales DROP CONSTRAINT test_sale_failure");
  const number = await db.fulfillNumberPurchase(sale(), 1, "long");
  assert.notEqual(number.id, old.id);
  assert.equal((await db.paymentByCharge("number-charge")).status, "done");
  assert.equal((await db.saleByCharge("number-charge")).fulfillment.numberID, number.id);
  assert.equal((await db.findNumber(old.phone)).id, old.id);
});

databaseTest("concurrent charge replays allocate exactly one number and snapshot", async (db) => {
  await user(db);
  await db.beginPayment("number-charge", 1, buildPayload("num_long", 0), 25);
  const numbers = await Promise.all(Array.from({ length: 8 }, () => db.fulfillNumberPurchase(sale(), 1, "long")));
  assert.equal(new Set(numbers.map((n) => n.id)).size, 1);
  assert.equal((await db.pool.query("SELECT count(*)::int n FROM sales")).rows[0].n, 1);
  assert.equal((await db.numbers(1)).length, 1);
  await db.failPayment("number-charge", "notification failed after commit");
  assert.equal((await db.paymentByCharge("number-charge")).status, "done");
  await assert.rejects(() => db.beginPayment("number-charge", 2, buildPayload("num_long", 0), 25), /IDEMPOTENCY_CONFLICT/);
  await assert.rejects(() => db.beginPayment("number-charge", 1, "other", 25), /IDEMPOTENCY_CONFLICT/);
  await assert.rejects(() => db.beginPayment("number-charge", 1, buildPayload("num_long", 0), 26), /IDEMPOTENCY_CONFLICT/);
});

databaseTest("a stored interrupted number payment can be recovered by an owner, not another user", async (db) => {
  await user(db);
  await db.beginPayment("recover-me", 1, buildPayload("num_long", 0), 25);
  const f = fixture(db);
  await f.command(2, "/retry_payment recover-me");
  assert.equal(await db.saleByCharge("recover-me"), null);
  await f.command(900, "/retry_payment recover-me");
  const first = await db.saleByCharge("recover-me");
  assert.ok(first);
  await f.command(900, "/retry_payment recover-me");
  assert.equal((await db.saleByCharge("recover-me")).fulfillment.numberID, first.fulfillment.numberID);
  assert.equal((await db.numbers(1)).length, 1);
});

databaseTest("refund rejects a bound account or unavailable lookup without deleting OTP access", async (db) => {
  await user(db);
  const number = await purchase(db);
  const record = await db.saleByCharge("number-charge");
  let refunds = 0;
  const execute = (resolveUserByPhone) => executeCompensatedRefund({ sale: record, telegramID: 1, db, gramsrv: { resolveUserByPhone }, refundStarPayment: async () => { refunds++; } });
  await assert.rejects(() => execute(async () => 424242), /still bound/);
  await assert.rejects(() => execute(async () => { throw new Error("API unavailable"); }), /API unavailable/);
  assert.equal(refunds, 0);
  assert.equal((await db.findNumber(number.phone)).id, number.id);
  assert.deepEqual((await db.updateLoginCode(number.phone, "12345")).chatIDs, [1]);
});

databaseTest("refund waits for the longest unexpired code, including a replay followed by a shorter code", async (db) => {
  await user(db);
  const number = await purchase(db);
  const expiry = Math.floor(Date.now() / 1000) + 600;
  await db.acceptLoginCodeDelivery("old-code", "fp1", number.phone, "12345", expiry);
  await db.acceptLoginCodeDelivery("new-code", "fp2", number.phone, "54321", expiry - 500);
  let lookedUp = false;
  await assert.rejects(() => db.revokePurchasedNumber(1, number.id, number.phone, async () => { lookedUp = true; return 0; }), /unexpired/);
  assert.equal(lookedUp, false);
  assert.equal((await db.findNumber(number.phone)).code_expires_at, expiry);
});

databaseTest("refund retirement serializes with a concurrent OTP acceptance", async (db) => {
  await user(db);
  const number = await purchase(db);
  let entered, release;
  const inside = new Promise((resolve) => { entered = resolve; });
  const proceed = new Promise((resolve) => { release = resolve; });
  const refund = db.revokePurchasedNumber(1, number.id, number.phone, async () => { entered(); await proceed; return 0; });
  await inside;
  const delivery = db.acceptLoginCodeDelivery("racing-code", "fp", number.phone, "12345", Math.floor(Date.now() / 1000) + 300).then(() => null, (error) => error);
  release();
  assert.equal(await refund, true);
  assert.match((await delivery).message, /NUMBER_RETIRED/);
  assert.equal((await db.pool.query("SELECT count(*)::int n FROM otp_deliveries")).rows[0].n, 0);
});

databaseTest("refund retry retires only its snapshotted number and never returns it to the pool", async (db) => {
  await user(db);
  const free = await db.createNumber(1, 1);
  const number = await purchase(db);
  const record = await db.saleByCharge("number-charge");
  let lookups = 0, telegramCalls = 0;
  const execute = () => executeCompensatedRefund({ sale: record, telegramID: 1, db, gramsrv: { resolveUserByPhone: async () => { lookups++; return 0; } }, refundStarPayment: async () => { if (++telegramCalls === 1) throw new Error("Telegram unavailable"); } });
  await assert.rejects(execute, /Telegram unavailable/);
  assert.equal((await db.currentNumber(1)).id, free.id);
  assert.equal(await db.findNumber(number.phone), null);
  await execute();
  assert.equal(lookups, 1);
  assert.equal(await db.isRefunded("number-charge"), true);
  await db.grantCodeAccess(number.phone, 1);
  await assert.rejects(() => db.updateLoginCode(number.phone, "12345"), /NUMBER_RETIRED/);
  await assert.rejects(() => db.acceptLoginCodeDelivery("retired-code", "fp", number.phone, "12345", 2000000000), /NUMBER_RETIRED/);
  const tombstone = (await db.pool.query("SELECT * FROM numbers WHERE id = $1", [number.id])).rows[0];
  assert.equal(tombstone.retired, true);
  let attempts = 0;
  db.generateNumber = () => ++attempts === 1 ? number : { phone: "+88809999999", display: "+88809999999", country: "ANON" };
  await user(db, 2);
  assert.equal((await db.createNumber(2, 2, "long", "ANON")).phone, "+88809999999");
  assert.equal(attempts, 2);
});

databaseTest("retirement migration is repeatable and preserves an existing allocated phone", async (db) => {
  await user(db);
  const number = await db.createNumber(1, 1);
  await db.pool.query("ALTER TABLE numbers DROP CONSTRAINT numbers_retired_current_check, DROP COLUMN retired");
  const migration = readFileSync(new URL("../db/migrations/001-number-retirement.sql", import.meta.url), "utf8");
  await db.pool.query(migration);
  await db.pool.query(migration);
  assert.equal((await db.currentNumber(1)).id, number.id);
  assert.equal((await db.findNumber(number.phone)).retired, false);
});

test("malformed account lookup responses fail closed", async () => {
  const gramsrv = new GramsrvClient({});
  for (const result of [null, {}, { found: true, user_id: 0 }, { found: true, user_id: "bad" }, { found: "false" }, { found: false }, { found: false, user_id: 123 }]) {
    gramsrv.post = async () => result;
    await assert.rejects(() => gramsrv.resolveUserByPhone("+15550000001"), /invalid account lookup response/);
  }
});

test("Docker copies only runtime source and excludes local credentials from its context", () => {
  const dockerfile = readFileSync(new URL("../Dockerfile", import.meta.url), "utf8");
  const ignore = readFileSync(new URL("../.dockerignore", import.meta.url), "utf8");
  assert.doesNotMatch(dockerfile, /COPY\s+\.\s+\./);
  assert.match(dockerfile, /COPY src\/ \.\/src\//);
  assert.match(ignore, /^\*$/m);
  assert.match(ignore, /^\*\*\/\.env$/m);
  assert.match(ignore, /^\*\*\/\.env\.\*$/m);
});
