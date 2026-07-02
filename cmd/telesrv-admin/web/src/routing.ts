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
  if (pathname.startsWith("/messages")) return t("route.messages");
  return t("route.dashboard");
}

export function routeSubtitle(pathname: string, t: TFunction): string {
  if (pathname.startsWith("/accounts")) return t("route.accountsSubtitle");
  if (pathname.startsWith("/channels")) return t("route.channelsSubtitle");
  if (pathname.startsWith("/messages")) return t("route.messagesSubtitle");
  return t("route.dashboardSubtitle");
}
