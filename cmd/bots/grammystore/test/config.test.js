import test from "node:test";
import assert from "node:assert/strict";
import { loadConfig } from "../src/config.js";

const managedEnv = [
  "BOT_TOKEN", "OWNER_IDS", "GRAMSRV_TOKEN", "CODE_WEBHOOK_SECRET",
  "DEFAULT_LANGUAGE", "DEFAULT_NUMBER_COUNTRY", "REFERRAL_BONUS",
  "DAILY_BONUS", "NOTIFICATION_TTL_DAYS", "DATABASE_URL", "DATABASE_HOST",
  "POSTGRES_USER", "POSTGRES_PASSWORD", "POSTGRES_DB", "BOT_MODE",
];

function withEnv(values, fn) {
  const previous = new Map(managedEnv.map((key) => [key, process.env[key]]));
  for (const key of managedEnv) delete process.env[key];
  Object.assign(process.env, values);
  try { return fn(); }
  finally {
    for (const key of managedEnv) {
      if (previous.get(key) === undefined) delete process.env[key];
      else process.env[key] = previous.get(key);
    }
  }
}

test("loadConfig supplies defaults required by runtime bot flows", () => {
  const config = withEnv({
    BOT_TOKEN: "999:TEST", OWNER_IDS: "1", GRAMSRV_TOKEN: "token",
    CODE_WEBHOOK_SECRET: "abcdefghijklmnopqrstuvwxyz", DATABASE_URL: "postgres://localhost/test",
  }, () => loadConfig());
  assert.equal(config.defaultLanguage, "ru");
  assert.equal(config.referralBonus, 100);
  assert.equal(config.dailyBonus, 15);
  assert.equal(config.notificationTTLDays, 30);
  assert.equal(config.botMode, "random");
});

test("loadConfig validates bonus and notification overrides", () => {
  const config = withEnv({
    BOT_TOKEN: "999:TEST", OWNER_IDS: "1", GRAMSRV_TOKEN: "token",
    CODE_WEBHOOK_SECRET: "abcdefghijklmnopqrstuvwxyz", DATABASE_URL: "postgres://localhost/test",
    DEFAULT_LANGUAGE: "en", REFERRAL_BONUS: "7", DAILY_BONUS: "3", NOTIFICATION_TTL_DAYS: "9",
  }, () => loadConfig());
  assert.equal(config.defaultLanguage, "en");
  assert.equal(config.referralBonus, 7);
  assert.equal(config.dailyBonus, 3);
  assert.equal(config.notificationTTLDays, 9);
});

test("loadConfig rejects placeholder values", () => {
  for (const placeholder of ["CHANGE_ME", "YOUR_TOKEN", "<secret>", "example-token"]) {
    assert.throws(() => withEnv({
      BOT_TOKEN: "999:TEST", OWNER_IDS: "1", GRAMSRV_TOKEN: placeholder,
      CODE_WEBHOOK_SECRET: "abcdefghijklmnopqrstuvwxyz", DATABASE_URL: "postgres://localhost/test",
    }, () => loadConfig()), /placeholder/i);
  }
});

test("loadConfig builds an encoded database URL from Compose settings", () => {
  const config = withEnv({
    BOT_TOKEN: "999:TEST", OWNER_IDS: "1", GRAMSRV_TOKEN: "token",
    CODE_WEBHOOK_SECRET: "abcdefghijklmnopqrstuvwxyz", DATABASE_HOST: "postgres",
    POSTGRES_USER: "grammystore", POSTGRES_PASSWORD: "p@ss:word/with?reserved#chars%literal",
    POSTGRES_DB: "grammystore",
  }, () => loadConfig());
  const parsed = new URL(config.dbUrl);
  assert.equal(parsed.hostname, "postgres");
  assert.equal(decodeURIComponent(parsed.username), "grammystore");
  assert.equal(decodeURIComponent(parsed.password), "p@ss:word/with?reserved#chars%literal");
  assert.equal(parsed.pathname, "/grammystore");
});

test("loadConfig requires database settings", () => {
  assert.throws(() => withEnv({
    BOT_TOKEN: "999:TEST", OWNER_IDS: "1", GRAMSRV_TOKEN: "token",
    CODE_WEBHOOK_SECRET: "abcdefghijklmnopqrstuvwxyz",
  }, () => loadConfig()), /POSTGRES_USER is required/);
});

test("loadConfig requires CODE_WEBHOOK_SECRET with minimum length", () => {
  assert.throws(() => withEnv({
    BOT_TOKEN: "999:TEST", OWNER_IDS: "1", GRAMSRV_TOKEN: "token",
    CODE_WEBHOOK_SECRET: "short", DATABASE_URL: "postgres://localhost/test",
  }, () => loadConfig()), /at least 24 characters/);
});

test("loadConfig validates BOT_MODE values", () => {
  assert.throws(() => withEnv({
    BOT_TOKEN: "999:TEST", OWNER_IDS: "1", GRAMSRV_TOKEN: "token",
    CODE_WEBHOOK_SECRET: "abcdefghijklmnopqrstuvwxyz", DATABASE_URL: "postgres://localhost/test",
    BOT_MODE: "invalid",
  }, () => loadConfig()), /BOT_MODE must be/);
  const config = withEnv({
    BOT_TOKEN: "999:TEST", OWNER_IDS: "1", GRAMSRV_TOKEN: "token",
    CODE_WEBHOOK_SECRET: "abcdefghijklmnopqrstuvwxyz", DATABASE_URL: "postgres://localhost/test",
    BOT_MODE: "real",
  }, () => loadConfig());
  assert.equal(config.botMode, "real");
});
