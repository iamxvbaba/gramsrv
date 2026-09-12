import { type Navigate, type RouteState } from "../routing";
import { AccountDetailPage } from "./AccountDetailPage";
import { AccountRatingDetailPage } from "./AccountRatingDetailPage";
import { AccountRatingsPage } from "./AccountRatingsPage";
import { AccountsPage } from "./AccountsPage";
import { AutoSubscribeChannelsPage } from "./AutoSubscribeChannelsPage";
import { CollectibleUsernameDetailPage } from "./CollectibleUsernameDetailPage";
import { CollectibleUsernamesPage } from "./CollectibleUsernamesPage";
import { CollectiblePhonesPage } from "./CollectiblePhonesPage";
import { ChannelDetailPage } from "./ChannelDetailPage";
import { ChannelsPage } from "./ChannelsPage";
import { BotDetailPage } from "./BotDetailPage";
import { BotsPage } from "./BotsPage";
import { BroadcastsPage } from "./BroadcastsPage";
import { EmojiPage } from "./EmojiPage";
import { GifCatalogPage } from "./GifCatalogPage";
import { Dashboard } from "./Dashboard";
import { GroupMessageDetailPage } from "./GroupMessageDetailPage";
import { GroupMessagesPage } from "./GroupMessagesPage";
import { MessageDetailPage } from "./MessageDetailPage";
import { MessagesPage } from "./MessagesPage";
import { AuctionsPage } from "./AuctionsPage";
import { GiftsPage } from "./GiftsPage";
import { GiveGiftsPage } from "./GiveGiftsPage";
import { ModerationCaseDetailPage } from "./ModerationCaseDetailPage";
import { ModerationCasesPage } from "./ModerationCasesPage";
import { PremiumPlansPage } from "./PremiumPlansPage";
import { BotVerificationPage } from "./BotVerificationPage";
import { BotVerificationRequestPage } from "./BotVerificationRequestPage";
import { VerificationDetailPage } from "./VerificationDetailPage";
import { VerificationPage } from "./VerificationPage";
import { StoragePage } from "./StoragePage";
import { StickerSetsPage } from "./StickerSetsPage";
import {
  PermissionGate,
  permissionBotVerificationReview,
  permissionPremiumManage,
  permissionVerificationReview
} from "../permissions";

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
    return <CollectiblePhonesPage />;
  }
  if (route.path === "/account-ratings") {
    return <AccountRatingsPage navigate={navigate} />;
  }
  if (route.path === "/auto-subscribe-channels") {
    return <AutoSubscribeChannelsPage />;
  }
  if (route.path === "/storage") {
    return <StoragePage />;
  }
  if (route.path === "/monetization" || route.path === "/premium") {
    return (
      <PermissionGate permission={permissionPremiumManage}>
        <PremiumPlansPage />
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
		return <GifCatalogPage />;
	}
	if (route.path === "/gifts") {
		return <GiftsPage />;
	}
	if (route.path === "/give-gifts") {
		return <GiveGiftsPage />;
	}
	if (route.path === "/auctions") {
		return <AuctionsPage />;
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
  if (route.path === "/messages/groups") {
    return <GroupMessagesPage navigate={navigate} />;
  }
  if (route.path === "/messages" || route.path === "/messages/private") {
    return <MessagesPage navigate={navigate} />;
  }
  return <Dashboard navigate={navigate} />;
}
