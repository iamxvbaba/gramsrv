import type { TFunction } from "./i18n";
import { stripBase } from "./lib/base";

export type Navigate = (href: string) => void;

export type RouteState = {
  href: string;
  path: string;
  search: URLSearchParams;
};

export function currentRoute(): RouteState {
  return {
    href: `${stripBase(window.location.pathname)}${window.location.search}`,
    path: stripBase(window.location.pathname),
    search: new URLSearchParams(window.location.search)
  };
}

export function routeTitle(pathname: string, t: TFunction): string {
  // Third-party verification is tested before the official section and before
  // "/bots": three different prefixes that all read as "verification of a bot".
  if (pathname.startsWith("/bot-verification")) return t("route.botVerification");
  if (pathname.startsWith("/verification")) return t("route.verification");
  if (pathname.startsWith("/admin-users")) return t("route.adminUsers");
  if (pathname.startsWith("/audit-log")) return t("route.auditLog");
  if (pathname.startsWith("/server-settings")) return t("route.serverSettings");
  if (pathname.startsWith("/collectible-usernames")) return t("route.collectibleUsernames");
  if (pathname.startsWith("/collectible-phones")) return t("route.collectiblePhones");
  if (pathname.startsWith("/account-ratings")) return t("route.accountRatings");
  if (pathname.startsWith("/stars")) return t("route.stars");
  if (pathname.startsWith("/storage")) return t("route.storage");
  if (pathname.startsWith("/accounts/shared-devices")) return t("route.sharedDevices");
  if (pathname.startsWith("/accounts")) return t("route.accounts");
  if (pathname.startsWith("/channels")) return t("route.channels");
  if (pathname.startsWith("/bots")) return t("route.bots");
  if (pathname.startsWith("/auto-subscribe-channels")) return t("autoSubscribe.pageTitle");
  if (pathname.startsWith("/broadcasts")) return t("route.broadcasts");
  if (pathname.startsWith("/monetization") || pathname.startsWith("/premium") || pathname.startsWith("/star-issue")) return t("route.premium");
  if (pathname.startsWith("/moderation")) return t("route.moderation");
  if (pathname.startsWith("/stickers")) return t("route.stickers");
  if (pathname.startsWith("/emoji")) return t("route.emoji");
  if (pathname.startsWith("/gif-catalog")) return t("route.gifCatalog");
  if (pathname.startsWith("/messages")) return t("route.messages");
  if (pathname.startsWith("/give-gifts")) return t("route.giveGifts");
  if (pathname.startsWith("/gifts")) return t("route.gifts");
  if (pathname.startsWith("/auctions")) return t("route.auctions");
  if (pathname.startsWith("/donations")) return t("route.donations");
  if (pathname.startsWith("/bot-shop")) return t("botShop.title");
  if (pathname.startsWith("/prices")) return t("route.prices");
  // "/statistics" and "/" both render the overview: the statistics charts were
  // folded into the dashboard rather than left as a page of their own, and the
  // old URL still has to read as the overview in the topbar.
  return t("route.dashboard");
}

export function routeSubtitle(pathname: string, t: TFunction): string {
  if (pathname.startsWith("/bot-verification")) return t("route.botVerificationSubtitle");
  if (pathname.startsWith("/verification")) return t("route.verificationSubtitle");
  if (pathname.startsWith("/admin-users")) return t("route.adminUsersSubtitle");
  if (pathname.startsWith("/audit-log")) return t("route.auditLogSubtitle");
  if (pathname.startsWith("/server-settings")) return t("route.serverSettingsSubtitle");
  if (pathname.startsWith("/collectible-usernames")) return t("route.collectibleUsernamesSubtitle");
  if (pathname.startsWith("/collectible-phones")) return t("route.collectiblePhonesSubtitle");
  if (pathname.startsWith("/account-ratings")) return t("route.accountRatingsSubtitle");
  if (pathname.startsWith("/stars")) return t("route.starsSubtitle");
  if (pathname.startsWith("/storage")) return t("route.storageSubtitle");
  if (pathname.startsWith("/accounts/shared-devices")) return t("route.sharedDevicesSubtitle");
  if (pathname.startsWith("/accounts")) return t("route.accountsSubtitle");
  if (pathname.startsWith("/channels")) return t("route.channelsSubtitle");
  if (pathname.startsWith("/bots")) return t("route.botsSubtitle");
  if (pathname.startsWith("/auto-subscribe-channels")) return t("autoSubscribe.eyebrow");
  if (pathname.startsWith("/broadcasts")) return t("route.broadcastsSubtitle");
  if (pathname.startsWith("/monetization") || pathname.startsWith("/premium") || pathname.startsWith("/star-issue")) return t("route.premiumSubtitle");
  if (pathname.startsWith("/moderation")) return t("route.moderationSubtitle");
  if (pathname.startsWith("/stickers")) return t("route.stickersSubtitle");
  if (pathname.startsWith("/emoji")) return t("route.emojiSubtitle");
  if (pathname.startsWith("/gif-catalog")) return t("route.gifCatalogSubtitle");
  if (pathname.startsWith("/messages")) return t("route.messagesSubtitle");
  if (pathname.startsWith("/give-gifts")) return t("route.giveGiftsSubtitle");
  if (pathname.startsWith("/gifts")) return t("route.giftsSubtitle");
  if (pathname.startsWith("/auctions")) return t("route.auctionsSubtitle");
  if (pathname.startsWith("/donations")) return t("route.donationsSubtitle");
  if (pathname.startsWith("/bot-shop")) return t("botShop.eyebrow");
  if (pathname.startsWith("/prices")) return t("route.pricesSubtitle");
  return t("route.dashboardSubtitle");
}
