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
    id: "accounts",
    labelKey: "layout.accounts",
    icon: Users,
    tabs: [
      { path: "/accounts", labelKey: "layout.accounts", permission: permissionAccountsRead },
      { path: "/account-ratings", labelKey: "layout.accountRatings", permission: permissionRatingsRead },
      { path: "/accounts/shared-devices", labelKey: "layout.sharedDevices", permission: permissionAccountsRead }
    ]
  },
  {
    id: "channels",
    labelKey: "layout.channels",
    icon: ShieldCheck,
    tabs: [{ path: "/channels", labelKey: "layout.channels", permission: permissionChannelsRead }]
  },
  {
    id: "bots",
    labelKey: "layout.bots",
    icon: Bot,
    tabs: [{ path: "/bots", labelKey: "layout.bots", permission: permissionBotsRead }]
  },
  {
    id: "verification",
    labelKey: "layout.verification",
    icon: BadgeCheck,
    tabs: [
      { path: "/verification", labelKey: "verification.tabOfficial", permission: permissionVerificationReview },
      { path: "/bot-verification", labelKey: "verification.tabThirdParty", permission: permissionBotVerificationReview }
    ]
  },
  {
    id: "gifts",
    labelKey: "layout.gifts",
    icon: Gift,
    tabs: [
      { path: "/gifts", labelKey: "gifts.pageTitle", permission: permissionGiftsRead },
      { path: "/give-gifts", labelKey: "layout.giveGifts", permission: permissionGiftsRead },
      { path: "/auctions", labelKey: "layout.auctions", permission: permissionGiftsRead }
    ]
  },
  {
    id: "commerce",
    labelKey: "layout.commerce",
    icon: BadgeDollarSign,
    tabs: [
      { path: "/monetization", labelKey: "layout.premium", permission: permissionPremiumManage },
      { path: "/prices", labelKey: "layout.prices", permission: permissionPricesManage },
      { path: "/stars", labelKey: "layout.stars", permission: permissionStarsRead }
    ]
  },
  {
    id: "nft",
    labelKey: "layout.nftItems",
    icon: AtSign,
    tabs: [
      { path: "/collectible-usernames", labelKey: "layout.collectibleUsernames", permission: permissionUsernamesRead },
      { path: "/collectible-phones", labelKey: "layout.collectiblePhones", permission: permissionPhonesRead },
      { path: "/nft-gifts", labelKey: "nft.gifts", permission: permissionGiftsRead }
    ]
  },
  {
    id: "media",
    labelKey: "layout.media",
    icon: Smile,
    tabs: [
      { path: "/stickers", labelKey: "layout.stickers", permission: permissionContentRead },
      { path: "/emoji", labelKey: "layout.emoji", permission: permissionContentRead },
      { path: "/gif-catalog", labelKey: "layout.gifCatalog", permission: permissionContentRead },
      { path: "/storage", labelKey: "layout.storage", permission: permissionStorageRead }
    ]
  },
  {
    id: "broadcasts",
    labelKey: "layout.broadcasts",
    icon: Megaphone,
    tabs: [{ path: "/broadcasts", labelKey: "layout.broadcasts", permission: permissionBroadcastsRead }]
  },
  {
    id: "messages",
    labelKey: "layout.messages",
    icon: MessageSquareText,
    tabs: [
      { path: "/messages/private", labelKey: "layout.privateMessages", permission: permissionMessagesRead, aliases: ["/messages"] },
      { path: "/messages/groups", labelKey: "layout.groupMessages", permission: permissionMessagesRead }
    ]
  },
  {
    id: "moderation",
    labelKey: "layout.moderation",
    icon: ShieldAlert,
    tabs: [{ path: "/moderation", labelKey: "layout.moderation", permission: permissionModerationReview }]
  },
  {
    id: "donations",
    labelKey: "layout.donations",
    icon: Wallet,
    tabs: [{ path: "/donations", labelKey: "layout.donations", permission: permissionDonationsManage }]
  },
  {
    id: "audit",
    labelKey: "layout.auditLog",
    icon: ScrollText,
    tabs: [{ path: "/audit-log", labelKey: "layout.auditLog", permission: permissionAuditRead }]
  },
  {
    id: "operators",
    labelKey: "layout.adminUsers",
    icon: UserCog,
    tabs: [{ path: "/admin-users", labelKey: "layout.adminUsers", permission: permissionAdminsManage }]
  },
  {
    id: "server",
    labelKey: "layout.serverSettings",
    icon: Settings,
    tabs: [{ path: "/server-settings", labelKey: "layout.serverSettings", permission: permissionServerManage }]
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

// sectionActive answers "which sidebar row is lit", including detail routes
// that belong to a tab rather than being one: /accounts/42 lights Accounts,
// /account-ratings/42 lights it too, and /messages/groups/detail lights
// Messages. Prefix matching per tab is what makes a section with one tab
// (/channels) still light up on /channels/42.
export function sectionActive(section: NavSection, path: string, can: (permission: string) => boolean): boolean {
  return visibleTabs(section, can).some(
    (tab) =>
      path === tab.path ||
      path.startsWith(`${tab.path}/`) ||
      (tab.aliases ?? []).some((alias) => path === alias || path.startsWith(`${alias}/`))
  );
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
