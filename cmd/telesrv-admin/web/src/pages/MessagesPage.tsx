import { History, Search, Trash2 } from "lucide-react";
import { useRef, useState } from "react";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { Avatar } from "../components/Avatar";
import { UserPicker } from "../components/EntityPicker";
import { MessagePreview, type PreviewMessage } from "../components/MessagePreview";
import { Alert, PageFrame, QueryPanel } from "../components/ui";
import { useI18n } from "../i18n";
import { parseIDs, toInt } from "../lib/format";
import { mediaFields } from "../lib/media";
import { loadPeerNames, peerLabel, userName, type PeerNames } from "../lib/peerNames";
import type { Navigate } from "../routing";
import type { AccountRow, LiveChatRow } from "../types";
import { GroupMessagesTab } from "./GroupMessagesPage";

const PAGE = 50;

type Cursor = { date: number; id: number };

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
    displayID: row.BoxID,
    date: row.Date,
    from: row.FromUserID,
    body: row.Body,
    outgoing: row.Outgoing,
    deleted: row.Deleted,
    media: Boolean(row.Media && row.Media !== "{}"),
    ...mediaFields(row.Media)
  }));
}

function groupPreview(rows: {
  ID: number;
  Date: number;
  SenderUserID: number;
  Body: string;
  Deleted: boolean;
  Media?: string;
  Post?: boolean;
}[]): PreviewMessage[] {
  return rows.map((row) => ({
    id: row.ID,
    displayID: row.ID,
    date: row.Date,
    from: row.SenderUserID,
    body: row.Body,
    deleted: row.Deleted,
    media: Boolean(row.Media && row.Media !== "{}"),
    ...mediaFields(row.Media),
    post: row.Post
  }));
}

function PrivateMessagesTab({ navigate }: { navigate: Navigate }) {
  const { t } = useI18n();
  const [owner, setOwner] = useState<AccountRow | null>(null);
  const [chats, setChats] = useState<LiveChatRow[]>([]);
  const [hasMoreChats, setHasMoreChats] = useState(false);
  const [loadingChats, setLoadingChats] = useState(false);
  const [active, setActive] = useState<LiveChatRow | null>(null);
  const [messages, setMessages] = useState<PreviewMessage[]>([]);
  const [oldest, setOldest] = useState<Cursor | null>(null);
  const [filter, setFilter] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [names, setNames] = useState<PeerNames | null>(null);
  const [ids, setIDs] = useState("");
  const [revoke, setRevoke] = useState(true);
  const [maxID, setMaxID] = useState("");
  const [maxBatches, setMaxBatches] = useState("1");
  const [justClear, setJustClear] = useState(false);
  // Guards against a slow response for chat A rendering under chat B.
  const requestRef = useRef(0);
  // Guards the dialog list the same way: a slow page for owner A must not
  // replace or extend owner B's list. Kept apart from requestRef so opening a
  // chat does not drop a pending "Load more chats".
  const listRef = useRef(0);

  function resolveNames(peers: LiveChatRow[], ownerID: number, senderIDs: number[]) {
    void loadPeerNames(
      [ownerID, ...peers.filter((c) => c.PeerType === "user").map((c) => c.PeerID), ...senderIDs],
      peers.filter((c) => c.PeerType !== "user").map((c) => c.PeerID)
    )
      .then((next) => setNames((prev) => ({ users: { ...(prev?.users ?? {}), ...next.users }, channels: { ...(prev?.channels ?? {}), ...next.channels } })))
      .catch(() => undefined);
  }

  async function chooseOwner(next: AccountRow | null) {
    requestRef.current++;
    const ticket = ++listRef.current;
    setOwner(next);
    setChats([]);
    setHasMoreChats(false);
    setLoadingChats(false);
    setActive(null);
    setMessages([]);
    setOldest(null);
    setNames(null);
    setError("");
    if (!next) return;
    setLoadingChats(true);
    try {
      const res = await api.liveChats(next.ID);
      if (ticket !== listRef.current) return;
      setChats(res.chats);
      setHasMoreChats(res.has_more);
      resolveNames(res.chats, next.ID, []);
    } catch (err) {
      if (ticket === listRef.current) setError(errorMessage(err));
    } finally {
      if (ticket === listRef.current) setLoadingChats(false);
    }
  }

  async function loadMoreChats() {
    if (!owner || chats.length === 0) return;
    const ticket = ++listRef.current;
    const ownerID = owner.ID;
    const last = chats[chats.length - 1];
    setLoadingChats(true);
    try {
      const res = await api.liveChats(ownerID, last.TopMessageDate, last.PeerID);
      if (ticket !== listRef.current) return;
      setChats((prev) => [...prev, ...res.chats]);
      setHasMoreChats(res.has_more);
      resolveNames(res.chats, ownerID, []);
    } catch (err) {
      if (ticket === listRef.current) setError(errorMessage(err));
    } finally {
      if (ticket === listRef.current) setLoadingChats(false);
    }
  }

  async function fetchConversation(chat: LiveChatRow, before: Cursor | null) {
    const ownerID = owner?.ID ?? 0;
    if (chat.PeerType === "channel" || chat.PeerType === "chat") {
      const params = new URLSearchParams({ channel_id: String(chat.PeerID), limit: String(PAGE) });
      if (before) {
        params.set("before_date", String(before.date));
        params.set("before_id", String(before.id));
      }
      const data = await api.groupMessages(params);
      return { rows: groupPreview(data.rows), senders: data.rows.map((r) => r.SenderUserID), cursor: data.rows.length ? { date: data.rows[data.rows.length - 1].Date, id: data.rows[data.rows.length - 1].ID } : null };
    }
    const params = new URLSearchParams({ owner_user_id: String(ownerID), peer_id: String(chat.PeerID), limit: String(PAGE) });
    if (before) {
      params.set("before_date", String(before.date));
      params.set("before_id", String(before.id));
    }
    const data = await api.messages(params);
    return { rows: privatePreview(data.rows), senders: data.rows.map((r) => r.FromUserID), cursor: data.rows.length ? { date: data.rows[data.rows.length - 1].Date, id: data.rows[data.rows.length - 1].BoxID } : null };
  }

  async function openChat(chat: LiveChatRow) {
    const ticket = ++requestRef.current;
    setActive(chat);
    setMessages([]);
    setOldest(null);
    setBusy(true);
    setError("");
    try {
      const result = await fetchConversation(chat, null);
      if (ticket !== requestRef.current) return;
      setMessages(result.rows);
      setOldest(result.rows.length >= PAGE ? result.cursor : null);
      resolveNames([chat], owner?.ID ?? 0, result.senders);
    } catch (err) {
      if (ticket === requestRef.current) setError(errorMessage(err));
    } finally {
      if (ticket === requestRef.current) setBusy(false);
    }
  }

  async function loadOlder() {
    if (!active || !oldest) return;
    const ticket = ++requestRef.current;
    const chat = active;
    setBusy(true);
    try {
      const result = await fetchConversation(chat, oldest);
      if (ticket !== requestRef.current) return;
      if (result.rows.length === 0) {
        setOldest(null);
        return;
      }
      // Rows are newest first (MessagePreview reverses them for display), so
      // the older page goes after the ones already shown.
      setMessages((prev) => [...prev, ...result.rows]);
      setOldest(result.rows.length >= PAGE ? result.cursor : null);
      resolveNames([chat], owner?.ID ?? 0, result.senders);
    } catch (err) {
      if (ticket === requestRef.current) setError(errorMessage(err));
    } finally {
      if (ticket === requestRef.current) setBusy(false);
    }
  }

  const filtered = chats.filter(
    (chat) => !filter.trim() || peerLabel(names, chat.PeerType, chat.PeerID).toLowerCase().includes(filter.trim().toLowerCase()) || String(chat.PeerID).includes(filter.trim())
  );

  const detailOf = (message: PreviewMessage): string | undefined => {
    if (!active || !owner) return undefined;
    if (active.PeerType === "channel" || active.PeerType === "chat") {
      return `/messages/groups/detail?channel_id=${active.PeerID}&msg_id=${message.id}`;
    }
    return `/messages/private/detail?owner_user_id=${owner.ID}&msg_id=${message.displayID}`;
  };

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
            <input value={filter} onChange={(event) => setFilter(event.target.value)} placeholder={t("messages.searchChats")} />
          </label>
          <div className="messenger-items">
            {filtered.length === 0 && <div className="messenger-placeholder">{owner ? t("messages.noChats") : t("messages.pickUserForChats")}</div>}
            {filtered.map((chat) => (
              <button
                key={`${chat.PeerType}-${chat.PeerID}`}
                type="button"
                className={`messenger-item${active?.PeerID === chat.PeerID && active?.PeerType === chat.PeerType ? " active" : ""}`}
                onClick={() => void openChat(chat)}
              >
                <Avatar id={Number(chat.PeerID)} kind={chat.PeerType === "user" ? "user" : "channel"} label={peerLabel(names, chat.PeerType, chat.PeerID)} size={38} />
                <span className="messenger-item-body">
                  <span className="messenger-item-top">
                    <strong>{peerLabel(names, chat.PeerType, chat.PeerID)}</strong>
                    {chat.UnreadCount ? <span className="messenger-unread">{chat.UnreadCount}</span> : null}
                  </span>
                  <span className="messenger-preview">{chat.TopMessageID ? `#${chat.TopMessageID}` : "—"}</span>
                </span>
              </button>
            ))}
            {hasMoreChats && (
              <button className="btn icon-text messenger-more" type="button" disabled={loadingChats} onClick={() => void loadMoreChats()}>
                {t("messages.loadMoreChats")}
              </button>
            )}
          </div>
        </aside>
        <section className="messenger-chat">
          {active ? (
            <>
              {oldest && (
                <button className="btn icon-text messenger-more" type="button" disabled={busy} onClick={() => void loadOlder()}>
                  {t("messages.loadOlder")}
                </button>
              )}
              <MessagePreview
                title={peerLabel(names, active.PeerType, active.PeerID)}
                emptyLabel={busy ? t("common.loading") : t("messages.noMessages")}
                messages={messages}
                nameOf={(from) => userName(names, Number(from))}
                avatar={<Avatar id={Number(active.PeerID)} kind={active.PeerType === "user" ? "user" : "channel"} label={peerLabel(names, active.PeerType, active.PeerID)} size={30} />}
                detailOf={detailOf}
                onOpenDetail={(path) => navigate(path)}
              />
            </>
          ) : (
            <div className="messenger-placeholder">{t("messages.selectChat")}</div>
          )}
        </section>
      </div>
      {owner && active && (
        <details className="section-block">
          <summary>{t("messages.moderationActions")}</summary>
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
              <input value={maxID} onChange={(event) => setMaxID(event.target.value)} placeholder={t("messages.maxIDPlaceholder")} />
              <input value={maxBatches} onChange={(event) => setMaxBatches(event.target.value)} placeholder={t("messages.maxBatchesPlaceholder")} />
              <label className="checkline"><input type="checkbox" checked={revoke} onChange={(event) => setRevoke(event.target.checked)} /> {t("messages.revoke")}</label>
              <label className="checkline"><input type="checkbox" checked={justClear} onChange={(event) => setJustClear(event.target.checked)} /> {t("messages.justClear")}</label>
              <ActionButton path="/api/actions/delete-history" label={t("messages.previewClearHistory")} payload={() => ({
                owner_user_id: owner.ID,
                peer_id: active.PeerID,
                max_id: toInt(maxID),
                max_batches: toInt(maxBatches),
                just_clear: justClear,
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
  void path;
  const { t } = useI18n();
  return (
    <PageFrame
      title={tab === "groups" ? t("messages.groupTitle") : t("messages.privateTitle")}
      eyebrow={tab === "groups" ? t("messages.groupEyebrow") : t("messages.privateEyebrow")}
    >
      {tab === "private" ? <PrivateMessagesTab navigate={navigate} /> : <GroupMessagesTab navigate={navigate} />}
    </PageFrame>
  );
}
