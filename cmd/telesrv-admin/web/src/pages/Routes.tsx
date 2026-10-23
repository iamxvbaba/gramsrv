import { AutoSubscribeChannelsPage } from "./AutoSubscribeChannelsPage";
import { routePermission } from "../nav";
import { PermissionDenied, usePermissions } from "../permissions";
import { type Navigate, type RouteState } from "../routing";
import { AccountDetailPage } from "./AccountDetailPage";
import { AccountRatingDetailPage } from "./AccountRatingDetailPage";
import { AccountRatingsPage } from "./AccountRatingsPage";
import { AccountsPage } from "./AccountsPage";
import { AdminUsersPage } from "./AdminUsersPage";
import { AuditLogPage } from "./AuditLogPage";
import { AuctionsPage } from "./AuctionsPage";
import { BotDetailPage } from "./BotDetailPage";
import { BotsPage } from "./BotsPage";
import { BotVerificationPage } from "./BotVerificationPage";
import { BotVerificationRequestPage } from "./BotVerificationRequestPage";
import { BroadcastsPage } from "./BroadcastsPage";
import { ChannelDetailPage } from "./ChannelDetailPage";
import { ChannelsPage } from "./ChannelsPage";
import { CollectiblePhonesPage } from "./CollectiblePhonesPage";
import { CollectibleUsernameDetailPage } from "./CollectibleUsernameDetailPage";
import { CollectibleUsernamesPage } from "./CollectibleUsernamesPage";
import { Dashboard } from "./Dashboard";
import { DonationsPage } from "./DonationsPage";
import { EmojiPage } from "./EmojiPage";
import { GifCatalogPage } from "./GifCatalogPage";
import { GiftsPage } from "./GiftsPage";
import { GiveGiftsPage } from "./GiveGiftsPage";
import { GroupMessageDetailPage } from "./GroupMessageDetailPage";
import { MessageDetailPage } from "./MessageDetailPage";
import { MessagesPage } from "./MessagesPage";
import { ModerationCaseDetailPage } from "./ModerationCaseDetailPage";
import { ModerationCasesPage } from "./ModerationCasesPage";
import { NftGiftsPage } from "./NftGiftsPage";
import { PremiumPlansPage } from "./PremiumPlansPage";
import { PricesPage } from "./PricesPage";
import { BotShopPage } from "./BotShopPage";
import { ServerSettingsPage } from "./ServerSettingsPage";
import { SharedDevicesPage } from "./SharedDevicesPage";
import { StarsDetailPage } from "./StarsDetailPage";
import { StarsPage } from "./StarsPage";
import { StickerSetsPage } from "./StickerSetsPage";
import { StoragePage } from "./StoragePage";
import { VerificationDetailPage } from "./VerificationDetailPage";
import { VerificationPage } from "./VerificationPage";

export function Routes({ route, navigate }: { route: RouteState; navigate: Navigate }) {
  // One gate for the whole surface, decided by the nav table, instead of a
  // PermissionGate around each of the forty branches below. Those wrappers were
  // added one at a time and a dozen sections never got one, so a session
  // without accounts.read still saw Accounts in the sidebar and then got a bare
  // 403 from every endpoint on the page instead of the explanation
  // PermissionDenied exists to give. Deriving the answer from the same table
  // the sidebar renders makes the two impossible to disagree.
  const required = routePermission(route.path);
  const { can } = usePermissions();
  if (required && !can(required)) {
    return <PermissionDenied permission={required} />;
  }
  return <RouteView route={route} navigate={navigate} />;
}

function RouteView({ route, navigate }: { route: RouteState; navigate: Navigate }) {
  const accountID = route.path.match(/^\/accounts\/(\d+)$/)?.[1];
  const channelID = route.path.match(/^\/channels\/(\d+)$/)?.[1];
  const botID = route.path.match(/^\/bots\/(\d+)$/)?.[1];
  const moderationCaseID = route.path.match(/^\/moderation\/(\d+)$/)?.[1];
  // int64 ids stay strings so large values never lose precision.
  const collectibleUsernameID = route.path.match(/^\/collectible-usernames\/(\d+)$/)?.[1];
  const ratingUserID = route.path.match(/^\/account-ratings\/(\d+)$/)?.[1];
  const verificationID = route.path.match(/^\/verification\/(\d+)$/)?.[1];
  // Third-party verification: a separate section with its own rights, matched before
  // the official one so neither prefix can shadow the other.
  const botVerificationRequestID = route.path.match(/^\/bot-verification\/(\d+)$/)?.[1];
  if (botVerificationRequestID) {
    return <BotVerificationRequestPage id={botVerificationRequestID} navigate={navigate} />;
  }
  if (route.path === "/bot-verification") {
    return <BotVerificationPage navigate={navigate} />;
  }
  // The detail match has to be tested before the exact "/verification" branch.
  if (verificationID) {
    return <VerificationDetailPage id={verificationID} navigate={navigate} />;
  }
  if (route.path === "/verification") {
    return <VerificationPage navigate={navigate} />;
  }
  if (route.path === "/admin-users") {
    return <AdminUsersPage />;
  }
  if (route.path === "/audit-log") {
    return <AuditLogPage />;
  }
  if (route.path === "/donations") {
    return <DonationsPage navigate={navigate} />;
  }
  if (route.path === "/bot-shop") {
    return <BotShopPage navigate={navigate} />;
  }
  if (route.path === "/prices") {
    return <PricesPage navigate={navigate} />;
  }
  if (route.path === "/server-settings") {
    return <ServerSettingsPage search={route.search} />;
  }
  const starsUserID = route.path.match(/^\/stars\/(\d+)$/)?.[1];
  if (starsUserID) {
    return <StarsDetailPage userID={starsUserID} navigate={navigate} />;
  }
  if (route.path === "/stars") {
    return <StarsPage navigate={navigate} />;
  }
  if (collectibleUsernameID) {
    return <CollectibleUsernameDetailPage id={collectibleUsernameID} navigate={navigate} />;
  }
  if (ratingUserID) {
    return <AccountRatingDetailPage userID={ratingUserID} navigate={navigate} />;
  }
  if (route.path === "/collectible-usernames") {
    return <CollectibleUsernamesPage navigate={navigate} />;
  }
  if (route.path === "/collectible-phones") {
    return <CollectiblePhonesPage navigate={navigate} />;
  }
  if (route.path === "/nft-gifts") {
    return <NftGiftsPage navigate={navigate} />;
  }
  if (route.path === "/account-ratings") {
    return <AccountRatingsPage navigate={navigate} />;
  }
  if (route.path === "/storage") {
    return <StoragePage navigate={navigate} />;
  }
  if (route.path === "/monetization" || route.path === "/premium" || route.path === "/star-issue") {
    // "/star-issue" used to be a page of its own holding a single button. That
    // button lives on the Stars & Premium page now, so the old URL opens the
    // same place instead of falling through to the dashboard.
    return <PremiumPlansPage navigate={navigate} />;
  }
  if (accountID) {
    return <AccountDetailPage id={Number(accountID)} navigate={navigate} />;
  }
  if (channelID) {
    return <ChannelDetailPage id={Number(channelID)} navigate={navigate} />;
  }
  if (botID) {
    return <BotDetailPage id={Number(botID)} navigate={navigate} />;
  }
  if (moderationCaseID) {
    return <ModerationCaseDetailPage id={Number(moderationCaseID)} navigate={navigate} />;
  }
  if (route.path === "/accounts") {
    return <AccountsPage navigate={navigate} />;
  }
  if (route.path === "/accounts/shared-devices") {
    return <SharedDevicesPage navigate={navigate} />;
  }
  if (route.path === "/channels") {
    return <ChannelsPage navigate={navigate} />;
  }
  if (route.path === "/bots") {
    return <BotsPage navigate={navigate} />;
  }
  if (route.path === "/auto-subscribe-channels") {
    return <AutoSubscribeChannelsPage />;
  }
  if (route.path === "/broadcasts") {
    return <BroadcastsPage />;
  }
  if (route.path === "/moderation") {
    return <ModerationCasesPage navigate={navigate} />;
  }
  if (route.path === "/emoji/documents") {
    return <EmojiPage navigate={navigate} />;
  }
  if (route.path === "/emoji") {
    return <StickerSetsPage kind="emoji" navigate={navigate} />;
  }
  if (route.path === "/stickers") {
    return <StickerSetsPage kind="stickers" navigate={navigate} />;
  }
  if (route.path === "/gif-catalog") {
    return <GifCatalogPage navigate={navigate} />;
  }
  if (route.path === "/gifts") {
    return <GiftsPage navigate={navigate} />;
  }
  if (route.path === "/give-gifts") {
    return <GiveGiftsPage navigate={navigate} />;
  }
  if (route.path === "/auctions") {
    return <AuctionsPage navigate={navigate} />;
  }
  if (route.path === "/messages/detail" || route.path === "/messages/private/detail") {
    return (
      <MessageDetailPage
        ownerUserID={Number(route.search.get("owner_user_id") || "0")}
        msgID={Number(route.search.get("msg_id") || "0")}
        navigate={navigate}
      />
    );
  }
  if (route.path === "/messages/groups/detail") {
    return (
      <GroupMessageDetailPage
        channelID={Number(route.search.get("channel_id") || "0")}
        msgID={Number(route.search.get("msg_id") || "0")}
        navigate={navigate}
      />
    );
  }
  // Both tabs keep their own path so a link to one still opens on it -- the
  // tab is a view of /messages, not a hidden bit of component state.
  if (route.path === "/messages" || route.path === "/messages/private" || route.path === "/messages/groups") {
    return (
      <MessagesPage
        navigate={navigate}
        tab={route.path === "/messages/groups" ? "groups" : "private"}
        path={route.path}
      />
    );
  }
  // "/statistics" used to be its own page; it is now folded into the overview,
  // so the old URL still lands on the overview rather than 404ing on a bookmark.
  return <Dashboard navigate={navigate} />;
}