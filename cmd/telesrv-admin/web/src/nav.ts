import {
  AtSign,
  BadgeCheck,
  BadgeDollarSign,
  Bot,
  Gift,
  LayoutDashboard,
  Megaphone,
  MessageSquareText,
  ShieldAlert,
  ShieldCheck,
  Smile,
  ScrollText,
  Settings,
  UserCog,
  Users,
  Wallet,
  type LucideIcon
} from "lucide-react";
import {
  permissionAccountsRead,
  permissionAdminsManage,
  permissionAuditRead,
  permissionBotsRead,
  permissionBotVerificationReview,
  permissionBroadcastsRead,
  permissionChannelsRead,
  permissionContentRead,
  permissionDashboardRead,
  permissionDonationsManage,
  permissionGiftsRead,
  permissionMessagesRead,
  permissionModerationReview,
  permissionPhonesRead,
  permissionPremiumManage,
  permissionPricesManage,
  permissionRatingsRead,
  permissionServerManage,
  permissionStarsRead,
  permissionStorageRead,
  permissionUsernamesRead,
  permissionVerificationReview
} from "./permissions";

// The console's navigation, declared once.
//
// Two rules shape this table:
//
//  1. One sidebar entry per logical section, and the section's sibling routes are
//     in-page tabs (see SectionTabs). A section is worth a sidebar row only if
//     an operator actually works in it; two rows for "Premium" and "Give gifts"
//     is the flat-list sprawl this replaced.
//
//  2. Every tab carries the right its route enforces server-side. The sidebar
//     hides what a session may not use, and Routes.tsx gates the same paths with
//     the same strings -- both read this table, so a section can no longer be
//     advertised and then answer 403. A right that is not in this table has no
//     route of its own (actions are gated at the endpoint), which is why
//     accounts.manage, gifts.manage, ratings.manage and friends do not appear:
//     they widen a tab that is already gated by its read right.

export type NavTab = {
  path: string;
  labelKey: string;
  // null = every authenticated session may use it.
  permission: string | null;
  // Extra paths that open this tab. /messages renders the private view, so the
  // bare path has to light the Messages row and mark the Private tab active
  // even though the sidebar link points at /messages/private.
  aliases?: string[];
  // Set false on a tab shown in a second section that must not own its path.
  active?: boolean;
};

export type NavSection = {
  id: string;
  labelKey: string;
  icon: LucideIcon;
  tabs: NavTab[];
};

export const navSections: NavSection[] = [
  {
    id: "overview",
    labelKey: "layout.dashboard",
    icon: LayoutDashboard,
    tabs: [{ path: "/", labelKey: "layout.dashboard", permission: permissionDashboardRead }]
  },
  {
    // Grouping mirrors the desired left panel: one row per job area, its pages
    // are the in-page tabs.
    id: "users-chats",
    labelKey: "nav.usersChats",
    icon: Users,
    tabs: [
      { path: "/accounts", labelKey: "layout.accounts", permission: permissionAccountsRead },
      { path: "/channels", labelKey: "layout.channels", permission: permissionChannelsRead },
      { path: "/bots", labelKey: "layout.bots", permission: permissionBotsRead },
      { path: "/broadcasts", labelKey: "layout.broadcasts", permission: permissionBroadcastsRead }
    ]
  },
  {
    id: "moderation",
    labelKey: "nav.moderation",
    icon: ShieldAlert,
    tabs: [
      { path: "/messages/private", labelKey: "layout.messages", permission: permissionMessagesRead, aliases: ["/messages"] },
      { path: "/messages/groups", labelKey: "layout.groupMessages", permission: permissionMessagesRead },
      { path: "/moderation", labelKey: "layout.moderation", permission: permissionModerationReview },
      { path: "/accounts/shared-devices", labelKey: "layout.sharedDevices", permission: permissionAccountsRead }
    ]
  },
  {
    id: "verification",
    labelKey: "nav.verification",
    icon: BadgeCheck,
    tabs: [
      { path: "/verification", labelKey: "verification.tabOfficial", permission: permissionVerificationReview },
      { path: "/bot-verification", labelKey: "verification.tabThirdParty", permission: permissionBotVerificationReview }
    ]
  },
  {
    id: "content",
    labelKey: "nav.content",
    icon: Smile,
    tabs: [
      { path: "/stickers", labelKey: "layout.stickers", permission: permissionContentRead },
      { path: "/emoji", labelKey: "layout.emoji", permission: permissionContentRead },
      { path: "/gif-catalog", labelKey: "layout.gifCatalog", permission: permissionContentRead }
    ]
  },
  {
    id: "economy",
    labelKey: "nav.economy",
    icon: BadgeDollarSign,
    tabs: [
      { path: "/stars", labelKey: "layout.stars", permission: permissionStarsRead },
      {
        path: "/monetization",
        labelKey: "nav.premium",
        permission: permissionPremiumManage,
        aliases: ["/premium", "/star-issue"]
      },
      { path: "/prices", labelKey: "nav.botPrices", permission: permissionPricesManage },
      { path: "/donations", labelKey: "layout.donations", permission: permissionDonationsManage },
      { path: "/account-ratings", labelKey: "layout.accountRatings", permission: permissionRatingsRead }
    ]
  },
  {
    id: "gifts",
    labelKey: "nav.gifts",
    icon: Gift,
    tabs: [
      { path: "/gifts", labelKey: "gifts.pageTitle", permission: permissionGiftsRead },
      { path: "/give-gifts", labelKey: "layout.giveGifts", permission: permissionGiftsRead },
      { path: "/auctions", labelKey: "layout.auctions", permission: permissionGiftsRead }
    ]
  },
  {
    id: "nft",
    labelKey: "nav.nftItems",
    icon: AtSign,
    tabs: [
      { path: "/collectible-usernames", labelKey: "layout.collectibleUsernames", permission: permissionUsernamesRead },
      { path: "/collectible-phones", labelKey: "layout.collectiblePhones", permission: permissionPhonesRead },
      { path: "/nft-gifts", labelKey: "nft.gifts", permission: permissionGiftsRead }
    ]
  },
  {
    id: "server",
    labelKey: "nav.server",
    icon: Settings,
    tabs: [
      { path: "/server-settings", labelKey: "layout.serverSettings", permission: permissionServerManage },
      { path: "/storage", labelKey: "layout.storage", permission: permissionStorageRead },
      { path: "/admin-users", labelKey: "layout.adminUsers", permission: permissionAdminsManage },
      { path: "/audit-log", labelKey: "layout.auditLog", permission: permissionAuditRead }
    ]
  }
];

// visibleTabs is the section a session actually has: the tabs whose right it
// holds. A section with none of them is not shown at all, and a section with
// exactly one loses its tab bar (SectionTabs returns null for a single tab).
export function visibleTabs(section: NavSection, can: (permission: string) => boolean): NavTab[] {
  return section.tabs.filter((tab) => tab.permission === null || can(tab.permission));
}

export function sectionById(id: string): NavSection | undefined {
  return navSections.find((section) => section.id === id);
}

// tabMatchLength is how specifically a tab claims a path: the length of its
// longest path or alias that is the path itself or a "<candidate>/" prefix of
// it, or -1 if none is. Prefix matching is what lets a detail route belong to
// its tab: /accounts/42 to /accounts, /messages/groups/detail to /messages/groups.
function tabMatchLength(tab: NavTab, path: string): number {
  if (tab.active === false) return -1;
  let best = -1;
  for (const candidate of [tab.path, ...(tab.aliases ?? [])]) {
    if ((path === candidate || path.startsWith(`${candidate}/`)) && candidate.length > best) {
      best = candidate.length;
    }
  }
  return best;
}

// matchTab is the tab among tabs that owns path: the longest match wins, so
// /messages/groups belongs to its own tab even though the /messages alias of
// the private tab also matches it.
export function matchTab(tabs: NavTab[], path: string): NavTab | undefined {
  let best: NavTab | undefined;
  let bestLen = -1;
  for (const tab of tabs) {
    const length = tabMatchLength(tab, path);
    if (length > bestLen) {
      bestLen = length;
      best = tab;
    }
  }
  return best;
}

// activeSectionId is the section whose tab bar belongs on this route, so the
// shell can render SectionTabs once instead of every page rendering its own.
// It picks the longest matching tab path, so /accounts/shared-devices lights
// Moderation (its exact tab), not Users and chats (whose /accounts prefix also
// matches).
export function activeSectionId(path: string, can: (permission: string) => boolean): string | null {
  let best: string | null = null;
  let bestLen = -1;
  for (const section of navSections) {
    for (const tab of visibleTabs(section, can)) {
      const length = tabMatchLength(tab, path);
      if (length > bestLen) {
        bestLen = length;
        best = section.id;
      }
    }
  }
  return best;
}

// routePermission is the single answer to "what right does this URL need", so
// Routes.tsx can gate the whole surface in one place instead of wrapping each
// branch and inevitably missing some. Exact paths are listed first because
// several share a prefix: /messages/private and /messages/groups are siblings,
// and /stars/42 has to fall to /stars rather than being read as /stars/top.
//
// A path that matches nothing returns null and falls through to the dashboard
// the same way an unknown URL does.
const routePermissions: { prefix: string; permission: string }[] = [
  { prefix: "/bot-verification", permission: permissionBotVerificationReview },
  { prefix: "/bot-verification/", permission: permissionBotVerificationReview },
  { prefix: "/verification", permission: permissionVerificationReview },
  { prefix: "/verification/", permission: permissionVerificationReview },
  { prefix: "/admin-users", permission: permissionAdminsManage },
  { prefix: "/audit-log", permission: permissionAuditRead },
  { prefix: "/donations", permission: permissionDonationsManage },
  { prefix: "/prices", permission: permissionPricesManage },
  { prefix: "/server-settings", permission: permissionServerManage },
  { prefix: "/stars", permission: permissionStarsRead },
  { prefix: "/storage", permission: permissionStorageRead },
  { prefix: "/account-ratings", permission: permissionRatingsRead },
  { prefix: "/accounts", permission: permissionAccountsRead },
  { prefix: "/channels", permission: permissionChannelsRead },
  { prefix: "/bots", permission: permissionBotsRead },
  { prefix: "/broadcasts", permission: permissionBroadcastsRead },
  { prefix: "/monetization", permission: permissionPremiumManage },
  { prefix: "/premium", permission: permissionPremiumManage },
  { prefix: "/star-issue", permission: permissionPremiumManage },
  { prefix: "/moderation", permission: permissionModerationReview },
  { prefix: "/stickers", permission: permissionContentRead },
  { prefix: "/emoji", permission: permissionContentRead },
  { prefix: "/gif-catalog", permission: permissionContentRead },
  { prefix: "/messages", permission: permissionMessagesRead },
  { prefix: "/give-gifts", permission: permissionGiftsRead },
  { prefix: "/gifts", permission: permissionGiftsRead },
  { prefix: "/auctions", permission: permissionGiftsRead },
  { prefix: "/collectible-usernames", permission: permissionUsernamesRead },
  { prefix: "/collectible-phones", permission: permissionPhonesRead },
  { prefix: "/nft-gifts", permission: permissionGiftsRead },
  { prefix: "/statistics", permission: permissionDashboardRead }
];

export function routePermission(path: string): string | null {
  for (const rule of routePermissions) {
    if (path === rule.prefix || path.startsWith(`${rule.prefix}/`)) {
      return rule.permission;
    }
  }
  // "/" and anything unrecognized land on the dashboard, so they are gated by
  // the dashboard right rather than rendering a page whose every fetch 403s.
  return permissionDashboardRead;
}