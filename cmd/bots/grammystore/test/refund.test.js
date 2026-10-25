import test from "node:test";
import assert from "node:assert/strict";
import { BotDatabase } from "../src/db.js";
import { executeCompensatedRefund, fulfillmentForSale, reverseSaleFulfillment } from "../src/bot.js";

test("legacy Stars sale resolves the exact granted amount from its title", () => {
  const item = fulfillmentForSale({
    product: "stars_1", title: "20 LegacyBrand Stars", recipient_id: 1780243200,
    invoice_payload: "store|stars_1|1780243200|", fulfillment: {},
  }, 50);
  assert.deepEqual(item, { kind: "stars", recipientID: 1780243200, amount: 20 });
});

test("Stars reversal debits the snapshotted grant with a deterministic key", async () => {
  const calls = [];
  const db = { starsRate: async () => 999 };
  const gramsrv = { debitStars: async (...args) => calls.push(args) };
  const sale = { charge_id: "charge-1", fulfillment: { kind: "stars", recipientID: 1001, amount: 20 } };
  await reverseSaleFulfillment(sale, db, gramsrv);
  assert.deepEqual(calls, [[1001, 20, "Telegram bot refund", "refund:charge-1:stars"]]);
});

test("Premium reversal only revokes the entitlement created by the purchase", async () => {
  const calls = [];
  const db = { starsRate: async () => 20 };
  const gramsrv = { revokePremium: async (...args) => calls.push(args) };
  const sale = { charge_id: "charge-premium", fulfillment: { kind: "premium", recipientID: 1001, months: 3, entitlementID: 77 } };
  await reverseSaleFulfillment(sale, db, gramsrv);
  assert.deepEqual(calls, [[1001, 77, "Telegram bot refund", "refund:charge-premium:premium"]]);
});

test("legacy Premium reversal fails safe instead of clearing unrelated Premium", async () => {
  const sale = { charge_id: "legacy", product: "premium_1m", recipient_id: 1001, fulfillment: {} };
  await assert.rejects(() => reverseSaleFulfillment(sale, { starsRate: async () => 20 }, {}), /ID/);
});

function mockDb() {
  const sales = new Map();
  const refunds = new Map();
  return {
    starsRate: async () => 20,
    addSale: async (sale) => { sales.set(sale.chargeID, { ...sale, payment_status: "done" }); },
    saleByCharge: async (id) => {
      const s = sales.get(id);
      return s ? { ...s, charge_id: s.chargeID, fulfillment: s.fulfillment, payment_status: "done" } : null;
    },
    beginRefund: async (chargeID) => {
      if (!refunds.has(chargeID)) refunds.set(chargeID, { charge_id: chargeID, status: "reversing", internal_reversed: false });
      return refunds.get(chargeID);
    },
    markRefundInternal: async (chargeID) => { const r = refunds.get(chargeID); if (r) { r.internal_reversed = true; r.status = "internal_reversed"; } },
    markRefunded: async (chargeID) => { const r = refunds.get(chargeID); if (r) { r.status = "completed"; r.internal_reversed = true; } },
    failRefund: async (chargeID) => { const r = refunds.get(chargeID); if (r) r.status = r.internal_reversed ? "internal_reversed" : "failed"; },
    isRefunded: async (chargeID) => refunds.get(chargeID)?.status === "completed",
    refundByCharge: async (id) => refunds.get(id) ?? null,
  };
}

test("Telegram retry does not debit the internal product twice", async () => {
  const db = mockDb();
  await db.addSale({
    product: "stars_1", title: "20 Stars", starsPrice: 1, recipientID: 1001,
    buyerID: 7, buyerName: "Buyer", chargeID: "charge-retry",
    fulfillment: { kind: "stars", recipientID: 1001, amount: 20 },
  });
  const sale = await db.saleByCharge("charge-retry");
  const debits = [];
  const gramsrv = { debitStars: async (...args) => debits.push(args) };
  await assert.rejects(() => executeCompensatedRefund({
    sale, telegramID: 7, db, gramsrv,
    refundStarPayment: async () => { throw new Error("temporary Telegram failure"); },
  }), /temporary/);
  const afterFail = await db.refundByCharge("charge-retry");
  assert.equal(afterFail.status, "internal_reversed");
  await executeCompensatedRefund({ sale, telegramID: 7, db, gramsrv, refundStarPayment: async () => true });
  assert.equal(debits.length, 1);
  assert.equal(await db.isRefunded("charge-retry"), true);
});
