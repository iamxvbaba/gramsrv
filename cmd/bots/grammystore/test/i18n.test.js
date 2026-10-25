import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { adminKeyboard, commandList, isStartCommand, mainKeyboard, settingsKeyboard, shopKeyboard } from "../src/bot.js";
import { catalog, findProduct, localizeProduct } from "../src/catalog.js";
import { messages, normalizeLanguage, translate, translateError } from "../src/i18n.js";

const cyrillic = /[А-Яа-яЁё]/u;
const buttonText = (keyboard) => keyboard.inline_keyboard.flat().map((button) => button.text);

test("Russian and English dictionaries have the same complete key set", () => {
  assert.deepEqual(Object.keys(messages.ru).sort(), Object.keys(messages.en).sort());
  assert.ok(Object.keys(messages.en).length > 100);
});

test("English interface strings and every English keyboard contain no Cyrillic", () => {
  for (const [key, value] of Object.entries(messages.en)) {
    assert.equal(cyrillic.test(value), false, `English translation ${key} contains Cyrillic: ${value}`);
  }
  const keyboards = [
    mainKeyboard("en", true),
    shopKeyboard("en"),
    settingsKeyboard("en", { language: "en", notifications: 1 }),
    adminKeyboard("en"),
  ];
  for (const keyboard of keyboards) {
    for (const label of buttonText(keyboard)) assert.equal(cyrillic.test(label), false, `English button contains Cyrillic: ${label}`);
  }
  for (const product of catalog()) {
    const view = localizeProduct(product, "en");
    assert.equal(cyrillic.test(`${view.title}\n${view.description}`), false, `English product ${product.code} contains Cyrillic`);
  }
});

test("runtime handlers contain no embedded Russian UI and reference existing translation keys", () => {
  const source = ["bot.js", "index.js"].map((file) => readFileSync(new URL(`../src/${file}`, import.meta.url), "utf8")).join("\n");
  assert.equal(cyrillic.test(source), false, "runtime handler contains a Russian string outside i18n.js");
  const keys = [...source.matchAll(/(?:\btr\([^,]+,|\btranslate\([^,]+,)\s*["']([^"']+)["']/g)].map((match) => match[1]);
  for (const key of keys) assert.ok(key in messages.en, `handler references missing translation key: ${key}`);
});

test("language normalization, commands and errors follow the selected language", () => {
  assert.equal(normalizeLanguage("en-US"), "en");
  assert.equal(normalizeLanguage("ru-RU"), "ru");
  assert.equal(normalizeLanguage("de", "en"), "en");
  assert.equal(commandList("en")[0].description, "Open the main menu");
  assert.equal(commandList("ru")[0].description, "Открыть главное меню");
  assert.equal(translate("en", "languageChanged"), "Language switched to English.");
  assert.match(translateError("en", new Error("already claimed")), /already claimed/i);
  assert.match(translateError("ru", new Error("already claimed")), /уже получили/i);
});

test("start commands are recognized before the generic user middleware", () => {
  assert.equal(isStartCommand("/start"), true);
  assert.equal(isStartCommand("/start ref_123"), true);
  assert.equal(isStartCommand("/start@test_bot ref_123"), true);
  assert.equal(isStartCommand("/menu"), false);
  assert.equal(isStartCommand("hello /start"), false);
});

test("catalog titles and invoice descriptions are localized", () => {
  const product = findProduct("premium_1m");
  assert.equal(localizeProduct(product, "en").title, "Premium — 1 month");
  assert.equal(localizeProduct(product, "ru").title, "Premium — 1 месяц");
  assert.equal(cyrillic.test(localizeProduct(product, "en").description), false);
  assert.equal(cyrillic.test(localizeProduct(product, "ru").description), true);
});

test("new real-number and admin lookup translation keys exist in both languages", () => {
  const requiredKeys = [
    "errorRealModeOnly", "errorRandomModeOnly", "phoneTitle", "phoneIntro",
    "phoneShareButton", "phoneCancelButton", "phoneStatus", "phoneBound",
    "phoneUnbindButton", "phoneUnbound", "errorContactNotOwn",
    "adminLookupButton", "adminPromptLookup", "adminLookupResult", "adminLookupNotFound",
  ];
  for (const key of requiredKeys) {
    assert.ok(key in messages.en, `Missing English key: ${key}`);
    assert.ok(key in messages.ru, `Missing Russian key: ${key}`);
  }
});
