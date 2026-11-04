// mediaLocationKey derives a blob location key ("photo:<id>" / "doc:<id>") from
// a message's stored media JSON, so the review screens can load the bytes from
// /api/live/media.
//
// The id is extracted as a STRING on purpose: photo/document ids are 64-bit and
// exceed Number.MAX_SAFE_INTEGER, so JSON.parse (and Number(id)) would round
// them and the lookup would miss. A regex keeps the exact digits.
export function mediaLocationKey(mediaJSON?: string): string | null {
  if (!mediaJSON || mediaJSON === "{}") return null;
  const idMatch = /"id"\s*:\s*(\d+)/.exec(mediaJSON);
  if (!idMatch) return null;
  const id = idMatch[1];
  const kindMatch = /"kind"\s*:\s*"([a-z_]+)"/.exec(mediaJSON);
  const kind = kindMatch?.[1];
  if (kind === "document" || /"document"\s*:/.test(mediaJSON)) {
    return `doc:${id}`;
  }
  if (kind === "photo" || /"photo"\s*:/.test(mediaJSON)) {
    return `photo:${id}`;
  }
  return null;
}
