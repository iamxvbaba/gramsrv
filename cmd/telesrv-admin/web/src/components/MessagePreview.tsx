import { useEffect, useRef, useState, type ReactNode } from "react";
import { useI18n } from "../i18n";
import { formatUnix } from "../lib/format";

// MessagePreview renders a conversation as chat bubbles -- the "live preview"
// an operator wants when reviewing what was said, next to the raw table.
export type PreviewMessage = {
  // id is unique across the list (React key, focus); a private message's is
  // "<owner>-<box>". displayID is what the operator sees and can paste into
  // "Delete selected": the box id for a private message, the message id for a
  // group one.
  id: string | number;
  displayID: string | number;
  date: number;
  from: number | string;
  body: string;
  outgoing?: boolean;
  deleted?: boolean;
  media?: boolean;
  mediaURL?: string;
  mediaKind?: "photo" | "document";
  post?: boolean;
};

export function MessagePreview({
  messages,
  title,
  emptyLabel,
  nameOf,
  avatar,
  focusID,
  detailOf,
  onOpenDetail
}: {
  messages: PreviewMessage[];
  title: string;
  emptyLabel: string;
  nameOf?: (from: number | string) => string;
  avatar?: ReactNode;
  focusID?: string | number;
  detailOf?: (message: PreviewMessage) => string | undefined;
  onOpenDetail?: (path: string) => void;
}) {
  const { t } = useI18n();
  const [zoom, setZoom] = useState<string | null>(null);
  // A detail page's preview ends at the focused message, so it is the last row
  // (or would be, if it was deleted from a group): keep the list scrolled to the
  // end, also while lazy photos load, until the operator scrolls it.
  const listRef = useRef<HTMLDivElement>(null);
  const userScrolled = useRef(false);
  const followFocus = () => {
    const list = listRef.current;
    if (focusID === undefined || !list || userScrolled.current) return;
    list.scrollTop = list.scrollHeight;
  };
  useEffect(() => {
    userScrolled.current = false;
    followFocus();
  }, [messages, focusID]);
  const stopFollowing = () => {
    userScrolled.current = true;
  };
  return (
    <section className="msg-preview">
      <div className="msg-preview-head">
        {avatar}
        <span>{title}</span>
      </div>
      <div className="msg-preview-list" ref={listRef} onWheel={stopFollowing} onTouchMove={stopFollowing} onKeyDown={stopFollowing}>
        {messages.length === 0 ? (
          <div className="msg-preview-empty">{emptyLabel}</div>
        ) : (
          messages
            .slice()
            .reverse()
            .map((message) => (
              <div
                key={message.id}
                className={`msg-preview-row${message.outgoing ? " out" : ""}${message.post ? " post" : ""}${focusID !== undefined && String(message.id) === String(focusID) ? " focus" : ""}`}
              >
                <div className={`msg-preview-bubble${message.deleted ? " deleted" : ""}`}>
                  {!message.outgoing && !message.post && <div className="msg-preview-from">{nameOf ? nameOf(message.from) : message.from}</div>}
                  {message.mediaURL && message.mediaKind !== "document" ? (
                    <img className="msg-preview-photo" src={message.mediaURL} alt="" loading="lazy" onLoad={followFocus} onClick={() => setZoom(message.mediaURL ?? null)} />
                  ) : message.mediaURL ? (
                    <a className="msg-preview-media" href={message.mediaURL} target="_blank" rel="noreferrer">{t("common.download")}</a>
                  ) : message.media ? (
                    <div className="msg-preview-media">{t("messages.media")}</div>
                  ) : null}
                  {message.body && <div className="msg-preview-text">{message.body}</div>}
                  <div className="msg-preview-meta">
                    {detailOf && message.id !== undefined && onOpenDetail ? (
                      <button
                        type="button"
                        className="msg-preview-link"
                        onClick={() => {
                          const path = detailOf(message);
                          if (path) onOpenDetail(path);
                        }}
                      >
                        {`#${message.displayID} · ${t("common.detail")}`}
                      </button>
                    ) : null}
                    {formatUnix(message.date)}
                    {message.deleted ? ` · ${t("common.deleted")}` : ""}
                  </div>
                </div>
              </div>
            ))
        )}
      </div>
      {zoom && (
        <div className="lightbox" role="presentation" onClick={() => setZoom(null)}>
          <img src={zoom} alt="" />
        </div>
      )}
    </section>
  );
}
