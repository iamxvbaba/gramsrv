import { type Navigate, type RouteState } from "../routing";
import { AccountDetailPage } from "./AccountDetailPage";
import { AccountRatingDetailPage } from "./AccountRatingDetailPage";
import { AccountRatingsPage } from "./AccountRatingsPage";
import { AccountsPage } from "./AccountsPage";
import { CollectibleUsernameDetailPage } from "./CollectibleUsernameDetailPage";
import { CollectibleUsernamesPage } from "./CollectibleUsernamesPage";
import { CollectiblePhonesPage } from "./CollectiblePhonesPage";
import { NftGiftsPage } from "./NftGiftsPage";
import { ChannelDetailPage } from "./ChannelDetailPage";
import { ChannelsPage } from "./ChannelsPage";
import { BotDetailPage } from "./BotDetailPage";
import { BotsPage } from "./BotsPage";
import { BroadcastsPage } from "./BroadcastsPage";
import { EmojiPage } from "./EmojiPage";
import { GifCatalogPage } from "./GifCatalogPage";
import { Dashboard } from "./Dashboard";
import { GroupMessageDetailPage } from "./GroupMessageDetailPage";
import { MessageDetailPage } from "./MessageDetailPage";
import { MessagesPage } from "./MessagesPage";
import { AuctionsPage } from "./AuctionsPage";
import { DonationsPage } from "./DonationsPage";
import { PricesPage } from "./PricesPage";
import { GiftsPage } from "./GiftsPage";
import { GiveGiftsPage } from "./GiveGiftsPage";
import { ModerationCaseDetailPage } from "./ModerationCaseDetailPage";
import { ModerationCasesPage } from "./ModerationCasesPage";
import { PremiumPlansPage } from "./PremiumPlansPage";
import { BotVerificationPage } from "./BotVerificationPage";
import { BotVerificationRequestPage } from "./BotVerificationRequestPage";
import { VerificationDetailPage } from "./VerificationDetailPage";
import { VerificationPage } from "./VerificationPage";
import { SharedDevicesPage } from "./SharedDevicesPage";
import { StarIssuePage } from "./StarIssuePage";
import { StoragePage } from "./StoragePage";
import { StickerSetsPage } from "./StickerSetsPage";
import { StarsDetailPage } from "./StarsDetailPage";
import { StarsPage } from "./StarsPage";
import {
  PermissionGate,
  permissionAccountsRead,
  permissionAdminsManage,
  permissionAuditRead,
  permissionBotVerificationReview,
  permissionMessagesRead,
  permissionPremiumManage,
  permissionDonationsManage,
  permissionPricesManage,
  permissionServerManage,
  permissionStarsRead,
  permissionVerificationReview
} from "../permissions";
import { AdminUsersPage } from "./AdminUsersPage";
import { AuditLogPage } from "./AuditLogPage";
import { ServerSettingsPage } from "./ServerSettingsPage";

export function Routes({ route, navigate }: { route: RouteState; navigate: Navigate }) {
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
    return (
      <PermissionGate permission={permissionBotVerificationReview}>
        <BotVerificationRequestPage id={botVerificationRequestID} navigate={navigate} />
      </PermissionGate>
    );
  }
  if (route.path === "/bot-verification") {
    return (
      <PermissionGate permission={permissionBotVerificationReview}>
        <BotVerificationPage navigate={navigate} />
      </PermissionGate>
    );
  }
  // The detail match has to be tested before the exact "/verification" branch, and
  // the whole section is wrapped in the permission gate so a direct URL explains
  // itself instead of rendering an empty queue.
  if (verificationID) {
    return (
      <PermissionGate permission={permissionVerificationReview}>
        <VerificationDetailPage id={verificationID} navigate={navigate} />
      </PermissionGate>
    );
  }
  if (route.path === "/verification") {
    return (
      <PermissionGate permission={permissionVerificationReview}>
        <VerificationPage navigate={navigate} />
      </PermissionGate>
    );
  }
  if (route.path === "/admin-users") {
    return (
      <PermissionGate permission={permissionAdminsManage}>
        <AdminUsersPage />
      </PermissionGate>
    );
  }
  if (route.path === "/audit-log") {
    return (
      <PermissionGate permission={permissionAuditRead}>
        <AuditLogPage />
      </PermissionGate>
    );
  }
  if (route.path === "/donations") {
    return (
      <PermissionGate permission={permissionDonationsManage}>
        <DonationsPage />
      </PermissionGate>
    );
  }
  if (route.path === "/prices") {
    return (
      <PermissionGate permission={permissionPricesManage}>
        <PricesPage />
      </PermissionGate>
    );
  }
  if (route.path === "/server-settings") {
    return (
      <PermissionGate permission={permissionServerManage}>
        <ServerSettingsPage search={route.search} />
      </PermissionGate>
    );
  }
  const starsUserID = route.path.match(/^\/stars\/(\d+)$/)?.[1];
  if (starsUserID) {
    return (
      <PermissionGate permission={permissionStarsRead}>
        <StarsDetailPage userID={starsUserID} navigate={navigate} />
      </PermissionGate>
    );
  }
  if (route.path === "/stars") {
    return (
      <PermissionGate permission={permissionStarsRead}>
        <StarsPage navigate={navigate} />
      </PermissionGate>
    );
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
    return <StoragePage />;
  }
  if (route.path === "/monetization" || route.path === "/premium") {
    return (
      <PermissionGate permission={permissionPremiumManage}>
        <PremiumPlansPage navigate={navigate} />
      </PermissionGate>
    );
  }
  if (route.path === "/star-issue") {
    return (
      <PermissionGate permission={permissionPremiumManage}>
        <StarIssuePage navigate={navigate} />
      </PermissionGate>
    );
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
    return (
      <PermissionGate permission={permissionAccountsRead}>
        <SharedDevicesPage navigate={navigate} />
      </PermissionGate>
    );
  }
  if (route.path === "/channels") {
    return <ChannelsPage navigate={navigate} />;
  }
  if (route.path === "/bots") {
    return <BotsPage navigate={navigate} />;
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
      <PermissionGate permission={permissionMessagesRead}>
        <MessageDetailPage
          ownerUserID={Number(route.search.get("owner_user_id") || "0")}
          msgID={Number(route.search.get("msg_id") || "0")}
          navigate={navigate}
        />
      </PermissionGate>
    );
  }
  if (route.path === "/messages/groups/detail") {
    return (
      <PermissionGate permission={permissionMessagesRead}>
        <GroupMessageDetailPage
          channelID={Number(route.search.get("channel_id") || "0")}
          msgID={Number(route.search.get("msg_id") || "0")}
          navigate={navigate}
        />
      </PermissionGate>
    );
  }
  // Both tabs keep their own path so a link to one still opens on it -- the
  // tab is a view of /messages, not a hidden bit of component state.
  if (route.path === "/messages" || route.path === "/messages/private" || route.path === "/messages/groups") {
    return (
      <PermissionGate permission={permissionMessagesRead}>
        <MessagesPage
          navigate={navigate}
          tab={route.path === "/messages/groups" ? "groups" : "private"}
          onTab={(tab) => navigate(tab === "groups" ? "/messages/groups" : "/messages/private")}
        />
      </PermissionGate>
    );
  }
  return <Dashboard navigate={navigate} />;
}
