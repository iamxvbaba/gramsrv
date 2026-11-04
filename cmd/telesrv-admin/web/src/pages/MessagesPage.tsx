import { History, Search, Trash2 } from "lucide-react";
import { useState } from "react";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { Avatar } from "../components/Avatar";
import { UserPicker } from "../components/EntityPicker";
import { MessagePreview, type PreviewMessage } from "../components/MessagePreview";
import { Alert, PageFrame, QueryPanel } from "../components/ui";
import { useI18n } from "../i18n";
import { parseIDs, toInt } from "../lib/format";
import { mediaLocationKey } from "../lib/media";
import { loadPeerNames, peerLabel, userName, type PeerNames } from "../lib/peerNames";
import type { Navigate } from "../routing";
import type { AccountRow, LiveChatRow } from "../types";
import { GroupMessagesTab } from "./GroupMessagesPage";

function mediaKeyURL(media?: string): string | undefined {
  const key = mediaLocationKey(media);
  return key ? api.mediaURL(key) : undefined;
}

function privatePreview(rows: {
  OwnerUserID: number;
  BoxID: number;
  Date: number;
  FromUserID: number;
  Body: string;
  Outgoing: boolean;
  Deleted: boolean;
  Media?: string;
}[]): PreviewMessage[] {
  return rows.map((row) => ({
    id: `${row.OwnerUserID}-${row.BoxID}`,
    date: row.Date,
    from: row.FromUserID,
    body: row.Body,
    outgoing: row.Outgoing,
    deleted: row.Deleted,
    media: Boolean(row.Media && row.Media !== "{}"),
    mediaURL: mediaKeyURL(row.Media)
  }));
}

function PrivateMessagesTab({ navigate }: { navigate: Navigate }) {
  void navigate;
  const { t } = useI18n();
  const [owner, setOwner] = useState<AccountRow | null>(null);
  const [chats, setChats] = useState<LiveChatRow[]>([]);
  const [active, setActive] = useState<LiveChatRow | null>(null);
  const [messages, setMessages] = useState<PreviewMessage[]>([]);
  const [filter, setFilter] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [ids, setIDs] = useState("");
  const [revoke, setRevoke] = useState(true);
  const [names, setNames] = useState<PeerNames | null>(null);

  async function chooseOwner(next: AccountRow | null) {
    setOwner(next);
    setChats([]);
    setActive(null);
    setMessages([]);
    setNames(null);
    setError("");
    if (!next) return;
    setBusy(true);
    try {
      const list = (await api.liveChats(next.ID)).chats;
      setChats(list);
      void loadPeerNames(
        [next.ID, ...list.filter((c) => c.PeerType === "user").map((c) => c.PeerID)],
        list.filter((c) => c.PeerType !== "user").map((c) => c.PeerID)
      ).then(setNames).catch(() => undefined);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function openChat(chat: LiveChatRow) {
    setActive(chat);
    setBusy(true);
    setError("");
    try {
      if (chat.PeerType === "channel" || chat.PeerType === "chat") {
        const data = await api.groupMessages(new URLSearchParams({ channel_id: String(chat.PeerID), limit: "50" }));
        setMessages(data.rows.map((row) => ({
          id: row.ID,
          date: row.Date,
          from: row.SenderUserID,
          body: row.Body,
          deleted: row.Deleted,
          media: Boolean(row.Media && row.Media !== "{}"),
          mediaURL: mediaKeyURL(row.Media),
          post: row.Post
        })));
        void loadPeerNames(data.rows.map((row) => row.SenderUserID), []).then((next) => setNames((prev) => ({ users: { ...(prev?.users ?? {}), ...next.users }, channels: prev?.channels ?? {} }))).catch(() => undefined);
      } else {
        const data = await api.messages(new URLSearchParams({
          owner_user_id: String(owner?.ID ?? 0),
          peer_id: String(chat.PeerID),
          limit: "50"
        }));
        setMessages(privatePreview(data.rows));
        void loadPeerNames(data.rows.map((row) => row.FromUserID), []).then((next) => setNames((prev) => ({ users: { ...(prev?.users ?? {}), ...next.users }, channels: prev?.channels ?? {} }))).catch(() => undefined);
      }
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  const filtered = chats.filter((chat) => !filter.trim() || `${chat.PeerType} ${chat.PeerID}`.toLowerCase().includes(filter.trim().toLowerCase()));

  return (
    <>
      {error && <Alert>{error}</Alert>}
      <QueryPanel>
        <div className="message-selector-grid single">
          <UserPicker label={t("messages.ownerUser")} value={owner} onChange={chooseOwner} />
        </div>
      </QueryPanel>
      <div className="messenger">
        <aside className="messenger-list">
          <label className="searchbox messenger-search">
            <Search size={14} />
            <input value={filter} onChange={(event) => setFilter(event.target.value)} placeholder={"Search chats"} />
          </label>
          <div className="messenger-items">
            {filtered.length === 0 && (
              <div className="messenger-placeholder">
                {owner ? "No chats" : "Pick a user to list their chats."}
              </div>
            )}
            {filtered.map((chat) => (
              <button
                key={`${chat.PeerType}-${chat.PeerID}`}
                type="button"
                className={`messenger-item${active?.PeerID === chat.PeerID && active?.PeerType === chat.PeerType ? " active" : ""}`}
                onClick={() => void openChat(chat)}
              >
                <Avatar
                  id={Number(chat.PeerID)}
                  kind={chat.PeerType === "user" ? "user" : "channel"}
                  label={peerLabel(names, chat.PeerType, chat.PeerID)}
                  size={38}
                />
                <span className="messenger-item-body">
                  <span className="messenger-item-top">
                    <strong>{peerLabel(names, chat.PeerType, chat.PeerID)}</strong>
                    {chat.UnreadCount ? <span className="messenger-unread">{chat.UnreadCount}</span> : null}
                  </span>
                  <span className="messenger-preview">{chat.TopMessageID ? `#${chat.TopMessageID}` : "—"}</span>
                </span>
              </button>
            ))}
          </div>
        </aside>
        <section className="messenger-chat">
          {active ? (
            <MessagePreview
              title={peerLabel(names, active.PeerType, active.PeerID)}
              emptyLabel={busy ? "Loading…" : "No messages"}
              messages={messages}
              nameOf={(from) => userName(names, Number(from))}
              avatar={
                <Avatar
                  id={Number(active.PeerID)}
                  kind={active.PeerType === "user" ? "user" : "channel"}
                  label={peerLabel(names, active.PeerType, active.PeerID)}
                  size={30}
                />
              }
            />
          ) : (
            <div className="messenger-placeholder">{"Select a chat to read it."}</div>
          )}
        </section>
      </div>
      {owner && active && (
        <details className="section-block">
          <summary>{"Moderation actions"}</summary>
          <div className="operation-row">
            <div className="operation-box">
              <div className="operation-title"><Trash2 size={15} /> {t("messages.deleteSelected")}</div>
              <input value={ids} onChange={(event) => setIDs(event.target.value)} placeholder={t("messages.idsPlaceholder")} />
              <label className="checkline"><input type="checkbox" checked={revoke} onChange={(event) => setRevoke(event.target.checked)} /> {t("messages.revoke")}</label>
              <ActionButton path="/api/actions/delete-messages" label={t("messages.previewDelete")} payload={() => ({
                owner_user_id: owner.ID,
                peer_id: active.PeerID,
                ids: parseIDs(ids, t("messages.msgIDsInvalid")),
                revoke
              })} />
            </div>
            <div className="operation-box">
              <div className="operation-title"><History size={15} /> {t("messages.clearHistory")}</div>
              <ActionButton path="/api/actions/delete-history" label={t("messages.previewClearHistory")} payload={() => ({
                owner_user_id: owner.ID,
                peer_id: active.PeerID,
                max_id: toInt(""),
                max_batches: 1,
                just_clear: false,
                revoke
              })} />
            </div>
          </div>
        </details>
      )}
    </>
  );
}

// The two message stores are one screen with two tabs rather than two sidebar
// entries: they are the same job ("look at what was said") over different peer
// kinds.
export function MessagesPage({ navigate, tab, path }: {
  navigate: Navigate;
  tab: "private" | "groups";
  path: string;
}) {
  const { t } = useI18n();
  void path;
  return (
    <PageFrame title={tab === "groups" ? t("messages.groupTitle") : t("messages.privateTitle")}>
      {tab === "private" ? <PrivateMessagesTab navigate={navigate} /> : <GroupMessagesTab navigate={navigate} />}
    </PageFrame>
  );
}
