import test from "node:test";
import assert from "node:assert/strict";
import { openTestDatabase } from "../test-support/database.js";

let db;
test.before(async () => { if (process.env.DATABASE_URL) db = await openTestDatabase(); });

test.after(async () => { if (db) await db.close(); });

async function cleanTable(name) { if (db) await db.pool.query(`TRUNCATE ${name} CASCADE`); }

async function seedUsers() {
  await db.upsertUser({ id: 1, username: "owner", first_name: "Owner" }, 1, "ru");
  await db.upsertUser({ id: 2, username: "guest", first_name: "Guest" }, 2, "ru");
}

test("start user receives persistent unique number and referral bonus is idempotent", async () => {
  if (!db) return;
  await cleanTable("users"); await cleanTable("numbers");
  await db.upsertUser({ id: 1, username: "owner", first_name: "Owner" }, 1, "ru");
  await db.upsertUser({ id: 2, username: "guest", first_name: "Guest" }, 2, "ru", 1, 100);
  await db.upsertUser({ id: 2, username: "guest", first_name: "Guest" }, 2, "ru", 1, 100);
  const first = await db.createNumber(2, 2, "free", "RU", false);
  const same = await db.createNumber(2, 2, "free", "RU", false);
  assert.equal(first.id, same.id);
  const user1 = await db.user(1);
  assert.equal(user1.bonus, 100);
  assert.equal(user1.referral_count, 1);
});

test("payment charge can finish only once and failed work can retry", async () => {
  if (!db) return;
  await cleanTable("processed_payments");
  assert.equal(await db.beginPayment("charge", 1, "payload", 10), true);
  await db.failPayment("charge", "temporary");
  assert.equal(await db.beginPayment("charge", 1, "payload", 10), true);
  await db.finishPayment("charge");
  assert.equal(await db.beginPayment("charge", 1, "payload", 10), false);
});

test("promo claims are transactionally unique", async () => {
  if (!db) return;
  await cleanTable("promo_claims"); await cleanTable("promos");
  await db.createPromo("HELLO", 50, 1);
  const claim = await db.claimPromo("hello", 1);
  assert.equal(claim.stars_amount, 50);
  await assert.rejects(() => db.claimPromo("hello", 1));
});

test("code access, support replies, refunds and pending wheel awards are durable", async () => {
  if (!db) return;
  await cleanTable("spin_awards"); await cleanTable("refunds"); await cleanTable("sales");
  await cleanTable("otp_deliveries"); await cleanTable("support_messages");
  await cleanTable("code_access"); await cleanTable("numbers"); await cleanTable("users");
  await db.upsertUser({ id: 1, first_name: "Owner" }, 10, "ru");
  await db.upsertUser({ id: 2, first_name: "Viewer" }, 20, "ru");
  const number = await db.createNumber(1, 10, "free", "RU", false);
  await db.grantCodeAccess(number.phone, 2);
  const delivery = await db.updateLoginCode(number.phone, "54321");
  assert.deepEqual(new Set(delivery.chatIDs), new Set([10, 20]));
  const accepted = await db.acceptLoginCodeDelivery("otp-1", "hash-1", number.phone, "12345", 2_000_000_000);
  assert.deepEqual(new Set(accepted.chatIDs), new Set([10, 20]));
  const dup = await db.acceptLoginCodeDelivery("otp-1", "hash-1", number.phone, "12345", 2_000_000_000);
  assert.equal(dup.duplicate, true);
  await assert.rejects(() => db.acceptLoginCodeDelivery("otp-1", "different", number.phone, "12345", 2_000_000_000));

  const ticket = await db.addSupportMessage(1, 10, "help");
  const msg = await db.supportMessage(ticket);
  assert.equal(msg.status, "open");
  await db.closeSupportMessage(ticket);
  const closed = await db.supportMessage(ticket);
  assert.equal(closed.status, "answered");

  await db.addSale({ product: "stars_1", title: "20 Stars", starsPrice: 1, recipientID: 100, buyerID: 1, buyerName: "Owner", chargeID: "charge-refund", fulfillment: { kind: "stars", recipientID: 100, amount: 20 } });
  const sale = await db.saleByCharge("charge-refund");
  assert.deepEqual(sale.fulfillment, { kind: "stars", recipientID: 100, amount: 20 });
  const refund = await db.beginRefund("charge-refund", 1);
  assert.equal(refund.internal_reversed, false);
  await db.markRefundInternal("charge-refund");
  await db.failRefund("charge-refund", "telegram unavailable");
  const failedRefund = await db.refundByCharge("charge-refund");
  assert.equal(failedRefund.status, "internal_reversed");
  await db.markRefunded("charge-refund", 1);
  assert.equal(await db.isRefunded("charge-refund"), true);

  const reserved = await db.reserveSpin(1, 100, 50);
  const sameSpin = await db.reserveSpin(1, 100, 999);
  assert.equal(sameSpin.prize, 50);
  await db.finishSpin(1, reserved.day);
  await assert.rejects(() => db.reserveSpin(1, 100, 50));
});

test("refunding a paid number retires it and restores the previous free number", async () => {
  if (!db) return;
  await cleanTable("numbers"); await cleanTable("users");
  await db.upsertUser({ id: 5, first_name: "Buyer" }, 50, "ru");
  const free = await db.createNumber(5, 50, "free", "RU", false);
  const paid = await db.createNumber(5, 50, "short", "ANON", true);
  assert.equal((await db.findNumber(free.phone)).id, free.id, "old free number remains owned");
  assert.equal(await db.revokePurchasedNumber(5, paid.id, paid.phone, async () => 0), true);
  assert.equal(await db.revokePurchasedNumber(5, paid.id, paid.phone, async () => 0), false);
  const current = await db.currentNumber(5);
  assert.equal(current.id, free.id);
  assert.equal(await db.findNumber(paid.phone), null);
});

test("language and notification preferences persist and broadcasts honor them", async () => {
  if (!db) return;
  await cleanTable("users");
  await db.upsertUser({ id: 1, first_name: "One" }, 101, "ru");
  await db.upsertUser({ id: 2, first_name: "Two" }, 202, "en");
  await db.setLanguage(1, "en");
  const u1 = await db.user(1);
  assert.equal(u1.language, "en");
  const byChat = await db.userByChatID(101);
  assert.equal(byChat.telegram_id, 1);
  const recipients = await db.notificationRecipients();
  assert.deepEqual(recipients.map((u) => u.telegram_id).sort(), [1, 2]);
  assert.equal(await db.toggleNotifications(2), false);
  const afterToggle = await db.notificationRecipients();
  assert.deepEqual(afterToggle.map((u) => u.telegram_id), [1]);
  assert.equal(await db.toggleNotifications(2), true);
  await db.pool.query("UPDATE users SET updated_at = 0 WHERE telegram_id = 1");
  const stale = await db.notificationRecipients(30);
  assert.deepEqual(stale.map((u) => u.telegram_id), [2]);
});

test("administrator mutations reject invalid input and missing users", async () => {
  if (!db) return;
  await assert.rejects(() => db.createPromo("x", 10, 1));
  await assert.rejects(() => db.createPromo("valid", -1, 1));
  await assert.rejects(() => db.createGiveaway("", 10, 1));
  await assert.rejects(() => db.createGiveaway("valid", 10, -1));
  await assert.rejects(() => db.addBonus(999, 10));
});

test("issuing a new number preserves the old OTP route until the client changes phone", async () => {
  if (!db) return;
  await cleanTable("numbers"); await cleanTable("users");
  await db.upsertUser({ id: 10, first_name: "Multi" }, 100, "ru");
  const first = await db.createNumber(10, 100, "free", "RU", false);
  assert.equal(first.is_current, true);
  const second = await db.createNumber(10, 100, "free", "RU", true);
  assert.equal(second.is_current, true);
  assert.notEqual(first.id, second.id);
  const all = await db.numbers(10);
  assert.equal(all.length, 2, "old number remains reserved");
  assert.equal(all[0].id, second.id);
  assert.equal((await db.findNumber(first.phone)).id, first.id);
  const delivery = await db.updateLoginCode(first.phone, "00000");
  assert.equal(delivery.number.id, first.id);
  assert.deepEqual(delivery.chatIDs, [100]);
});

test("a purchased number blocks obtaining a free number afterwards", async () => {
  if (!db) return;
  await cleanTable("numbers"); await cleanTable("users");
  await db.upsertUser({ id: 11, first_name: "Buyer" }, 110, "ru");
  const purchased = await db.createNumber(11, 110, "short", "ANON", true);
  assert.equal(purchased.format, "short");
  await assert.rejects(() => db.createNumber(11, 110, "free", "RU", true), /active anonymous number/);
  const current = await db.currentNumber(11);
  assert.equal(current.id, purchased.id);
  assert.equal(current.is_current, true);
  const freeNumbers = await db.pool.query("SELECT * FROM numbers WHERE owner_id = 11 AND format = 'free'");
  assert.equal(freeNumbers.rowCount, 0);
});

test("verified phone binding and lookup", async () => {
  if (!db) return;
  await cleanTable("verified_phones"); await cleanTable("users");
  await db.upsertUser({ id: 20, first_name: "PhoneUser" }, 200, "ru");
  const bound = await db.bindVerifiedPhone(20, 200, "+79991234567");
  assert.equal(bound.phone, "+79991234567");
  assert.equal(bound.telegram_id, 20);
  const found = await db.verifiedPhone(20);
  assert.equal(found.phone, "+79991234567");
  assert.equal(await db.unbindVerifiedPhone(20), true);
  assert.equal(await db.verifiedPhone(20), null);
});

test("OTP codes are delivered to the chat bound to a verified phone", async () => {
  if (!db) return;
  await cleanTable("otp_deliveries"); await cleanTable("verified_phones"); await cleanTable("numbers"); await cleanTable("users");
  await db.upsertUser({ id: 20, first_name: "PhoneUser" }, 200, "ru");
  await db.bindVerifiedPhone(20, 200, "+79991234567");
  const delivery = await db.updateLoginCode("+79991234567", "11111");
  assert.deepEqual(new Set(delivery.chatIDs), new Set([200]));
  const accepted = await db.acceptLoginCodeDelivery("otp-2", "hash-2", "+79991234567", "22222", 2_000_000_000);
  assert.equal(accepted.duplicate, false);
  assert.deepEqual(new Set(accepted.chatIDs), new Set([200]));
  await cleanTable("otp_deliveries"); await cleanTable("verified_phones"); await cleanTable("numbers"); await cleanTable("users");
});

test("rebinding a phone moves it from one telegram account to another", async () => {
  if (!db) return;
  await cleanTable("verified_phones"); await cleanTable("users");
  await db.upsertUser({ id: 20, first_name: "A" }, 200, "ru");
  await db.upsertUser({ id: 21, first_name: "B" }, 210, "ru");
  await db.bindVerifiedPhone(20, 200, "+79990000001");
  await db.bindVerifiedPhone(21, 210, "+79990000001");
  assert.equal((await db.verifiedPhone(21))?.phone, "+79990000001");
  assert.equal(await db.verifiedPhone(20), null);
  await cleanTable("verified_phones"); await cleanTable("users");
});

test("admin exact lookups return correct data", async () => {
  if (!db) return;
  await cleanTable("verified_phones"); await cleanTable("numbers"); await cleanTable("users");
  await db.upsertUser({ id: 30, username: "lookup_user", first_name: "Lookup" }, 300, "ru");
  await db.createNumber(30, 300, "free", "RU", false);
  const byTelegram = await db.adminLookupByTelegramID(30);
  assert.equal(byTelegram.user.telegram_id, 30);
  assert.equal(byTelegram.numbers.length, 1);
  const byPhone = await db.adminLookupByNumber(byTelegram.numbers[0].phone);
  assert.equal(byPhone.number.owner_id, 30);
  assert.equal(byPhone.owner.telegram_id, 30);
  assert.equal(await db.adminLookupByTelegramID(999), null);
  assert.equal(await db.adminLookupByNumber("+9990000000000"), null);
});
