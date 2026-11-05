import { api } from "../api";

// Peer name resolution for the review screens: the backend returns display
// names for users and channels by id, so the console shows "Alice" / "News"
// instead of raw ids.

export type PeerNames = {
  users: Record<number, string>;
  channels: Record<number, string>;
};

export async function loadPeerNames(userIDs: number[], channelIDs: number[]): Promise<PeerNames> {
  const uniqueUsers = [...new Set(userIDs.filter((id) => id > 0))];
  const uniqueChannels = [...new Set(channelIDs.filter((id) => id > 0))];
  if (uniqueUsers.length === 0 && uniqueChannels.length === 0) {
    return { users: {}, channels: {} };
  }
  const res = await api.livePeers(uniqueUsers, uniqueChannels);
  const users: Record<number, string> = {};
  for (const [key, value] of Object.entries(res.users)) {
    users[Number(key)] = value;
  }
  const channels: Record<number, string> = {};
  for (const [key, value] of Object.entries(res.channels)) {
    channels[Number(key)] = value;
  }
  return { users, channels };
}

export function peerLabel(names: PeerNames | null, type: string, id: number): string {
  if (names) {
    if (type === "channel" || type === "chat") {
      const title = names.channels[id];
      if (title) return title;
    } else {
      const name = names.users[id];
      if (name) return name;
    }
  }
  return `${type} ${id}`;
}

export function userName(names: PeerNames | null, id: number): string {
  const name = names?.users[id];
  return name || String(id);
}
