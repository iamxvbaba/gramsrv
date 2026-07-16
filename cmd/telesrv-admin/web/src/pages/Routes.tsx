import { type Navigate, type RouteState } from "../routing";
import { AccountDetailPage } from "./AccountDetailPage";
import { AccountsPage } from "./AccountsPage";
import { ChannelDetailPage } from "./ChannelDetailPage";
import { ChannelsPage } from "./ChannelsPage";
import { Dashboard } from "./Dashboard";
import { GroupMessageDetailPage } from "./GroupMessageDetailPage";
import { GroupMessagesPage } from "./GroupMessagesPage";
import { MessageDetailPage } from "./MessageDetailPage";
import { MessagesPage } from "./MessagesPage";
import { GiftsPage } from "./GiftsPage";

export function Routes({ route, navigate }: { route: RouteState; navigate: Navigate }) {
  const accountID = route.path.match(/^\/accounts\/(\d+)$/)?.[1];
  const channelID = route.path.match(/^\/channels\/(\d+)$/)?.[1];
  if (accountID) {
    return <AccountDetailPage id={Number(accountID)} navigate={navigate} />;
  }
  if (channelID) {
    return <ChannelDetailPage id={Number(channelID)} navigate={navigate} />;
  }
  if (route.path === "/accounts") {
    return <AccountsPage navigate={navigate} />;
  }
  if (route.path === "/channels") {
    return <ChannelsPage navigate={navigate} />;
  }
	if (route.path === "/gifts") {
		return <GiftsPage />;
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
