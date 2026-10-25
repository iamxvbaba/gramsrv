import { Keyboard } from "grammy";
import { translate, translateError } from "./i18n.js";

function escapeHTML(value) { return String(value ?? "").replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;"); }

export function isRandomMode(config) { return config.botMode === "random"; }
export function isRealMode(config) { return config.botMode === "real"; }

export function phoneShareKeyboard(language) {
  return new Keyboard().requestContact(translate(language, "phoneShareButton")).row().text(translate(language, "phoneCancelButton")).resized().oneTime();
}

export function rejectRandomInRealMode(ctx, config, language) {
  if (!isRealMode(config)) return false;
  ctx.reply(translate(language, "errorRealModeOnly")).catch(() => {});
  return true;
}
