import test from "node:test";
import assert from "node:assert/strict";
import { GramsrvClient } from "../src/gramsrv.js";

test("Admin API retries reuse a deterministic command id", () => {
  const client = new GramsrvClient({ gramsrvActor: "test" });
  const first = client.command("purchase", { user_id: 1 }, "payment:charge-1");
  const retry = client.command("purchase", { user_id: 1 }, "payment:charge-1");
  const other = client.command("purchase", { user_id: 1 }, "payment:charge-2");
  assert.equal(first.command_id, retry.command_id);
  assert.notEqual(first.command_id, other.command_id);
});

test("resolveUserByPhone forwards the phone and returns the numeric user id", async () => {
  const client = new GramsrvClient({ gramsrvActor: "test" });
  client.post = async (route, payload) => {
    assert.equal(route, "/v1/accounts/resolve-by-phone");
    assert.deepEqual(payload, { phone: "+79991234567" });
    return { found: true, user_id: 1780243207 };
  };
  assert.equal(await client.resolveUserByPhone("+79991234567"), 1780243207);
});

test("resolveUserByPhone returns 0 when the account is missing", async () => {
  const client = new GramsrvClient({ gramsrvActor: "test" });
  client.post = async () => ({ found: false, user_id: 0 });
  assert.equal(await client.resolveUserByPhone("+79990000000"), 0);
});
