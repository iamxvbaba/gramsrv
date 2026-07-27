import type { TFunction } from "./i18n";

export type Navigate = (href: string) => void;

export type RouteState = {
  href: string;
  path: string;
  search: URLSearchParams;
};

export function currentRoute(): RouteState {
  return {
    href: `${window.location.pathname}${window.location.search}`,
    path: window.location.pathname,
    search: new URLSearchParams(window.location.search)
  };
}

export function routeTitle(pathname: string, t: TFunction): string {
  if (pathname.startsWith("/accounts")) return t("route.accounts");
  if (pathname.startsWith("/channels")) return t("route.channels");
  if (pathname.startsWith("/bots")) return t("route.bots");
  if (pathname.startsWith("/moderation")) return t("route.moderation");
  if (pathname.startsWith("/emoji")) return t("route.emoji");
  if (pathname.startsWith("/messages")) return t("route.messages");
	if (pathname.startsWith("/give-gifts")) return t("route.giveGifts");
	if (pathname.startsWith("/gifts")) return t("route.gifts");
  return t("route.dashboard");
}

export function routeSubtitle(pathname: string, t: TFunction): string {
  if (pathname.startsWith("/accounts")) return t("route.accountsSubtitle");
  if (pathname.startsWith("/channels")) return t("route.channelsSubtitle");
  if (pathname.startsWith("/bots")) return t("route.botsSubtitle");
  if (pathname.startsWith("/moderation")) return t("route.moderationSubtitle");
  if (pathname.startsWith("/emoji")) return t("route.emojiSubtitle");
  if (pathname.startsWith("/messages")) return t("route.messagesSubtitle");
	if (pathname.startsWith("/give-gifts")) return t("route.giveGiftsSubtitle");
	if (pathname.startsWith("/gifts")) return t("route.giftsSubtitle");
  return t("route.dashboardSubtitle");
}
