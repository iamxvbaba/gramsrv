import { useState, type ReactNode } from "react";
import { formatUnix } from "../lib/format";

// MessagePreview renders a conversation as chat bubbles -- the "live preview"
// an operator wants when reviewing what was said, next to the raw table.
export type PreviewMessage = {
  id: string | number;
  date: number;
  from: number | string;
  body: string;
  outgoing?: boolean;
  deleted?: boolean;
  media?: boolean;
  mediaURL?: string;
  post?: boolean;
};

export function MessagePreview({
  messages,
  title,
  emptyLabel,
  nameOf,
  avatar,
  focusID
}: {
  messages: PreviewMessage[];
  title: string;
  emptyLabel: string;
  nameOf?: (from: number | string) => string;
  avatar?: ReactNode;
  focusID?: string | number;
}) {
  const [zoom, setZoom] = useState<string | null>(null);
  return (
    <section className="msg-preview">
      <div className="msg-preview-head">
        {avatar}
        <span>{title}</span>
      </div>
      <div className="msg-preview-list">
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
                  {message.mediaURL ? (
                    <img className="msg-preview-photo" src={message.mediaURL} alt="" loading="lazy" onClick={() => setZoom(message.mediaURL ?? null)} />
                  ) : message.media ? (
                    <div className="msg-preview-media">media</div>
                  ) : null}
                  {message.body && <div className="msg-preview-text">{message.body}</div>}
                  <div className="msg-preview-meta">
                    {formatUnix(message.date)}
                    {message.deleted ? " · deleted" : ""}
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
