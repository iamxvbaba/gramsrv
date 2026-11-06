// Parsing of a message's stored media JSON for the review screens.
//
// The JSON is a domain.MessageMedia snapshot (internal/domain/media.go) as the
// read store pretty-prints it: a top-level "kind" next to "photo", "document",
// "web_page" and so on. The nested objects carry their own "kind" and "id"
// keys (document attributes, photo sizes, a live photo's video, a sticker's
// set), so the media is read as an object, never by scanning for a key.
//
// Photo and document ids are 64-bit and exceed Number.MAX_SAFE_INTEGER, so
// every number literal is turned into a string before JSON.parse: a parsed
// number would be rounded and the /api/live/media lookup would miss.
import { api } from "../api";

export type MediaRef = { key: string; kind: "photo" | "document" };

type JsonObject = { [key: string]: unknown };

// A JSON string, or a number literal outside one. Strings are matched first
// and kept as they are, so digits inside them are never touched.
const jsonToken = /"(?:[^"\\]|\\.)*"|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/g;

function asObject(value: unknown): JsonObject | null {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? (value as JsonObject) : null;
}

// idOf returns the decimal id of a photo or document object, or "" if it has none.
function idOf(value: unknown): string {
  const id = asObject(value)?.id;
  return typeof id === "string" && /^-?\d+$/.test(id) && id !== "0" ? id : "";
}

export function parseMedia(mediaJSON?: string): MediaRef | null {
  if (!mediaJSON || mediaJSON === "{}") return null;
  let media: JsonObject | null;
  try {
    media = asObject(JSON.parse(mediaJSON.replace(jsonToken, (token) => (token.startsWith('"') ? token : `"${token}"`))));
  } catch {
    return null;
  }
  if (!media) return null;
  switch (media.kind) {
    case "document": {
      const id = idOf(media.document);
      return id ? { key: `doc:${id}`, kind: "document" } : null;
    }
    case "photo": {
      // A live photo also carries live_photo_video; the still photo is shown.
      const id = idOf(media.photo);
      return id ? { key: `photo:${id}`, kind: "photo" } : null;
    }
    case "web_page": {
      // A link preview shows its photo, if it has one.
      const id = idOf(asObject(media.web_page)?.photo);
      return id ? { key: `photo:${id}`, kind: "photo" } : null;
    }
    default:
      return null;
  }
}

export function mediaLocationKey(mediaJSON?: string): string | null {
  return parseMedia(mediaJSON)?.key ?? null;
}

// mediaFields returns the preview fields for a message's media JSON.
export function mediaFields(mediaJSON?: string): { mediaURL?: string; mediaKind?: "photo" | "document" } {
  const ref = parseMedia(mediaJSON);
  return ref ? { mediaURL: api.mediaURL(ref.key), mediaKind: ref.kind } : {};
}
