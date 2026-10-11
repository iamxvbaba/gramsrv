import { ShieldOff } from "lucide-react";
import { createContext, useContext, useMemo, type ReactNode } from "react";
import { Alert, PageFrame } from "./components/ui";
import { useI18n } from "./i18n";

// Permission names exactly as the backend spells them
// (cmd/telesrv-admin/security.go). "*" is the wildcard an operator configures for
// a full-access session.
export const permissionAll = "*";
export const permissionPremiumManage = "premium.manage";
export const permissionDonationsManage = "donations.manage";
// Shop pricing: effective product prices, product kill switches and the stars
// rate. Separate from premium.manage — this is what a customer is charged.
export const permissionPricesManage = "prices.manage";
export const permissionBotTokenRead = "bots.token.read";
export const permissionVerificationReview = "verification.review";
export const permissionVerificationRevoke = "verification.revoke";
// Third-party verification is a separate mechanism and therefore a separate pair of
// rights: review reads the section and decides applications, manage owns the
// verifier roster, the icon catalogue and taking a granted mark away.
export const permissionBotVerificationReview = "botverification.review";
export const permissionBotVerificationManage = "botverification.manage";
// Operator accounts. The one right that can hand out every other right, so it
// is never implied by anything else.
export const permissionAdminsManage = "admins.manage";
// The global action log. Supervisory, so it gets its own right rather than
// riding on the sections it covers.
export const permissionAuditRead = "audit.read";
// Section rights, in read/manage pairs following the sidebar -- see the const
// block in security.go, which these must match exactly.
export const permissionAccountsRead = "accounts.read";
export const permissionAccountsManage = "accounts.manage";
export const permissionChannelsRead = "channels.read";
export const permissionChannelsManage = "channels.manage";
export const permissionBotsRead = "bots.read";
export const permissionBotsManage = "bots.manage";
export const permissionMessagesRead = "messages.read";
export const permissionMessagesManage = "messages.manage";
export const permissionModerationReview = "moderation.review";
export const permissionBroadcastsRead = "broadcasts.read";
export const permissionBroadcastsSend = "broadcasts.send";
export const permissionStorageRead = "storage.read";
export const permissionStorageManage = "storage.manage";
export const permissionContentRead = "content.read";
export const permissionContentManage = "content.manage";
export const permissionUsernamesRead = "usernames.read";
export const permissionUsernamesManage = "usernames.manage";
export const permissionPhonesRead = "phones.read";
export const permissionPhonesManage = "phones.manage";
export const permissionGiftsRead = "gifts.read";
export const permissionGiftsManage = "gifts.manage";
export const permissionRatingsRead = "ratings.read";
export const permissionRatingsManage = "ratings.manage";
export const permissionStarsRead = "stars.read";
export const permissionDashboardRead = "dashboard.read";
// The Server Settings console section (identity, login-notification templates,
// .env editing, service status). One right for the whole surface rather than
// split read/manage pairs: it only ever edits this machine's own config files,
// so there is nothing to hand an operator read-only that they can't also see in
// the repo/code, and the actions are deliberately small and audited by reason.
// It is not implied by anything else, like the console-self rights below.
export const permissionServerManage = "server.manage";

// GET /api/session is read once at boot; the panel keeps the answer here so a
// section the session may not use is hidden instead of rendered into a 403. This
// is a convenience for the operator, not a security boundary: every route is
// checked again server-side.
const PermissionsContext = createContext<readonly string[]>([]);

export function PermissionsProvider({
  permissions,
  children
}: {
  permissions: readonly string[];
  children: ReactNode;
}) {
  return <PermissionsContext.Provider value={permissions}>{children}</PermissionsContext.Provider>;
}

export function usePermissions(): { permissions: readonly string[]; can: (permission: string) => boolean } {
  const permissions = useContext(PermissionsContext);
  return useMemo(
    () => ({
      permissions,
      can: (permission: string) => permissions.includes(permissionAll) || permissions.includes(permission)
    }),
    [permissions]
  );
}

export function useCan(permission: string): boolean {
  return usePermissions().can(permission);
}

// PermissionGate is what a direct URL hits: without the right the operator gets
// an explanation naming the missing permission, not an empty table that looks
// like "no data".
export function PermissionGate({ permission, children }: { permission: string; children: ReactNode }) {
  const { can } = usePermissions();
  if (can(permission)) {
    return <>{children}</>;
  }
  return <PermissionDenied permission={permission} />;
}

export function PermissionDenied({ permission }: { permission: string }) {
  const { t } = useI18n();
  return (
    <PageFrame title={t("permission.deniedTitle")} eyebrow={t("permission.deniedEyebrow")}>
      <Alert>{t("permission.deniedBody", { permission })}</Alert>
      <section className="section-block">
        <div className="entity-head">
          <div>
            <div className="entity-title"><ShieldOff size={16} /> {t("permission.deniedHeading")}</div>
            <div className="entity-subtitle">
              {permissionTitle(permission) !== permission
                ? t("permission.deniedHintNamed", { title: permissionTitle(permission), raw: permission })
                : t("permission.deniedHint")}
            </div>
          </div>
        </div>
      </section>
    </PageFrame>
  );
}

// Human-readable names for the permission strings. The raw value is what the
// backend stores and checks, but "content.manage" is a machine's word for it --
// an operator ticking boxes should read what the right actually lets someone do.
//
// Anything missing from this map falls back to the raw string rather than being
// hidden, so a right added on the server still appears (just untranslated)
// instead of silently vanishing from the editor.
const permissionLabels: Record<string, { title: string; hint: string }> = {
  "accounts.read": { title: "View accounts", hint: "Browse users, their profiles and sessions" },
  "accounts.manage": { title: "Edit accounts", hint: "Change profiles, usernames, freeze and revoke sessions" },
  "channels.read": { title: "View groups and channels", hint: "Browse supergroups and channels" },
  "channels.manage": { title: "Edit groups and channels", hint: "Change settings, usernames and avatars" },
  "bots.read": { title: "View bots", hint: "Browse the bot list and their details" },
  "bots.manage": { title: "Create and delete bots", hint: "Add new bots and remove existing ones" },
  "bots.token.read": { title: "Reveal bot tokens", hint: "Export a bot's live credential" },
  "messages.read": { title: "View messages", hint: "Read private and group message history" },
  "messages.manage": { title: "Delete messages", hint: "Remove messages and clear history" },
  "moderation.review": { title: "Handle reports", hint: "Work the moderation queue and decide cases" },
  "broadcasts.read": { title: "View broadcasts", hint: "See past and scheduled broadcasts" },
  "broadcasts.send": { title: "Send broadcasts", hint: "Deliver a message to many users at once" },
  "content.read": { title: "View stickers, emoji and GIFs", hint: "Browse the packs and the GIF catalogue" },
  "content.manage": { title: "Edit stickers, emoji and GIFs", hint: "Create, rename and remove packs and catalogue entries" },
  "usernames.read": { title: "View NFT usernames", hint: "Browse collectible usernames" },
  "usernames.manage": { title: "Manage NFT usernames", hint: "Mint, transfer and revoke collectible usernames" },
  "phones.read": { title: "View anonymous numbers", hint: "Browse the +888 number registry" },
  "phones.manage": { title: "Manage anonymous numbers", hint: "Issue, transfer and burn +888 numbers" },
  "gifts.read": { title: "View the gift catalogue", hint: "Browse star gifts, auctions and official gifts" },
  "gifts.manage": { title: "Edit the gift catalogue", hint: "Import gifts, publish collectibles and give gifts" },
  "ratings.read": { title: "View account ratings", hint: "Browse the account-rating leaderboard" },
  "ratings.manage": { title: "Adjust account ratings", hint: "Recompute ratings and apply manual corrections" },
  "stars.read": { title: "View the Stars ledger", hint: "Browse Stars balances and signed per-account history" },
  "storage.read": { title: "View storage", hint: "See media usage per account" },
  "storage.manage": { title: "Purge storage", hint: "Manually delete stored media" },
  "dashboard.read": { title: "View the dashboard", hint: "See the overview counters and server health" },
  "premium.manage": { title: "Manage Premium", hint: "Grant, revoke and refund Premium" },
  "prices.manage": { title: "Manage shop prices", hint: "Edit product prices, hide products and set the Stars rate" },
  "verification.review": { title: "Verify accounts", hint: "Work the verification queue and grant badges" },
  "verification.revoke": { title: "Remove verification", hint: "Take a granted badge away (needs the right above too)" },
  "botverification.review": { title: "Handle third-party marks", hint: "Work the third-party verification queue" },
  "botverification.manage": { title: "Appoint verifiers", hint: "Grant verifier status and curate mark icons" },
  "admins.manage": { title: "Manage operators", hint: "Create operators and decide what everyone can do" },
  "audit.read": { title: "View the audit log", hint: "See who did what across the console" },
  "server.manage": { title: "Manage server settings", hint: "Edit the server identity, login-notification templates and .env, and view service status" },
  "*": { title: "Full access", hint: "Every right, including future ones" }
};

export function permissionTitle(permission: string): string {
  return permissionLabels[permission]?.title ?? permission;
}

export function permissionHint(permission: string): string {
  return permissionLabels[permission]?.hint ?? "";
}

// Rights grouped by the part of the console they govern, so the editor reads as
// a few short decisions instead of one wall of checkboxes.
//
// The order is roughly "everyday work first, keys to the building last": an
// operator scanning down the list meets the routine rights before the ones that
// can undo the deployment.
export const permissionGroups: { title: string; hint: string; permissions: string[] }[] = [
  {
    title: "People and chats",
    hint: "Users, groups and their message history",
    permissions: ["accounts.read", "accounts.manage", "channels.read", "channels.manage", "messages.read", "messages.manage"]
  },
  {
    title: "Moderation and verification",
    hint: "Reports, badges and third-party marks",
    permissions: ["moderation.review", "verification.review", "verification.revoke", "botverification.review", "botverification.manage"]
  },
  {
    title: "Content",
    hint: "Sticker packs, emoji, GIFs and collectibles",
    permissions: ["content.read", "content.manage", "usernames.read", "usernames.manage", "phones.read", "phones.manage"]
  },
  {
    title: "Bots",
    hint: "The bot roster and its credentials",
    permissions: ["bots.read", "bots.manage", "bots.token.read"]
  },
  {
    title: "Broadcasting",
    hint: "Messages sent to many users at once",
    permissions: ["broadcasts.read", "broadcasts.send"]
  },
  {
    title: "Gifts and ratings",
    hint: "Catalogues, give-gifts and the rating leaderboard",
    permissions: ["gifts.read", "gifts.manage", "ratings.read", "ratings.manage"]
  },
  {
    title: "Storage and overview",
    hint: "Media usage and the dashboard",
    permissions: ["storage.read", "storage.manage", "dashboard.read"]
  },
  {
    title: "Billing",
    hint: "Premium grants, refunds, the Stars ledger and shop pricing",
    permissions: ["premium.manage", "stars.read", "prices.manage"]
  },
  {
    title: "The console itself",
    hint: "The rights that can hand out every other right or review the trail",
    permissions: ["admins.manage", "audit.read", "server.manage"]
  }
];

// groupPermissions arranges the server's list into the groups above. Anything
// the server offers that no group claims is collected at the end rather than
// dropped, so a right added on the backend still appears here without this file
// having to be edited first.
export function groupPermissions(available: string[]): { title: string; hint: string; permissions: string[] }[] {
  const remaining = new Set(available);
  const out: { title: string; hint: string; permissions: string[] }[] = [];
  for (const group of permissionGroups) {
    const present = group.permissions.filter((p) => remaining.has(p));
    present.forEach((p) => remaining.delete(p));
    if (present.length > 0) {
      out.push({ title: group.title, hint: group.hint, permissions: present });
    }
  }
  if (remaining.size > 0) {
    out.push({ title: "Other", hint: "Rights this console version does not have a group for", permissions: [...remaining] });
  }
  return out;
}
