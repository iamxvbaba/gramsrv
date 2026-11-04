// Parsing of a message's stored media JSON for the review screens.
//
// The id is read as a STRING on purpose: photo/document ids are 64-bit and
// exceed Number.MAX_SAFE_INTEGER, so JSON.parse (or Number(id)) would round
// them and the /api/live/media lookup would miss. A targeted regex also avoids
// picking the wrong "id" (a webpage or a live photo's embedded video) that a
// naive first-id scan would find in the key-sorted pretty JSON.
import { api } from "../api";

export type MediaRef = { key: string; kind: "photo" | "document" };

export function parseMedia(mediaJSON?: string): MediaRef | null {
  if (!mediaJSON || mediaJSON === "{}") return null;
  const kindMatch = /"kind"\s*:\s*"([a-z_]+)"/.exec(mediaJSON);
  const kind = kindMatch?.[1];
  const wanted = kind === "document" ? "document" : "photo";
  const objectMatch = new RegExp(`"${wanted}"\\s*:\\s*\\{`).exec(mediaJSON);
  if (!objectMatch) return null;
  const idMatch = /"id"\s*:\s*(\d+)/.exec(mediaJSON.slice(objectMatch.index + objectMatch[0].length));
  if (!idMatch) return null;
  return { key: `${wanted === "document" ? "doc" : "photo"}:${idMatch[1]}`, kind: wanted };
}

export function mediaLocationKey(mediaJSON?: string): string | null {
  return parseMedia(mediaJSON)?.key ?? null;
}

// mediaFields returns the preview fields for a message's media JSON.
export function mediaFields(mediaJSON?: string): { mediaURL?: string; mediaKind?: "photo" | "document" } {
  const ref = parseMedia(mediaJSON);
  return ref ? { mediaURL: api.mediaURL(ref.key), mediaKind: ref.kind } : {};
}
